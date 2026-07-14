package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/ory/dockertest/v3"
	"github.com/stretchr/testify/suite"

	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	"github.com/cosmos/cosmos-sdk/std"
)

const (
	e2eChainImage   = "cosmos/vaas-e2e"
	dockerNetwork   = "vaas-e2e-testnet"
	relayerMnemonic = "abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon art"
)

// IntegrationTestSuite is the main e2e test suite that orchestrates
// provider chain and consumer chain containers.
type IntegrationTestSuite struct {
	baseTestSuite

	cdc codec.Codec
}

// makeCodec creates a proto codec with the standard cosmos SDK interfaces registered.
func makeCodec() codec.Codec {
	registry := codectypes.NewInterfaceRegistry()
	std.RegisterInterfaces(registry)
	return codec.NewProtoCodec(registry)
}

// TestIntegrationTestSuite is the entry point for the e2e test suite.
func TestIntegrationTestSuite(t *testing.T) {
	suite.Run(t, new(IntegrationTestSuite))
}

// testDir returns the absolute path to the directory containing this test file.
func testDir() string {
	_, filename, _, _ := runtime.Caller(0)
	return filepath.Dir(filename)
}

// SetupSuite:
// 1. Create Docker pool and network
// 2. Initialize and start provider chain
// 3. Register consumer on provider
// 4. Fetch consumer genesis from provider
// 5. Initialize and start consumer chain
// 6. Start ts-relayer and create IBC v2 path
func (s *IntegrationTestSuite) SetupSuite() {
	s.T().Log("setting up e2e integration test suite...")

	s.cfg = baseSuiteConfig{
		providerChainID:  providerChainID,
		consumerChainID:  consumerChainID,
		dockerNetwork:    dockerNetwork,
		providerInitName: "provider-init",
		consumerInitName: "consumer-init",
		tmpDirPrefix:     "vaas-e2e-",
		providerRPCPort:  "26657",
		providerGRPCPort: "9090",
		providerRESTPort: "1317",
		providerP2PPort:  "26656",
		consumerRPCPort:  "26667",
		consumerGRPCPort: "9092",
		consumerRESTPort: "1327",
		consumerP2PPort:  "26666",

		consumerTemplateFile:        "create_consumer.json",
		consumerTemplatePlaceholder: "CONSUMER_CHAIN_ID",

		patchProviderGenesis: func(appState map[string]any) {
			// Set fast voting period
			if gov, ok := appState["gov"].(map[string]any); ok {
				if params, ok := gov["params"].(map[string]any); ok {
					params["voting_period"] = "15s"
				}
			}

			// Set fast epoch for VSC and a small per-block fee amount. The fee
			// denom is fixed to feeDenom at module wiring, only the amount is
			// configurable here. val is funded with feeDenom in provider-init.sh
			if provider, ok := appState["provider"].(map[string]any); ok {
				if params, ok := provider["params"].(map[string]any); ok {
					params["blocks_per_epoch"] = "5"
					params["fees_per_block_amount"] = "1000"
				}
			}
		},
	}

	s.cdc = makeCodec()

	var err error

	// Create Docker pool
	s.dkrPool, err = dockertest.NewPool("")
	s.Require().NoError(err, "failed to create docker pool")

	s.dkrPool.MaxWait = 5 * time.Minute

	// Remove stale containers from previous failed runs
	s.cleanupStaleContainers()

	// Create Docker network
	s.dkrNet, err = s.dkrPool.CreateNetwork(dockerNetwork)
	s.Require().NoError(err, "failed to create docker network")

	s.T().Log("step 1: initializing provider chain...")
	s.provider = &chain{id: s.cfg.providerChainID}
	s.initAndStartProvider()

	s.T().Log("step 2: registering consumer chain on provider...")
	s.registerConsumerOnProvider()

	s.T().Log("step 3: fetching consumer genesis from provider...")
	consumerGenesisJSON := s.fetchConsumerGenesis()

	s.T().Log("step 4: initializing consumer chain...")
	s.consumer = &chain{id: s.cfg.consumerChainID}
	s.initAndStartConsumer(consumerGenesisJSON)

	s.T().Log("step 5: starting ts-relayer and creating IBC v2 path...")
	s.setupTSRelayer()

	s.T().Log("step 6: checking IBC counterparty registration...")
	s.collectIBCDiagnosticsLog()

	s.T().Log("e2e test suite setup complete!")
}

// chmodRecursive changes permissions on a directory recursively.
func chmodRecursive(path string, mode os.FileMode) error {
	cmd := exec.Command("chmod", "-R", fmt.Sprintf("%o", mode), path)
	return cmd.Run()
}
