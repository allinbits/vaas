package e2e

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ory/dockertest/v3"
	"github.com/ory/dockertest/v3/docker"
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
	suite.Suite

	cdc               codec.Codec
	tmpDirs           []string
	provider          *chain
	consumer          *chain
	dkrPool           *dockertest.Pool
	dkrNet            *dockertest.Network
	providerValRes    []*dockertest.Resource
	consumerValRes    []*dockertest.Resource
	tsRelayerResource *dockertest.Resource

	providerVal1DataDir string
	consumerVal1DataDir string
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
	s.provider = &chain{id: providerChainID}
	s.initAndStartProvider()

	s.T().Log("step 2: registering consumer chain on provider...")
	s.registerConsumerOnProvider()

	s.T().Log("step 3: fetching consumer genesis from provider...")
	consumerGenesisJSON := s.fetchConsumerGenesis()

	s.T().Log("step 4: initializing consumer chain...")
	s.consumer = &chain{id: consumerChainID}
	s.initAndStartConsumer(consumerGenesisJSON)

	s.T().Log("step 5: starting ts-relayer and creating IBC v2 path...")
	s.setupTSRelayer()

	s.T().Log("step 6: checking IBC counterparty registration...")
	s.collectIBCDiagnosticsLog()

	s.T().Log("e2e test suite setup complete!")
}

// TearDownSuite cleans up all Docker resources and temp directories.
func (s *IntegrationTestSuite) TearDownSuite() {
	s.T().Log("tearing down e2e integration test suite...")

	if os.Getenv("VAAS_E2E_SKIP_CLEANUP") == "true" {
		s.T().Log("skipping cleanup (VAAS_E2E_SKIP_CLEANUP=true)")
		return
	}

	s.stopTSRelayer()

	// Purge consumer validators
	for _, r := range s.consumerValRes {
		if err := s.dkrPool.Purge(r); err != nil {
			s.T().Logf("failed to purge consumer container: %v", err)
		}
	}

	// Purge provider validators
	for _, r := range s.providerValRes {
		if err := s.dkrPool.Purge(r); err != nil {
			s.T().Logf("failed to purge provider container: %v", err)
		}
	}

	// Remove network
	if s.dkrNet != nil {
		if err := s.dkrPool.RemoveNetwork(s.dkrNet); err != nil {
			s.T().Logf("failed to remove network: %v", err)
		}
	}

	// Remove temp dirs
	for _, dir := range s.tmpDirs {
		_ = os.RemoveAll(dir)
	}
}

// cleanupStaleContainers removes containers from previous failed test runs.
func (s *IntegrationTestSuite) cleanupStaleContainers() {
	staleNames := []string{
		"provider-init",
		"consumer-init",
		"consumer-val1-init",
		fmt.Sprintf("%s-val0", providerChainID),
		fmt.Sprintf("%s-val1", providerChainID),
		fmt.Sprintf("%s-val0", consumerChainID),
		fmt.Sprintf("%s-val1", consumerChainID),
		fmt.Sprintf("%s-%s-ts-relayer", providerChainID, consumerChainID),
	}
	for _, name := range staleNames {
		c, err := s.dkrPool.Client.InspectContainer(name)
		if err != nil {
			continue // container doesn't exist
		}
		s.T().Logf("removing stale container: %s", name)
		_ = s.dkrPool.Client.RemoveContainer(docker.RemoveContainerOptions{
			ID:            c.ID,
			Force:         true,
			RemoveVolumes: true,
		})
	}
	// Also remove stale network
	_ = s.dkrPool.Client.RemoveNetwork(dockerNetwork)
}

// runInitContainer starts an init container with the given script mounted,
// waits for it to exit, checks the exit code, and purges it.
func (s *IntegrationTestSuite) runInitContainer(name, scriptPath, containerScriptPath, dataDir, homePath string, env []string) {
	initResource, err := s.dkrPool.RunWithOptions(
		&dockertest.RunOptions{
			Name:       name,
			Repository: e2eChainImage,
			NetworkID:  s.dkrNet.Network.ID,
			User:       "nonroot",
			Env:        env,
			Mounts: []string{
				fmt.Sprintf("%s:%s", dataDir, homePath),
				fmt.Sprintf("%s:%s", scriptPath, containerScriptPath),
			},
			Entrypoint: []string{"sh", containerScriptPath},
		},
		func(config *docker.HostConfig) {
			config.RestartPolicy = docker.RestartPolicy{Name: "no"}
		},
	)
	s.Require().NoError(err, "failed to start %s container", name)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	exitCode, err := s.dkrPool.Client.WaitContainerWithContext(initResource.Container.ID, ctx)
	s.Require().NoError(err, "%s container wait failed", name)

	var logBuf bytes.Buffer
	_ = s.dkrPool.Client.Logs(docker.LogsOptions{
		Container:    initResource.Container.ID,
		OutputStream: &logBuf,
		ErrorStream:  &logBuf,
		Stdout:       true,
		Stderr:       true,
	})
	s.Require().Equal(0, exitCode, "%s container exited with code %d\noutput:\n%s", name, exitCode, logBuf.String())

	s.Require().NoError(s.dkrPool.Purge(initResource), "failed to purge %s container", name)
}

// initAndStartProvider initializes the provider chain using a temporary Docker
// container that runs provider-init.sh, then starts the actual chain containers.
func (s *IntegrationTestSuite) initAndStartProvider() {
	// Create host directory for provider val0 data
	providerDir, err := os.MkdirTemp("", "vaas-e2e-provider-")
	s.Require().NoError(err)
	s.tmpDirs = append(s.tmpDirs, providerDir)
	s.provider.dataDir = providerDir

	// Create host directory for provider val1 data
	providerVal1Dir, err := os.MkdirTemp("", "vaas-e2e-provider-val1-")
	s.Require().NoError(err)
	s.tmpDirs = append(s.tmpDirs, providerVal1Dir)
	s.providerVal1DataDir = providerVal1Dir

	// Make writable
	s.Require().NoError(os.Chmod(providerDir, 0o777))
	s.Require().NoError(os.Chmod(providerVal1Dir, 0o777))

	// Run init script in a temporary container with both data dirs mounted
	scriptPath := filepath.Join(testDir(), "scripts", "provider-init.sh")
	providerVal1HomePath := providerHomePath + "-val1"
	initResource, err := s.dkrPool.RunWithOptions(
		&dockertest.RunOptions{
			Name:       "provider-init",
			Repository: e2eChainImage,
			NetworkID:  s.dkrNet.Network.ID,
			User:       "nonroot",
			Env: []string{
				"BINARY=" + providerBinary,
				"HOME_DIR=" + providerHomePath,
				"CHAIN_ID=" + providerChainID,
				"DENOM=" + bondDenom,
				"MNEMONIC=" + relayerMnemonic,
			},
			Mounts: []string{
				fmt.Sprintf("%s:%s", providerDir, providerHomePath),
				fmt.Sprintf("%s:%s", providerVal1Dir, providerVal1HomePath),
				fmt.Sprintf("%s:/scripts/provider-init.sh", scriptPath),
			},
			Entrypoint: []string{"sh", "/scripts/provider-init.sh"},
		},
		func(config *docker.HostConfig) {
			config.RestartPolicy = docker.RestartPolicy{Name: "no"}
		},
	)
	s.Require().NoError(err, "failed to start provider-init container")

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	exitCode, err := s.dkrPool.Client.WaitContainerWithContext(initResource.Container.ID, ctx)
	s.Require().NoError(err, "provider-init container wait failed")

	var logBuf bytes.Buffer
	_ = s.dkrPool.Client.Logs(docker.LogsOptions{
		Container:    initResource.Container.ID,
		OutputStream: &logBuf,
		ErrorStream:  &logBuf,
		Stdout:       true,
		Stderr:       true,
	})
	s.Require().Equal(0, exitCode, "provider-init exited with code %d\noutput:\n%s", exitCode, logBuf.String())
	s.Require().NoError(s.dkrPool.Purge(initResource), "failed to purge provider-init container")

	// Modify genesis on the host: set fast voting period and small blocks_per_epoch
	genesisFile := filepath.Join(providerDir, "config", "genesis.json")
	s.patchGenesisJSON(genesisFile, func(genesis map[string]any) {
		appState := genesis["app_state"].(map[string]any)

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
	})

	// Copy the patched genesis to val1's data dir
	s.Require().NoError(
		copyFile(genesisFile, filepath.Join(providerVal1Dir, "config", "genesis.json")),
		"failed to copy patched genesis to val1 data dir",
	)

	// Now start the provider val0 container
	s.T().Log("starting provider val0 container...")

	val0Resource, err := s.dkrPool.RunWithOptions(
		&dockertest.RunOptions{
			Name:       fmt.Sprintf("%s-val0", providerChainID),
			Repository: e2eChainImage,
			NetworkID:  s.dkrNet.Network.ID,
			Mounts: []string{
				fmt.Sprintf("%s:%s", providerDir, providerHomePath),
			},
			PortBindings: map[docker.Port][]docker.PortBinding{
				"26657/tcp": {{HostIP: "", HostPort: "26657"}},
				"9090/tcp":  {{HostIP: "", HostPort: "9090"}},
				"1317/tcp":  {{HostIP: "", HostPort: "1317"}},
				"26656/tcp": {{HostIP: "", HostPort: "26656"}},
			},
			Cmd: []string{
				providerBinary, "start",
				"--home", providerHomePath,
			},
		},
		func(config *docker.HostConfig) {
			config.RestartPolicy = docker.RestartPolicy{Name: "no"}
		},
	)
	s.Require().NoError(err, "failed to start provider val0 container")

	s.providerValRes = append(s.providerValRes, val0Resource)
	s.T().Logf("provider val0 container started: %s", val0Resource.Container.ID[:12])

	// Wait for provider val0 to produce blocks
	waitCtx, waitCancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer waitCancel()
	err = s.waitForChainHeight(waitCtx, "http://localhost:26657", 3)
	s.Require().NoError(err, "provider failed to produce blocks")
	s.T().Log("provider chain is producing blocks")

	// Start provider val1 (second validator node) connecting to val0 as persistent peer
	s.T().Log("starting provider val1 container...")

	val0NodeID := queryNodeID("http://localhost:26657")
	s.Require().NotEmpty(val0NodeID, "failed to query val0 node ID")
	val0P2PAddr := fmt.Sprintf("%s@%s:26656", val0NodeID, val0Resource.Container.Name[1:])
	s.T().Logf("provider val1 persistent peer: %s", val0P2PAddr)

	// Set persistent_peers in val1's config.toml so it connects to val0 on startup
	s.setPersistentPeers(filepath.Join(providerVal1Dir, "config", "config.toml"), val0P2PAddr)

	val1Resource, err := s.dkrPool.RunWithOptions(
		&dockertest.RunOptions{
			Name:       fmt.Sprintf("%s-val1", providerChainID),
			Repository: e2eChainImage,
			NetworkID:  s.dkrNet.Network.ID,
			Mounts: []string{
				fmt.Sprintf("%s:%s", providerVal1Dir, providerVal1HomePath),
			},
			PortBindings: map[docker.Port][]docker.PortBinding{
				"26657/tcp": {{HostIP: "", HostPort: "26658"}},
				"9090/tcp":  {{HostIP: "", HostPort: "9091"}},
				"1317/tcp":  {{HostIP: "", HostPort: "1318"}},
				"26656/tcp": {{HostIP: "", HostPort: "26659"}},
			},
			Cmd: []string{
				providerBinary, "start",
				"--home", providerVal1HomePath,
			},
		},
		func(config *docker.HostConfig) {
			config.RestartPolicy = docker.RestartPolicy{Name: "no"}
		},
	)
	s.Require().NoError(err, "failed to start provider val1 container")

	s.providerValRes = append(s.providerValRes, val1Resource)
	s.T().Logf("provider val1 container started: %s", val1Resource.Container.ID[:12])

	// Wait for provider val1 to catch up
	waitCtx1, waitCancel1 := context.WithTimeout(context.Background(), 2*time.Minute)
	defer waitCancel1()
	err = s.waitForChainHeight(waitCtx1, "http://localhost:26658", 3)
	s.Require().NoError(err, "provider val1 failed to catch up")
	s.T().Log("provider val1 is caught up")
}

// registerConsumerOnProvider creates a consumer chain registration on the provider.
func (s *IntegrationTestSuite) registerConsumerOnProvider() {
	// Read consumer JSON template and substitute chain ID
	templatePath := filepath.Join(testDir(), "testdata", "create_consumer.json")
	templateBytes, err := os.ReadFile(templatePath)
	s.Require().NoError(err, "failed to read create_consumer.json template")

	createConsumerJSON := strings.ReplaceAll(string(templateBytes), "CONSUMER_CHAIN_ID", consumerChainID)

	// Write JSON to container and execute tx
	s.dockerExecMust(s.providerValRes[0].Container.ID, []string{
		"sh", "-c", fmt.Sprintf("echo '%s' > /tmp/create_consumer.json", createConsumerJSON),
	})

	_, _, err = s.dockerExec(s.providerValRes[0].Container.ID, []string{
		providerBinary, "tx", "provider", "create-consumer", "/tmp/create_consumer.json",
		"--from", "val",
		"--home", providerHomePath,
		"--keyring-backend", "test",
		"--chain-id", providerChainID,
		"--gas", "auto",
		"--gas-adjustment", "1.5",
		"--fees", "10000" + bondDenom,
		"-y",
	})

	s.Require().NoError(err, "failed to create consumer on provider")

	// Wait for the tx to be included
	time.Sleep(10 * time.Second)
}

// fetchConsumerGenesis queries the provider for the consumer genesis data.
func (s *IntegrationTestSuite) fetchConsumerGenesis() []byte {
	var output string
	var lastErr error

	// Retry fetching consumer genesis (it may take a few blocks)
	for range 30 {
		stdout, _, err := s.dockerExec(s.providerValRes[0].Container.ID, []string{
			providerBinary, "query", "provider", "consumer-genesis", "0",
			"--home", providerHomePath,
			"--output", "json",
		})
		if err == nil {
			output = stdout.String()
			if output != "" && !strings.Contains(output, "not found") && !strings.Contains(output, "Error") {
				break
			}
		}
		lastErr = err
		time.Sleep(3 * time.Second)
	}
	s.Require().NotEmpty(output, "consumer genesis is empty, last error: %v", lastErr)

	s.T().Logf("fetched consumer genesis (%d bytes)", len(output))
	return []byte(output)
}

// initAndStartConsumer initializes the consumer chain and starts it.
func (s *IntegrationTestSuite) initAndStartConsumer(consumerGenesisJSON []byte) {
	// Create host directory for consumer val0 data
	consumerDir, err := os.MkdirTemp("", "vaas-e2e-consumer-")
	s.Require().NoError(err)
	s.tmpDirs = append(s.tmpDirs, consumerDir)
	s.consumer.dataDir = consumerDir

	s.Require().NoError(os.Chmod(consumerDir, 0o777))

	// Run init script in a temporary container
	scriptPath := filepath.Join(testDir(), "scripts", "consumer-init.sh")
	s.runInitContainer("consumer-init", scriptPath, "/scripts/consumer-init.sh", consumerDir, consumerHomePath, []string{
		"BINARY=" + consumerBinary,
		"HOME_DIR=" + consumerHomePath,
		"CHAIN_ID=" + consumerChainID,
		"DENOM=" + bondDenom,
		"MNEMONIC=" + relayerMnemonic,
	})

	// Patch consumer genesis with provider's consumer-genesis data on the host
	genesisFile := filepath.Join(consumerDir, "config", "genesis.json")
	err = patchConsumerGenesisWithProviderData(genesisFile, consumerGenesisJSON)
	s.Require().NoError(err, "failed to patch consumer genesis")

	// Patch consumer slashing params for aggressive downtime detection
	s.patchConsumerSlashingParams()

	// Copy validator keys from provider val0 to consumer val0
	providerDir := s.provider.dataDir
	err = copyFile(
		filepath.Join(providerDir, "config", "priv_validator_key.json"),
		filepath.Join(consumerDir, "config", "priv_validator_key.json"),
	)
	s.Require().NoError(err, "failed to copy priv_validator_key.json")

	err = copyFile(
		filepath.Join(providerDir, "config", "node_key.json"),
		filepath.Join(consumerDir, "config", "node_key.json"),
	)
	s.Require().NoError(err, "failed to copy node_key.json")

	// Start the consumer val0 container
	s.T().Log("starting consumer val0 container...")

	consumerVal0Resource, err := s.dkrPool.RunWithOptions(
		&dockertest.RunOptions{
			Name:       fmt.Sprintf("%s-val0", consumerChainID),
			Repository: e2eChainImage,
			NetworkID:  s.dkrNet.Network.ID,
			Mounts: []string{
				fmt.Sprintf("%s:%s", consumerDir, consumerHomePath),
			},
			PortBindings: map[docker.Port][]docker.PortBinding{
				"26657/tcp": {{HostIP: "", HostPort: "26667"}},
				"9090/tcp":  {{HostIP: "", HostPort: "9092"}},
				"1317/tcp":  {{HostIP: "", HostPort: "1327"}},
				"26656/tcp": {{HostIP: "", HostPort: "26666"}},
			},
			Cmd: []string{
				consumerBinary, "start",
				"--home", consumerHomePath,
			},
		},
		func(config *docker.HostConfig) {
			config.RestartPolicy = docker.RestartPolicy{Name: "no"}
		},
	)
	s.Require().NoError(err, "failed to start consumer val0 container")

	s.consumerValRes = append(s.consumerValRes, consumerVal0Resource)
	s.T().Logf("consumer val0 container started: %s", consumerVal0Resource.Container.ID[:12])

	// Wait for consumer to produce blocks
	waitCtx, waitCancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer waitCancel()
	err = s.waitForChainHeight(waitCtx, "http://localhost:26667", 3)
	s.Require().NoError(err, "consumer failed to produce blocks")
	s.T().Log("consumer chain is producing blocks")

	// Create consumer val1 (second validator node with val1's keys)
	s.T().Log("setting up consumer val1 node...")

	consumerVal1Dir, err := os.MkdirTemp("", "vaas-e2e-consumer-val1-")
	s.Require().NoError(err)
	s.tmpDirs = append(s.tmpDirs, consumerVal1Dir)
	s.consumerVal1DataDir = consumerVal1Dir
	s.Require().NoError(os.Chmod(consumerVal1Dir, 0o777))

	// Run consumer init for val1's node
	consumerVal1HomePath := consumerHomePath + "-val1"
	s.runInitContainer("consumer-val1-init", scriptPath, "/scripts/consumer-init.sh", consumerVal1Dir, consumerVal1HomePath, []string{
		"BINARY=" + consumerBinary,
		"HOME_DIR=" + consumerVal1HomePath,
		"CHAIN_ID=" + consumerChainID,
		"DENOM=" + bondDenom,
		"MNEMONIC=" + relayerMnemonic,
	})

	// Copy the patched genesis from consumer val0
	s.Require().NoError(
		copyFile(genesisFile, filepath.Join(consumerVal1Dir, "config", "genesis.json")),
		"failed to copy consumer genesis to val1 data dir",
	)

	// Copy val1's validator keys from provider val1 to consumer val1
	s.Require().NoError(
		copyFile(
			filepath.Join(s.providerVal1DataDir, "config", "priv_validator_key.json"),
			filepath.Join(consumerVal1Dir, "config", "priv_validator_key.json"),
		),
		"failed to copy val1 priv_validator_key.json to consumer val1",
	)

	// Start consumer val1 connecting to val0 as persistent peer
	consumerVal0NodeID := queryNodeID("http://localhost:26667")
	s.Require().NotEmpty(consumerVal0NodeID, "failed to query consumer val0 node ID")
	consumerVal0P2PAddr := fmt.Sprintf("%s@%s:26656", consumerVal0NodeID, consumerVal0Resource.Container.Name[1:])
	s.T().Logf("consumer val1 persistent peer: %s", consumerVal0P2PAddr)

	// Set persistent_peers in consumer val1's config.toml
	s.setPersistentPeers(filepath.Join(consumerVal1Dir, "config", "config.toml"), consumerVal0P2PAddr)

	consumerVal1Resource, err := s.dkrPool.RunWithOptions(
		&dockertest.RunOptions{
			Name:       fmt.Sprintf("%s-val1", consumerChainID),
			Repository: e2eChainImage,
			NetworkID:  s.dkrNet.Network.ID,
			Mounts: []string{
				fmt.Sprintf("%s:%s", consumerVal1Dir, consumerVal1HomePath),
			},
			PortBindings: map[docker.Port][]docker.PortBinding{
				"26657/tcp": {{HostIP: "", HostPort: "26668"}},
				"9090/tcp":  {{HostIP: "", HostPort: "9093"}},
				"1317/tcp":  {{HostIP: "", HostPort: "1328"}},
				"26656/tcp": {{HostIP: "", HostPort: "26669"}},
			},
			Cmd: []string{
				consumerBinary, "start",
				"--home", consumerVal1HomePath,
			},
		},
		func(config *docker.HostConfig) {
			config.RestartPolicy = docker.RestartPolicy{Name: "no"}
		},
	)
	s.Require().NoError(err, "failed to start consumer val1 container")

	s.consumerValRes = append(s.consumerValRes, consumerVal1Resource)
	s.T().Logf("consumer val1 container started: %s", consumerVal1Resource.Container.ID[:12])

	// Wait for consumer val1 to catch up
	waitCtx2, waitCancel2 := context.WithTimeout(context.Background(), 2*time.Minute)
	defer waitCancel2()
	err = s.waitForChainHeight(waitCtx2, "http://localhost:26668", 3)
	s.Require().NoError(err, "consumer val1 failed to catch up")
	s.T().Log("consumer val1 is caught up")
}

// setupTSRelayer starts the ts-relayer container, configures it with
// mnemonics and gas prices for both chains, and creates an IBC v2 path.
// The ts-relayer handles counterparty registration and packet relaying.
func (s *IntegrationTestSuite) setupTSRelayer() {
	s.startTSRelayer()

	s.tsRelayerAddMnemonic(providerChainID, relayerMnemonic)
	s.tsRelayerAddMnemonic(consumerChainID, relayerMnemonic)
	s.tsRelayerAddGasPrice(providerChainID, "0.025"+bondDenom)
	s.tsRelayerAddGasPrice(consumerChainID, "0.025"+bondDenom)

	s.tsRelayerAddPath(IBCv2)

	s.tsRelayerDumpPaths()
	s.startTSRelayerRelay()
	s.T().Log("ts-relayer IBC v2 path configured")
}

// setPersistentPeers sets the persistent_peers field in a CometBFT config.toml.
func (s *IntegrationTestSuite) setPersistentPeers(configPath, peers string) {
	data, err := os.ReadFile(configPath)
	s.Require().NoError(err, "failed to read config.toml")
	data = []byte(strings.Replace(string(data),
		"persistent_peers = \"\"",
		fmt.Sprintf("persistent_peers = \"%s\"", peers),
		1,
	))
	s.Require().NoError(os.WriteFile(configPath, data, 0o644), "failed to write config.toml")
}

// waitForChainHeight polls a CometBFT RPC endpoint until the chain reaches
// the given block height.
func (s *IntegrationTestSuite) waitForChainHeight(ctx context.Context, rpcEndpoint string, minHeight int64) error {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("timeout waiting for chain at %s to reach height %d", rpcEndpoint, minHeight)
		case <-ticker.C:
			height, err := queryBlockHeight(rpcEndpoint)
			if err != nil {
				continue
			}
			if height >= minHeight {
				return nil
			}
		}
	}
}
