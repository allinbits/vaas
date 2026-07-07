package e2e

// e2e_liveness_suite_test.go contains the LivenessIntegrationTestSuite, a
// Docker-based e2e suite that exercises liveness behaviour. The liveness grace
// period is unbonding * LivenessGraceFraction; this suite sets the fraction to
// 0.75 in provider genesis so the grace (~150s) is observable within a CI run,
// while keeping a long-enough provider/consumer unbonding (200s) that the
// relayer-derived IBC client trusting period (unbonding * 0.66 = ~132s) stays
// viable. Shrinking the grace via a short unbonding instead would collapse the
// trusting period and the relayer could never establish the clients.
//
// Two infrastructure measures keep the timing-sensitive assertions reliable:
//   - Fast blocks (patchConfigToml lowers the CometBFT timeouts to ~1s blocks).
//     The provider seeds lastAck at consumer launch and starts the grace clock
//     then, so every setup step that precedes the first VSC sync -- relayer
//     add-path, the first relay cycle, the recv/ack round-trip -- must finish
//     inside the grace. At the default ~5s block time these add up to minutes
//     and the consumer is swept before it ever syncs.
//   - A first-sync gate (waitForConsumerSync) that blocks setup until the
//     provider's lastAck has actually advanced past the launch seed, i.e. the
//     relayer is delivering and the consumer is acking. Assertions then run from
//     a known-synced state, independent of relayer startup jitter.
//
// The existing IntegrationTestSuite uses a ~21d provider unbonding, making the
// liveness sweep untestable in real time. This suite launches its own isolated
// set of containers (distinct chain IDs, Docker network, and host ports) and
// registers a single consumer via create_consumer_short_unbonding.json which
// carries a 200s consumer unbonding (200000000000 ns), 10m vaas_timeout
// (600000000000 ns), and 5s safe_mode_threshold (5000000000 ns).
//
// Test ordering within TestLivenessVAAS:
//   1. testRecoverBeforeGrace  - brief pause < grace (~150s); consumer stays LAUNCHED.
//   2. testRealSafeMode        - relayer paused > 5s; consumer enters restricted
//                                mode; bank send rejected; relayer unpaused; accepted.
//   3. testLivenessQuery       - QueryConsumerLiveness via CLI; last_ack_time recent,
//                                non-zero grace, removal_eta present.
//   4. testForcedTimeoutSnapshotResync - relayer paused > the short (20s) provider
//                                vaas_timeout while the consumer keeps producing
//                                blocks, so a VSC packet genuinely times out; the
//                                consumer stays LAUNCHED (demoted OnTimeout) and
//                                heals via a snapshot resync (asserted via the
//                                provider timeout log and the consumer's
//                                snapshot-resync event).
//   5. testAutoSweepRemoval    - relayer stopped indefinitely; poll until STOPPED.
//
// The STOPPED -> DELETED edge (deletion at stop-time + unbonding) is unit-tested
// (TestSweepRemovesStaleConsumer), not exercised here -- see the note above
// testAutoSweepRemoval's trailing comment.

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ory/dockertest/v3"
	"github.com/stretchr/testify/suite"
)

const (
	livenessProviderChainID = "provider-liveness"
	livenessConsumerChainID = "consumer-liveness"
	livenessDockerNetwork   = "vaas-liveness-testnet"

	// Host port block: offset +10 from the existing suite (26657/9090/1317/26656)
	// to avoid conflicts when both suites run in the same Docker host.
	livenessProviderRPCPort  = "26757"
	livenessProviderGRPCPort = "9190"
	livenessProviderRESTPort = "1417"
	livenessProviderP2PPort  = "26756"

	livenessConsumerRPCPort  = "26767"
	livenessConsumerGRPCPort = "9192"
	livenessConsumerRESTPort = "1427"
	livenessConsumerP2PPort  = "26766"
)

// LivenessIntegrationTestSuite mirrors IntegrationTestSuite with two key
// differences:
//   - provider genesis is patched with unbonding_time "200s" and
//     liveness_grace_fraction "0.75" (grace ~150s, trusting period ~132s)
//   - consumer is registered via create_consumer_short_unbonding.json
//     (200s unbonding, 10m vaas_timeout, 5s safe_mode_threshold)
type LivenessIntegrationTestSuite struct {
	baseTestSuite
}

// TestLivenessIntegrationTestSuite is the entry point for the short-unbonding
// liveness e2e suite.
func TestLivenessIntegrationTestSuite(t *testing.T) {
	suite.Run(t, new(LivenessIntegrationTestSuite))
}

// SetupSuite brings up provider + consumer + ts-relayer with short timers.
func (s *LivenessIntegrationTestSuite) SetupSuite() {
	s.T().Log("setting up liveness e2e suite (short unbonding)...")

	s.cfg = baseSuiteConfig{
		providerChainID:  livenessProviderChainID,
		consumerChainID:  livenessConsumerChainID,
		dockerNetwork:    livenessDockerNetwork,
		providerInitName: "liveness-provider-init",
		consumerInitName: "liveness-consumer-init",
		tmpDirPrefix:     "vaas-liveness-",
		providerRPCPort:  livenessProviderRPCPort,
		providerGRPCPort: livenessProviderGRPCPort,
		providerRESTPort: livenessProviderRESTPort,
		providerP2PPort:  livenessProviderP2PPort,
		consumerRPCPort:  livenessConsumerRPCPort,
		consumerGRPCPort: livenessConsumerGRPCPort,
		consumerRESTPort: livenessConsumerRESTPort,
		consumerP2PPort:  livenessConsumerP2PPort,

		consumerTemplateFile:        "create_consumer_short_unbonding.json",
		consumerTemplatePlaceholder: "CONSUMER_SHORT_CHAIN_ID",

		patchProviderGenesis: func(appState map[string]any) {
			if gov, ok := appState["gov"].(map[string]any); ok {
				if params, ok := gov["params"].(map[string]any); ok {
					params["voting_period"] = "15s"
				}
			}

			if provider, ok := appState["provider"].(map[string]any); ok {
				if params, ok := provider["params"].(map[string]any); ok {
					// One block per epoch so a VSC packet (and its ack) flows every
					// block. Combined with the fast block time (see patchConfigToml)
					// this refreshes the provider's lastAck every few seconds, well
					// inside the grace, so a healthy consumer is never swept between
					// acks.
					params["blocks_per_epoch"] = "1"
					params["fees_per_block_amount"] = "1000"
					// Short VSC packet timeout so testForcedTimeoutSnapshotResync can
					// make a packet actually time out within a CI run (the relayer is
					// paused while the consumer keeps producing blocks past this
					// deadline). Now possible since the MinVAASTimeoutPeriod floor was
					// dropped. Timeouts are log-only, so this does not perturb the
					// other liveness tests.
					params["vaas_timeout_period"] = "20s"
					// Shrink the liveness grace fraction so the grace period
					// (unbonding * fraction = 200s * 0.75 = ~150s) is observable in
					// a CI run, while keeping the unbonding itself long enough that
					// the relayer-derived IBC client trusting period (unbonding *
					// 0.66 = ~132s) survives the relayer add-path window.
					//
					// The grace must exceed the time from consumer launch to first
					// VSC sync (the provider seeds lastAck at launch, so the clock
					// starts before the relayer has delivered anything). With fast
					// blocks that first sync is ~60-90s; 150s leaves margin. The
					// suite also waits for that first sync explicitly before
					// asserting (see waitForConsumerSync).
					params["liveness_grace_fraction"] = "0.75"
				}
			}

			// Keep unbonding long enough for a viable IBC client trusting period
			// (see liveness_grace_fraction note above); the short grace comes from
			// the fraction, not from a short unbonding.
			if staking, ok := appState["staking"].(map[string]any); ok {
				if params, ok := staking["params"].(map[string]any); ok {
					params["unbonding_time"] = "200s"
				}
			}
		},

		// Fast blocks so add-path, epochs, and VSC delivery all complete well
		// inside the grace (see patchConfigToml).
		patchProviderConfigToml: s.patchConfigToml,
		patchConsumerConfigToml: s.patchConfigToml,
	}

	var err error

	s.dkrPool, err = dockertest.NewPool("")
	s.Require().NoError(err, "failed to create docker pool")
	s.dkrPool.MaxWait = 5 * time.Minute

	s.cleanupStaleContainers()

	s.dkrNet, err = s.dkrPool.CreateNetwork(livenessDockerNetwork)
	s.Require().NoError(err, "failed to create liveness docker network")

	s.T().Log("step 1: initializing liveness provider chain...")
	s.provider = &chain{id: s.cfg.providerChainID}
	s.initAndStartProvider()

	s.T().Log("step 2: registering consumer on liveness provider...")
	s.registerConsumerOnProvider()

	s.T().Log("step 3: fetching consumer genesis from liveness provider...")
	consumerGenesisJSON := s.fetchConsumerGenesis()

	s.T().Log("step 4: initializing liveness consumer chain...")
	s.consumer = &chain{id: s.cfg.consumerChainID}
	s.initAndStartConsumer(consumerGenesisJSON)

	s.T().Log("step 5: starting ts-relayer for liveness suite...")
	s.setupTSRelayer()

	s.T().Log("step 6: waiting for the consumer to sync its first VSC...")
	s.waitForConsumerSync("0")

	s.T().Log("liveness e2e suite setup complete!")
}

// TestLivenessVAAS sequences the liveness tests in a safe order: non-destructive
// tests (recover, safe-mode, query) before the destructive sweep/delete tests.
func (s *LivenessIntegrationTestSuite) TestLivenessVAAS() {
	s.testRecoverBeforeGrace()
	s.testRealSafeMode()
	s.testLivenessQuery()
	s.testForcedTimeoutSnapshotResync()
	s.testAutoSweepRemoval()
}

// ---- test methods ----------------------------------------------------------

// testRecoverBeforeGrace pauses the consumer for ~10s (far less than the ~150s
// grace), unpauses it, and asserts the consumer remains LAUNCHED. This
// exercises the ack-refreshing code path: as long as an ack arrives before
// grace expires, the clock resets and the consumer is not swept.
func (s *LivenessIntegrationTestSuite) testRecoverBeforeGrace() {
	s.Run("recover before grace: consumer stays LAUNCHED after brief outage", func() {
		const consumerID = "0"

		// Non-fatal diagnostic: log the consumer's liveness state at the start of
		// the run so the ack cadence (last_ack vs removal_eta) is visible even if
		// a later assertion fails. The dedicated check is testLivenessQuery.
		livenessURL := fmt.Sprintf("http://localhost:%s/vaas/provider/consumer_liveness/%s",
			s.cfg.providerRESTPort, consumerID)
		if body, err := httpGet(livenessURL); err == nil {
			s.T().Logf("diagnostic: initial consumer liveness: %s", string(body))
		}

		phase := s.queryProviderConsumerPhase(consumerID)
		s.Require().Equalf("CONSUMER_PHASE_LAUNCHED", phase,
			"consumer %s must be LAUNCHED before recover-before-grace test", consumerID)

		s.T().Log("pausing consumer container for ~10s (far less than ~150s grace)...")
		err := s.dkrPool.Client.PauseContainer(s.consumerValRes[0].Container.ID)
		s.Require().NoError(err, "failed to pause consumer container")

		time.Sleep(10 * time.Second)

		s.T().Log("unpausing consumer container (outage ends before grace expiry)...")
		err = s.dkrPool.Client.UnpauseContainer(s.consumerValRes[0].Container.ID)
		s.Require().NoError(err, "failed to unpause consumer container")

		// Allow a few epochs for an ack to arrive and reset the liveness clock.
		time.Sleep(10 * time.Second)

		// Diagnostic: capture the liveness state at the moment of assertion so a
		// failure shows lastAck/removal_eta vs the sweep, not just the phase.
		if body, err := httpGet(livenessURL); err == nil {
			s.T().Logf("diagnostic: post-recovery consumer liveness: %s", string(body))
		}

		phase = s.queryProviderConsumerPhase(consumerID)
		s.Require().Equalf("CONSUMER_PHASE_LAUNCHED", phase,
			"consumer %s must remain LAUNCHED after a transient outage shorter than grace", consumerID)
		s.T().Log("consumer remains LAUNCHED after recovery before grace")
	})
}

// testRealSafeMode freezes VSC delivery so the consumer goes >5s without a VSC
// packet (the safe_mode_threshold). The consumer's MsgFilterDecorator then
// enters restricted mode and rejects bank sends. Once VSC delivery resumes, the
// consumer exits restricted mode.
//
// This exercises the VSC-staleness path directly (unlike the existing suite
// which relies on the fee-debt path as an approximation, because its
// safe_mode_threshold is 3h).
//
// Delivery is frozen by *pausing the relayer container* (docker pause), not by
// purging and rebuilding it. The distinction is essential: a rebuild runs
// add-path, which creates brand-new IBC clients. The provider, however, keeps
// using its already-discovered client as long as that client stays Active and
// has a counterparty (see discoverActiveConsumerClient) -- which it does for
// the ~132s trusting period. So a rebuild would leave the provider sending VSCs
// to the original, now-unrelayed client and the consumer would never recover
// within grace. Pausing the relayer leaves the original client untouched, so on
// unpause the relay loop resumes on the same client and delivery continues. It
// also keeps the outage short, decoupling this consumer-side test from the
// provider-side liveness grace. The consumer keeps producing blocks throughout
// (its staleness clock is wall-time on its own block height), which is why we
// pause the relayer rather than the consumer.
func (s *LivenessIntegrationTestSuite) testRealSafeMode() {
	s.Run("real safe mode: VSC-stale consumer rejects bank sends, recovers when delivery resumes", func() {
		const consumerID = "0"

		// Ensure the fee pool is funded so debt does not contaminate the test.
		s.T().Log("funding consumer fee pool to avoid debt interference...")
		s.providerFundConsumerFeePool(consumerID, "20000000"+feeDenom)

		// Verify normal mode before freezing delivery.
		s.Require().Eventuallyf(func() bool {
			out, err := s.consumerBankSendDryRun()
			if err != nil {
				return false
			}
			return !strings.Contains(out, "consumer chain is in debt") &&
				!strings.Contains(out, "stale validator set")
		}, 2*time.Minute, 5*time.Second,
			"consumer did not enter normal mode after fee pool funding")

		s.T().Log("pausing relayer container so VSC packets are no longer relayed...")
		err := s.dkrPool.Client.PauseContainer(s.tsRelayerResource.Container.ID)
		s.Require().NoError(err, "failed to pause relayer container")

		// Wait for the consumer to enter restricted mode (VSC stale after >5s).
		s.T().Log("waiting for consumer to enter restricted mode (VSC stale after ~5s)...")
		s.Require().Eventuallyf(func() bool {
			out, err := s.consumerBankSendDryRun()
			return err != nil || strings.Contains(out, "stale validator set") ||
				strings.Contains(out, "consumer chain is in debt")
		}, 2*time.Minute, 3*time.Second,
			"consumer did not enter restricted mode after VSC staleness")
		s.T().Log("restricted mode confirmed; bank sends are rejected")

		// Resume VSC delivery by unpausing the relayer (same client, no rebuild).
		s.T().Log("unpausing relayer container to resume VSC delivery...")
		err = s.dkrPool.Client.UnpauseContainer(s.tsRelayerResource.Container.ID)
		s.Require().NoError(err, "failed to unpause relayer container")

		// Wait for the consumer to exit restricted mode.
		s.T().Log("waiting for consumer to exit restricted mode after VSC delivery resumes...")
		s.Require().Eventuallyf(func() bool {
			out, err := s.consumerBankSendDryRun()
			if err != nil {
				return false
			}
			return !strings.Contains(out, "stale validator set") &&
				!strings.Contains(out, "consumer chain is in debt")
		}, 3*time.Minute, 5*time.Second,
			"consumer did not exit restricted mode after VSC delivery resumed")
		s.T().Log("consumer back in normal mode after VSC delivery")
	})
}

// testLivenessQuery queries the provider's QueryConsumerLiveness via the CLI
// (there is no dedicated liveness CLI subcommand; we use the gRPC-gateway REST
// path which is exercised by the main suite's httpGet helper) and asserts that
// last_ack_time is recent (within the last 5 minutes), grace_period is non-zero,
// and removal_eta is present.
//
// The gRPC-gateway REST path is:
//
//	GET /vaas/provider/consumer_liveness/{consumer_id}
//
// The response JSON uses protobuf JSON encoding: Timestamps are RFC3339 strings
// and Durations are strings like "19.8s".
func (s *LivenessIntegrationTestSuite) testLivenessQuery() {
	s.Run("liveness query: last_ack_time recent, grace_period non-zero, removal_eta present", func() {
		const consumerID = "0"

		// Confirm the consumer is still LAUNCHED before checking liveness.
		phase := s.queryProviderConsumerPhase(consumerID)
		s.Require().Equalf("CONSUMER_PHASE_LAUNCHED", phase,
			"consumer must be LAUNCHED for liveness query test")

		// Diagnostic: log provider staking params and consumer chain init_params so
		// the controller run can confirm the genesis patches actually took effect.
		stakingOut, _, _ := s.dockerExec(s.providerValRes[0].Container.ID, []string{
			providerBinary, "query", "staking", "params",
			"--home", providerHomePath, "--output", "json",
		})
		s.T().Logf("diagnostic: provider staking params: %s", stakingOut.String())

		chainOut, _, _ := s.dockerExec(s.providerValRes[0].Container.ID, []string{
			providerBinary, "query", "provider", "consumer-chain", consumerID,
			"--home", providerHomePath, "--output", "json",
		})
		s.T().Logf("diagnostic: consumer chain (init_params): %s", chainOut.String())

		// Query liveness via the gRPC-gateway REST path.
		// The response Timestamps are RFC3339 strings; Duration is a string like "19.8s".
		livenessURL := fmt.Sprintf("http://localhost:%s/vaas/provider/consumer_liveness/%s",
			s.cfg.providerRESTPort, consumerID)

		var livenessRes struct {
			LastAckTime string `json:"last_ack_time"`
			GracePeriod string `json:"grace_period"`
			RemovalEta  string `json:"removal_eta"`
		}

		s.Require().Eventuallyf(func() bool {
			body, err := httpGet(livenessURL)
			if err != nil {
				s.T().Logf("liveness REST query attempt failed: %v", err)
				return false
			}
			s.T().Logf("liveness REST raw body: %s", string(body))
			if jsonErr := json.Unmarshal(body, &livenessRes); jsonErr != nil {
				s.T().Logf("liveness REST decode failed: %v (body: %s)", jsonErr, string(body))
				return false
			}
			return livenessRes.LastAckTime != "" && livenessRes.GracePeriod != ""
		}, 30*time.Second, 3*time.Second,
			"liveness REST query did not return a valid response")

		s.T().Logf("diagnostic: consumer liveness: last_ack=%s grace=%s removal_eta=%s",
			livenessRes.LastAckTime, livenessRes.GracePeriod, livenessRes.RemovalEta)

		// last_ack_time must be parseable and within 5 minutes of now.
		lastAck, err := time.Parse(time.RFC3339Nano, livenessRes.LastAckTime)
		if err != nil {
			// gRPC-gateway may omit fractional seconds for round values.
			lastAck, err = time.Parse(time.RFC3339, livenessRes.LastAckTime)
		}
		s.Require().NoError(err, "last_ack_time is not a valid RFC3339 timestamp: %s", livenessRes.LastAckTime)
		s.Require().WithinDurationf(time.Now().UTC(), lastAck, 5*time.Minute,
			"last_ack_time %s is not recent", livenessRes.LastAckTime)

		// grace_period must be non-empty and non-zero.
		s.Require().NotEmpty(livenessRes.GracePeriod, "grace_period must be non-empty")
		s.Require().NotEqualf("0s", livenessRes.GracePeriod, "grace_period must be non-zero")

		// removal_eta must be present and parseable.
		s.Require().NotEmpty(livenessRes.RemovalEta, "removal_eta must be present")
		retaAck, err := time.Parse(time.RFC3339Nano, livenessRes.RemovalEta)
		if err != nil {
			retaAck, err = time.Parse(time.RFC3339, livenessRes.RemovalEta)
		}
		s.Require().NoError(err, "removal_eta is not a valid RFC3339 timestamp: %s", livenessRes.RemovalEta)
		s.Require().Truef(retaAck.After(time.Now().Add(-24*time.Hour)),
			"removal_eta %s looks implausibly old", livenessRes.RemovalEta)
	})
}

// testForcedTimeoutSnapshotResync proves the two behaviours the main suite's
// transient-outage smoke cannot: that a genuinely timed-out VSC packet does NOT
// remove the consumer (the demoted OnTimeout), and that a consumer that fell
// behind heals via a snapshot resync (not a resent diff).
//
// A real IBC timeout requires the packet to expire on the *consumer's* clock
// while still undelivered. Pausing the consumer would freeze that clock, so
// instead the relayer is paused while the consumer keeps producing blocks: its
// clock advances past the short vaas_timeout_period (20s) with packets
// undelivered. On unpause the relayer submits MsgTimeout for the expired packets
// (the provider's OnTimeout fires, log-only) and delivers a snapshot to the
// now-behind consumer.
func (s *LivenessIntegrationTestSuite) testForcedTimeoutSnapshotResync() {
	s.Run("forced timeout: demoted OnTimeout keeps consumer LAUNCHED; behind consumer heals via snapshot", func() {
		const consumerID = "0"

		s.Require().Equalf("CONSUMER_PHASE_LAUNCHED", s.queryProviderConsumerPhase(consumerID),
			"consumer %s must be LAUNCHED before the forced-timeout test", consumerID)

		// Pause the relayer; the consumer keeps producing blocks, so its clock
		// advances past the 20s packet timeout while VSC packets go undelivered.
		s.T().Log("pausing relayer for ~35s (> 20s vaas_timeout) to force VSC packet timeouts...")
		s.Require().NoError(s.dkrPool.Client.PauseContainer(s.tsRelayerResource.Container.ID),
			"failed to pause relayer container")
		time.Sleep(35 * time.Second)

		s.T().Log("unpausing relayer; it submits MsgTimeout for expired packets and delivers a snapshot...")
		s.Require().NoError(s.dkrPool.Client.UnpauseContainer(s.tsRelayerResource.Container.ID),
			"failed to unpause relayer container")

		// (a) The demoted OnTimeout must not have removed the consumer.
		s.Require().Eventuallyf(func() bool {
			return s.queryProviderConsumerPhase(consumerID) == "CONSUMER_PHASE_LAUNCHED"
		}, 30*time.Second, 3*time.Second,
			"consumer %s must remain LAUNCHED despite VSC packet timeouts", consumerID)
		s.T().Log("consumer remained LAUNCHED through the timeouts")

		// (b) Prove a real timeout actually fired -- otherwise (a) is vacuous.
		// The provider logs the demoted OnTimeout handler.
		s.Require().Eventuallyf(func() bool {
			return strings.Contains(s.providerLogs(), "packet timeout, retrying next epoch")
		}, 30*time.Second, 3*time.Second,
			"provider never logged a VSC packet timeout; the timeout path was not exercised")
		s.T().Log("provider processed a demoted VSC timeout (OnTimeout is log-only)")

		// (c) Prove the behind consumer healed via a SNAPSHOT (not a resent diff):
		// the consumer logs "applied snapshot resync" (and emits the matching
		// event) only when it applies an is_snapshot packet.
		s.Require().Eventuallyf(func() bool {
			return strings.Contains(s.consumerLogs(), "applied snapshot resync")
		}, 2*time.Minute, 5*time.Second,
			"consumer never applied a snapshot resync after recovery")
		s.T().Log("consumer applied a snapshot resync after recovery")
	})
}

// testAutoSweepRemoval stops the relayer (and pauses the consumer) so no VSC
// acks return to the provider. After the liveness grace period (~150s) expires,
// the provider's SweepUnresponsiveConsumers moves the consumer to
// CONSUMER_PHASE_STOPPED. Polls with a ~2min window.
func (s *LivenessIntegrationTestSuite) testAutoSweepRemoval() {
	s.Run("auto sweep removal: sustained outage causes LAUNCHED -> STOPPED", func() {
		const consumerID = "0"

		phase := s.queryProviderConsumerPhase(consumerID)
		s.Require().Equalf("CONSUMER_PHASE_LAUNCHED", phase,
			"consumer %s must be LAUNCHED before auto-sweep test", consumerID)

		// Diagnostic: log provider staking params and consumer liveness state so
		// the controller run can confirm the genesis patches took effect.
		stakingOut, _, _ := s.dockerExec(s.providerValRes[0].Container.ID, []string{
			providerBinary, "query", "staking", "params",
			"--home", providerHomePath, "--output", "json",
		})
		s.T().Logf("diagnostic: provider staking params: %s", stakingOut.String())

		chainOut, _, _ := s.dockerExec(s.providerValRes[0].Container.ID, []string{
			providerBinary, "query", "provider", "consumer-chain", consumerID,
			"--home", providerHomePath, "--output", "json",
		})
		s.T().Logf("diagnostic: consumer chain (init_params): %s", chainOut.String())

		livenessURL := fmt.Sprintf("http://localhost:%s/vaas/provider/consumer_liveness/%s",
			s.cfg.providerRESTPort, consumerID)
		if body, err := httpGet(livenessURL); err == nil {
			s.T().Logf("diagnostic: consumer liveness: %s", string(body))
		}

		s.T().Log("purging ts-relayer so no VSC acks are returned to provider...")
		s.stopTSRelayer()

		s.T().Log("pausing consumer container to prevent acks...")
		err := s.dkrPool.Client.PauseContainer(s.consumerValRes[0].Container.ID)
		s.Require().NoError(err, "failed to pause consumer container for sweep test")

		// The grace period is provider_unbonding * liveness_grace_fraction =
		// 200s * 0.75 = ~150s. Wait for the grace period to elapse before polling,
		// so that the sweep has had time to fire by the first poll iteration.
		const gracePlusBuffer = 160 * time.Second
		s.T().Logf("outage started; waiting %s for grace period to elapse...", gracePlusBuffer)
		time.Sleep(gracePlusBuffer)

		// Poll until the provider sweeps the consumer to STOPPED.
		// Allow up to 2 minutes total (grace already elapsed above).
		s.T().Log("polling for CONSUMER_PHASE_STOPPED (timeout 2min)...")
		s.Require().Eventuallyf(func() bool {
			p := s.queryProviderConsumerPhase(consumerID)
			s.T().Logf("consumer %s phase: %s", consumerID, p)
			return p == "CONSUMER_PHASE_STOPPED"
		}, 2*time.Minute, 5*time.Second,
			"provider did not sweep consumer %s to STOPPED within 2 minutes", consumerID)

		s.T().Logf("consumer %s successfully swept to STOPPED", consumerID)
	})
}

// Note on STOPPED -> DELETED: the sweep schedules deletion at
// blockTime + providerUnbonding, the same path a governance removal takes.
// With a viable (relayer-survivable) ~200s unbonding, the real DELETED edge
// would only fire ~200s after STOPPED and so is deliberately not exercised
// here -- it is covered by TestSweepRemovesStaleConsumer, which asserts the
// removal_time scheduling directly. This e2e suite's contribution is proving
// the LAUNCHED -> STOPPED sweep fires end-to-end under real IBC silence.
