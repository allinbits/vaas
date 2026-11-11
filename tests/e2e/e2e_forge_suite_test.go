package e2e

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"
)

// The byzantine-consumer suite runs four validator nodes per chain and a
// consumer that lies, and observes the provider's answer to each lie: a
// forged downtime accusation against a live validator (told by every
// consumer node running consumer-forge, see tests/e2e/forge), a fabricated
// double-sign, and a fork, each signed with keys the test holds as a
// malicious binary on the validators' nodes would. It ends with the removal
// such a consumer gets. The lying node is built only into its own image and
// runs only here.
const (
	forgeProviderChainID = "provider-forge"
	forgeConsumerChainID = "consumer-forge"
	forgeDockerNetwork   = "vaas-forge-testnet"

	forgeProviderRPCPort  = "26957"
	forgeProviderGRPCPort = "9390"
	forgeProviderRESTPort = "1617"
	forgeProviderP2PPort  = "26956"
	forgeConsumerRPCPort  = "26967"
	forgeConsumerGRPCPort = "9392"
	forgeConsumerRESTPort = "1627"
	forgeConsumerP2PPort  = "26966"

	// forgeImage is the e2e chain image plus consumer-forge.
	forgeImage = "cosmos/vaas-e2e-forge"
	// forgeBinary is the lying consumer node, which offers only `start`.
	forgeBinary = "consumer-forge"
)

type ByzantineConsumerIntegrationTestSuite struct {
	baseTestSuite

	// val1StakeBeforeLie is val1's stake before the forge accused it, which
	// the suite's last check compares against: nothing the lying consumer
	// did may have cost val1 anything.
	val1StakeBeforeLie string
}

func TestByzantineConsumerIntegrationTestSuite(t *testing.T) {
	suite.Run(t, new(ByzantineConsumerIntegrationTestSuite))
}

func (s *ByzantineConsumerIntegrationTestSuite) SetupSuite() {
	s.T().Log("setting up the byzantine-consumer e2e suite (four validators per chain)...")

	s.cfg = baseSuiteConfig{
		providerChainID:  forgeProviderChainID,
		consumerChainID:  forgeConsumerChainID,
		dockerNetwork:    forgeDockerNetwork,
		providerInitName: "forge-provider-init",
		consumerInitName: "forge-consumer-init",
		tmpDirPrefix:     "vaas-forge-",
		providerRPCPort:  forgeProviderRPCPort,
		providerGRPCPort: forgeProviderGRPCPort,
		providerRESTPort: forgeProviderRESTPort,
		providerP2PPort:  forgeProviderP2PPort,
		consumerRPCPort:  forgeConsumerRPCPort,
		consumerGRPCPort: forgeConsumerGRPCPort,
		consumerRESTPort: forgeConsumerRESTPort,
		consumerP2PPort:  forgeConsumerP2PPort,

		// A short consumer unbonding period (10 minutes, so a client trusting
		// period of about 6.6), for the fork drill to wait out.
		consumerTemplateFile:        "create_consumer_forge.json",
		consumerTemplatePlaceholder: "CONSUMER_CHAIN_ID",

		validators: 4,

		patchProviderGenesis: s.patchFourValidatorProviderGenesis,
		govVotingPeriod:      30 * time.Second,

		patchProviderConfigToml: s.patchConfigToml,
		patchConsumerConfigToml: s.patchConfigToml,
	}

	s.bringUpSuite()
	s.T().Log("byzantine-consumer e2e suite setup complete!")
}

// TestByzantineConsumerVAAS runs the lies in the one order the protocol
// allows: downtime evidence and misbehaviour are accepted only from a
// launched consumer, so the forged downtime (which ends in a pause and a
// resume) comes first, the fork (which ends in a pause only the drill's
// recovery resumes, once the trusting period has elapsed) next, then the
// revision bump, which needs the launched consumer and the active, recovered
// client the drill leaves and ends with the consumer renamed and served, and
// the removal last.
func (s *ByzantineConsumerIntegrationTestSuite) TestByzantineConsumerVAAS() {
	s.testForgedDowntimeIsChallenged()
	s.testFabricatedDoubleSignJailsAndDefers()
	s.testForkFreezesAndPausesWithoutPunishing()
	s.testForkRecoveryWaitsOutTheTrustingPeriod()
	s.testRevisionBumpIsServedOnlyAfterTheRename()
	s.testRemovalCancelsTheFabricatedPunishment()
}

// switchConsumerToForge recreates the four consumer nodes on consumer-forge,
// lying that target signed none of the next blocks blocks, counted from 20
// above the height the consumer halted at for the switch. Every node replays
// at most up to that height on restart, so the four agree on every block
// under either binary; the margin is spare. Past the range the forge lies
// about nothing, so the nodes stay on it.
func (s *ByzantineConsumerIntegrationTestSuite) switchConsumerToForge(target validatorIdentity, blocks int64) (from, until int64) {
	hexAddr, err := consAddrToCometHex(target.consAddr)
	s.Require().NoErrorf(err, "failed to convert %s to its CometBFT address", target.consAddr)

	s.recreateConsumerNodes(func(halted int64) nodeRun {
		from = halted + 20
		until = from + blocks - 1
		s.T().Logf("switching the consumer to the forge at height %d: val%d (%s) absent in blocks %d..%d",
			halted, target.index, hexAddr, from, until)
		return nodeRun{
			image: forgeImage,
			cmd:   []string{forgeBinary, "start", "--home", consumerHomePath},
			env: []string{
				"FORGE_ABSENT_ADDRESS=" + hexAddr,
				fmt.Sprintf("FORGE_FROM_HEIGHT=%d", from),
				fmt.Sprintf("FORGE_UNTIL_HEIGHT=%d", until),
			},
		}
	})
	return from, until
}
