package e2e

// e2e_liveness_suite_test.go contains the LivenessIntegrationTestSuite, a
// Docker-based e2e suite that exercises liveness behaviour. The liveness grace
// period is unbonding * LivenessGraceFraction; this suite sets the fraction to
// 0.375 in provider genesis so the grace (~225s) is observable within a CI run,
// while keeping a long-enough provider/consumer unbonding (600s) that the
// relayer-derived IBC client trusting period (unbonding * 0.66 = ~396s) stays
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
// carries a 600s consumer unbonding (600000000000 ns), 20s vaas_timeout
// (20000000000 ns, so consumer-sent evidence packets can genuinely expire
// within a CI-sized relayer outage -- see testEvidenceRequeueOnTimeout), and
// 5s safe_mode_threshold (5000000000 ns).
//
// Test ordering within TestLivenessVAAS:
//   1. testRecoverBeforeGrace  - brief pause < grace (~225s); consumer stays LAUNCHED.
//   2. testRealSafeMode        - relayer paused > 5s; consumer enters restricted
//                                mode; bank send rejected; relayer unpaused; accepted.
//   3. testLivenessQuery       - QueryConsumerLiveness via CLI; last_ack_time recent,
//                                non-zero grace, removal_eta present.
//   4. testForcedTimeoutSnapshotResync - relayer paused > the short (20s) provider
//                                vaas_timeout while the consumer keeps producing
//                                blocks, so a VSC packet genuinely times out; the
//                                consumer stays LAUNCHED (log-only OnTimeout) and
//                                heals via a snapshot resync (asserted via the
//                                provider timeout log and the consumer's
//                                snapshot-resync event).
//   5. testEvidenceRequeueOnTimeout - a silent second validator produces real
//                                downtime evidence; the relayer is paused across
//                                two consumer downtime windows so the evidence
//                                packets genuinely expire on the consumer's
//                                clock; on unpause the relayer submits MsgTimeout
//                                and the consumer re-queues the evidence (per
//                                window) instead of losing it, and the provider
//                                eventually accepts both windows.
//   6. testAutoSweepRemoval    - relayer stopped indefinitely; poll until STOPPED.
//
// The STOPPED -> DELETED edge (deletion at stop-time + unbonding) is unit-tested
// (TestSweepRemovesStaleConsumer), not exercised here -- see the note above
// testAutoSweepRemoval's trailing comment.

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ory/dockertest/v3"
	"github.com/stretchr/testify/suite"

	sdk "github.com/cosmos/cosmos-sdk/types"
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
//   - provider genesis is patched with unbonding_time "600s" and
//     liveness_grace_fraction "0.375" (grace ~225s, trusting period ~396s)
//   - consumer is registered via create_consumer_short_unbonding.json
//     (600s unbonding, 20s vaas_timeout, 5s safe_mode_threshold)
type LivenessIntegrationTestSuite struct {
	baseTestSuite

	// Cross-step state for the equivocation steps: lqval's punishment must
	// survive the liveness stop and execute, lqval2's (queued mid-outage)
	// must defer behind a removal vote passed after the stop and be cancelled.
	lqvalValoper   string
	lqvalConsAddr  string
	lqval2Valoper  string
	lqval2ConsAddr string
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
		govVotingPeriod:             180 * time.Second,

		patchProviderGenesis: func(appState map[string]any) {
			// Long enough for a removal vote submitted after the liveness stop
			// to still be open when the mid-outage punishment matures (see
			// testDeferredEquivocationCancelledByVotePassedAfterStop).
			if gov, ok := appState["gov"].(map[string]any); ok {
				if params, ok := gov["params"].(map[string]any); ok {
					params["voting_period"] = "180s"
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
					// deadline). Timeouts are log-only, so this does not perturb the
					// other liveness tests.
					params["vaas_timeout_period"] = "20s"
					// Long enough for a queued equivocation punishment to
					// still be pending when the liveness sweep stops the
					// consumer (grace ~225s plus the sweep poll), short
					// enough to then execute within the run; see
					// testEquivocationSurvivesLivenessStop.
					params["equivocation_execution_delay"] = "420s"
					// Covers the tally block after a vote ends; the deferred
					// punishment resolves this long past the voting end.
					params["removal_vote_deferral_margin"] = "30s"
					// Shrink the liveness grace fraction so the grace period
					// (unbonding * fraction = 600s * 0.375 = ~225s) is observable in
					// a CI run, while keeping the unbonding itself long enough that
					// the relayer-derived IBC client trusting period (unbonding *
					// 0.66 = ~396s) clears the declaration check: the declared
					// consumer client's trusting period must exceed the downtime
					// challenge horizon (180s + 180s below).
					//
					// The grace must exceed the time from consumer launch to first
					// VSC sync (the provider seeds lastAck at launch, so the clock
					// starts before the relayer has delivered anything). With fast
					// blocks that first sync is ~60-90s; 225s leaves margin. The
					// suite also waits for that first sync explicitly before
					// asserting (see waitForConsumerSync).
					params["liveness_grace_fraction"] = "0.375"
				}
			}

			if provider, ok := appState["provider"].(map[string]any); ok {
				// The suite's whole clock is the 600s unbonding: consumer
				// registration requires the unbonding period to exceed the
				// downtime challenge horizon (evidence max age + challenge
				// window), and declaring the consumer client requires the
				// declared client's trusting period to exceed it too -- at the
				// shipped defaults that horizon is 10 days, so this provider
				// must carry proportionally short values or no consumer can be
				// registered at all. Evidence age must not exceed the challenge
				// window (InfractionParameters.Validate).
				//
				// Shortened downtime detection params, mirroring the main suite's
				// patch (see e2e_setup_test.go) so testEvidenceRequeueOnTimeout's
				// silent validator produces provider-accepted downtime evidence
				// within a CI run. signed_blocks_window is echoed into the
				// consumer genesis at launch, so the consumer closes a tumbling
				// window every 30 of its ~1s blocks.
				//
				// downtime_challenge_window / downtime_evidence_max_age diverge
				// from the main suite's 30s: evidence that survives an IBC
				// timeout is only delivered a full timeout-and-requeue cycle
				// after the relayer outage ends, and its window-end time is
				// anchored to the first post-outage client update -- the
				// provider-side age check must tolerate the relayer's whole
				// post-outage backlog (VSC timeouts, snapshot resync, evidence
				// timeouts, possibly a second timeout-and-requeue round)
				// between that anchor and the eventual delivery. 180s absorbs
				// that; the challenge window must be >= the max age and, at
				// 180s, also keeps accepted windows visibly pending long enough
				// for the test's state assertions.
				provider["infraction_parameters"] = map[string]any{
					"double_sign": map[string]any{
						"slash_fraction": "0.050000000000000000",
						"jail_duration":  "315360000s",
						"tombstone":      true,
					},
					"downtime": map[string]any{
						"slash_fraction": "0.010000000000000000",
						"jail_duration":  "0s",
						"tombstone":      false,
					},
					"downtime_grace_period":     "604800s",
					"signed_blocks_window":      strconv.FormatInt(downtimeSignedBlocksWindow, 10),
					"min_signed_per_window":     "0.500000000000000000",
					"downtime_challenge_window": "180s",
					"downtime_evidence_max_age": "180s",
				}
			}

			// Loosen the provider's own native x/slashing downtime window so the
			// permanently-silent validator bonded by testEvidenceRequeueOnTimeout
			// is never natively jailed on the provider (which would drop it from
			// the bonded set and void its consumer-side downtime evidence). At
			// this suite's ~1s blocks the default 100-block window would jail it
			// within ~2 minutes of bonding.
			if slashing, ok := appState["slashing"].(map[string]any); ok {
				if params, ok := slashing["params"].(map[string]any); ok {
					params["signed_blocks_window"] = "100000"
				}
			}

			// Keep unbonding long enough for a viable IBC client trusting period
			// (see liveness_grace_fraction note above); the short grace comes from
			// the fraction, not from a short unbonding.
			if staking, ok := appState["staking"].(map[string]any); ok {
				if params, ok := staking["params"].(map[string]any); ok {
					params["unbonding_time"] = "600s"
				}
			}
		},

		// Fast blocks so add-path, epochs, and VSC delivery all complete well
		// inside the grace (see patchConfigToml).
		patchProviderConfigToml: s.patchConfigToml,
		patchConsumerConfigToml: s.patchConfigToml,
	}

	s.cdc = makeCodec()

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

	s.T().Log("step 5b: declaring the relayer-created clients (owner, both chains)...")
	s.declareConsumerClients("0")

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
	s.testEvidenceRequeueOnTimeout()
	// Queue an equivocation punishment right before the sweep stops the
	// consumer, then prove the stop did not cancel it (asserted right after).
	s.testQueueEquivocationBeforeLivenessStop()
	s.testAutoSweepRemoval()
	s.testEquivocationSurvivesLivenessStop()
	s.testDeferredEquivocationCancelledByVotePassedAfterStop()
}

// ---- test methods ----------------------------------------------------------

// testRecoverBeforeGrace pauses the consumer for ~10s (far less than the ~225s
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

		s.T().Log("pausing consumer container for ~10s (far less than ~225s grace)...")
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
// remove the consumer (the log-only OnTimeout), and that a consumer that fell
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
	s.Run("forced timeout: log-only OnTimeout keeps consumer LAUNCHED; behind consumer heals via snapshot", func() {
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

		// (a) The log-only OnTimeout must not have removed the consumer.
		s.Require().Eventuallyf(func() bool {
			return s.queryProviderConsumerPhase(consumerID) == "CONSUMER_PHASE_LAUNCHED"
		}, 30*time.Second, 3*time.Second,
			"consumer %s must remain LAUNCHED despite VSC packet timeouts", consumerID)
		s.T().Log("consumer remained LAUNCHED through the timeouts")

		// (b) Prove a real timeout actually fired -- otherwise (a) is vacuous.
		// The provider logs the log-only OnTimeout handler. The wait must
		// absorb the relayer's whole post-unpause reconciliation: it first
		// retries the expired deliveries against the consumer and only then
		// submits MsgTimeout to the provider, which can take well over 30s
		// after a 35s pause.
		s.Require().Eventuallyf(func() bool {
			return strings.Contains(s.providerLogs(), "packet timeout, retrying next epoch")
		}, 2*time.Minute, 3*time.Second,
			"provider never logged a VSC packet timeout; the timeout path was not exercised")
		s.T().Log("provider processed a VSC timeout (OnTimeout is log-only)")

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
// acks return to the provider. After the liveness grace period (~225s) expires,
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
		// 600s * 0.375 = ~225s. Wait for the grace period to elapse before polling,
		// so that the sweep has had time to fire by the first poll iteration.
		// Partway through, while the provider still sees the consumer LAUNCHED,
		// a second equivocation punishment is queued whose maturity lands well
		// after lqval's (see queueEquivocationMidOutage).
		const midOutage = 100 * time.Second
		const gracePlusBuffer = 235 * time.Second
		s.T().Logf("outage started; waiting %s for grace period to elapse...", gracePlusBuffer)
		time.Sleep(midOutage)
		s.queueEquivocationMidOutage()
		time.Sleep(gracePlusBuffer - midOutage)

		// Poll until the provider sweeps the consumer to STOPPED.
		// Allow up to 2 minutes total (grace already elapsed above).
		s.T().Log("polling for CONSUMER_PHASE_STOPPED (timeout 2min)...")
		s.Require().Eventuallyf(func() bool {
			p := s.queryProviderConsumerPhase(consumerID)
			s.T().Logf("consumer %s phase: %s", consumerID, p)
			if body, err := httpGet(livenessURL); err == nil {
				s.T().Logf("consumer %s liveness: %s", consumerID, string(body))
			}
			return p == "CONSUMER_PHASE_STOPPED"
		}, 2*time.Minute, 5*time.Second,
			"provider did not sweep consumer %s to STOPPED within 2 minutes", consumerID)

		s.T().Logf("consumer %s successfully swept to STOPPED", consumerID)
	})
}

// Note on STOPPED -> DELETED: the sweep schedules deletion at
// blockTime + providerUnbonding, the same path a governance removal takes.
// With a viable (relayer-survivable) ~600s unbonding, the real DELETED edge
// would only fire ~600s after STOPPED and so is deliberately not exercised
// here -- it is covered by TestSweepRemovesStaleConsumer, which asserts the
// removal_time scheduling directly. This e2e suite's contribution is proving
// the LAUNCHED -> STOPPED sweep fires end-to-end under real IBC silence.

// testQueueEquivocationBeforeLivenessStop bonds an equivocating validator on
// the liveness provider and queues a fabricated punishment against it right
// before testAutoSweepRemoval lets the liveness sweep stop the consumer.
func (s *LivenessIntegrationTestSuite) testQueueEquivocationBeforeLivenessStop() {
	s.Run("equivocation: queue a punishment the liveness stop must not cancel", func() {
		const consumerID = "0"

		valoper, priv := s.createEquivocatingValidator("lqval", "5000000"+bondDenom)
		s.lqvalValoper = valoper
		s.lqvalConsAddr = sdk.ConsAddress(priv.PubKey().Address()).String()

		evidencePath, headerPath := s.fabricateDoubleVote(priv, s.cfg.consumerChainID, 1_000_002, "lqval")
		s.submitDoubleVoteEvidence(consumerID, evidencePath, headerPath)

		s.Require().Eventuallyf(func() bool {
			pending, err := s.queryPendingEquivocations(consumerID)
			return err == nil && len(pending) == 1
		}, time.Minute, 2*time.Second, "the punishment never queued")
		jailed, _ := s.stakingValidatorState(valoper)
		s.Require().True(jailed, "lqval must be jailed at queue time")
	})
}

// queueEquivocationMidOutage bonds a second equivocating validator and queues a
// punishment against it partway through testAutoSweepRemoval's outage, while
// the provider still sees the consumer LAUNCHED. Its maturity lands well after
// lqval's, so a removal vote timed over it defers only this entry (see
// testDeferredEquivocationCancelledByVotePassedAfterStop).
func (s *LivenessIntegrationTestSuite) queueEquivocationMidOutage() {
	const consumerID = "0"

	valoper, priv := s.createEquivocatingValidator("lqval2", "5000000"+bondDenom)
	s.lqval2Valoper = valoper
	s.lqval2ConsAddr = sdk.ConsAddress(priv.PubKey().Address()).String()

	evidencePath, headerPath := s.fabricateDoubleVote(priv, s.cfg.consumerChainID, 1_000_003, "lqval2")
	s.submitDoubleVoteEvidence(consumerID, evidencePath, headerPath)

	s.Require().Eventuallyf(func() bool {
		pending, err := s.queryPendingEquivocations(consumerID)
		return err == nil && len(pending) == 2
	}, time.Minute, 2*time.Second, "the mid-outage punishment never queued")
	s.T().Log("second equivocation punishment queued mid-outage")
}

// pendingEquivocationFor returns the pending entry naming the consensus
// address, if any.
func pendingEquivocationFor(pending []pendingEquivocationJSON, consAddr string) (pendingEquivocationJSON, bool) {
	for _, p := range pending {
		if sdk.ConsAddress(p.ProviderConsAddr).String() == consAddr {
			return p, true
		}
	}
	return pendingEquivocationJSON{}, false
}

// testEquivocationSurvivesLivenessStop asserts, after the liveness sweep has
// stopped the consumer, that the stop was not a verdict: the punishment is
// still pending, and it then executes at maturity (tombstone, slash) rather
// than being cancelled.
func (s *LivenessIntegrationTestSuite) testEquivocationSurvivesLivenessStop() {
	s.Run("equivocation: a liveness stop does not cancel the pending punishment", func() {
		const consumerID = "0"

		s.Require().Equal("CONSUMER_PHASE_STOPPED", s.queryProviderConsumerPhase(consumerID),
			"precondition: the liveness sweep stopped the consumer")

		pending := s.mustPendingEquivocations(consumerID)
		s.Require().Len(pending, 2, "the liveness stop must leave both punishments pending")
		entry, found := pendingEquivocationFor(pending, s.lqvalConsAddr)
		s.Require().True(found, "lqval's punishment must still be pending")
		s.Require().False(entry.Extended, "no removal vote ran, so nothing extended it")

		s.T().Logf("waiting for lqval's punishment to execute at maturity (%s)...", entry.ExecutesAt)
		s.Require().Eventuallyf(func() bool {
			pending, err := s.queryPendingEquivocations(consumerID)
			if err != nil {
				return false
			}
			_, stillPending := pendingEquivocationFor(pending, s.lqvalConsAddr)
			return !stillPending
		}, time.Until(entry.ExecutesAt)+2*time.Minute, 5*time.Second,
			"the punishment never executed after the liveness stop")

		s.Require().Eventuallyf(func() bool {
			return s.signingInfoTombstoned(s.lqvalConsAddr)
		}, time.Minute, 2*time.Second, "lqval was not tombstoned at execution")
		jailed, _ := s.stakingValidatorState(s.lqvalValoper)
		s.Require().True(jailed, "a tombstoned validator stays jailed")
	})
}

// testDeferredEquivocationCancelledByVotePassedAfterStop: lqval2's punishment,
// queued mid-outage, matures during a removal vote submitted after the
// liveness stop, so it defers behind that vote. The vote passes, but its
// MsgRemoveConsumer finds the consumer already STOPPED and gov records the
// proposal FAILED; the tally is still the community's verdict, so the deferred
// punishment is cancelled: entry gone, no tombstone, jail opened.
func (s *LivenessIntegrationTestSuite) testDeferredEquivocationCancelledByVotePassedAfterStop() {
	s.Run("equivocation: a removal vote passed after the liveness stop cancels the deferred punishment", func() {
		const consumerID = "0"

		s.Require().Equal("CONSUMER_PHASE_STOPPED", s.queryProviderConsumerPhase(consumerID),
			"precondition: the liveness sweep stopped the consumer")
		pending := s.mustPendingEquivocations(consumerID)
		s.Require().Len(pending, 1, "only lqval2's punishment is left pending")
		entry, found := pendingEquivocationFor(pending, s.lqval2ConsAddr)
		s.Require().True(found)
		s.Require().False(entry.Extended)
		s.Require().Truef(time.Until(entry.ExecutesAt) > 30*time.Second,
			"lqval2's maturity (%s) must fall inside the vote about to be submitted", entry.ExecutesAt)

		s.T().Log("submitting a removal proposal against the stopped consumer: the vote passes, the removal cannot execute...")
		proposalID := s.submitProposalWithVote(
			s.removalProposalJSON(consumerID, "Remove consumer (liveness e2e, passes after the stop)"),
			"yes", "PROPOSAL_STATUS_FAILED")

		pending = s.mustPendingEquivocations(consumerID)
		s.Require().Len(pending, 1, "the punishment waits for the verdict at its extended maturity")
		s.Require().True(pending[0].Extended, "the punishment must have deferred behind the vote")
		s.Require().Equal(strconv.FormatUint(proposalID, 10), pending[0].DeferredByProposalId,
			"the punishment must record the vote it deferred behind")

		s.T().Log("waiting for the deferred punishment to be cancelled at its extended maturity...")
		s.Require().Eventuallyf(func() bool {
			return s.pendingEquivocationsEmpty(consumerID)
		}, time.Until(pending[0].ExecutesAt)+2*time.Minute, 5*time.Second,
			"the punishment was not cancelled after the passed vote")
		s.Require().False(s.signingInfoTombstoned(s.lqval2ConsAddr), "a cancelled punishment must not tombstone")

		s.T().Log("unjailing lqval2 now that the cancelled punishment opened the jail...")
		s.mustUnjail("lqval2", s.lqval2Valoper)
	})
}
