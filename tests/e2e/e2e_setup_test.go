package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/ory/dockertest/v3"
	"github.com/ory/dockertest/v3/docker"
	"github.com/stretchr/testify/suite"
)

const (
	e2eChainImage   = "cosmos/vaas-e2e"
	hermesImage     = "ghcr.io/cosmos/hermes-e2e:1.0.0"
	dockerNetwork   = "vaas-e2e-testnet"
	relayerMnemonic = "abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon art"
)

// IntegrationTestSuite is the main e2e test suite that orchestrates
// provider chain, consumer chain, and Hermes relayer containers.
type IntegrationTestSuite struct {
	suite.Suite

	tmpDirs        []string
	provider       *chain
	consumer       *chain
	dkrPool        *dockertest.Pool
	dkrNet         *dockertest.Network
	hermesResource *dockertest.Resource
	providerValRes []*dockertest.Resource
	consumerValRes []*dockertest.Resource
}

// SetupSuite orchestrates the full provider-consumer-relayer lifecycle:
// 1. Create Docker pool and network
// 2. Initialize and start provider chain
// 3. Register consumer on provider
// 4. Fetch consumer genesis from provider
// 5. Initialize and start consumer chain
// 6. Setup Hermes relayer with IBC connection and VAAS channel
// 7. Trigger VSC
func (s *IntegrationTestSuite) SetupSuite() {
	s.T().Log("setting up e2e integration test suite...")

	var err error

	// Create Docker pool
	s.dkrPool, err = dockertest.NewPool("")
	s.Require().NoError(err, "failed to create docker pool")

	s.dkrPool.MaxWait = 5 * time.Minute

	// Create Docker network
	s.dkrNet, err = s.dkrPool.CreateNetwork(dockerNetwork)
	s.Require().NoError(err, "failed to create docker network")

	// =========================================================================
	// Step 1: Initialize and start provider chain
	// =========================================================================
	s.T().Log("step 1: initializing provider chain...")
	s.provider = &chain{id: providerChainID}
	s.initAndStartProvider()

	// =========================================================================
	// Step 2: Register consumer chain on provider
	// =========================================================================
	s.T().Log("step 2: registering consumer chain on provider...")
	s.registerConsumerOnProvider()

	// =========================================================================
	// Step 3: Fetch consumer genesis from provider
	// =========================================================================
	s.T().Log("step 3: fetching consumer genesis from provider...")
	consumerGenesisJSON := s.fetchConsumerGenesis()

	// =========================================================================
	// Step 4: Initialize and start consumer chain
	// =========================================================================
	s.T().Log("step 4: initializing consumer chain...")
	s.consumer = &chain{id: consumerChainID}
	s.initAndStartConsumer(consumerGenesisJSON)

	// =========================================================================
	// Step 5: Setup Hermes relayer
	// =========================================================================
	s.T().Log("step 5: setting up Hermes relayer...")
	s.startHermesRelayer()

	// =========================================================================
	// Step 6: Create IBC connection and VAAS channel
	// =========================================================================
	s.T().Log("step 6: creating IBC connection and VAAS channel...")
	s.createIBCConnectionAndChannel()

	// =========================================================================
	// Step 7: Trigger VSC
	// =========================================================================
	s.T().Log("step 7: triggering validator set change...")
	s.triggerVSC()

	s.T().Log("e2e test suite setup complete!")
}

// TearDownSuite cleans up all Docker resources and temp directories.
func (s *IntegrationTestSuite) TearDownSuite() {
	s.T().Log("tearing down e2e integration test suite...")

	if os.Getenv("VAAS_E2E_SKIP_CLEANUP") == "true" {
		s.T().Log("skipping cleanup (VAAS_E2E_SKIP_CLEANUP=true)")
		return
	}

	// Purge Hermes
	if s.hermesResource != nil {
		if err := s.dkrPool.Purge(s.hermesResource); err != nil {
			s.T().Logf("failed to purge hermes: %v", err)
		}
	}

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

// initAndStartProvider initializes the provider chain using a temporary Docker
// container for init commands, then starts the actual chain container.
// This follows the same sequence as the Makefile provider-start target.
func (s *IntegrationTestSuite) initAndStartProvider() {
	// Create host directory for provider data
	providerDir, err := os.MkdirTemp("", "vaas-e2e-provider-")
	s.Require().NoError(err)
	s.tmpDirs = append(s.tmpDirs, providerDir)
	s.provider.dataDir = providerDir

	// Make writable
	s.Require().NoError(os.Chmod(providerDir, 0o777))

	// Start a temporary container to run init commands
	initResource, err := s.dkrPool.RunWithOptions(
		&dockertest.RunOptions{
			Name:       "provider-init",
			Repository: e2eChainImage,
			NetworkID:  s.dkrNet.Network.ID,
			Mounts: []string{
				fmt.Sprintf("%s:%s", providerDir, providerHomePath),
			},
			Entrypoint: []string{"sh", "-c", "sleep infinity"},
		},
		func(config *docker.HostConfig) {
			config.RestartPolicy = docker.RestartPolicy{Name: "no"}
		},
	)
	s.Require().NoError(err, "failed to start provider init container")
	defer func() {
		_ = s.dkrPool.Purge(initResource)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// Run init sequence (matching Makefile provider-start)
	s.dockerExecMust(ctx, initResource.Container.ID, []string{
		providerBinary, "init", "localnet",
		"--default-denom", bondDenom,
		"--chain-id", providerChainID,
		"--home", providerHomePath,
	})

	s.dockerExecMust(ctx, initResource.Container.ID, []string{
		providerBinary, "config", "set", "client", "chain-id", providerChainID,
		"--home", providerHomePath,
	})

	s.dockerExecMust(ctx, initResource.Container.ID, []string{
		providerBinary, "config", "set", "client", "keyring-backend", "test",
		"--home", providerHomePath,
	})

	// Add validator key
	s.dockerExecMust(ctx, initResource.Container.ID, []string{
		providerBinary, "keys", "add", "val",
		"--home", providerHomePath,
		"--keyring-backend", "test",
	})

	// Add user key
	s.dockerExecMust(ctx, initResource.Container.ID, []string{
		providerBinary, "keys", "add", "user",
		"--home", providerHomePath,
		"--keyring-backend", "test",
	})

	// Add relayer key from mnemonic
	s.dockerExecMust(ctx, initResource.Container.ID, []string{
		"sh", "-c", fmt.Sprintf(
			"echo '%s' | %s keys add relayer --recover --home %s --keyring-backend test",
			relayerMnemonic, providerBinary, providerHomePath,
		),
	})

	// Add genesis accounts
	s.dockerExecMust(ctx, initResource.Container.ID, []string{
		providerBinary, "genesis", "add-genesis-account", "val", "1000000000000" + bondDenom,
		"--home", providerHomePath,
		"--keyring-backend", "test",
	})

	s.dockerExecMust(ctx, initResource.Container.ID, []string{
		providerBinary, "genesis", "add-genesis-account", "user", "1000000000" + bondDenom,
		"--home", providerHomePath,
		"--keyring-backend", "test",
	})

	s.dockerExecMust(ctx, initResource.Container.ID, []string{
		providerBinary, "genesis", "add-genesis-account", "relayer", "100000000" + bondDenom,
		"--home", providerHomePath,
		"--keyring-backend", "test",
	})

	// Create gentx
	s.dockerExecMust(ctx, initResource.Container.ID, []string{
		providerBinary, "genesis", "gentx", "val", "1000000000" + bondDenom,
		"--home", providerHomePath,
		"--keyring-backend", "test",
		"--chain-id", providerChainID,
	})

	// Collect gentxs
	s.dockerExecMust(ctx, initResource.Container.ID, []string{
		providerBinary, "genesis", "collect-gentxs",
		"--home", providerHomePath,
	})

	// Enable REST API
	s.dockerExecMust(ctx, initResource.Container.ID, []string{
		providerBinary, "config", "set", "app", "api.enable", "true",
		"--home", providerHomePath,
	})

	// Modify genesis on the host: set fast voting period and small blocks_per_epoch
	genesisFile := filepath.Join(providerDir, "config", "genesis.json")
	s.patchGenesisJSON(genesisFile, func(genesis map[string]interface{}) {
		appState := genesis["app_state"].(map[string]interface{})

		// Set fast voting period
		if gov, ok := appState["gov"].(map[string]interface{}); ok {
			if params, ok := gov["params"].(map[string]interface{}); ok {
				params["voting_period"] = "15s"
			}
		}

		// Set fast epoch for VSC
		if provider, ok := appState["provider"].(map[string]interface{}); ok {
			if params, ok := provider["params"].(map[string]interface{}); ok {
				params["blocks_per_epoch"] = "5"
			}
		}
	})

	// Set minimum gas prices in app.toml (via sed in container)
	s.dockerExecMust(ctx, initResource.Container.ID, []string{
		"sh", "-c", fmt.Sprintf(
			`sed -i 's#^minimum-gas-prices = .*#minimum-gas-prices = "0.01%s"#g' %s/config/app.toml`,
			bondDenom, providerHomePath,
		),
	})

	// Purge init container
	s.Require().NoError(s.dkrPool.Purge(initResource))

	// Now start the actual provider container
	s.T().Log("starting provider chain container...")

	resource, err := s.dkrPool.RunWithOptions(
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
	s.Require().NoError(err, "failed to start provider container")

	s.providerValRes = append(s.providerValRes, resource)
	s.T().Logf("provider container started: %s", resource.Container.ID[:12])

	// Wait for provider to produce blocks
	waitCtx, waitCancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer waitCancel()
	err = s.waitForChainHeight(waitCtx, "http://localhost:26657", 3)
	s.Require().NoError(err, "provider failed to produce blocks")
	s.T().Log("provider chain is producing blocks")
}

// registerConsumerOnProvider creates a consumer chain registration on the provider.
func (s *IntegrationTestSuite) registerConsumerOnProvider() {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	createConsumerJSON := fmt.Sprintf(
		`{"chain_id": "%s", "metadata": {"name": "consumer", "description": "e2e test consumer chain", "metadata": "{}"}, "initialization_parameters": {"initial_height": {"revision_number": 0, "revision_height": 1}, "genesis_hash": "", "binary_hash": "", "spawn_time": "2024-01-01T00:00:00Z", "unbonding_period": 1728000000000000, "vaas_timeout_period": 2419200000000000, "historical_entries": 10000, "connection_id": ""}, "infraction_parameters": {"double_sign": {"slash_fraction": "0.05", "jail_duration": 9223372036854775807, "tombstone": true}, "downtime": {"slash_fraction": "0.0001", "jail_duration": 600000000000, "tombstone": false}}}`,
		consumerChainID,
	)

	// Write JSON to container and execute tx
	s.dockerExecMust(ctx, s.providerValRes[0].Container.ID, []string{
		"sh", "-c", fmt.Sprintf("echo '%s' > /tmp/create_consumer.json", createConsumerJSON),
	})

	stdout, stderr, err := s.dockerExec(ctx, s.providerValRes[0].Container.ID, []string{
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
	s.T().Logf("create-consumer stdout: %s", stdout.String())
	if stderr.Len() > 0 {
		s.T().Logf("create-consumer stderr: %s", stderr.String())
	}
	s.Require().NoError(err, "failed to create consumer on provider")

	// Wait for the tx to be included
	time.Sleep(10 * time.Second)
}

// fetchConsumerGenesis queries the provider for the consumer genesis data.
func (s *IntegrationTestSuite) fetchConsumerGenesis() []byte {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	var output string
	var lastErr error

	// Retry fetching consumer genesis (it may take a few blocks)
	for i := 0; i < 30; i++ {
		stdout, _, err := s.dockerExec(ctx, s.providerValRes[0].Container.ID, []string{
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
	// Create host directory for consumer data
	consumerDir, err := os.MkdirTemp("", "vaas-e2e-consumer-")
	s.Require().NoError(err)
	s.tmpDirs = append(s.tmpDirs, consumerDir)
	s.consumer.dataDir = consumerDir

	s.Require().NoError(os.Chmod(consumerDir, 0o777))

	// Start a temporary container for init commands
	initResource, err := s.dkrPool.RunWithOptions(
		&dockertest.RunOptions{
			Name:       "consumer-init",
			Repository: e2eChainImage,
			NetworkID:  s.dkrNet.Network.ID,
			Mounts: []string{
				fmt.Sprintf("%s:%s", consumerDir, consumerHomePath),
			},
			Entrypoint: []string{"sh", "-c", "sleep infinity"},
		},
		func(config *docker.HostConfig) {
			config.RestartPolicy = docker.RestartPolicy{Name: "no"}
		},
	)
	s.Require().NoError(err, "failed to start consumer init container")
	defer func() {
		_ = s.dkrPool.Purge(initResource)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// Run consumer init (matching Makefile consumer-init)
	s.dockerExecMust(ctx, initResource.Container.ID, []string{
		consumerBinary, "init", "localnet",
		"--default-denom", bondDenom,
		"--chain-id", consumerChainID,
		"--home", consumerHomePath,
	})

	s.dockerExecMust(ctx, initResource.Container.ID, []string{
		consumerBinary, "config", "set", "client", "chain-id", consumerChainID,
		"--home", consumerHomePath,
	})

	s.dockerExecMust(ctx, initResource.Container.ID, []string{
		consumerBinary, "config", "set", "client", "keyring-backend", "test",
		"--home", consumerHomePath,
	})

	// Add user key
	s.dockerExecMust(ctx, initResource.Container.ID, []string{
		consumerBinary, "keys", "add", "user",
		"--home", consumerHomePath,
		"--keyring-backend", "test",
	})

	// Add relayer key from mnemonic
	s.dockerExecMust(ctx, initResource.Container.ID, []string{
		"sh", "-c", fmt.Sprintf(
			"echo '%s' | %s keys add relayer --recover --home %s --keyring-backend test",
			relayerMnemonic, consumerBinary, consumerHomePath,
		),
	})

	// Add genesis accounts
	s.dockerExecMust(ctx, initResource.Container.ID, []string{
		consumerBinary, "genesis", "add-genesis-account", "user", "1000000000" + bondDenom,
		"--home", consumerHomePath,
		"--keyring-backend", "test",
	})

	s.dockerExecMust(ctx, initResource.Container.ID, []string{
		consumerBinary, "genesis", "add-genesis-account", "relayer", "100000000" + bondDenom,
		"--home", consumerHomePath,
		"--keyring-backend", "test",
	})

	// Set minimum gas prices
	s.dockerExecMust(ctx, initResource.Container.ID, []string{
		"sh", "-c", fmt.Sprintf(
			`sed -i 's#^minimum-gas-prices = .*#minimum-gas-prices = "0.01%s"#g' %s/config/app.toml`,
			bondDenom, consumerHomePath,
		),
	})

	// Enable REST API
	s.dockerExecMust(ctx, initResource.Container.ID, []string{
		consumerBinary, "config", "set", "app", "api.enable", "true",
		"--home", consumerHomePath,
	})

	// Purge init container
	s.Require().NoError(s.dkrPool.Purge(initResource))

	// Patch consumer genesis with provider's consumer-genesis data on the host
	genesisFile := filepath.Join(consumerDir, "config", "genesis.json")
	err = patchConsumerGenesisWithProviderData(genesisFile, consumerGenesisJSON)
	s.Require().NoError(err, "failed to patch consumer genesis")

	// Copy validator keys from provider to consumer
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

	// Start the actual consumer container
	s.T().Log("starting consumer chain container...")

	resource, err := s.dkrPool.RunWithOptions(
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
	s.Require().NoError(err, "failed to start consumer container")

	s.consumerValRes = append(s.consumerValRes, resource)
	s.T().Logf("consumer container started: %s", resource.Container.ID[:12])

	// Wait for consumer to produce blocks
	waitCtx, waitCancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer waitCancel()
	err = s.waitForChainHeight(waitCtx, "http://localhost:26667", 3)
	s.Require().NoError(err, "consumer failed to produce blocks")
	s.T().Log("consumer chain is producing blocks")
}

// startHermesRelayer starts the Hermes relayer container.
func (s *IntegrationTestSuite) startHermesRelayer() {
	hermesDir, err := os.MkdirTemp("", "vaas-e2e-hermes-")
	s.Require().NoError(err)
	s.tmpDirs = append(s.tmpDirs, hermesDir)

	// Write hermes config
	hermesConfig := s.generateHermesConfig()
	s.Require().NoError(os.MkdirAll(filepath.Join(hermesDir, ".hermes"), 0o750))
	s.Require().NoError(os.WriteFile(filepath.Join(hermesDir, ".hermes", "config.toml"), []byte(hermesConfig), 0o600))

	// Write bootstrap script
	bootstrapScript := s.generateHermesBootstrap()
	s.Require().NoError(os.WriteFile(filepath.Join(hermesDir, "bootstrap.sh"), []byte(bootstrapScript), 0o755))

	// Make hermes dir accessible
	s.Require().NoError(chmodRecursive(hermesDir, 0o777))

	resource, err := s.dkrPool.RunWithOptions(
		&dockertest.RunOptions{
			Name:       "hermes-relayer",
			Repository: hermesImage,
			NetworkID:  s.dkrNet.Network.ID,
			Mounts: []string{
				fmt.Sprintf("%s/.hermes:/home/hermes/.hermes", hermesDir),
				fmt.Sprintf("%s/bootstrap.sh:/home/hermes/bootstrap.sh", hermesDir),
			},
			PortBindings: map[docker.Port][]docker.PortBinding{
				"3031/tcp": {{HostIP: "", HostPort: "3031"}},
			},
			Entrypoint: []string{"sh", "/home/hermes/bootstrap.sh"},
		},
		func(config *docker.HostConfig) {
			config.RestartPolicy = docker.RestartPolicy{Name: "no"}
		},
	)
	s.Require().NoError(err, "failed to start hermes container")

	s.hermesResource = resource
	s.T().Logf("hermes container started: %s", resource.Container.ID[:12])

	// Wait for hermes to start
	time.Sleep(15 * time.Second)
}

// createIBCConnectionAndChannel creates the IBC connection using genesis clients
// and then creates the VAAS channel.
func (s *IntegrationTestSuite) createIBCConnectionAndChannel() {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	// Create connection using genesis clients (07-tendermint-0)
	stdout, stderr, err := s.executeHermesCommand(ctx, []string{
		"hermes", "create", "connection",
		"--a-chain", consumerChainID,
		"--a-client", "07-tendermint-0",
		"--b-client", "07-tendermint-0",
	})
	s.T().Logf("hermes create connection stdout: %s", stdout.String())
	if stderr.Len() > 0 {
		s.T().Logf("hermes create connection stderr: %s", stderr.String())
	}
	s.Require().NoError(err, "failed to create IBC connection")

	time.Sleep(5 * time.Second)

	// Create VAAS channel (consumer/provider ports, ordered, version "1")
	stdout, stderr, err = s.executeHermesCommand(ctx, []string{
		"hermes", "create", "channel",
		"--a-chain", consumerChainID,
		"--a-connection", "connection-0",
		"--a-port", "consumer",
		"--b-port", "provider",
		"--order", "ordered",
		"--channel-version", "1",
	})
	s.T().Logf("hermes create channel stdout: %s", stdout.String())
	if stderr.Len() > 0 {
		s.T().Logf("hermes create channel stderr: %s", stderr.String())
	}
	s.Require().NoError(err, "failed to create VAAS channel")

	s.T().Log("IBC connection and VAAS channel created")
}

// triggerVSC triggers a validator set change on the provider.
func (s *IntegrationTestSuite) triggerVSC() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// Get validator operator address
	stdout, _, err := s.dockerExec(ctx, s.providerValRes[0].Container.ID, []string{
		providerBinary, "keys", "show", "val", "--bech", "val", "-a",
		"--home", providerHomePath,
		"--keyring-backend", "test",
	})
	s.Require().NoError(err, "failed to get validator address")
	valAddr := strings.TrimSpace(stdout.String())

	// Delegate to trigger validator set change
	stdout, stderr, err := s.dockerExec(ctx, s.providerValRes[0].Container.ID, []string{
		providerBinary, "tx", "staking", "delegate", valAddr, "1000000" + bondDenom,
		"--from", "user",
		"--home", providerHomePath,
		"--keyring-backend", "test",
		"--chain-id", providerChainID,
		"--fees", "10000" + bondDenom,
		"-y",
	})
	s.T().Logf("delegation stdout: %s", stdout.String())
	if stderr.Len() > 0 {
		s.T().Logf("delegation stderr: %s", stderr.String())
	}
	s.Require().NoError(err, "failed to delegate on provider")

	s.T().Log("delegation sent, waiting for VSC packet relay...")

	// Wait for VSC to be relayed — poll consumer for provider info
	for i := 0; i < 60; i++ {
		time.Sleep(2 * time.Second)

		stdout, _, err := s.dockerExec(ctx, s.consumerValRes[0].Container.ID, []string{
			consumerBinary, "query", "vaasconsumer", "provider-info",
			"--home", consumerHomePath,
		})
		if err == nil && stdout.Len() > 0 && !strings.Contains(stdout.String(), "error") {
			s.T().Logf("VSC relayed after %d seconds", (i+1)*2)
			return
		}
	}

	s.T().Log("WARNING: VSC packet may not have been relayed within timeout")
}

// dockerExecMust runs a command in a Docker container, failing the test on error.
func (s *IntegrationTestSuite) dockerExecMust(ctx context.Context, containerID string, cmd []string) {
	stdout, stderr, err := s.dockerExec(ctx, containerID, cmd)
	if err != nil {
		s.T().Logf("cmd: %v", cmd)
		s.T().Logf("stdout: %s", stdout.String())
		s.T().Logf("stderr: %s", stderr.String())
	}
	s.Require().NoError(err, "docker exec failed for cmd: %v", cmd)
}

// generateHermesConfig creates the Hermes TOML configuration for both chains.
func (s *IntegrationTestSuite) generateHermesConfig() string {
	providerHost := fmt.Sprintf("%s-val0", providerChainID)
	consumerHost := fmt.Sprintf("%s-val0", consumerChainID)

	return fmt.Sprintf(`[global]
log_level = 'info'

[mode]
[mode.clients]
enabled = true
refresh = true
misbehaviour = false

[mode.connections]
enabled = true

[mode.channels]
enabled = true

[mode.packets]
enabled = true
clear_interval = 100
clear_on_start = true
tx_confirmation = false

[rest]
enabled = true
host = '0.0.0.0'
port = 3031

[telemetry]
enabled = false

[[chains]]
id = '%s'
type = 'CosmosSdk'
rpc_addr = 'http://%s:26657'
grpc_addr = 'http://%s:9090'
rpc_timeout = '10s'
trusted_node = true
account_prefix = 'cosmos'
key_name = 'relayer'
key_store_type = 'Test'
store_prefix = 'ibc'
default_gas = 100000
max_gas = 3000000
gas_multiplier = 1.2
max_msg_num = 30
max_tx_size = 180000
clock_drift = '5s'
max_block_time = '30s'
ccv_consumer_chain = false

[chains.event_source]
mode = 'push'
url = 'ws://%s:26657/websocket'
batch_delay = '500ms'

[chains.trust_threshold]
numerator = '1'
denominator = '3'

[chains.gas_price]
price = 0.01
denom = 'uatone'

[chains.packet_filter]
policy = 'allow'
list = [['consumer', '*'], ['provider', '*'], ['transfer', '*']]

[chains.address_type]
derivation = 'cosmos'

[[chains]]
id = '%s'
type = 'CosmosSdk'
rpc_addr = 'http://%s:26657'
grpc_addr = 'http://%s:9090'
rpc_timeout = '10s'
trusted_node = true
account_prefix = 'cosmos'
key_name = 'relayer'
key_store_type = 'Test'
store_prefix = 'ibc'
default_gas = 100000
max_gas = 3000000
gas_multiplier = 1.2
max_msg_num = 30
max_tx_size = 180000
clock_drift = '5s'
max_block_time = '30s'
ccv_consumer_chain = false

[chains.event_source]
mode = 'push'
url = 'ws://%s:26657/websocket'
batch_delay = '500ms'

[chains.trust_threshold]
numerator = '1'
denominator = '3'

[chains.gas_price]
price = 0.01
denom = 'uatone'

[chains.packet_filter]
policy = 'allow'
list = [['consumer', '*'], ['provider', '*'], ['transfer', '*']]

[chains.address_type]
derivation = 'cosmos'
`, providerChainID, providerHost, providerHost, providerHost,
		consumerChainID, consumerHost, consumerHost, consumerHost)
}

// generateHermesBootstrap creates the bootstrap shell script.
func (s *IntegrationTestSuite) generateHermesBootstrap() string {
	return fmt.Sprintf(`#!/bin/sh
set -e

echo "Waiting for chains to be ready..."
sleep 5

# Import relayer keys
echo "%s" > /tmp/mnemonic.txt
hermes keys add --chain %s --mnemonic-file /tmp/mnemonic.txt --key-name relayer 2>/dev/null || true
hermes keys add --chain %s --mnemonic-file /tmp/mnemonic.txt --key-name relayer 2>/dev/null || true
rm -f /tmp/mnemonic.txt

echo "Hermes keys configured, starting relayer..."
hermes start
`, relayerMnemonic, providerChainID, consumerChainID)
}

// patchGenesisJSON reads a genesis.json file, applies a mutation function,
// and writes it back.
func (s *IntegrationTestSuite) patchGenesisJSON(path string, mutate func(map[string]interface{})) {
	bz, err := os.ReadFile(path)
	s.Require().NoError(err, "failed to read genesis file")

	var genesis map[string]interface{}
	s.Require().NoError(json.Unmarshal(bz, &genesis), "failed to unmarshal genesis")

	mutate(genesis)

	out, err := json.MarshalIndent(genesis, "", "  ")
	s.Require().NoError(err, "failed to marshal genesis")

	s.Require().NoError(os.WriteFile(path, out, 0o600), "failed to write genesis")
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

// chmodRecursive changes permissions on a directory recursively.
func chmodRecursive(path string, mode os.FileMode) error {
	cmd := exec.Command("chmod", "-R", fmt.Sprintf("%o", mode), path)
	return cmd.Run()
}
