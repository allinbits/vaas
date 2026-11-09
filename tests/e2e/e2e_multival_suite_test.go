package e2e

import (
	"testing"
	"time"

	"github.com/ory/dockertest/v3"
	"github.com/stretchr/testify/suite"
)

// The multi-validator suite runs four validator nodes per chain, one consumer
// node per provider validator, and proves what the single-node suites cannot:
// the consumer's set mirrors every validator with its power, a delegation
// reaches it, a validator's node can stop and restart while the other three
// keep the consumer live, and a key assignment survives the node restarting
// under the new key. Four is the smallest set where one node can stop and
// both chains keep producing blocks: three of four hold 75% of the power.
const (
	multivalProviderChainID = "provider-multival"
	multivalConsumerChainID = "consumer-multival"
	multivalDockerNetwork   = "vaas-multival-testnet"

	multivalProviderRPCPort  = "26857"
	multivalProviderGRPCPort = "9290"
	multivalProviderRESTPort = "1517"
	multivalProviderP2PPort  = "26856"
	multivalConsumerRPCPort  = "26867"
	multivalConsumerGRPCPort = "9292"
	multivalConsumerRESTPort = "1527"
	multivalConsumerP2PPort  = "26866"

	// multivalValidators is the number of validator nodes per chain.
	multivalValidators = 4
	// multivalValidatorPower is each validator's voting power at genesis: a
	// 1000000000uatone self-bond at the default power reduction.
	multivalValidatorPower = int64(1000)
)

type MultiValidatorIntegrationTestSuite struct {
	baseTestSuite
}

func TestMultiValidatorIntegrationTestSuite(t *testing.T) {
	suite.Run(t, new(MultiValidatorIntegrationTestSuite))
}

func (s *MultiValidatorIntegrationTestSuite) SetupSuite() {
	s.T().Log("setting up multi-validator e2e suite (four validators per chain)...")

	s.cfg = baseSuiteConfig{
		providerChainID:  multivalProviderChainID,
		consumerChainID:  multivalConsumerChainID,
		dockerNetwork:    multivalDockerNetwork,
		providerInitName: "multival-provider-init",
		consumerInitName: "multival-consumer-init",
		tmpDirPrefix:     "vaas-multival-",
		providerRPCPort:  multivalProviderRPCPort,
		providerGRPCPort: multivalProviderGRPCPort,
		providerRESTPort: multivalProviderRESTPort,
		providerP2PPort:  multivalProviderP2PPort,
		consumerRPCPort:  multivalConsumerRPCPort,
		consumerGRPCPort: multivalConsumerGRPCPort,
		consumerRESTPort: multivalConsumerRESTPort,
		consumerP2PPort:  multivalConsumerP2PPort,

		consumerTemplateFile:        "create_consumer.json",
		consumerTemplatePlaceholder: "CONSUMER_CHAIN_ID",

		validators: multivalValidators,

		// The main suite's provider parameters: fast epochs, a per-block fee,
		// and the shortened downtime detection so a tumbling window closes in
		// seconds and a challenge window in minutes (see e2e_setup_test.go
		// for why each value is what it is). Photon fees stay off: they are
		// not what this suite tests.
		patchProviderGenesis: func(appState map[string]any) {
			if gov, ok := appState["gov"].(map[string]any); ok {
				if params, ok := gov["params"].(map[string]any); ok {
					params["voting_period"] = "30s"
				}
			}

			if provider, ok := appState["vaasprovider"].(map[string]any); ok {
				if params, ok := provider["params"].(map[string]any); ok {
					params["blocks_per_epoch"] = "5"
					params["fees_per_block_amount"] = "1000"
				}
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
					"signed_blocks_window":      "30",
					"min_signed_per_window":     "0.500000000000000000",
					"downtime_challenge_window": "120s",
					"downtime_evidence_max_age": "120s",
				}
			}

			// A stopped consumer node must not get its validator jailed on
			// the provider for provider downtime: its provider node keeps
			// running, and the provider's own window is loosened as in the
			// other suites.
			if slashing, ok := appState["slashing"].(map[string]any); ok {
				if params, ok := slashing["params"].(map[string]any); ok {
					params["signed_blocks_window"] = "100000"
				}
			}
		},
		govVotingPeriod: 30 * time.Second,

		// Fast blocks on both chains, as in the liveness suite: every
		// time-bounded step of the scenarios is measured in blocks.
		patchProviderConfigToml: s.patchConfigToml,
		patchConsumerConfigToml: s.patchConfigToml,
	}

	s.cdc = makeCodec()

	var err error

	s.dkrPool, err = dockertest.NewPool("")
	s.Require().NoError(err, "failed to create docker pool")
	s.dkrPool.MaxWait = 5 * time.Minute

	s.cleanupStaleContainers()

	s.dkrNet, err = s.dkrPool.CreateNetwork(multivalDockerNetwork)
	s.Require().NoError(err, "failed to create multi-validator docker network")

	s.T().Log("step 1: initializing the multi-validator provider chain...")
	s.provider = &chain{id: s.cfg.providerChainID}
	s.initAndStartProvider()

	s.T().Log("step 2: registering the consumer on the provider...")
	s.registerConsumerOnProvider()

	s.T().Log("step 3: fetching the consumer genesis from the provider...")
	consumerGenesisJSON := s.fetchConsumerGenesis()

	s.T().Log("step 4: initializing the multi-validator consumer chain...")
	s.consumer = &chain{id: s.cfg.consumerChainID}
	s.initAndStartConsumer(consumerGenesisJSON)

	s.T().Log("step 5: starting ts-relayer for the multi-validator suite...")
	s.setupTSRelayer()

	s.T().Log("step 5b: declaring the relayer-created clients (owner, both chains)...")
	s.declareConsumerClients("0")

	s.T().Log("step 6: waiting for the consumer to sync its first VSC...")
	s.waitForConsumerSync("0")

	// The downtime slash is priced from the epoch fee share; keep the pool
	// funded so the consumer never runs in debt during the scenarios.
	s.T().Log("step 7: funding the consumer's fee pool...")
	s.providerFundConsumerFeePool("0", "20000000"+feeDenom)

	s.T().Log("multi-validator e2e suite setup complete!")
}

// TestMultiValidatorVAAS sequences the scenarios so each leaves the set whole
// for the next.
func (s *MultiValidatorIntegrationTestSuite) TestMultiValidatorVAAS() {
	s.testValidatorSetMirror()
	s.testDelegationReachesConsumer()
	s.testKeyAssignmentWithNodeRestart()
	s.testOfflineValidatorIsSlashedAndRecovers()
}
