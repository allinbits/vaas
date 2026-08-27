package e2e

// e2e_downtime_challenge_test.go exercises MsgChallengeConsumerDowntime
// against real chain data: the CLI assembles the challenge from the consumer's
// own CometBFT RPC (canonical commit for the claimed height H, light-client
// header for H+1, validator sets) and the provider verifies it on-chain
// through the 07-tendermint light client -- the one part of the challenge path
// no unit test can cover, since every unit test stubs the header verification
// out (see OverrideVerifyDowntimeChallengeHeaderForTest).
//
// What is asserted here is the rejection side, and that is a property of the
// design rather than a gap in the test: a challenge disproves a *false*
// accusation by exhibiting the accused validator's signature sealed into the
// consumer chain at a height the accusation claims it missed. The consumer
// marks a height missed only when the validator is absent from that height's
// commit (TrackMissedBlocks only sets a bit for BlockIDFlagAbsent), and the
// challenge only accepts a Commit-or-Nil signature for the same consumer
// address at the same height -- so for an honest consumer the two are mutually
// exclusive, and no challenge can ever succeed against it. A successful
// challenge (and therefore the PAUSED phase and MsgResumeConsumer, which are
// only reachable through one) requires a consumer that reports missed blocks
// it did not observe, i.e. injecting a forged evidence packet from the
// consumer side -- a Byzantine-consumer harness this suite does not have. That
// flow is covered by unit tests (TestHandleChallengeConsumerDowntime_Success,
// TestPauseConsumerChain_Success, TestResumeConsumerChain_Success).
//
// So the property proven here is the one that protects a live consumer: a
// challenge that cannot exhibit a chain-sealed signature is rejected at
// exactly that step -- after the pending-slash lookup, the bitmap check, the
// chain-id/height checks and the full light-client verification of a real
// consumer header have all passed -- the consumer is not paused, and the
// queued downtime slash still executes.

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"time"
)

// challengeMinRemainingWindow is how much of the downtime challenge window a
// candidate pending slash must have left before this test picks it: enough for
// the CLI to assemble the challenge and for the tx to be committed while the
// slash is still pending.
const challengeMinRemainingWindow = 25 * time.Second

// downtimeChallengeTarget is a pending downtime slash together with everything
// needed to challenge it: the accused validator in the address forms the
// provider and the consumer each use, one height its bitmap claims missed, and
// the token amount the slash was priced at.
type downtimeChallengeTarget struct {
	providerConsAddr string
	consumerConsAddr string
	valoper          string
	claimedHeight    int64
	slashTokens      string
}

func (s *IntegrationTestSuite) testDowntimeChallengeWithoutSealedSignature() {
	s.Run("downtime challenge without a chain-sealed signature is rejected", func() {
		const consumerID = "0"

		// A pending slash for the permanently-silent validator is what makes a
		// challenge attemptable at all; the silent validator misses every
		// window, so one is queued (and re-queued) continuously.
		s.ensureSilentValidator("val2", "5000000"+bondDenom)

		var target downtimeChallengeTarget
		s.Require().Eventuallyf(func() bool {
			t, ok := s.findDowntimeChallengeTarget(consumerID)
			if ok {
				target = t
			}
			return ok
		}, 10*time.Minute, 5*time.Second,
			"no challengeable pending downtime slash appeared for consumer %s", consumerID)
		s.T().Logf("challenging the pending downtime slash for validator %s (consumer address %s, priced at %s%s) at claimed height %d",
			target.valoper, target.consumerConsAddr, target.slashTokens, bondDenom, target.claimedHeight)

		tokensBefore, err := s.getProviderValidatorTokensByAddr(target.valoper)
		s.Require().NoError(err, "failed to read the accused validator's stake before the challenge")

		// The header submitted for claimed_height+1 must verify against a
		// consensus state the provider's consumer client already stores, at a
		// height strictly below it: the light client rejects a header at or
		// below its trusted height, so the CLI's default (the client's latest
		// tracked height) only works while the client trails the consumer tip.
		// With a relayer keeping the client at the tip, an explicit
		// --trusted-height is required.
		clientID := s.queryConsumerClientID(consumerID)
		trustedHeight, ok := s.pickChallengeTrustedHeight(clientID, target.claimedHeight+1)
		s.Require().Truef(ok,
			"no stored consensus state below consumer height %d is usable as a trusted height for client %s",
			target.claimedHeight+1, clientID)
		s.T().Logf("using trusted height %d for the claimed_height+1 header", trustedHeight)

		consumerRPC := "http://" + s.consumerValRes[0].Container.Name[1:] + ":26657"
		stdout, stderr, err := s.dockerExec(s.providerValRes[0].Container.ID, []string{
			providerBinary, "tx", "provider", "challenge-consumer-downtime",
			consumerID, target.consumerConsAddr, fmt.Sprintf("%d", target.claimedHeight),
			"--consumer-rpc", consumerRPC,
			"--trusted-height", fmt.Sprintf("%d", trustedHeight),
			"--from", "val",
			"--home", providerHomePath,
			"--keyring-backend", "test",
			"--chain-id", providerChainID,
			// An explicit gas limit: --gas auto would simulate the (deliberately
			// failing) message and never produce an estimate. The fee clears the
			// node's 0.01uatone minimum gas price for that limit.
			"--gas", "5000000",
			"--fees", "100000" + bondDenom,
			"-y",
			"-o", "json",
		})
		s.Require().NoErrorf(err, "failed to run challenge-consumer-downtime: stderr=%s", stderr.String())
		s.Require().NotZerof(stdout.Len(),
			"challenge-consumer-downtime produced no broadcast response: stderr=%s", stderr.String())

		code, rawLog := s.queryTxResult(stdout.Bytes())
		s.Require().NotZerof(code,
			"challenge for a validator that was genuinely absent must fail on-chain, but the tx succeeded: %s", rawLog)
		// The specific step is the assertion: reaching it means the pending
		// slash was found, its bitmap did claim this height, and the real
		// header for claimed_height+1 passed light-client verification and was
		// shown to seal the supplied commit.
		s.Require().Containsf(rawLog, "last_commit carries no signature for validator_addr",
			"challenge failed for the wrong reason: %s", rawLog)

		// A failed challenge changes nothing: the consumer keeps running (a
		// successful one would have paused it and cancelled every pending
		// downtime slash for it)...
		s.Require().Equalf("CONSUMER_PHASE_LAUNCHED", s.queryProviderConsumerPhase(consumerID),
			"consumer %s must stay LAUNCHED after a failed downtime challenge", consumerID)

		// ... and the queued slash still executes against the accused stake
		// once its challenge window matures.
		s.T().Log("verifying the queued downtime slash still executes after the failed challenge...")
		s.Require().Eventuallyf(func() bool {
			tokensNow, err := s.getProviderValidatorTokensByAddr(target.valoper)
			return err == nil && tokensNow.LT(tokensBefore)
		}, 4*time.Minute, 5*time.Second,
			"the pending downtime slash for %s was never executed after the failed challenge (stake still %s)",
			target.valoper, tokensBefore)

		s.Require().Equalf("CONSUMER_PHASE_LAUNCHED", s.queryProviderConsumerPhase(consumerID),
			"consumer %s must still be LAUNCHED after the downtime slash executed", consumerID)
	})
}

// findDowntimeChallengeTarget picks a pending downtime slash that can actually
// be challenged right now: enough of its challenge window is left for the
// challenge tx to land while it is still pending, the consumer has already
// committed the claimed_height+1 block the challenge needs, and the accused
// validator was in the consumer's validator set at the claimed height (the CLI
// reads its consensus pubkey from that set). Returns false when no pending
// slash currently qualifies.
func (s *IntegrationTestSuite) findDowntimeChallengeTarget(consumerID string) (downtimeChallengeTarget, bool) {
	consumerHeight, err := s.queryConsumerBlockHeight()
	if err != nil {
		return downtimeChallengeTarget{}, false
	}

	var (
		best      downtimeChallengeTarget
		bestFound bool
		bestMatur time.Time
	)
	for _, slash := range s.queryPendingDowntimeSlashDetails(consumerID) {
		maturesAt, err := time.Parse(time.RFC3339, slash.MaturesAt)
		if err != nil || time.Until(maturesAt) < challengeMinRemainingWindow {
			continue
		}
		// A slash priced at zero tokens is dropped as vacuous by the sweep
		// instead of executing, so it cannot show that a failed challenge left
		// the punishment in place.
		if slash.SlashTokens == "0" {
			continue
		}
		if bestFound && !maturesAt.After(bestMatur) {
			continue
		}

		// Every parse below is non-fatal: this runs inside an Eventually
		// condition, where failing the test from the polling goroutine would
		// only stall it until the timeout.
		windowStart, werr := strconv.ParseInt(slash.WindowStartHeight, 10, 64)
		span, serr := strconv.ParseInt(slash.Span, 10, 64)
		bitmap, berr := base64.StdEncoding.DecodeString(slash.MissedBlocksBitmap)
		if werr != nil || serr != nil || berr != nil {
			continue
		}
		claimedHeight := int64(-1)
		for i := int64(0); i < span; i++ {
			if bitmapIsSet(bitmap, i) {
				claimedHeight = windowStart + i
				break
			}
		}
		if claimedHeight <= 0 || claimedHeight+1 >= consumerHeight {
			continue
		}

		providerConsAddr, aerr := consAddrFromBase64(slash.ProviderConsAddr)
		if aerr != nil {
			continue
		}
		consumerConsAddr := s.queryValidatorConsumerAddr(consumerID, providerConsAddr)
		if consumerConsAddr == "" {
			// No assigned key: the validator validates the consumer under its
			// own provider consensus address.
			consumerConsAddr = providerConsAddr
		}

		// The accused address must have been in the consumer's validator set at
		// the claimed height: that is where the CLI reads its consensus pubkey
		// from, and after a key assignment (see e2e_key_assignment_test.go) a
		// validator's older accusations name an address it no longer uses.
		hexAddr, herr := consAddrToCometHex(consumerConsAddr)
		if herr != nil {
			continue
		}
		hexAddrs, verr := s.consumerValidatorHexAddrsAtHeight(claimedHeight)
		if verr != nil || !slices.Contains(hexAddrs, hexAddr) {
			continue
		}

		valoper := s.providerValoperForConsAddr(providerConsAddr)
		if valoper == "" {
			continue
		}

		best = downtimeChallengeTarget{
			providerConsAddr: providerConsAddr,
			consumerConsAddr: consumerConsAddr,
			valoper:          valoper,
			claimedHeight:    claimedHeight,
			slashTokens:      slash.SlashTokens,
		}
		bestFound, bestMatur = true, maturesAt
	}

	return best, bestFound
}

// pickChallengeTrustedHeight returns the highest height below headerHeight for
// which the provider's consumer client stores a consensus state and the
// consumer's validator set did not change into the next block.
//
// The second condition is what makes the assembled header verifiable: the CLI
// submits the validator set *at* the trusted height as the header's
// TrustedValidators, while the light client checks them against the stored
// consensus state's NextValidatorsHash -- the set for the following height.
// The two agree exactly when the block at the trusted height has
// validators_hash == next_validators_hash.
func (s *IntegrationTestSuite) pickChallengeTrustedHeight(clientID string, headerHeight int64) (int64, bool) {
	heights := s.queryClientConsensusStateHeights(clientID)
	slices.Sort(heights)
	slices.Reverse(heights)

	const maxCandidates = 25
	tried := 0
	for _, h := range heights {
		if h >= headerHeight {
			continue
		}
		if tried >= maxCandidates {
			break
		}
		tried++

		valsHash, nextValsHash, err := s.consumerBlockValidatorHashes(h)
		if err != nil {
			continue
		}
		if valsHash == nextValsHash {
			return h, true
		}
	}
	return 0, false
}

// queryClientConsensusStateHeights returns the revision heights of every
// consensus state the provider stores for an IBC client.
func (s *IntegrationTestSuite) queryClientConsensusStateHeights(clientID string) []int64 {
	stdout, _, err := s.dockerExec(s.providerValRes[0].Container.ID, []string{
		providerBinary, "query", "ibc", "client", "consensus-state-heights", clientID,
		"--home", providerHomePath,
		"--limit", "5000",
		"--output", "json",
	})
	s.Require().NoError(err, "failed to query consensus state heights for client %s", clientID)

	var res struct {
		ConsensusStateHeights []struct {
			RevisionHeight string `json:"revision_height"`
		} `json:"consensus_state_heights"`
	}
	s.Require().NoErrorf(json.Unmarshal(stdout.Bytes(), &res),
		"failed to decode consensus-state-heights response: %s", stdout.String())

	heights := make([]int64, 0, len(res.ConsensusStateHeights))
	for _, h := range res.ConsensusStateHeights {
		heights = append(heights, parseInt64(s.T(), h.RevisionHeight))
	}
	return heights
}

// queryConsumerClientID returns the id of the provider's IBC client tracking a
// consumer chain.
func (s *IntegrationTestSuite) queryConsumerClientID(consumerID string) string {
	stdout, _, err := s.dockerExec(s.providerValRes[0].Container.ID, []string{
		providerBinary, "query", "provider", "consumer-chain", consumerID,
		"--home", providerHomePath,
		"--output", "json",
	})
	s.Require().NoError(err, "failed to query consumer-chain %s", consumerID)

	var res struct {
		ClientID string `json:"client_id"`
	}
	s.Require().NoErrorf(json.Unmarshal(stdout.Bytes(), &res),
		"failed to decode consumer-chain response: %s", stdout.String())
	s.Require().NotEmptyf(res.ClientID, "consumer %s has no discovered IBC client yet", consumerID)
	return res.ClientID
}

// bitmapIsSet reports whether the missed-block bitmap of a downtime accusation
// marks the window offset i as missed. This mirrors vaastypes.BitmapIsSet: the
// e2e module deliberately does not depend on the chain module, and this bit
// layout is part of the evidence wire format the accusation carries.
func bitmapIsSet(bitmap []byte, i int64) bool {
	byteIdx := i / 8
	if i < 0 || byteIdx >= int64(len(bitmap)) {
		return false
	}
	return bitmap[byteIdx]&(byte(1)<<uint(i%8)) != 0
}

// downtimeSlashDetail carries the full pending-downtime-slash record this test
// needs: the accused validator, the claimed window and its missed-block
// bitmap, and when the slash matures.
type downtimeSlashDetail struct {
	ProviderConsAddr   string `json:"provider_cons_addr"`
	WindowStartHeight  string `json:"window_start_height"`
	Span               string `json:"span"`
	MissedBlocksBitmap string `json:"missed_blocks_bitmap"`
	SlashTokens        string `json:"slash_tokens"`
	MaturesAt          string `json:"matures_at"`
}

// queryPendingDowntimeSlashDetails returns the pending downtime slashes queued
// for a consumer. Returns nil on any query/decode error so callers can poll it
// directly inside Eventually.
func (s *IntegrationTestSuite) queryPendingDowntimeSlashDetails(consumerID string) []downtimeSlashDetail {
	stdout, _, err := s.dockerExec(s.providerValRes[0].Container.ID, []string{
		providerBinary, "query", "provider", "pending-downtime-slashes", consumerID,
		"--home", providerHomePath,
		"--output", "json",
	})
	if err != nil {
		return nil
	}
	var res struct {
		Slashes []downtimeSlashDetail `json:"slashes"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &res); err != nil {
		return nil
	}
	return res.Slashes
}
