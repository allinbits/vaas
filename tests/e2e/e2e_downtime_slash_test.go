package e2e

// e2e_downtime_slash_test.go exercises the VAAS-owned downtime detection and
// slashing flow end-to-end:
//
//   1. testDowntimeSlash's first sub-test pauses the *entire* single-node
//      consumer chain container. With no consumer blocks being produced at
//      all, nobody's vote is individually recorded as missing, so this is a
//      negative control: whole-chain downtime must never queue or execute a
//      slash for the still-live provider validator.
//   2. testDowntimeSlashQueueThenExecute bonds a second, permanently-silent
//      provider validator (see createSilentValidator) that VAAS syncs into
//      the consumer's validator set (VAAS has no opt-in/opt-out -- every
//      bonded validator validates every consumer) but which never actually
//      runs a consumer node. Real per-validator missed-block evidence
//      therefore accumulates for it on the single existing consumer node,
//      without needing a second physical consumer container. This proves the
//      full queue-then-execute lifecycle: a PendingDowntimeSlash is queued
//      (stake untouched) and the validator is excluded from that epoch's fee
//      share, then -- once the downtime challenge window matures -- the next
//      BeginBlock sweep executes the slash and the pending entry disappears.
//
// Both sub-tests rely on the shortened downtime/challenge-window genesis
// params configured in e2e_setup_test.go's patchProviderGenesis.

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"cosmossdk.io/math"

	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
)

// Downtime parameters this suite's provider genesis patches in
// patchProviderGenesis (e2e_setup_test.go); the negative control below
// derives its provable waits from them instead of guessing at wall-clock
// durations.
const (
	// downtimeSignedBlocksWindow is the consumer's tumbling-window size in
	// consumer blocks (provider genesis signed_blocks_window, echoed into the
	// consumer genesis at launch).
	downtimeSignedBlocksWindow = 30
	// downtimeChallengeWindow mirrors the provider genesis
	// downtime_challenge_window: how long an accepted downtime slash stays
	// pending (and challengeable) before the BeginBlock sweep executes it.
	downtimeChallengeWindow = 30 * time.Second
	// downtimeEvidenceRelaySlack is the allowance for a window-close evidence
	// packet to be relayed to and accepted by the provider, on top of the
	// challenge window, when proving that no evidence ever arrived.
	downtimeEvidenceRelaySlack = 30 * time.Second
)

func (s *IntegrationTestSuite) testDowntimeSlash() {
	s.Run("no downtime slash, consumer down", func() {
		const consumerID = "0"

		valoperAddr, tokensBefore := s.getProviderValidatorTokens()
		s.Require().False(tokensBefore.IsZero(), "validator should have tokens before downtime test")

		jailed := s.isProviderValidatorJailed()
		s.Require().False(jailed, "validator should not be jailed before downtime test")

		s.T().Log("pausing consumer container to simulate downtime...")
		err := s.dkrPool.Client.PauseContainer(s.consumerValRes[0].Container.ID)
		s.Require().NoError(err, "failed to pause consumer container")

		time.Sleep(10 * time.Second)

		s.T().Log("unpausing consumer container...")
		err = s.dkrPool.Client.UnpauseContainer(s.consumerValRes[0].Container.ID)
		s.Require().NoError(err, "failed to unpause consumer container")

		var heightAfterUnpause int64
		s.Require().Eventuallyf(func() bool {
			h, err := s.queryConsumerBlockHeight()
			if err != nil || h <= 0 {
				return false
			}
			heightAfterUnpause = h
			return true
		}, time.Minute, 2*time.Second,
			"consumer RPC never came back after unpausing the container")

		// The accusation window is provably closed once the consumer commits
		// past the boundary of the tumbling window containing every height the
		// outage could have touched: heights are contiguous across the pause,
		// so they all sit at or below heightAfterUnpause, whose window
		// [S, S+W-1] is evaluated while block S+W executes (the vote for
		// height h is delivered in block h+1); every earlier window closed at
		// an earlier height still. Any evidence the outage could ever produce
		// is queued consumer-side by that block.
		windowCloseHeight := (heightAfterUnpause/downtimeSignedBlocksWindow + 1) * downtimeSignedBlocksWindow
		s.T().Logf("waiting for the consumer to provably close the accusation window (height %d, closes at %d)...",
			heightAfterUnpause, windowCloseHeight)
		// Up to a full window of consumer blocks may need to be produced
		// first, so the cap is sized for the suite's ~5s consumer blocks.
		s.Require().Eventuallyf(func() bool {
			h, err := s.queryConsumerBlockHeight()
			return err == nil && h > windowCloseHeight
		}, 5*time.Minute, 5*time.Second,
			"consumer never closed the accusation window covering the outage (unpause height %d, close height %d)",
			heightAfterUnpause, windowCloseHeight)

		// Whole-chain downtime halts block production entirely, so nobody's
		// vote is individually recorded as missing and no evidence may reach
		// the provider: the pending-downtime-slashes queue must stay empty for
		// the entire challenge window plus relay slack after the window close.
		// That is long enough that wrongly queued evidence would have been
		// relayed, accepted, and still be sitting out its challenge window --
		// pending and visible -- while this watch runs.
		s.T().Log("verifying no pending downtime slash is ever queued while the challenge window matures...")
		s.Require().Neverf(func() bool {
			return len(s.queryPendingDowntimeSlashes(consumerID)) > 0
		}, downtimeChallengeWindow+downtimeEvidenceRelaySlack, 3*time.Second,
			"a pending downtime slash was queued for consumer %s during whole-chain downtime", consumerID)

		s.T().Log("verifying validator tokens are unchanged now that the challenge window has matured...")
		tokensAfter, err := s.getProviderValidatorTokensByAddr(valoperAddr)
		s.Require().NoError(err, "failed to read validator tokens after the challenge window")
		s.Require().Truef(tokensAfter.Equal(tokensBefore),
			"validator tokens changed during whole consumer chain downtime (before: %s, after: %s, valoper: %s)",
			tokensBefore, tokensAfter, valoperAddr)

		s.T().Log("verifying validator was not jailed after the downtime window...")
		jailed = s.isProviderValidatorJailed()
		s.Require().False(jailed, "validator should not be jailed after whole-chain downtime")
	})

	s.testDowntimeSlashQueueThenExecute()
}

// testDowntimeSlashQueueThenExecute proves the queue-then-execute downtime
// lifecycle end-to-end using a second, permanently-silent validator (see
// createSilentValidator):
//
//  1. Evidence acceptance queues a PendingDowntimeSlash and excludes the
//     validator from that epoch's fee share -- without touching its stake.
//  2. Once the downtime challenge window matures, the next BeginBlock sweep
//     executes the slash (stake strictly decreases) and the pending entry is
//     removed.
func (s *IntegrationTestSuite) testDowntimeSlashQueueThenExecute() {
	s.Run("queue-then-execute downtime slash for a permanently silent validator", func() {
		const consumerID = "0"

		// Fund generously so the epoch fee distribution this test asserts on
		// (val2's share withheld) has a pool to draw from regardless of
		// whatever testConsumerDebtFlow already deposited and regardless of
		// subtest filtering (e.g. running only this subtest via
		// `go test -run .../queue-then-execute`, which skips the sibling debt
		// test that would otherwise have funded it).
		s.providerFundConsumerFeePool(consumerID, "20000000"+feeDenom)

		s.T().Log("bonding a second, permanently-silent validator on the provider...")
		_, val2Valoper := s.createSilentValidator("val2", "5000000"+bondDenom)

		s.T().Log("waiting for the silent validator to sync into the consumer's validator set...")
		s.Require().Eventuallyf(func() bool {
			vals, err := s.queryConsumerNetValidators()
			return err == nil && len(vals) >= 2
		}, 3*time.Minute, 5*time.Second,
			"consumer never synced the second validator into its validator set")

		tokensBefore, err := s.getProviderValidatorTokensByAddr(val2Valoper)
		s.Require().NoError(err, "failed to read val2 tokens before downtime evidence")
		s.Require().True(tokensBefore.IsPositive(), "val2 should have a positive stake before downtime evidence")

		s.T().Log("waiting for the provider to queue a pending downtime slash for the silent validator...")
		s.Require().Eventuallyf(func() bool {
			return len(s.queryPendingDowntimeSlashes(consumerID)) > 0
		}, 6*time.Minute, 5*time.Second,
			"provider never queued a pending downtime slash for consumer %s", consumerID)

		s.T().Log("verifying the pending slash has not yet touched val2's stake...")
		tokensPending, err := s.getProviderValidatorTokensByAddr(val2Valoper)
		s.Require().NoError(err, "failed to read val2 tokens while the downtime slash is pending")
		s.Require().Truef(tokensPending.Equal(tokensBefore),
			"val2 stake must be unchanged while the downtime slash is only pending (before=%s, pending=%s)",
			tokensBefore, tokensPending)

		// Downtime exclusion is only in effect for the single epoch
		// distribution immediately following evidence acceptance: the
		// exclusion flag is cleared at every epoch boundary, and evidence
		// re-submissions are rejected while a slash is pending. Epochs are 5
		// blocks (~5s) while the pending-slash poll above ticks every 5s, so
		// balance snapshots race the one excluded distribution -- a snapshot
		// taken just after it would observe val2 legitimately earning again
		// in the next epoch. Assert the exclusion through its persistent
		// artifact instead: the withheld-fee record written when the excluded
		// share stays in the consumer's pool, which lives for the full
		// downtime challenge window (30s) before being swept.
		s.T().Log("verifying val2's fee share was withheld for the epoch with pending downtime evidence...")
		s.Require().Eventuallyf(func() bool {
			return len(s.queryWithheldFeeRecords(consumerID)) > 0
		}, 90*time.Second, 3*time.Second,
			"no withheld fee record appeared for consumer %s; val2 was never excluded from an epoch fee distribution", consumerID)

		s.T().Log("waiting for the downtime challenge window to mature and the sweep to execute the slash...")
		s.Require().Eventuallyf(func() bool {
			tokensNow, err := s.getProviderValidatorTokensByAddr(val2Valoper)
			return err == nil && tokensNow.LT(tokensBefore)
		}, 3*time.Minute, 5*time.Second,
			"val2's stake was never reduced by the matured downtime slash")

		s.T().Log("verifying the pending downtime slash entry is cleared after execution...")
		s.Require().Eventuallyf(func() bool {
			return len(s.queryPendingDowntimeSlashes(consumerID)) == 0
		}, 60*time.Second, 3*time.Second,
			"pending downtime slash entry for consumer %s was not cleared after execution", consumerID)
	})
}

// createSilentValidator bonds a new validator on the provider chain that
// never runs any consumer node under its own key. `key` is added to the
// keyring purely to derive account/operator addresses; it is funded from
// "val" and self-delegated against a freshly generated ed25519 consensus
// pubkey whose private half is discarded immediately (see
// generateEd25519PubKeyJSON) -- nothing ever signs with it.
//
// Because VAAS syncs the entire bonded set to every consumer with no
// opt-in/opt-out, this validator's absence from actual consensus becomes
// real, provider-verifiable downtime evidence on the consumer once its
// tumbling window closes, without needing a second physical consumer node.
// Returns the new validator's account and operator (valoper) bech32
// addresses.
func (s *IntegrationTestSuite) createSilentValidator(key, selfBondAmount string) (accAddr, valoperAddr string) {
	containerID := s.providerValRes[0].Container.ID

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
	accAddr = strings.TrimSpace(stdout.String())

	stdout, _, err = s.dockerExec(containerID, []string{
		providerBinary, "keys", "show", key, "--bech", "val", "-a",
		"--home", providerHomePath,
		"--keyring-backend", "test",
	})
	s.Require().NoError(err, "failed to get %s operator address", key)
	valoperAddr = strings.TrimSpace(stdout.String())

	// Fund the new key enough to cover the self-delegation plus tx fees.
	s.providerFundAddress(accAddr, "10000000"+bondDenom)

	validatorJSON := fmt.Sprintf(`{
  "pubkey": %s,
  "amount": %q,
  "moniker": %q,
  "commission-rate": "0.1",
  "commission-max-rate": "0.2",
  "commission-max-change-rate": "0.01",
  "min-self-delegation": "1"
}`, generateEd25519PubKeyJSON(), selfBondAmount, key)

	payload := base64.StdEncoding.EncodeToString([]byte(validatorJSON))
	s.dockerExecMust(containerID, []string{
		"sh", "-c", fmt.Sprintf("echo %s | base64 -d > /tmp/%s.json", payload, key),
	})

	stdout, stderr, err := s.dockerExec(containerID, []string{
		providerBinary, "tx", "staking", "create-validator", "/tmp/" + key + ".json",
		"--from", key,
		"--home", providerHomePath,
		"--keyring-backend", "test",
		"--chain-id", providerChainID,
		"--gas", "auto",
		"--gas-adjustment", "1.5",
		"--fees", "10000" + bondDenom,
		"-y",
		"-o", "json",
	})
	s.Require().NoErrorf(err, "failed to submit create-validator for %s: stderr=%s", key, stderr.String())
	s.requireTxCommitted(stdout.Bytes())

	s.Require().Eventuallyf(func() bool {
		vals, verr := s.queryProviderValidators()
		if verr != nil {
			return false
		}
		for _, v := range vals {
			if v.OperatorAddress == valoperAddr {
				return v.Status == stakingtypes.Bonded
			}
		}
		return false
	}, 30*time.Second, 2*time.Second,
		"validator %s (%s) never bonded on the provider", key, valoperAddr)

	return accAddr, valoperAddr
}

// generateEd25519PubKeyJSON generates a fresh ed25519 keypair, discards the
// private half (see createSilentValidator -- it never needs to sign
// anything), and returns the public half as an inline Cosmos SDK Any-JSON
// pubkey blob suitable for `tx staking create-validator`'s validator.json.
func generateEd25519PubKeyJSON() string {
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		panic(fmt.Sprintf("failed to generate ed25519 pubkey: %v", err))
	}
	return fmt.Sprintf(`{"@type":"/cosmos.crypto.ed25519.PubKey","key":%q}`,
		base64.StdEncoding.EncodeToString(pub))
}

// pendingDowntimeSlashJSON mirrors the fields this test needs from the
// `pending-downtime-slashes` CLI query's JSON output.
type pendingDowntimeSlashJSON struct {
	ConsumerId  string `json:"consumer_id"`
	SlashTokens string `json:"slash_tokens"`
	MaturesAt   string `json:"matures_at"`
}

// queryPendingDowntimeSlashes returns the pending downtime slashes queued for
// a consumer, awaiting the challenge window before execution. Returns nil on
// any query/decode error so callers can poll it directly inside Eventually.
func (s *IntegrationTestSuite) queryPendingDowntimeSlashes(consumerID string) []pendingDowntimeSlashJSON {
	stdout, _, err := s.dockerExec(s.providerValRes[0].Container.ID, []string{
		providerBinary, "query", "provider", "pending-downtime-slashes", consumerID,
		"--home", providerHomePath,
		"--output", "json",
	})
	if err != nil {
		return nil
	}
	var res struct {
		Slashes []pendingDowntimeSlashJSON `json:"slashes"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &res); err != nil {
		return nil
	}
	return res.Slashes
}

// withheldFeeRecordJSON mirrors the fields this test needs from the
// `withheld-fee-records` CLI query's JSON output.
type withheldFeeRecordJSON struct {
	ConsumerId       string `json:"consumer_id"`
	ProviderConsAddr string `json:"provider_cons_addr"`
}

// queryWithheldFeeRecords returns the fee shares currently withheld from
// validators for a consumer following a downtime-driven fee exclusion.
// Returns nil on any query/decode error so callers can poll it directly
// inside Eventually.
func (s *IntegrationTestSuite) queryWithheldFeeRecords(consumerID string) []withheldFeeRecordJSON {
	stdout, _, err := s.dockerExec(s.providerValRes[0].Container.ID, []string{
		providerBinary, "query", "provider", "withheld-fee-records", consumerID,
		"--home", providerHomePath,
		"--output", "json",
	})
	if err != nil {
		return nil
	}
	var res struct {
		Records []withheldFeeRecordJSON `json:"records"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &res); err != nil {
		return nil
	}
	return res.Records
}

func (s *baseTestSuite) patchConsumerSlashingParams() {
	s.patchGenesisJSON(s.consumer.dataDir+"/config/genesis.json", func(genesis map[string]any) {
		appState, ok := genesis["app_state"].(map[string]any)
		if !ok {
			return
		}
		slashing, ok := appState["slashing"].(map[string]any)
		if !ok {
			slashing = make(map[string]any)
		}
		params, ok := slashing["params"].(map[string]any)
		if !ok {
			params = make(map[string]any)
		}
		params["signed_blocks_window"] = "5"
		params["min_signed_per_window"] = "0.050000000000000000"
		params["slash_fraction_downtime"] = "0.000000000000000000"
		params["downtime_jail_duration"] = "60s"
		slashing["params"] = params
		appState["slashing"] = slashing
	})
}

func (s *IntegrationTestSuite) isProviderValidatorJailed() bool {
	vals, err := s.queryProviderValidators()
	if err != nil {
		return false
	}
	for _, v := range vals {
		if v.Jailed {
			return true
		}
	}
	return false
}

// getProviderValidatorTokens returns the first bonded validator's operator address and token amount.
func (s *IntegrationTestSuite) getProviderValidatorTokens() (string, math.Int) {
	vals, err := s.queryProviderValidators()
	if err != nil {
		return "", math.ZeroInt()
	}
	for _, v := range vals {
		if v.Status == stakingtypes.Bonded {
			return v.OperatorAddress, v.Tokens
		}
	}
	return "", math.ZeroInt()
}

// getProviderValidatorTokensByAddr returns the token amount for a specific validator by operator address.
func (s *IntegrationTestSuite) getProviderValidatorTokensByAddr(valoperAddr string) (math.Int, error) {
	vals, err := s.queryProviderValidators()
	if err != nil {
		return math.ZeroInt(), err
	}
	for _, v := range vals {
		if v.OperatorAddress == valoperAddr {
			return v.Tokens, nil
		}
	}
	return math.ZeroInt(), fmt.Errorf("validator %s not found", valoperAddr)
}
