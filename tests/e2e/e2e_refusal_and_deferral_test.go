package e2e

// e2e_refusal_and_deferral_test.go exercises the refusal-protection model end
// to end (see docs/consumer-refusal.md): the stake-weighted refusal pause and
// its governance resume, the deferral of matured downtime slashes behind a
// removal vote, and the full equivocation-punishment lifecycle -- queue with
// immediate jail and unbonding holds, deferral behind a rejected removal vote
// followed by execution (slash + tombstone), and cancellation with unjail
// when a removal vote passes.
//
// The equivocation tests play the attacker for real: they bond validators
// whose consensus keys the test generates and keeps, then hand-sign two
// conflicting precommits with those keys, exactly the fabrication a malicious
// consumer binary holding an in-process key could produce. The provider
// cannot tell the difference, which is the threat model these mechanisms
// answer.

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	cmted25519 "github.com/cometbft/cometbft/crypto/ed25519"
	tmproto "github.com/cometbft/cometbft/proto/tendermint/types"
	cmtversion "github.com/cometbft/cometbft/proto/tendermint/version"
	tmtypes "github.com/cometbft/cometbft/types"
	cometversion "github.com/cometbft/cometbft/version"

	clienttypes "github.com/cosmos/ibc-go/v10/modules/core/02-client/types"
	ibctmtypes "github.com/cosmos/ibc-go/v10/modules/light-clients/07-tendermint"

	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
)

// createEquivocatingValidator bonds a validator like createSilentValidator
// but generates its consensus key in the test and returns it, so the test can
// sign fabricated votes with it.
func (s *baseTestSuite) createEquivocatingValidator(key, selfBondAmount string) (valoperAddr string, priv cmted25519.PrivKey) {
	containerID := s.providerValRes[0].Container.ID
	priv = cmted25519.GenPrivKey()
	pubJSON := fmt.Sprintf(`{"@type":"/cosmos.crypto.ed25519.PubKey","key":%q}`,
		base64.StdEncoding.EncodeToString(priv.PubKey().Bytes()))

	s.dockerExecMust(containerID, []string{
		providerBinary, "keys", "add", key,
		"--home", providerHomePath,
		"--keyring-backend", "test",
	})
	stdout, _, err := s.dockerExec(containerID, []string{
		providerBinary, "keys", "show", key, "-a",
		"--home", providerHomePath,
		"--keyring-backend", "test",
	})
	s.Require().NoError(err, "failed to get %s account address", key)
	accAddr := strings.TrimSpace(stdout.String())

	stdout, _, err = s.dockerExec(containerID, []string{
		providerBinary, "keys", "show", key, "--bech", "val", "-a",
		"--home", providerHomePath,
		"--keyring-backend", "test",
	})
	s.Require().NoError(err, "failed to get %s operator address", key)
	valoperAddr = strings.TrimSpace(stdout.String())

	s.providerFundAddress(accAddr, "10000000"+bondDenom)

	validatorJSON := fmt.Sprintf(`{
  "pubkey": %s,
  "amount": %q,
  "moniker": %q,
  "commission-rate": "0.1",
  "commission-max-rate": "0.2",
  "commission-max-change-rate": "0.01",
  "min-self-delegation": "1"
}`, pubJSON, selfBondAmount, key)
	payload := base64.StdEncoding.EncodeToString([]byte(validatorJSON))
	s.dockerExecMust(containerID, []string{
		"sh", "-c", fmt.Sprintf("echo %s | base64 -d > /tmp/%s.json", payload, key),
	})
	stdout, stderr, err := s.dockerExec(containerID, []string{
		providerBinary, "tx", "staking", "create-validator", "/tmp/" + key + ".json",
		"--from", key,
		"--home", providerHomePath,
		"--keyring-backend", "test",
		"--chain-id", s.cfg.providerChainID,
		"--gas", "auto",
		"--gas-adjustment", "1.5",
		"--fees", "10000" + bondDenom,
		"-y",
		"-o", "json",
	})
	s.Require().NoErrorf(err, "failed to submit create-validator for %s: stderr=%s", key, stderr.String())
	s.requireTxCommitted(stdout.Bytes())
	return valoperAddr, priv
}

// fabricateDoubleVote signs two conflicting precommits at the same height and
// round with the validator's real consumer key and writes the evidence and a
// minimal but well-formed infraction header into the provider container,
// returning the in-container file paths. The header carries a validator set
// with the signing key and a commit over its own hash so that it passes the
// message's ValidateBasic; the handler matches its chain id, reads the
// validator set for the pubkey, and verifies the votes' signatures, not the
// commit's.
func (s *baseTestSuite) fabricateDoubleVote(priv cmted25519.PrivKey, chainID string, height int64, tag string) (evidencePath, headerPath string) {
	valAddr := priv.PubKey().Address()
	now := time.Now().UTC()

	makeVote := func(hashByte byte) *tmproto.Vote {
		blockHash := make([]byte, 32)
		partHash := make([]byte, 32)
		for i := range blockHash {
			blockHash[i] = hashByte
			partHash[i] = hashByte ^ 0xFF
		}
		v := &tmproto.Vote{
			Type:   tmproto.PrecommitType,
			Height: height,
			Round:  0,
			BlockID: tmproto.BlockID{
				Hash:          blockHash,
				PartSetHeader: tmproto.PartSetHeader{Total: 1, Hash: partHash},
			},
			Timestamp:        now,
			ValidatorAddress: valAddr,
			ValidatorIndex:   0,
		}
		sig, err := priv.Sign(tmtypes.VoteSignBytes(chainID, v))
		s.Require().NoError(err, "failed to sign fabricated vote")
		v.Signature = sig
		return v
	}

	evidence := &tmproto.DuplicateVoteEvidence{
		VoteA:            makeVote(0xAA),
		VoteB:            makeVote(0xBB),
		TotalVotingPower: 5,
		ValidatorPower:   5,
		Timestamp:        now,
	}

	tmVal := tmtypes.NewValidator(priv.PubKey(), 5)
	valset := tmtypes.NewValidatorSet([]*tmtypes.Validator{tmVal})
	valsetProto, err := valset.ToProto()
	s.Require().NoError(err, "failed to build fabricated validator set")

	tmHeader := tmtypes.Header{
		Version:            cmtversion.Consensus{Block: cometversion.BlockProtocol},
		ChainID:            chainID,
		Height:             height,
		Time:               now,
		ValidatorsHash:     valset.Hash(),
		NextValidatorsHash: valset.Hash(),
		ProposerAddress:    valset.Proposer.Address,
	}
	commit := &tmtypes.Commit{
		Height:  height,
		BlockID: tmtypes.BlockID{Hash: tmHeader.Hash()},
		Signatures: []tmtypes.CommitSig{{
			BlockIDFlag:      tmtypes.BlockIDFlagCommit,
			ValidatorAddress: valset.Proposer.Address,
			Timestamp:        now,
			Signature:        []byte{0x01},
		}},
	}
	signedHeader := tmtypes.SignedHeader{Header: &tmHeader, Commit: commit}
	revision := clienttypes.ParseChainID(chainID)
	header := &ibctmtypes.Header{
		SignedHeader:      signedHeader.ToProto(),
		ValidatorSet:      valsetProto,
		TrustedHeight:     clienttypes.NewHeight(revision, uint64(height-1)),
		TrustedValidators: valsetProto,
	}

	cdc := codec.NewProtoCodec(codectypes.NewInterfaceRegistry())
	evidenceJSON, err := cdc.MarshalJSON(evidence)
	s.Require().NoError(err, "failed to marshal fabricated evidence")
	headerJSON, err := cdc.MarshalJSON(header)
	s.Require().NoError(err, "failed to marshal fabricated header")

	containerID := s.providerValRes[0].Container.ID
	evidencePath = fmt.Sprintf("/tmp/evidence_%s.json", tag)
	headerPath = fmt.Sprintf("/tmp/header_%s.json", tag)
	s.dockerExecMust(containerID, []string{
		"sh", "-c", fmt.Sprintf("echo %s | base64 -d > %s",
			base64.StdEncoding.EncodeToString(evidenceJSON), evidencePath),
	})
	s.dockerExecMust(containerID, []string{
		"sh", "-c", fmt.Sprintf("echo %s | base64 -d > %s",
			base64.StdEncoding.EncodeToString(headerJSON), headerPath),
	})
	return evidencePath, headerPath
}

// submitDoubleVoteEvidence submits fabricated evidence via the CLI and
// requires the transaction to commit.
func (s *baseTestSuite) submitDoubleVoteEvidence(consumerID, evidencePath, headerPath string) {
	stdout, stderr, err := s.dockerExec(s.providerValRes[0].Container.ID, []string{
		providerBinary, "tx", "provider", "submit-consumer-double-voting",
		consumerID, evidencePath, headerPath,
		"--from", "val",
		"--home", providerHomePath,
		"--keyring-backend", "test",
		"--chain-id", s.cfg.providerChainID,
		"--gas", "auto",
		"--gas-adjustment", "1.5",
		"--fees", "10000" + bondDenom,
		"-y",
		"-o", "json",
	})
	s.Require().NoErrorf(err, "failed to submit double-voting evidence: stderr=%s", stderr.String())
	s.requireTxCommitted(stdout.Bytes())
}

// pendingEquivocationJSON mirrors the CLI query output fields the tests need.
type pendingEquivocationJSON struct {
	ConsumerId           string    `json:"consumer_id"`
	ProviderConsAddr     []byte    `json:"provider_cons_addr"`
	ExecutesAt           time.Time `json:"executes_at"`
	Extended             bool      `json:"executes_at_extended"`
	DeferredByProposalId string    `json:"deferred_by_proposal_id"`
}

// queryPendingEquivocations lists the consumer's pending equivocation
// punishments; the error lets polling callers retry and asserting callers
// fail rather than read a broken query as an empty queue.
func (s *baseTestSuite) queryPendingEquivocations(consumerID string) ([]pendingEquivocationJSON, error) {
	stdout, stderr, err := s.dockerExec(s.providerValRes[0].Container.ID, []string{
		providerBinary, "query", "provider", "pending-equivocation-punishments", consumerID,
		"--home", providerHomePath,
		"--output", "json",
	})
	if err != nil {
		return nil, fmt.Errorf("query pending-equivocation-punishments: %w (stderr=%s)", err, stderr.String())
	}
	if stdout.Len() == 0 {
		return nil, fmt.Errorf("query pending-equivocation-punishments: empty output (stderr=%s)", stderr.String())
	}
	var res struct {
		Punishments []pendingEquivocationJSON `json:"punishments"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &res); err != nil {
		return nil, fmt.Errorf("decode pending-equivocation-punishments: %w (raw=%s)", err, stdout.String())
	}
	return res.Punishments, nil
}

// mustPendingEquivocations is queryPendingEquivocations for assertions.
func (s *baseTestSuite) mustPendingEquivocations(consumerID string) []pendingEquivocationJSON {
	pending, err := s.queryPendingEquivocations(consumerID)
	s.Require().NoError(err)
	return pending
}

// pendingEquivocationsEmpty reports whether the consumer's queue reads back
// empty; a failing query does not count as empty.
func (s *baseTestSuite) pendingEquivocationsEmpty(consumerID string) bool {
	pending, err := s.queryPendingEquivocations(consumerID)
	return err == nil && len(pending) == 0
}

// stakingValidatorState reads the validator's jailed flag and tokens.
func (s *baseTestSuite) stakingValidatorState(valoper string) (jailed bool, tokens string) {
	stdout, _, err := s.dockerExec(s.providerValRes[0].Container.ID, []string{
		providerBinary, "query", "staking", "validator", valoper,
		"--home", providerHomePath,
		"--output", "json",
	})
	s.Require().NoError(err, "failed to query staking validator %s", valoper)
	var res struct {
		Validator struct {
			Jailed bool   `json:"jailed"`
			Tokens string `json:"tokens"`
		} `json:"validator"`
	}
	s.Require().NoError(json.Unmarshal(stdout.Bytes(), &res),
		"failed to decode staking validator: %s", stdout.String())
	return res.Validator.Jailed, res.Validator.Tokens
}

// signingInfoTombstoned reads the slashing signing-info tombstone flag over
// REST for the given consensus address.
func (s *baseTestSuite) signingInfoTombstoned(consAddr string) bool {
	body, err := httpGetWithRetry(fmt.Sprintf("%s/cosmos/slashing/v1beta1/signing_infos/%s",
		s.providerRESTEndpoint(), consAddr), 3)
	if err != nil {
		return false
	}
	var res struct {
		ValSigningInfo struct {
			Tombstoned bool `json:"tombstoned"`
		} `json:"val_signing_info"`
	}
	if err := json.Unmarshal(body, &res); err != nil {
		return false
	}
	return res.ValSigningInfo.Tombstoned
}

// unbondingEntryJSON is one unbonding-delegation entry as the staking CLI
// prints it. The AutoCLI encoder omits zero-valued fields, so an entry off
// hold has no unbonding_on_hold_ref_count key at all; the reader restores the
// "0".
type unbondingEntryJSON struct {
	Balance  string `json:"balance"`
	RefCount string `json:"unbonding_on_hold_ref_count"`
}

// unbondingEntries lists the delegator's unbonding entries toward the
// validator (balance and on-hold refcount), or nothing while the query fails.
func (s *baseTestSuite) unbondingEntries(delegator, valoper string) []unbondingEntryJSON {
	stdout, _, err := s.dockerExec(s.providerValRes[0].Container.ID, []string{
		providerBinary, "query", "staking", "unbonding-delegation", delegator, valoper,
		"--home", providerHomePath,
		"--output", "json",
	})
	if err != nil || stdout.Len() == 0 {
		return nil
	}
	var res struct {
		Unbond struct {
			Entries []unbondingEntryJSON `json:"entries"`
		} `json:"unbond"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &res); err != nil {
		return nil
	}
	for i := range res.Unbond.Entries {
		if res.Unbond.Entries[i].RefCount == "" {
			res.Unbond.Entries[i].RefCount = "0"
		}
	}
	return res.Unbond.Entries
}

// singleUnbondingEntryHas reports whether the delegator has exactly one
// unbonding entry toward the validator with the given on-hold refcount.
func (s *baseTestSuite) singleUnbondingEntryHas(delegator, valoper, refCount string) bool {
	entries := s.unbondingEntries(delegator, valoper)
	return len(entries) == 1 && entries[0].RefCount == refCount
}

// mustUnbondingBalance returns the balance of the delegator's single unbonding
// entry toward the validator.
func (s *baseTestSuite) mustUnbondingBalance(delegator, valoper string) int64 {
	entries := s.unbondingEntries(delegator, valoper)
	s.Require().Len(entries, 1, "expected exactly one unbonding entry")
	v, err := strconv.ParseInt(entries[0].Balance, 10, 64)
	s.Require().NoErrorf(err, "unparsable unbonding balance %q", entries[0].Balance)
	return v
}

// unbondFrom starts an undelegation of amount from the validator, signed by
// the keyring key, and waits for the tx to commit.
func (s *baseTestSuite) unbondFrom(key, valoper, amount string) {
	stdout, stderr, err := s.dockerExec(s.providerValRes[0].Container.ID, []string{
		providerBinary, "tx", "staking", "unbond", valoper, amount,
		"--from", key,
		"--home", providerHomePath,
		"--keyring-backend", "test",
		"--chain-id", s.cfg.providerChainID,
		"--gas", "auto", "--gas-adjustment", "1.5",
		"--fees", "10000" + bondDenom,
		"-y", "-o", "json",
	})
	s.Require().NoErrorf(err, "failed to unbond from %s: stderr=%s", key, stderr.String())
	s.requireTxCommitted(stdout.Bytes())
}

// mustUnjail broadcasts MsgUnjail from the keyring key until it is accepted,
// then waits for the validator to read back unjailed.
func (s *baseTestSuite) mustUnjail(key, valoper string) {
	s.Require().Eventuallyf(func() bool {
		stdout, _, err := s.dockerExec(s.providerValRes[0].Container.ID, []string{
			providerBinary, "tx", "slashing", "unjail",
			"--from", key,
			"--home", providerHomePath,
			"--keyring-backend", "test",
			"--chain-id", s.cfg.providerChainID,
			"--fees", "10000" + bondDenom,
			"-y", "-o", "json",
		})
		if err != nil {
			return false
		}
		var res struct {
			Code int `json:"code"`
		}
		return json.Unmarshal(stdout.Bytes(), &res) == nil && res.Code == 0
	}, time.Minute, 5*time.Second, "%s could not broadcast unjail", key)

	s.Require().Eventuallyf(func() bool {
		jailed, _ := s.stakingValidatorState(valoper)
		return !jailed
	}, time.Minute, 2*time.Second, "%s must read back unjailed", key)
}

// removalProposalJSON builds a MsgRemoveConsumer gov proposal body.
func (s *baseTestSuite) removalProposalJSON(consumerID, title string) string {
	govAddr := s.queryGovAuthority()
	return fmt.Sprintf(`{
  "messages": [{
    "@type": "/vaas.provider.v1.MsgRemoveConsumer",
    "authority": %q,
    "consumer_id": %q
  }],
  "metadata": "ipfs://test",
  "deposit": "10000000%s",
  "title": %q,
  "summary": "refusal-and-deferral e2e"
}`, govAddr, consumerID, bondDenom, title)
}

// setConsumerRefusal records or withdraws val's refusal of the consumer and
// waits for the tx to commit.
func (s *IntegrationTestSuite) setConsumerRefusal(consumerID string, refused bool) {
	stdout, stderr, err := s.dockerExec(s.providerValRes[0].Container.ID, []string{
		providerBinary, "tx", "provider", "set-consumer-refusal", consumerID, strconv.FormatBool(refused),
		"--from", "val",
		"--home", providerHomePath,
		"--keyring-backend", "test",
		"--chain-id", s.cfg.providerChainID,
		"--fees", "10000" + bondDenom,
		"-y", "-o", "json",
	})
	s.Require().NoErrorf(err, "failed to set refusal=%v: stderr=%s", refused, stderr.String())
	s.requireTxCommitted(stdout.Bytes())
}

// resumeProposalJSON builds a MsgResumeConsumer gov proposal body.
func (s *IntegrationTestSuite) resumeProposalJSON(consumerID, title string) string {
	return fmt.Sprintf(`{
  "messages": [{
    "@type": "/vaas.provider.v1.MsgResumeConsumer",
    "authority": %q,
    "consumer_id": %q
  }],
  "metadata": "ipfs://test",
  "deposit": "10000000%s",
  "title": %q,
  "summary": "refusal e2e resume"
}`, s.queryGovAuthority(), consumerID, bondDenom, title)
}

// testConsumerRefusalPauseAndResume drives the stake-weighted refusal pause:
// the single validator's refusal is 100% of bonded power, over the one-third
// threshold, so the consumer pauses. A governance resume against the standing
// coalition is refused (the proposal passes its vote but its message fails),
// withdrawing the refusal alone resumes nothing, and a resume voted after the
// withdrawal brings the consumer back and keeps it back.
func (s *IntegrationTestSuite) testConsumerRefusalPauseAndResume() {
	s.Run("refusal: threshold pause, resume refused while standing, resume after withdrawal", func() {
		const consumerID = "0"

		s.Require().Equal("CONSUMER_PHASE_LAUNCHED", s.queryProviderConsumerPhase(consumerID))

		s.T().Log("signaling refusal from val (100% of bonded power)...")
		s.setConsumerRefusal(consumerID, true)

		s.T().Log("waiting for the EndBlock evaluation to pause the consumer...")
		s.Require().Eventuallyf(func() bool {
			return s.queryProviderConsumerPhase(consumerID) == "CONSUMER_PHASE_PAUSED"
		}, time.Minute, 2*time.Second,
			"consumer was not paused after a 100%% refusal")

		s.T().Log("voting a resume while the refusal stands: the vote passes, the resume is refused...")
		s.submitProposalWithVote(s.resumeProposalJSON(consumerID, "Resume consumer (refusal standing)"),
			"yes", "PROPOSAL_STATUS_FAILED")
		s.Require().Equal("CONSUMER_PHASE_PAUSED", s.queryProviderConsumerPhase(consumerID),
			"a resume against a standing coalition must leave the consumer paused")

		s.T().Log("withdrawing the refusal: nothing resumes on its own...")
		s.setConsumerRefusal(consumerID, false)
		s.Require().Never(func() bool {
			return s.queryProviderConsumerPhase(consumerID) != "CONSUMER_PHASE_PAUSED"
		}, 16*time.Second, 4*time.Second, "withdrawing a refusal must not resume the consumer by itself")

		s.T().Log("resuming the consumer via governance now that the coalition is gone...")
		s.submitAndPassProposal(s.resumeProposalJSON(consumerID, "Resume consumer (refusal withdrawn)"))
		s.Require().Eventuallyf(func() bool {
			return s.queryProviderConsumerPhase(consumerID) == "CONSUMER_PHASE_LAUNCHED"
		}, time.Minute, 2*time.Second,
			"consumer did not resume after governance approval")

		// Still launched over the next blocks: the withdrawn signal stays
		// withdrawn, so nothing re-pauses the consumer.
		s.Require().Never(func() bool {
			return s.queryProviderConsumerPhase(consumerID) != "CONSUMER_PHASE_LAUNCHED"
		}, 16*time.Second, 4*time.Second, "the resumed consumer must stay launched")
	})
}

// testDowntimeDeferralBehindRejectedRemoval drives the downtime deferral: a
// pending slash whose maturity falls inside a removal vote is extended once
// instead of executing, and executes after the vote is rejected.
func (s *IntegrationTestSuite) testDowntimeDeferralBehindRejectedRemoval() {
	s.Run("downtime deferral: matured slash waits out a rejected removal vote", func() {
		const consumerID = "0"

		// val2 (bonded and permanently silent since the downtime test) keeps
		// generating accusations; wait for a fresh pending slash whose
		// maturity is still far enough out to time a vote around.
		var target pendingDowntimeSlashJSON
		var targetMaturity time.Time
		s.Require().Eventuallyf(func() bool {
			for _, p := range s.queryPendingDowntimeSlashes(consumerID) {
				maturity, err := time.Parse(time.RFC3339Nano, p.MaturesAt)
				if err != nil {
					continue
				}
				// A fresh entry matures downtime_challenge_window (30s) after
				// acceptance; leave room to submit the vote 20s before that.
				if !p.MaturesAtExtended && time.Until(maturity) > 22*time.Second {
					target = p
					targetMaturity = maturity
					return true
				}
			}
			return false
		}, 5*time.Minute, 5*time.Second,
			"no fresh pending downtime slash to defer")

		// Time the removal vote so its voting period covers the maturity:
		// submitted 20s before it, the proposal lands well before the entry
		// matures and its 30s vote ends well after.
		wait := time.Until(targetMaturity) - 20*time.Second
		s.T().Logf("waiting %s so the vote window covers the slash maturity...", wait)
		if wait > 0 {
			time.Sleep(wait)
		}
		s.T().Log("submitting a removal proposal that will be rejected...")
		s.submitAndRejectProposal(s.removalProposalJSON(consumerID, "Remove consumer (deferral e2e, to reject)"))

		// The matured entry must have been deferred, not executed: same window
		// end, extended flag set, maturity pushed past the voting end.
		s.Require().Eventuallyf(func() bool {
			for _, p := range s.queryPendingDowntimeSlashes(consumerID) {
				if p.WindowStartHeight == target.WindowStartHeight && p.MaturesAtExtended {
					return true
				}
			}
			return false
		}, time.Minute, 2*time.Second,
			"the matured pending slash was not deferred behind the removal vote")

		// After the rejection and the deferral margin, it executes.
		s.T().Log("waiting for the deferred slash to execute after the rejected vote...")
		s.Require().Eventuallyf(func() bool {
			for _, p := range s.queryPendingDowntimeSlashes(consumerID) {
				if p.WindowStartHeight == target.WindowStartHeight {
					return false
				}
			}
			return true
		}, 2*time.Minute, 5*time.Second,
			"the deferred slash never executed after the vote was rejected")

		s.Require().Equal("CONSUMER_PHASE_LAUNCHED", s.queryProviderConsumerPhase(consumerID),
			"a rejected removal must leave the consumer launched")
	})
}

// testEquivocationDeferralBehindRejectedRemoval drives the full pending-
// punishment lifecycle for the rejected-vote branch: fabricated double-vote
// evidence queues the punishment and jails the validator immediately, an
// undelegation started while pending is held, the matured punishment defers
// behind a removal vote, and after the vote is rejected it executes: slash,
// tombstone, holds released.
func (s *IntegrationTestSuite) testEquivocationDeferralBehindRejectedRemoval() {
	s.Run("equivocation: queue, defer behind rejected removal, execute", func() {
		const consumerID = "0"

		s.T().Log("bonding the equivocating validator...")
		valoper, priv := s.createEquivocatingValidator("eqval", "5000000"+bondDenom)
		accAddr := s.mustKeyAddress("eqval")

		tokensBefore := s.mustValidatorTokens(valoper)

		s.T().Log("fabricating and submitting double-vote evidence signed with eqval's real key...")
		evidencePath, headerPath := s.fabricateDoubleVote(priv, s.cfg.consumerChainID, 1_000_000, "eqval")
		s.submitDoubleVoteEvidence(consumerID, evidencePath, headerPath)

		s.T().Log("verifying the punishment queued and the validator was jailed, not slashed...")
		var executesAt time.Time
		s.Require().Eventuallyf(func() bool {
			pending, err := s.queryPendingEquivocations(consumerID)
			if err != nil || len(pending) == 0 {
				return false
			}
			executesAt = pending[0].ExecutesAt
			return true
		}, time.Minute, 2*time.Second, "the equivocation punishment never queued")

		jailed, _ := s.stakingValidatorState(valoper)
		s.Require().True(jailed, "the accused validator must be jailed at queue time")
		s.Require().Equal(tokensBefore, s.mustValidatorTokens(valoper),
			"no stake may move at queue time; the slash waits out the delay")

		s.T().Log("starting an undelegation and verifying it is held...")
		s.unbondFrom("eqval", valoper, "1000000"+bondDenom)
		s.Require().Eventuallyf(func() bool {
			return s.singleUnbondingEntryHas(accAddr, valoper, "1")
		}, time.Minute, 2*time.Second,
			"the undelegation started while pending was not put on hold")
		tokensHeld := s.mustValidatorTokens(valoper)
		unbondingHeld := s.mustUnbondingBalance(accAddr, valoper)

		// Time the removal vote so its voting period covers the maturity:
		// submitted 20s before it, the proposal lands well before the entry
		// matures and its 30s vote ends well after.
		wait := time.Until(executesAt) - 20*time.Second
		s.T().Logf("waiting %s so the vote window covers the punishment maturity...", wait)
		if wait > 0 {
			time.Sleep(wait)
		}
		s.T().Log("submitting a removal proposal that will be rejected...")
		s.submitAndRejectProposal(s.removalProposalJSON(consumerID, "Remove consumer (equivocation e2e, to reject)"))

		s.Require().Eventuallyf(func() bool {
			pending, err := s.queryPendingEquivocations(consumerID)
			return err == nil && len(pending) == 1 && pending[0].Extended
		}, time.Minute, 2*time.Second,
			"the matured punishment was not deferred behind the removal vote")

		s.T().Log("waiting for the punishment to execute after the rejected vote...")
		s.Require().Eventuallyf(func() bool {
			return s.pendingEquivocationsEmpty(consumerID)
		}, 2*time.Minute, 5*time.Second,
			"the deferred punishment never executed after the vote was rejected")

		consAddr := sdk.ConsAddress(priv.PubKey().Address()).String()
		s.Require().Eventuallyf(func() bool {
			return s.signingInfoTombstoned(consAddr)
		}, time.Minute, 2*time.Second, "the validator was not tombstoned at execution")

		// The double-sign fraction is 5% (see the provider genesis patch in
		// e2e_setup_test.go). The slash is sized over the bonded tokens plus
		// the held undelegation, the undelegation pays its own share, and the
		// rest comes out of the bonded tokens, jailed validator or not.
		tokensAfter := s.mustValidatorTokens(valoper)
		s.Require().Equalf(tokensHeld-tokensHeld*5/100, tokensAfter,
			"execution must slash 5%% of the bonded tokens (held=%d, after=%d)", tokensHeld, tokensAfter)
		s.Require().Eventuallyf(func() bool {
			return s.singleUnbondingEntryHas(accAddr, valoper, "0")
		}, time.Minute, 2*time.Second,
			"the unbonding hold was not released after execution")
		s.Require().Equalf(unbondingHeld-unbondingHeld*5/100, s.mustUnbondingBalance(accAddr, valoper),
			"execution must slash 5%% of the held undelegation (held=%d)", unbondingHeld)

		s.Require().Equal("CONSUMER_PHASE_LAUNCHED", s.queryProviderConsumerPhase(consumerID),
			"a rejected removal must leave the consumer launched")
	})
}

// testEquivocationQueuedBeforeRemoval queues a punishment against a second
// fabricated equivocator right before the suite's explicit removal of the
// consumer, so testEquivocationCancelledByRemoval can observe the passed vote
// cancelling it.
func (s *IntegrationTestSuite) testEquivocationQueuedBeforeRemoval() {
	s.Run("equivocation: queue a punishment the upcoming removal will cancel", func() {
		const consumerID = "0"

		valoper, priv := s.createEquivocatingValidator("eqval2", "5000000"+bondDenom)
		s.eqval2Valoper = valoper
		s.eqval2AccAddr = s.mustKeyAddress("eqval2")

		evidencePath, headerPath := s.fabricateDoubleVote(priv, s.cfg.consumerChainID, 1_000_001, "eqval2")
		s.submitDoubleVoteEvidence(consumerID, evidencePath, headerPath)

		s.Require().Eventuallyf(func() bool {
			pending, err := s.queryPendingEquivocations(consumerID)
			return err == nil && len(pending) == 1
		}, time.Minute, 2*time.Second, "the second punishment never queued")
		jailed, _ := s.stakingValidatorState(valoper)
		s.Require().True(jailed, "eqval2 must be jailed at queue time")

		// An in-flight undelegation to observe the hold release on cancel.
		// The stake to compare against after the cancellation is read once
		// the undelegation has moved its tokens out.
		s.unbondFrom("eqval2", valoper, "1000000"+bondDenom)
		s.Require().Eventuallyf(func() bool {
			return s.singleUnbondingEntryHas(s.eqval2AccAddr, valoper, "1")
		}, time.Minute, 2*time.Second, "eqval2's undelegation was not held")
		s.eqval2TokensBefore = s.mustValidatorTokens(valoper)
	})
}

// testEquivocationCancelledByRemoval asserts, after the suite's removal
// proposal has passed and stopped the consumer, that the condemned chain took
// its evidence down with it: the pending punishment is gone, the hold is
// released, the stake is intact, and the validator can unjail.
func (s *IntegrationTestSuite) testEquivocationCancelledByRemoval() {
	s.Run("equivocation: a passed removal cancels the pending punishment", func() {
		const consumerID = "0"

		s.Require().Eventuallyf(func() bool {
			return s.pendingEquivocationsEmpty(consumerID)
		}, time.Minute, 2*time.Second,
			"a passed removal must cancel the consumer's pending punishments")
		s.Require().Empty(s.mustPendingEquivocations(consumerID))

		s.Require().Eventuallyf(func() bool {
			return s.singleUnbondingEntryHas(s.eqval2AccAddr, s.eqval2Valoper, "0")
		}, time.Minute, 2*time.Second,
			"cancellation must release eqval2's unbonding hold")

		tokensAfter := s.mustValidatorTokens(s.eqval2Valoper)
		s.Require().Equalf(s.eqval2TokensBefore, tokensAfter,
			"cancellation must leave eqval2's stake untouched (before=%d, after=%d)",
			s.eqval2TokensBefore, tokensAfter)

		s.T().Log("unjailing eqval2 now that the cancelled punishment opened the jail...")
		s.mustUnjail("eqval2", s.eqval2Valoper)
	})
}

// mustKeyAddress returns the bech32 account address of a keyring key.
func (s *baseTestSuite) mustKeyAddress(key string) string {
	stdout, _, err := s.dockerExec(s.providerValRes[0].Container.ID, []string{
		providerBinary, "keys", "show", key, "-a",
		"--home", providerHomePath,
		"--keyring-backend", "test",
	})
	s.Require().NoError(err, "failed to get %s address", key)
	return strings.TrimSpace(stdout.String())
}

// mustValidatorTokens reads the validator's tokens as an integer.
func (s *baseTestSuite) mustValidatorTokens(valoper string) int64 {
	_, tokens := s.stakingValidatorState(valoper)
	v, err := strconv.ParseInt(tokens, 10, 64)
	s.Require().NoErrorf(err, "unparsable validator tokens %q", tokens)
	return v
}
