package e2e

// e2e_consumer_liveness_test.go exercises the three liveness-removal scenarios
// introduced by issue #36:
//
//   1. testLivenessRemoval         - sustained outage causes LAUNCHED -> STOPPED -> DELETED.
//   2. testLivenessTransientOutage - brief outage + valset change; consumer
//                                    stays LAUNCHED and re-syncs after grace.
//   3. testLivenessSafeMode        - while VSC is stale, consumer rejects bank
//                                    sends; after a fresh VSC it accepts them.
//
// HARNESS CONSTRAINTS
//
// These tests share the single IntegrationTestSuite spun up by TestVAAS in
// e2e_test.go.  That consumer's unbonding_period is ~20 days (from
// testdata/create_consumer.json), so the derived liveness grace period
// (unbonding * 0.66) is ~13 days.  Waiting for an automatic STOPPED transition
// in real-time is therefore impractical in CI.
//
// For testLivenessRemoval we approximate by issuing an explicit
// remove-consumer tx (which immediately stops the consumer), then asserting the
// provider transitions through STOPPED and eventually to DELETED.  The STOPPED
// -> DELETED leg also relies on time elapsing (the removal_time set at stop),
// so in a normal CI run the consumer will stay STOPPED until a follow-up epoch
// advances the chain past removal_time.  We therefore assert STOPPED as the
// terminal state within the test window, and note that a proper timing-faithful
// run would need a short unbonding_period consumer (e.g. 60s -> grace ~40s).
//
// For testLivenessTransientOutage we pause the consumer container (same
// mechanism as testDowntimeSlash) for a brief window and verify the consumer
// stays LAUNCHED and its VP re-converges after recovery.  This exercises the
// snapshot-resync path introduced in issue #36.
//
// For testLivenessSafeMode we verify that the consumer's MsgFilterDecorator
// rejects bank sends while in restricted mode (debt or stale VSC), and accepts
// them once normal conditions are restored.  The DefaultSafeModeThreshold for
// VSC staleness is 3 hours (hard-coded), so we trigger restricted mode via the
// fee-debt path instead, which exercises the same ante-handler code branch.
//
// Sequencing in TestVAAS:
//   testLivenessTransientOutage runs after testValidatorSetSync (non-destructive VP change).
//   testLivenessSafeMode        runs after testConsumerDebtFlow (reuses the debt cycle).
//   testLivenessRemoval         runs second-to-last, before testGenesisRoundTrip.

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// queryConsumerPhase returns the phase string for a given consumer ID from the
// provider chain (e.g. "CONSUMER_PHASE_LAUNCHED").
func (s *IntegrationTestSuite) queryConsumerPhase(consumerID string) string {
	stdout, _, err := s.dockerExec(s.providerValRes[0].Container.ID, []string{
		providerBinary, "query", "provider", "consumer-chain", consumerID,
		"--home", providerHomePath,
		"--output", "json",
	})
	if err != nil {
		s.T().Logf("queryConsumerPhase(%s): exec error: %v", consumerID, err)
		return ""
	}
	var res struct {
		Phase string `json:"phase"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &res); err != nil {
		s.T().Logf("queryConsumerPhase(%s): decode error: %v (raw: %s)", consumerID, err, stdout.String())
		return ""
	}
	return res.Phase
}

// testLivenessTransientOutage verifies snapshot resync: a brief consumer
// outage (shorter than the liveness grace period) does not stop the consumer.
// After recovery the consumer's consensus VP must re-converge with the
// provider's updated VP.
//
// Issue #36 context: the snapshot resync sends the full current valset (not
// just a delta) when a reconnecting consumer's last-known VSC ID lags behind.
func (s *IntegrationTestSuite) testLivenessTransientOutage() {
	s.Run("liveness transient outage: consumer stays LAUNCHED and re-syncs valset", func() {
		const consumerID = "0"

		// Record provider VP before the outage.
		providerValsBefore, err := s.queryProviderNetValidators()
		s.Require().NoError(err, "failed to query provider validators before outage")
		s.Require().NotEmpty(providerValsBefore, "provider must have at least one validator")
		_, vpsBefore := s.extractPubKeys(providerValsBefore)
		s.Require().NotEmpty(vpsBefore)
		initialProviderVP := vpsBefore[0]

		// Pause the consumer to simulate a transient network outage.
		s.T().Log("pausing consumer container (transient outage starts)...")
		err = s.dkrPool.Client.PauseContainer(s.consumerValRes[0].Container.ID)
		s.Require().NoError(err, "failed to pause consumer container")

		// While the consumer is paused, delegate extra stake on the provider so
		// it emits a VSC packet for the VP change.
		s.T().Log("delegating extra stake on provider to generate a valset change...")
		stdout, _, err := s.dockerExec(s.providerValRes[0].Container.ID, []string{
			providerBinary, "keys", "show", "val", "--bech", "val", "-a",
			"--home", providerHomePath,
			"--keyring-backend", "test",
		})
		s.Require().NoError(err, "failed to get validator operator address")
		valoperAddr := strings.TrimSpace(stdout.String())

		_, _, err = s.dockerExec(s.providerValRes[0].Container.ID, []string{
			providerBinary, "tx", "staking", "delegate", valoperAddr, "500000" + bondDenom,
			"--from", "user",
			"--home", providerHomePath,
			"--keyring-backend", "test",
			"--chain-id", providerChainID,
			"--fees", "10000" + bondDenom,
			"-y",
		})
		s.Require().NoError(err, "failed to delegate extra stake on provider")

		// Wait for provider to reflect the new VP.
		var newProviderVP uint64
		s.Require().Eventuallyf(func() bool {
			vals, err := s.queryProviderNetValidators()
			if err != nil || len(vals) == 0 {
				return false
			}
			_, vps := s.extractPubKeys(vals)
			if len(vps) == 0 {
				return false
			}
			newProviderVP = vps[0]
			return newProviderVP > initialProviderVP
		}, 30*time.Second, 2*time.Second,
			"provider VP did not increase after delegation (before=%d)", initialProviderVP)
		s.T().Logf("provider VP changed: %d -> %d", initialProviderVP, newProviderVP)

		// Restore the consumer (outage ends well before grace expires).
		s.T().Log("unpausing consumer container (outage ends)...")
		err = s.dkrPool.Client.UnpauseContainer(s.consumerValRes[0].Container.ID)
		s.Require().NoError(err, "failed to unpause consumer container")

		// The consumer must still be LAUNCHED after the transient outage.
		phase := s.queryConsumerPhase(consumerID)
		s.Require().Equalf("CONSUMER_PHASE_LAUNCHED", phase,
			"consumer %s must remain LAUNCHED after a transient outage", consumerID)

		// Consumer VP must re-converge to the updated provider VP via snapshot resync.
		s.T().Log("waiting for consumer valset to re-converge via snapshot resync...")
		s.Require().Eventuallyf(func() bool {
			consumerVals, err := s.queryConsumerNetValidators()
			if err != nil || len(consumerVals) == 0 {
				return false
			}
			_, consumerVPs := s.extractPubKeys(consumerVals)
			if len(consumerVPs) == 0 {
				return false
			}
			s.T().Logf("consumer VP: %d (want %d)", consumerVPs[0], newProviderVP)
			return consumerVPs[0] == newProviderVP
		}, 3*time.Minute, 3*time.Second,
			"consumer VP did not converge to provider VP %d after snapshot resync", newProviderVP)
	})
}

// testLivenessSafeMode verifies that the consumer's MsgFilterDecorator enters
// restricted mode when the consumer is in debt or has a stale validator set
// (the two conditions share the same code path in the ante handler), and exits
// restricted mode after recovery.
//
// Issue #36 context: the safe-mode gate allows only /ibc.core.* and
// /cosmos.gov.* messages through.  A bank send is rejected in restricted mode
// but accepted in normal mode.
//
// Approximation note: VSC staleness (DefaultSafeModeThreshold = 3h) cannot be
// triggered within a short test run.  Debt (empty fee pool) uses the same
// restricted-mode branch and can be triggered within epochs.  This test
// therefore relies on the debt path to trigger restricted mode.
func (s *IntegrationTestSuite) testLivenessSafeMode() {
	s.Run("liveness safe mode: bank send rejected while restricted, accepted after recovery", func() {
		// Wait for the consumer to enter restricted mode (debt due to empty fee pool).
		s.T().Log("waiting for consumer to enter restricted mode...")
		s.Require().Eventuallyf(func() bool {
			out, err := s.consumerBankSendDryRun()
			return err != nil || strings.Contains(out, "consumer chain is in debt") ||
				strings.Contains(out, "stale validator set")
		}, 3*time.Minute, 5*time.Second,
			"consumer did not enter restricted mode; fee pool may not be empty")
		s.T().Log("restricted mode confirmed; bank sends are now rejected")

		// A /cosmos.gov.* message must NOT be blocked by the admission gate in
		// restricted mode.  We probe this with a dry-run gov submit-proposal and
		// verify the rejection reason (if any) does not come from the gate.
		user := s.consumerUserBech32()
		_, govStderr, _ := s.dockerExec(s.consumerValRes[0].Container.ID, []string{
			consumerBinary, "tx", "gov", "submit-proposal", "/dev/null",
			"--from", user,
			"--home", consumerHomePath,
			"--keyring-backend", "test",
			"--chain-id", consumerChainID,
			"--fees", "1000" + bondDenom,
			"--dry-run",
			"-y",
		})
		govErrStr := govStderr.String()
		s.Require().False(
			strings.Contains(govErrStr, "consumer chain is in debt") ||
				strings.Contains(govErrStr, "stale validator set"),
			"gov tx must not be rejected by the admission gate in restricted mode; stderr=%q",
			govErrStr,
		)

		// Fund the fee pool to restore normal mode.
		s.T().Log("re-funding consumer fee pool to restore normal mode...")
		s.providerFundConsumerFeePool("0", "20000000"+feeDenom)

		// After funding, bank sends must be accepted again.
		s.T().Log("waiting for consumer to exit restricted mode after re-fund...")
		s.Require().Eventuallyf(func() bool {
			out, err := s.consumerBankSendDryRun()
			if err != nil {
				return false
			}
			return !strings.Contains(out, "consumer chain is in debt") &&
				!strings.Contains(out, "stale validator set")
		}, 2*time.Minute, 5*time.Second,
			"consumer did not exit restricted mode after fee pool was re-funded")
		s.T().Log("consumer back in normal mode; bank sends accepted")
	})
}

// testLivenessRemoval verifies the LAUNCHED -> STOPPED lifecycle when a
// consumer is explicitly removed via MsgRemoveConsumer.
//
// Issue #36 context: the redesign makes explicit removal (and the liveness
// sweep) the only way to stop a consumer.  Timed-out VSC packets no longer
// trigger removal.  This test exercises the explicit removal path.
//
// Ordering: this test must run after all other sub-tests that depend on
// consumer "0" being LAUNCHED, and before testGenesisRoundTrip (which still
// succeeds because verifyExportedGenesis only requires consumer fields to be
// present, not a specific phase).
//
// Approximation note: the STOPPED -> DELETED transition requires
// removal_time to elapse (unbonding * 0.66).  With the 20-day unbonding period
// used by the shared suite this takes ~13 days, so within the test window the
// consumer will remain STOPPED.  A timing-faithful CI test would use a
// short-unbonding consumer (60s -> grace ~40s) launched in a dedicated suite.
func (s *IntegrationTestSuite) testLivenessRemoval() {
	s.Run("liveness removal: explicit remove-consumer transitions to STOPPED", func() {
		const consumerID = "0"

		// Precondition: consumer must still be LAUNCHED.
		phase := s.queryConsumerPhase(consumerID)
		s.Require().Equalf("CONSUMER_PHASE_LAUNCHED", phase,
			"consumer %s must be LAUNCHED before the liveness removal test", consumerID)

		// Issue an explicit remove-consumer via governance (gov authority is required
		// since PR #53; a direct --from val tx would fail DeliverTx).
		s.T().Log("submitting remove-consumer governance proposal...")
		govAddr := s.queryGovAuthority()
		removeJSON := fmt.Sprintf(`{
  "messages": [{
    "@type": "/vaas.provider.v1.MsgRemoveConsumer",
    "authority": %q,
    "consumer_id": %q
  }],
  "metadata": "ipfs://test",
  "deposit": "10000000%s",
  "title": "Remove consumer %s",
  "summary": "e2e liveness removal test"
}`, govAddr, consumerID, bondDenom, consumerID)
		s.submitAndPassProposal(removeJSON)

		// Wait for the provider to reflect the STOPPED phase.
		s.T().Log("waiting for provider to move consumer to STOPPED or DELETED...")
		s.Require().Eventuallyf(func() bool {
			p := s.queryConsumerPhase(consumerID)
			s.T().Logf("consumer %s phase: %s", consumerID, p)
			return p == "CONSUMER_PHASE_STOPPED" || p == "CONSUMER_PHASE_DELETED"
		}, 2*time.Minute, 5*time.Second,
			"provider did not transition consumer %s to STOPPED/DELETED after remove-consumer",
			consumerID)

		finalPhase := s.queryConsumerPhase(consumerID)
		s.T().Logf("consumer %s terminal phase: %s", consumerID, finalPhase)
		s.Require().True(
			finalPhase == "CONSUMER_PHASE_STOPPED" || finalPhase == "CONSUMER_PHASE_DELETED",
			"expected STOPPED or DELETED, got %q", finalPhase,
		)
	})
}
