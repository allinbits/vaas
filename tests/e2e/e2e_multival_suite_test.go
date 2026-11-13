package e2e

import (
	"testing"
	"time"

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
		patchProviderGenesis: s.patchFourValidatorProviderGenesis,
		govVotingPeriod:      30 * time.Second,

		// Fast blocks on both chains, as in the liveness suite: every
		// time-bounded step of the scenarios is measured in blocks.
		patchProviderConfigToml: s.patchConfigToml,
		patchConsumerConfigToml: s.patchConfigToml,
	}

	s.bringUpSuite()

	s.T().Log("multi-validator e2e suite setup complete!")
}

// TestMultiValidatorVAAS sequences the scenarios so each leaves the set whole
// for the next.
func (s *MultiValidatorIntegrationTestSuite) TestMultiValidatorVAAS() {
	s.testValidatorSetMirror()
	s.testDelegationReachesConsumer()
	s.testKeyAssignmentWithNodeRestart()
	s.testOfflineValidatorIsSlashedAndRecovers()
	s.testRefusalThresholdIsStakeWeighted()
}
