package e2e

// e2e_consumer_liveness_test.go exercises the liveness scenarios introduced by
// issue #36 that require a full provider+consumer network:
//
//   1. testLivenessTransientOutage - brief outage + valset change; the consumer
//                                    stays LAUNCHED and re-syncs via snapshot.
//   2. testLivenessRemoval         - explicit governance removal transitions the
//                                    consumer to STOPPED.
//
// Consumer-side safe mode (the MsgFilterDecorator restricting txs while the
// consumer is in debt or its VSC is stale) is not duplicated here: the ante
// gate's allow/block logic is covered deterministically by the ante unit tests
// (x/vaas/consumer/ante), the debt path end-to-end by testConsumerDebtFlow, and
// the real VSC-staleness safe mode end-to-end by the short-unbonding
// LivenessIntegrationTestSuite.
//
// HARNESS CONSTRAINTS
//
// These tests share the single IntegrationTestSuite spun up by TestVAAS in
// e2e_test.go.  That consumer's unbonding_period is ~20 days (from
// testdata/create_consumer.json), so the derived liveness grace period
// (unbonding * 0.66) is ~13 days.  Waiting for an automatic STOPPED transition
// in real-time is therefore impractical in CI.
//
// For testLivenessRemoval we issue an explicit remove-consumer via governance
// (gov authority is required since PR #53), then assert the provider transitions
// to STOPPED.  The STOPPED -> DELETED leg relies on removal_time elapsing, which
// needs a short-unbonding consumer -- exercised by LivenessIntegrationTestSuite.
//
// For testLivenessTransientOutage we pause the consumer container (same
// mechanism as testDowntimeSlash) for a brief window and verify the consumer
// stays LAUNCHED and its VP re-converges after recovery.  This exercises the
// snapshot-resync path introduced in issue #36.
//
// Sequencing in TestVAAS:
//   testLivenessTransientOutage runs after testValidatorSetSync (non-destructive VP change).
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
		// Safety net: always unpause on exit so a failure before the explicit
		// unpause below cannot leave the container paused and cascade into the
		// next test (a paused container rejects docker exec). Errors ignored:
		// the happy path already unpaused it, so this is a no-op then.
		defer func() { _ = s.dkrPool.Client.UnpauseContainer(s.consumerValRes[0].Container.ID) }()

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
			// Delegate at least one unit of voting power (tokens / powerReduction,
			// default 1e6) so the validator's integer power actually increases; a
			// smaller delegation truncates to +0 power and the VSC never changes.
			providerBinary, "tx", "staking", "delegate", valoperAddr, "100000000" + bondDenom,
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
