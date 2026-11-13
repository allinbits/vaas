package e2e

// e2e_lifecycle_suite_test.go contains the LifecycleIntegrationTestSuite, a
// Docker-based e2e suite that walks one consumer through the back half of its
// lifecycle on real chains: a refusal pause that freezes a pending
// equivocation punishment and a resume that moves it past the pause, the
// withheld-fee record a downtime accusation leaves across an epoch boundary
// and its lapse once the accused leaves the bonded set, the MaxPauseDuration
// auto-stop, double-voting evidence accepted during the pause and after the
// stop until a governance removal of the stopped consumer voids it, the
// owner's fee-pool sweep once STOPPED, the unbonding-delayed erasure to
// DELETED with the chain id released, and the immediate erasure of consumers
// that never launched. The main suite proves the mechanisms around a consumer
// that stays LAUNCHED for most of its run and ends with a governance removal;
// this suite is built around the clocks those transitions run on, so every one
// of them is short enough to observe in a CI run, and every deadline is
// checked against chain data (the block times at the heights where a phase or
// a queue entry changed), not against the wall clock.
//
// Parameters (the constants below and patchProviderGenesis):
//   - fast blocks on both chains (patchConfigToml, about a second), as in the
//     liveness suite, so every wait below counts in seconds;
//   - blocks_per_epoch 60 (about a minute) over a 15-block consumer downtime
//     window: an epoch holds four windows, which is what makes the
//     withheld-fee record deterministic (see
//     testWithheldFeeRecordAcrossEpoch);
//   - a 120s downtime challenge window and evidence max age, well above the
//     window-plus-relay lag an accusation's anchored age reaches here. The
//     lapse scenario also needs the boundary after the departure, 60 blocks
//     after the last one, to land before the record expires, 120s after that
//     last boundary: blocks must average under 2s over that epoch, which
//     leaves a loaded runner's 1.5s a 30s margin;
//   - a 45s equivocation execution delay and a 150s max pause duration: the
//     thaw scenario's pause spans the end of the delay, the auto-stop
//     scenario waits the whole duration out, and the punishment queued
//     during that pause executes 45s after the stop, before the
//     stopped-consumer evidence scenario submits its own. The duration also
//     exceeds the challenge window, so the record the auto-stop's pause
//     froze, due one window after a boundary the pause followed, has lapsed
//     when the stop measures it again;
//   - a 400s provider unbonding, matched by the consumer's: the relayer
//     gives each client two thirds of the tracked chain's unbonding as its
//     trusting period (about 265s), which the declaration requires above
//     the challenge horizon (evidence max age plus challenge window, 240s)
//     and which also stays above the longest pause a launched consumer sees
//     here. The provider's bounds the STOPPED -> DELETED wait, which holds
//     the stopped-consumer evidence scenario (about two minutes from the
//     stop) with room to spare, outlasts the lapse scenario's wait for the
//     departing validator's last accusation (under four minutes from its
//     undelegation), so its unbonding entry is still there to be read
//     slashed, and with a 0.9 grace fraction gives a 360s liveness grace
//     that covers the setup's first VSC sync with epochs to spare;
//   - the consumer registered from testdata/create_consumer_lifecycle.json
//     carries the same 400s unbonding (it may not exceed the provider's and
//     must exceed the challenge horizon) and a 120s safe-mode threshold,
//     above an epoch and below the liveness grace, so a healthy consumer
//     never sits in safe mode.
//
// Test ordering within TestLifecycleVAAS (each scenario leaves the state the
// next one starts from):
//  1. testFundFeePoolAndSweepLockedWhileLaunched - the owner funds the pool;
//     a sweep is locked while LAUNCHED.
//  2. testEquivocationThawAcrossPause - queue a punishment, pause by refusal
//     across its maturity, resume by governance: executes_at moves by the
//     pause and the punishment executes at the moved time.
//  3. testWithheldFeeRecordAcrossEpoch - a silent validator's accusation
//     spans an epoch boundary: its share is escrowed, the slash executes.
//  4. testWithheldFeeRecordLapsesWhileLaunched - the silent validator leaves
//     the bonded set: its record lapses unpaid one challenge window after the
//     last boundary that withheld, with accusations pending that execute
//     later against its unbonding stake.
//  5. testMaxPauseAutoStop - bond a third silent validator so a record is
//     live, pause by refusal, accept double-voting evidence during the pause
//     (its clock starts at the pause), wait out max_pause_duration: STOPPED
//     on its own, the punishment moved to one delay after the stop, the
//     frozen record lapses unpaid, a resume is refused.
//  6. testEquivocationEvidenceOnTheStoppedConsumer - the punishment queued
//     during the pause executes after the stop (a lapsed pause is not a
//     verdict); evidence submitted after the stop is accepted; a governance
//     removal of the stopped consumer passes, cancels the punishment that
//     evidence queued and leaves the consumer STOPPED; fresh evidence is
//     then refused. It runs
//     inside the wait for the deletion, so it adds no time to the suite.
//  7. testFeePoolSweepWhenStopped - only the owner's sweep returns the pool.
//  8. testStoppedToDeleted - DELETED once the provider unbonding elapses,
//     counted from the auto-stop (the governance removal moved nothing); the
//     chain id is free again.
//  9. testPrelaunchRemoval - registered-only consumers are erased at once by
//     their owner and by governance, their pools returned.

import (
	"fmt"
	"strconv"
	"testing"
	"time"

	"github.com/ory/dockertest/v3"
	"github.com/stretchr/testify/suite"
)

const (
	lifecycleProviderChainID = "provider-lifecycle"
	lifecycleConsumerChainID = "consumer-lifecycle"
	lifecycleDockerNetwork   = "vaas-lifecycle-testnet"

	// Host port block: offset +400 from the main suite (26657/9090/1317/26656),
	// clear of the blocks the other suites take, so the suites can run on
	// one host.
	lifecycleProviderRPCPort  = "27057"
	lifecycleProviderGRPCPort = "9490"
	lifecycleProviderRESTPort = "1717"
	lifecycleProviderP2PPort  = "27056"

	lifecycleConsumerRPCPort  = "27067"
	lifecycleConsumerGRPCPort = "9492"
	lifecycleConsumerRESTPort = "1727"
	lifecycleConsumerP2PPort  = "27066"
)

// The provider parameters this suite's genesis carries; the scenarios derive
// their deadlines and expected amounts from these.
const (
	// lifecycleBlocksPerEpoch is the VSC and fee-distribution cadence, in
	// provider blocks: four consumer downtime windows.
	lifecycleBlocksPerEpoch int64 = 60
	// lifecycleSignedBlocksWindow is the consumer's tumbling downtime window,
	// in consumer blocks.
	lifecycleSignedBlocksWindow int64 = 15
	// lifecycleFeesPerBlock is the per-block consumer fee amount, in feeDenom.
	lifecycleFeesPerBlock int64 = 1000
	// lifecycleDowntimeChallengeWindow is how long an accepted downtime
	// accusation stays pending, and the escrow behind it claimable; also the
	// evidence max age.
	lifecycleDowntimeChallengeWindow = 120 * time.Second
	// lifecycleEquivocationExecutionDelay is the delay between a verified
	// equivocation's queue and its execution.
	lifecycleEquivocationExecutionDelay = 45 * time.Second
	// lifecycleMaxPauseDuration is how long a consumer may stay PAUSED
	// before the auto-stop; it exceeds the thaw scenario's pause and the
	// challenge window.
	lifecycleMaxPauseDuration = 150 * time.Second
	// lifecycleProviderUnbonding is the provider staking unbonding time,
	// which is also the STOPPED -> DELETED delay; the consumers registered
	// here carry the same unbonding, and the trusting periods the relayer
	// derives from it must exceed the challenge horizon (see the file
	// comment).
	lifecycleProviderUnbonding = 400 * time.Second
	// lifecycleGovVotingPeriod is the provider gov voting period.
	lifecycleGovVotingPeriod = 30 * time.Second
)

// LifecycleIntegrationTestSuite drives one consumer from LAUNCHED through
// PAUSED, STOPPED and DELETED, then erases pre-launch consumers registered
// under the released chain id.
type LifecycleIntegrationTestSuite struct {
	baseTestSuite

	// ownerAddr is the consumer owner's provider account; it funds and
	// sweeps the fee pools. feePoolAddr is consumer 0's fee pool.
	ownerAddr   string
	feePoolAddr string

	// The permanently silent validators: val2 is bonded by the withheld-record
	// scenario and leaves the bonded set in the lapse scenario; val3 is bonded
	// by the auto-stop scenario, which watches its frozen record lapse unpaid.
	val2, val3 silentValidator

	// The validator the auto-stop scenario accuses of double voting during the
	// pause, whose punishment the stopped-consumer evidence scenario watches
	// execute.
	pauseEquivocator equivocator

	// The auto-stop's block, from which the deletion delay runs.
	stopHeight int64
	stoppedAt  time.Time

	// The consumer registered under the released chain id, erased by the
	// pre-launch removal scenario.
	prelaunchConsumerID string
}

// TestLifecycleIntegrationTestSuite is the entry point for the lifecycle
// e2e suite.
func TestLifecycleIntegrationTestSuite(t *testing.T) {
	suite.Run(t, new(LifecycleIntegrationTestSuite))
}

// secondsString renders a duration the way the genesis JSON carries one.
func secondsString(d time.Duration) string {
	return fmt.Sprintf("%ds", int64(d.Seconds()))
}

// SetupSuite brings up provider + consumer + ts-relayer with the short clocks
// described in the file comment.
func (s *LifecycleIntegrationTestSuite) SetupSuite() {
	s.T().Log("setting up lifecycle e2e suite...")

	s.cfg = baseSuiteConfig{
		providerChainID:  lifecycleProviderChainID,
		consumerChainID:  lifecycleConsumerChainID,
		dockerNetwork:    lifecycleDockerNetwork,
		providerInitName: "lifecycle-provider-init",
		consumerInitName: "lifecycle-consumer-init",
		tmpDirPrefix:     "vaas-lifecycle-",
		providerRPCPort:  lifecycleProviderRPCPort,
		providerGRPCPort: lifecycleProviderGRPCPort,
		providerRESTPort: lifecycleProviderRESTPort,
		providerP2PPort:  lifecycleProviderP2PPort,
		consumerRPCPort:  lifecycleConsumerRPCPort,
		consumerGRPCPort: lifecycleConsumerGRPCPort,
		consumerRESTPort: lifecycleConsumerRESTPort,
		consumerP2PPort:  lifecycleConsumerP2PPort,

		consumerTemplateFile:        "create_consumer_lifecycle.json",
		consumerTemplatePlaceholder: "CONSUMER_LIFECYCLE_CHAIN_ID",
		govVotingPeriod:             lifecycleGovVotingPeriod,

		patchProviderGenesis: func(appState map[string]any) {
			s.setGovVotingPeriod(appState)

			params := s.genesisSection(appState, "vaasprovider", "params")
			params["blocks_per_epoch"] = strconv.FormatInt(lifecycleBlocksPerEpoch, 10)
			params["fees_per_block_amount"] = strconv.FormatInt(lifecycleFeesPerBlock, 10)
			params["equivocation_execution_delay"] = secondsString(lifecycleEquivocationExecutionDelay)
			params["max_pause_duration"] = secondsString(lifecycleMaxPauseDuration)
			// The liveness grace is unbonding x fraction; the fraction is
			// raised so the grace covers the setup's first VSC sync and the
			// epoch-long gaps between acks with room to spare.
			params["liveness_grace_fraction"] = "0.9"
			// downtime.slash_fraction must be set explicitly: the genesis
			// JSON is unmarshaled straight into InfractionParameters, and
			// an absent key becomes a zero fraction that caps every downtime
			// slash at nothing. downtime_grace_period is irrelevant: the
			// template's 2024 spawn time is years in the past. Evidence age
			// must not exceed the challenge window (InfractionParameters
			// .Validate), and their sum, the challenge horizon, must stay
			// below the trusting period of the consumer client (two thirds
			// of the consumer's 400s unbonding).
			params["infraction_parameters"] = map[string]any{
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
				"signed_blocks_window":      strconv.FormatInt(lifecycleSignedBlocksWindow, 10),
				"min_signed_per_window":     "0.500000000000000000",
				"downtime_challenge_window": secondsString(lifecycleDowntimeChallengeWindow),
				"downtime_evidence_max_age": secondsString(lifecycleDowntimeChallengeWindow),
			}

			// The provider's own x/slashing must never jail the validators
			// this suite bonds without a node (the equivocator and the
			// silent validator): at fast blocks the default 100-block window
			// would do so within minutes and drop them from the bonded set.
			s.genesisSection(appState, "slashing", "params")["signed_blocks_window"] = "100000"

			s.genesisSection(appState, "staking", "params")["unbonding_time"] = secondsString(lifecycleProviderUnbonding)
		},

		patchProviderConfigToml: s.patchConfigToml,
		patchConsumerConfigToml: s.patchConfigToml,
	}

	s.cdc = makeCodec()

	var err error

	s.dkrPool, err = dockertest.NewPool("")
	s.Require().NoError(err, "failed to create docker pool")
	s.dkrPool.MaxWait = 5 * time.Minute

	s.cleanupStaleContainers()

	s.dkrNet, err = s.dkrPool.CreateNetwork(lifecycleDockerNetwork)
	s.Require().NoError(err, "failed to create lifecycle docker network")

	s.T().Log("step 1: initializing lifecycle provider chain...")
	s.provider = &chain{id: s.cfg.providerChainID}
	s.initAndStartProvider()

	s.T().Log("step 2: registering consumer on lifecycle provider...")
	s.registerConsumerOnProvider()

	s.T().Log("step 3: fetching consumer genesis from lifecycle provider...")
	consumerGenesisJSON := s.fetchConsumerGenesis()

	s.T().Log("step 4: initializing lifecycle consumer chain...")
	s.consumer = &chain{id: s.cfg.consumerChainID}
	s.initAndStartConsumer(consumerGenesisJSON)

	s.T().Log("step 5: starting ts-relayer for lifecycle suite...")
	s.setupTSRelayer()

	s.T().Log("step 5b: declaring the relayer-created clients (owner, both chains)...")
	s.declareConsumerClients(lifecycleConsumerID)

	s.T().Log("step 6: waiting for the consumer to sync its first VSC...")
	s.waitForConsumerSync(lifecycleConsumerID)

	s.ownerAddr = s.mustKeyAddress("owner")
	s.feePoolAddr = s.queryConsumerFeePoolAddress(lifecycleConsumerID)

	s.T().Log("lifecycle e2e suite setup complete!")
}

// TestLifecycleVAAS sequences the lifecycle scenarios; see the file comment
// for what each one needs and leaves behind.
func (s *LifecycleIntegrationTestSuite) TestLifecycleVAAS() {
	s.testFundFeePoolAndSweepLockedWhileLaunched()
	s.testEquivocationThawAcrossPause()
	s.testWithheldFeeRecordAcrossEpoch()
	s.testWithheldFeeRecordLapsesWhileLaunched()
	s.testMaxPauseAutoStop()
	s.testEquivocationEvidenceOnTheStoppedConsumer()
	s.testFeePoolSweepWhenStopped()
	s.testStoppedToDeleted()
	s.testPrelaunchRemoval()
}
