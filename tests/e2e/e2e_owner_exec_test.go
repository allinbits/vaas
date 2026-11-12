package e2e

import (
	"encoding/json"
	"fmt"
	"strconv"
	"time"
)

// testOwnerExec: the consumer's owner holds the reference consumer's
// authority through MsgOwnerExec. It updates the consumer params as the
// consumer module account, paying in vouchers like every post-pin
// transaction, and a user's attempt at the same is refused as not the
// owner's.
//
// Runs after the photon test, which leaves the owner and the user with
// vouchers and the fee policy enforcing, and before the node fee floor is
// raised.
func (s *IntegrationTestSuite) testOwnerExec() {
	consumerClientID := s.tendermintClientTracking(
		s.consumerValRes[0].Container.ID, consumerBinary, consumerHomePath, s.cfg.providerChainID)
	voucherDenom := photonVoucherDenom(consumerClientID)

	var authority struct {
		Authority string `json:"authority"`
		Owner     string `json:"owner"`
	}
	stdout, stderr, err := s.dockerExec(s.consumerValRes[0].Container.ID, []string{
		consumerBinary, "query", "vaasconsumer", "owner-authority", "--home", consumerHomePath, "-o", "json",
	})
	s.Require().NoError(err, "querying the owner authority: %s", stderr.String())
	s.Require().NoError(json.Unmarshal(stdout.Bytes(), &authority), "decoding the owner authority: %s", stdout.String())
	s.Require().NotEmpty(authority.Authority)
	s.Require().NotEmpty(authority.Owner, "the provider seeds the owner into the consumer params")

	// The update carries the whole params back, with one entry changed; the
	// CLI prints them proto-JSON encoded, which is what the message file
	// takes.
	params := s.queryConsumerParamsJSON()
	entries := s.historicalEntries(params)
	params["historical_entries"] = strconv.FormatInt(entries+1, 10)
	file, err := json.Marshal(map[string]any{"messages": []any{map[string]any{
		"@type":     "/vaas.consumer.v1.MsgUpdateParams",
		"authority": authority.Authority,
		"params":    params,
	}}})
	s.Require().NoError(err)
	s.dockerExecMust(s.consumerValRes[0].Container.ID, []string{
		"sh", "-c", fmt.Sprintf("echo '%s' > /tmp/owner_exec.json", string(file)),
	})

	ownerExec := func(from string) []byte {
		stdout, stderr, err := s.dockerExec(s.consumerValRes[0].Container.ID, []string{
			consumerBinary, "tx", "vaasconsumer", "owner-exec", "/tmp/owner_exec.json",
			"--from", from,
			"--home", consumerHomePath,
			"--keyring-backend", "test",
			"--chain-id", s.cfg.consumerChainID,
			"--gas", "300000",
			"--fees", "5000" + voucherDenom,
			"--broadcast-mode", "sync",
			"-y",
			"-o", "json",
		})
		s.Require().NoError(err, "broadcast failed to run: %s", stderr.String())
		return stdout.Bytes()
	}

	s.Run("a user cannot execute as the owner", func() {
		code, rawLog := s.txResultOn(s.consumerValRes[0].Container.ID, consumerBinary, consumerHomePath, ownerExec("user"))
		s.Require().NotZero(code, "a user's owner exec must fail on-chain")
		s.Require().Contains(rawLog, "not the consumer owner", "the refusal must come from the owner check: %s", rawLog)
		s.Require().Equal(entries, s.historicalEntries(s.queryConsumerParamsJSON()), "the params must not change on a refused exec")
	})

	s.Run("the owner updates the consumer params as the consumer module", func() {
		s.requireTxCommittedOn(s.consumerValRes[0].Container.ID, consumerBinary, consumerHomePath, ownerExec("owner"))
		s.Require().Equal(entries+1, s.historicalEntries(s.queryConsumerParamsJSON()), "the owner's parameter update must apply")
	})
}

// queryConsumerParamsJSON returns the consumer params as the CLI prints them
// (proto-JSON), ready to be carried back in a MsgUpdateParams.
func (s *IntegrationTestSuite) queryConsumerParamsJSON() map[string]any {
	stdout, stderr, err := s.dockerExec(s.consumerValRes[0].Container.ID, []string{
		consumerBinary, "query", "vaasconsumer", "params", "--home", consumerHomePath, "-o", "json",
	})
	s.Require().NoError(err, "querying the consumer params: %s", stderr.String())
	var res struct {
		Params map[string]any `json:"params"`
	}
	s.Require().NoError(json.Unmarshal(stdout.Bytes(), &res), "decoding the consumer params: %s", stdout.String())
	s.Require().NotEmpty(res.Params)
	return res.Params
}

// historicalEntries reads the int64 param the way proto-JSON renders it: as
// a string.
func (s *IntegrationTestSuite) historicalEntries(params map[string]any) int64 {
	raw, ok := params["historical_entries"].(string)
	s.Require().True(ok, "historical_entries must be a proto-JSON int64: %v", params["historical_entries"])
	n, err := strconv.ParseInt(raw, 10, 64)
	s.Require().NoError(err, "historical_entries: %q", raw)
	return n
}

// txResultOn waits for a broadcast transaction to be included and returns its
// on-chain result code and log. The broadcast itself must have been accepted
// (CheckTx code 0): a refusal that happens at execution, as the owner check's
// does, only shows in the committed result.
func (s *baseTestSuite) txResultOn(containerID, binary, homePath string, broadcastOut []byte) (int, string) {
	var bres struct {
		TxHash string `json:"txhash"`
		Code   int    `json:"code"`
		RawLog string `json:"raw_log"`
	}
	s.Require().NoErrorf(json.Unmarshal(broadcastOut, &bres), "decode broadcast response: %s", string(broadcastOut))
	s.Require().Equalf(0, bres.Code, "tx rejected at CheckTx: %s", bres.RawLog)
	s.Require().NotEmptyf(bres.TxHash, "broadcast returned empty txhash: %s", string(broadcastOut))

	var code int
	var rawLog, lastOut string
	s.Require().Eventuallyf(func() bool {
		out, _, qerr := s.dockerExec(containerID, []string{
			binary, "query", "tx", bres.TxHash, "--home", homePath, "--output", "json",
		})
		if qerr != nil || out.Len() == 0 {
			return false
		}
		lastOut = out.String()
		var qres struct {
			Code   int    `json:"code"`
			RawLog string `json:"raw_log"`
		}
		if json.Unmarshal(out.Bytes(), &qres) != nil {
			return false
		}
		code, rawLog = qres.Code, qres.RawLog
		return true
	}, 30*time.Second, 2*time.Second,
		"tx %s was not included in time; last query output: %s", bres.TxHash, lastOut)
	return code, rawLog
}
