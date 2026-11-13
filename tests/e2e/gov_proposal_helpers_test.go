package e2e

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

// queryModuleAccountAddress returns the bech32 address of the named module
// account on the provider chain.
func (s *baseTestSuite) queryModuleAccountAddress(name string) string {
	stdout, _, err := s.dockerExec(s.providerValRes[0].Container.ID, []string{
		providerBinary, "query", "auth", "module-account", name,
		"--home", providerHomePath,
		"--output", "json",
	})
	s.Require().NoError(err, "failed to query module account %s", name)

	// JSON shape varies across SDK versions: try the common nests in order.
	var res struct {
		Account struct {
			Address     string `json:"address"`
			BaseAccount struct {
				Address string `json:"address"`
			} `json:"base_account"`
			Value struct {
				Address     string `json:"address"`
				BaseAccount struct {
					Address string `json:"address"`
				} `json:"base_account"`
			} `json:"value"`
		} `json:"account"`
	}
	s.Require().NoError(json.Unmarshal(stdout.Bytes(), &res),
		"failed to decode module-account response: %s", stdout.String())

	candidates := []string{
		res.Account.BaseAccount.Address,
		res.Account.Value.Address,
		res.Account.Value.BaseAccount.Address,
		res.Account.Address,
	}
	for _, addr := range candidates {
		if addr != "" {
			return addr
		}
	}

	s.T().Logf("module-account response for %s: %s", name, stdout.String())
	s.Require().Failf("module account address not found", "name=%s", name)
	return ""
}

// queryGovAuthority is a thin wrapper for queryModuleAccountAddress("gov").
func (s *baseTestSuite) queryGovAuthority() string {
	return s.queryModuleAccountAddress("gov")
}

// queryCommunityPoolBalance returns the current community-pool balance for the
// given denom, truncated to int64 (on-chain value is DecCoins). Returns 0 if
// denom not present.
func (s *IntegrationTestSuite) queryCommunityPoolBalance(denom string) int64 {
	stdout, _, err := s.dockerExec(s.providerValRes[0].Container.ID, []string{
		providerBinary, "query", "distribution", "community-pool",
		"--home", providerHomePath,
		"--output", "json",
	})
	s.Require().NoError(err, "failed to query community pool")

	var res struct {
		Pool []string `json:"pool"`
	}
	s.Require().NoError(json.Unmarshal(stdout.Bytes(), &res),
		"failed to decode community-pool response: %s", stdout.String())

	for _, raw := range res.Pool {
		dc, err := sdk.ParseDecCoin(raw)
		if err != nil {
			continue
		}
		if dc.Denom == denom {
			return dc.Amount.TruncateInt64()
		}
	}
	return 0
}

// submitAndPassProposal writes proposalJSON to /tmp/proposal.json in the
// provider container, submits the proposal from "val", votes YES from val and
// waits for the tally to execute. Fails the test on submit/vote/tally errors.
//
// proposalJSON must be a valid gov v1 proposal body. The submitter and voting
// fees are paid in bondDenom by val.
func (s *baseTestSuite) submitAndPassProposal(proposalJSON string) {
	s.submitProposalWithVote(proposalJSON, "yes", "PROPOSAL_STATUS_PASSED")
}

// submitAndRejectProposal submits a proposal, votes no from val, and waits for
// it to be REJECTED. Used by tests exercising the punishment deferral behind a
// removal vote that the community then declines.
func (s *baseTestSuite) submitAndRejectProposal(proposalJSON string) {
	s.submitProposalWithVote(proposalJSON, "no", "PROPOSAL_STATUS_REJECTED")
}

// submitProposalWithVote submits a gov v1 proposal, votes the given option
// from val, and waits until the proposal reaches wantStatus; any other
// terminal status fails the test. It returns the proposal ID.
func (s *baseTestSuite) submitProposalWithVote(proposalJSON, voteOption, wantStatus string) uint64 {
	proposalID := s.submitProposalAndVote(proposalJSON, voteOption)
	s.waitForProposalStatus(proposalID, wantStatus)
	return proposalID
}

// submitProposalAndVote submits a gov v1 proposal and votes the given option
// from val, returning the proposal ID as soon as the vote is broadcast: the
// caller decides what to observe while the vote is open and waits for the
// tally with waitForProposalStatus.
func (s *baseTestSuite) submitProposalAndVote(proposalJSON, voteOption string) uint64 {
	containerID := s.providerValRes[0].Container.ID

	// 1. Write the proposal body to /tmp/proposal.json via base64 to avoid
	//    shell-quoting headaches.
	payload := base64.StdEncoding.EncodeToString([]byte(proposalJSON))
	_, _, err := s.dockerExec(containerID, []string{
		"sh", "-c",
		fmt.Sprintf("echo %s | base64 -d > /tmp/proposal.json", payload),
	})
	s.Require().NoError(err, "failed to write proposal.json")

	// 2. Submit the proposal.
	stdout, stderr, err := s.dockerExec(containerID, []string{
		providerBinary, "tx", "gov", "submit-proposal", "/tmp/proposal.json",
		"--from", "val",
		"--home", providerHomePath,
		"--keyring-backend", "test",
		"--chain-id", s.cfg.providerChainID,
		"--fees", "10000" + bondDenom,
		"--yes",
		"-o", "json",
	})
	s.Require().NoErrorf(err, "failed to submit proposal: stdout=%s stderr=%s",
		stdout.String(), stderr.String())

	var submitRes struct {
		TxHash string `json:"txhash"`
		Code   int    `json:"code"`
		RawLog string `json:"raw_log"`
	}
	s.Require().NoError(json.Unmarshal(stdout.Bytes(), &submitRes),
		"failed to decode submit-proposal response: %s", stdout.String())
	s.Require().Equalf(0, submitRes.Code, "submit-proposal failed: %s", submitRes.RawLog)
	s.Require().NotEmpty(submitRes.TxHash, "submit-proposal returned empty txhash: %s", stdout.String())

	// Poll for the submit tx to land and yield a proposal_id.
	var proposalID uint64
	var lastTxOut string
	s.Require().Eventuallyf(func() bool {
		txOut, _, qerr := s.dockerExec(containerID, []string{
			providerBinary, "query", "tx", submitRes.TxHash,
			"--home", providerHomePath,
			"--output", "json",
		})
		if qerr != nil || txOut.Len() == 0 {
			return false
		}
		lastTxOut = txOut.String()
		proposalID = extractProposalID(txOut.Bytes())
		return proposalID != 0
	}, 30*time.Second, 2*time.Second,
		"could not parse proposal_id from tx events for %s: last stdout=%s",
		submitRes.TxHash, lastTxOut)

	// 3. Vote from val.
	voteStdout, voteStderr, err := s.dockerExec(containerID, []string{
		providerBinary, "tx", "gov", "vote", fmt.Sprintf("%d", proposalID), voteOption,
		"--from", "val",
		"--home", providerHomePath,
		"--keyring-backend", "test",
		"--chain-id", s.cfg.providerChainID,
		"--fees", "10000" + bondDenom,
		"--yes",
	})
	s.Require().NoErrorf(err, "failed to vote on proposal %d: stdout=%s stderr=%s",
		proposalID, voteStdout.String(), voteStderr.String())

	return proposalID
}

// waitForProposalStatus waits until the proposal reaches wantStatus; any other
// terminal status fails the test.
func (s *baseTestSuite) waitForProposalStatus(proposalID uint64, wantStatus string) {
	// Wait for tally: the voting period plus a buffer for the proposal to
	// land and the tally block to commit, 2s tick.
	s.Require().Eventuallyf(func() bool {
		status := s.queryProposalStatus(proposalID)
		switch status {
		case wantStatus:
			return true
		case "PROPOSAL_STATUS_PASSED", "PROPOSAL_STATUS_REJECTED", "PROPOSAL_STATUS_FAILED":
			body := s.dumpProposal(proposalID)
			s.Require().Failf("proposal reached the wrong terminal status",
				"proposal %d ended with status %s, want %s; full body: %s",
				proposalID, status, wantStatus, body)
			return true
		default:
			return false
		}
	}, s.cfg.govVotingPeriod+45*time.Second, 2*time.Second,
		"proposal %d did not reach %s within timeout", proposalID, wantStatus)
}

// dumpProposal returns the raw JSON of a gov proposal query, used to surface
// the failed_reason / messages on terminal-bad status.
func (s *baseTestSuite) dumpProposal(proposalID uint64) string {
	stdout, _, err := s.dockerExec(s.providerValRes[0].Container.ID, []string{
		providerBinary, "query", "gov", "proposal", fmt.Sprintf("%d", proposalID),
		"--home", providerHomePath,
		"--output", "json",
	})
	if err != nil {
		return fmt.Sprintf("<failed to query: %v>", err)
	}
	return stdout.String()
}

// queryProposalStatus returns the textual status of a gov proposal (e.g.
// "PROPOSAL_STATUS_PASSED").
func (s *baseTestSuite) queryProposalStatus(proposalID uint64) string {
	stdout, _, err := s.dockerExec(s.providerValRes[0].Container.ID, []string{
		providerBinary, "query", "gov", "proposal", fmt.Sprintf("%d", proposalID),
		"--home", providerHomePath,
		"--output", "json",
	})
	if err != nil {
		s.T().Logf("queryProposalStatus failed: %v", err)
		return ""
	}
	var res struct {
		Status   string `json:"status"`
		Proposal struct {
			Status string `json:"status"`
		} `json:"proposal"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &res); err != nil {
		s.T().Logf("queryProposalStatus failed: %v", err)
		return ""
	}
	if res.Status != "" {
		return res.Status
	}
	return res.Proposal.Status
}

// extractProposalID walks a tx-query JSON response and returns the first
// submit_proposal.proposal_id event attribute that parses as a uint64.
// Returns 0 if there is none.
func extractProposalID(raw []byte) uint64 {
	for _, v := range txEventAttributes(raw, "submit_proposal", "proposal_id") {
		if id, err := strconv.ParseUint(v, 10, 64); err == nil {
			return id
		}
	}
	return 0
}

// txEventAttribute walks a tx-query JSON response and returns the value of
// the first attribute named key on an event of type eventType (see
// txEventAttributes).
func txEventAttribute(raw []byte, eventType, key string) (string, bool) {
	values := txEventAttributes(raw, eventType, key)
	if len(values) == 0 {
		return "", false
	}
	return values[0], true
}

// txEventAttributes walks a tx-query JSON response and returns the values of
// every attribute named key on an event of type eventType, trimmed of quotes,
// in order, wherever the response shape carries its events (top-level, under
// tx_response, or inside the legacy logs).
func txEventAttributes(raw []byte, eventType, key string) []string {
	type event struct {
		Type       string `json:"type"`
		Attributes []struct {
			Key   string `json:"key"`
			Value string `json:"value"`
		} `json:"attributes"`
	}
	var doc struct {
		Events     []event `json:"events"`
		TxResponse struct {
			Events []event `json:"events"`
			Logs   []struct {
				Events []event `json:"events"`
			} `json:"logs"`
		} `json:"tx_response"`
		Logs []struct {
			Events []event `json:"events"`
		} `json:"logs"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil
	}

	var values []string
	scan := func(events []event) {
		for _, ev := range events {
			if ev.Type != eventType {
				continue
			}
			for _, attr := range ev.Attributes {
				if attr.Key == key {
					values = append(values, strings.Trim(strings.TrimSpace(attr.Value), "\" "))
				}
			}
		}
	}

	scan(doc.Events)
	scan(doc.TxResponse.Events)
	for _, lg := range doc.TxResponse.Logs {
		scan(lg.Events)
	}
	for _, lg := range doc.Logs {
		scan(lg.Events)
	}
	return values
}
