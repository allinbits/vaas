package e2e

// base_suite_test.go holds the shared Docker-based e2e scaffolding used by both
// IntegrationTestSuite (the main suite) and LivenessIntegrationTestSuite. The
// two suites differ only in a handful of constants (chain IDs, Docker network,
// host ports, container names) and in the genesis / config mutations they apply
// at bring-up. Those differences are captured in baseSuiteConfig; everything
// else -- container lifecycle, chain init, ts-relayer wiring, exec helpers --
// lives here on baseTestSuite and is embedded by each concrete suite.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ory/dockertest/v3"
	"github.com/ory/dockertest/v3/docker"
	"github.com/stretchr/testify/suite"
)

// baseSuiteConfig captures everything that differs between the main and
// liveness suites. A concrete suite fills this in during its SetupSuite before
// invoking the shared bring-up helpers.
type baseSuiteConfig struct {
	providerChainID string
	consumerChainID string
	dockerNetwork   string

	// Init-container names and the MkdirTemp prefix keep the two suites'
	// transient containers and host dirs from colliding on a shared host.
	providerInitName string
	consumerInitName string
	tmpDirPrefix     string

	// Host port bindings for the provider chain container.
	providerRPCPort  string
	providerGRPCPort string
	providerRESTPort string
	providerP2PPort  string

	// Host port bindings for the consumer chain container.
	consumerRPCPort  string
	consumerGRPCPort string
	consumerRESTPort string
	consumerP2PPort  string

	// create-consumer template file (under testdata) and the chain-id
	// placeholder to substitute in it.
	consumerTemplateFile        string
	consumerTemplatePlaceholder string

	// patchProviderGenesis mutates the provider's genesis app_state before the
	// chain container starts.
	patchProviderGenesis func(appState map[string]any)

	// patchProviderConfigToml / patchConsumerConfigToml, when non-nil, mutate
	// the respective config.toml before the chain container starts (the
	// liveness suite uses this for fast blocks). Leaving them nil skips the
	// step.
	patchProviderConfigToml func(path string)
	patchConsumerConfigToml func(path string)
}

// baseTestSuite carries the shared Docker state and helper methods. Concrete
// suites embed it and supply their own cfg and SetupSuite.
type baseTestSuite struct {
	suite.Suite

	cfg baseSuiteConfig

	tmpDirs           []string
	provider          *chain
	consumer          *chain
	dkrPool           *dockertest.Pool
	dkrNet            *dockertest.Network
	providerValRes    []*dockertest.Resource
	consumerValRes    []*dockertest.Resource
	tsRelayerResource *dockertest.Resource
}

// TearDownSuite cleans up all Docker resources and temp directories.
func (s *baseTestSuite) TearDownSuite() {
	s.T().Log("tearing down e2e suite...")

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
func (s *baseTestSuite) cleanupStaleContainers() {
	staleNames := []string{
		s.cfg.providerInitName,
		s.cfg.consumerInitName,
		fmt.Sprintf("%s-val0", s.cfg.providerChainID),
		fmt.Sprintf("%s-val0", s.cfg.consumerChainID),
		fmt.Sprintf("%s-%s-ts-relayer", s.cfg.providerChainID, s.cfg.consumerChainID),
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
	_ = s.dkrPool.Client.RemoveNetwork(s.cfg.dockerNetwork)
}

// runInitContainer starts an init container with the given script mounted,
// waits for it to exit, checks the exit code, and purges it.
func (s *baseTestSuite) runInitContainer(name, scriptPath, containerScriptPath, dataDir, homePath string, env []string) {
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
// container that runs provider-init.sh, then starts the actual chain container.
func (s *baseTestSuite) initAndStartProvider() {
	// Create host directory for provider data
	providerDir, err := os.MkdirTemp("", s.cfg.tmpDirPrefix+"provider-")
	s.Require().NoError(err)
	s.tmpDirs = append(s.tmpDirs, providerDir)
	s.provider.dataDir = providerDir

	// Make writable
	s.Require().NoError(os.Chmod(providerDir, 0o777))

	// Run init script in a temporary container
	scriptPath := filepath.Join(testDir(), "scripts", "provider-init.sh")
	s.runInitContainer(s.cfg.providerInitName, scriptPath, "/scripts/provider-init.sh", providerDir, providerHomePath, []string{
		"BINARY=" + providerBinary,
		"HOME_DIR=" + providerHomePath,
		"CHAIN_ID=" + s.cfg.providerChainID,
		"DENOM=" + bondDenom,
		"MNEMONIC=" + relayerMnemonic,
	})

	// Apply the suite-specific genesis mutations.
	genesisFile := filepath.Join(providerDir, "config", "genesis.json")
	s.patchGenesisJSON(genesisFile, func(genesis map[string]any) {
		appState := genesis["app_state"].(map[string]any)
		s.cfg.patchProviderGenesis(appState)
	})

	// Apply the suite-specific config.toml mutations (e.g. fast blocks).
	if s.cfg.patchProviderConfigToml != nil {
		s.cfg.patchProviderConfigToml(filepath.Join(providerDir, "config", "config.toml"))
	}

	// Now start the actual provider container
	s.T().Log("starting provider chain container...")

	resource, err := s.dkrPool.RunWithOptions(
		&dockertest.RunOptions{
			Name:       fmt.Sprintf("%s-val0", s.cfg.providerChainID),
			Repository: e2eChainImage,
			NetworkID:  s.dkrNet.Network.ID,
			Mounts: []string{
				fmt.Sprintf("%s:%s", providerDir, providerHomePath),
			},
			PortBindings: map[docker.Port][]docker.PortBinding{
				"26657/tcp": {{HostIP: "", HostPort: s.cfg.providerRPCPort}},
				"9090/tcp":  {{HostIP: "", HostPort: s.cfg.providerGRPCPort}},
				"1317/tcp":  {{HostIP: "", HostPort: s.cfg.providerRESTPort}},
				"26656/tcp": {{HostIP: "", HostPort: s.cfg.providerP2PPort}},
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
	err = s.waitForChainHeight(waitCtx, "http://localhost:"+s.cfg.providerRPCPort, 3)
	s.Require().NoError(err, "provider failed to produce blocks")
	s.T().Log("provider chain is producing blocks")
}

// registerConsumerOnProvider creates a consumer chain registration on the provider.
func (s *baseTestSuite) registerConsumerOnProvider() {
	// Read consumer JSON template and substitute chain ID
	templatePath := filepath.Join(testDir(), "testdata", s.cfg.consumerTemplateFile)
	templateBytes, err := os.ReadFile(templatePath)
	s.Require().NoError(err, "failed to read %s template", s.cfg.consumerTemplateFile)

	createConsumerJSON := strings.ReplaceAll(string(templateBytes), s.cfg.consumerTemplatePlaceholder, s.cfg.consumerChainID)

	// Write JSON to container and execute tx
	s.dockerExecMust(s.providerValRes[0].Container.ID, []string{
		"sh", "-c", fmt.Sprintf("echo '%s' > /tmp/create_consumer.json", createConsumerJSON),
	})

	stdout, stderr, err := s.dockerExec(s.providerValRes[0].Container.ID, []string{
		providerBinary, "tx", "provider", "create-consumer", "/tmp/create_consumer.json",
		"--from", "val",
		"--home", providerHomePath,
		"--keyring-backend", "test",
		"--chain-id", s.cfg.providerChainID,
		"--gas", "auto",
		"--gas-adjustment", "1.5",
		"--fees", "10000" + bondDenom,
		"--broadcast-mode", "sync",
		"-y",
		"-o", "json",
	})
	s.Require().NoErrorf(err, "failed to run create-consumer: stderr=%s", stderr.String())

	// Assert the tx was accepted. Without this, a tx rejected at CheckTx (e.g.
	// an init-parameter validation failure) passes silently here and only
	// surfaces later as an empty consumer genesis.
	var res struct {
		Code   int    `json:"code"`
		RawLog string `json:"raw_log"`
	}
	s.Require().NoErrorf(json.Unmarshal(stdout.Bytes(), &res),
		"failed to decode create-consumer response:\nstdout=%q\nstderr=%q", stdout.String(), stderr.String())
	s.Require().Equalf(0, res.Code, "create-consumer tx failed: %s", res.RawLog)

	// Wait for the tx to be included
	time.Sleep(10 * time.Second)
}

// fetchConsumerGenesis queries the provider for the consumer genesis data.
func (s *baseTestSuite) fetchConsumerGenesis() []byte {
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
func (s *baseTestSuite) initAndStartConsumer(consumerGenesisJSON []byte) {
	// Create host directory for consumer data
	consumerDir, err := os.MkdirTemp("", s.cfg.tmpDirPrefix+"consumer-")
	s.Require().NoError(err)
	s.tmpDirs = append(s.tmpDirs, consumerDir)
	s.consumer.dataDir = consumerDir

	s.Require().NoError(os.Chmod(consumerDir, 0o777))

	// Run init script in a temporary container
	scriptPath := filepath.Join(testDir(), "scripts", "consumer-init.sh")
	s.runInitContainer(s.cfg.consumerInitName, scriptPath, "/scripts/consumer-init.sh", consumerDir, consumerHomePath, []string{
		"BINARY=" + consumerBinary,
		"HOME_DIR=" + consumerHomePath,
		"CHAIN_ID=" + s.cfg.consumerChainID,
		"DENOM=" + bondDenom,
		"MNEMONIC=" + relayerMnemonic,
	})

	// Patch consumer genesis with provider's consumer-genesis data on the host
	genesisFile := filepath.Join(consumerDir, "config", "genesis.json")
	err = patchConsumerGenesisWithProviderData(genesisFile, consumerGenesisJSON)
	s.Require().NoError(err, "failed to patch consumer genesis")

	// Patch consumer slashing params for aggressive downtime detection
	s.patchConsumerSlashingParams()

	// Apply the suite-specific config.toml mutations (e.g. fast blocks).
	if s.cfg.patchConsumerConfigToml != nil {
		s.cfg.patchConsumerConfigToml(filepath.Join(consumerDir, "config", "config.toml"))
	}

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
			Name:       fmt.Sprintf("%s-val0", s.cfg.consumerChainID),
			Repository: e2eChainImage,
			NetworkID:  s.dkrNet.Network.ID,
			Mounts: []string{
				fmt.Sprintf("%s:%s", consumerDir, consumerHomePath),
			},
			PortBindings: map[docker.Port][]docker.PortBinding{
				"26657/tcp": {{HostIP: "", HostPort: s.cfg.consumerRPCPort}},
				"9090/tcp":  {{HostIP: "", HostPort: s.cfg.consumerGRPCPort}},
				"1317/tcp":  {{HostIP: "", HostPort: s.cfg.consumerRESTPort}},
				"26656/tcp": {{HostIP: "", HostPort: s.cfg.consumerP2PPort}},
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
	err = s.waitForChainHeight(waitCtx, "http://localhost:"+s.cfg.consumerRPCPort, 3)
	s.Require().NoError(err, "consumer failed to produce blocks")
	s.T().Log("consumer chain is producing blocks")
}

// setupTSRelayer starts the ts-relayer container, configures it with
// mnemonics and gas prices for both chains, and creates an IBC v2 path.
// The ts-relayer handles counterparty registration and packet relaying.
func (s *baseTestSuite) setupTSRelayer() {
	s.startTSRelayer()

	s.tsRelayerAddMnemonic(s.cfg.providerChainID, relayerMnemonic)
	s.tsRelayerAddMnemonic(s.cfg.consumerChainID, relayerMnemonic)
	s.tsRelayerAddGasPrice(s.cfg.providerChainID, "0.025"+bondDenom)
	s.tsRelayerAddGasPrice(s.cfg.consumerChainID, "0.025"+bondDenom)

	s.tsRelayerAddPath(IBCv2)

	s.tsRelayerDumpPaths()
	s.startTSRelayerRelay()
	s.T().Log("ts-relayer IBC v2 path configured")
}

// waitForChainHeight polls a CometBFT RPC endpoint until the chain reaches
// the given block height.
func (s *baseTestSuite) waitForChainHeight(ctx context.Context, rpcEndpoint string, minHeight int64) error {
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

// waitForConsumerSync blocks until the provider has recorded a VSC ack from the
// consumer that is strictly newer than the launch seed, i.e. the relayer is
// actually delivering VSC packets and the consumer is acking them.
//
// This gate is what makes the timing-sensitive liveness tests deterministic:
// the provider seeds lastAck at launch and starts the grace clock then, but the
// relayer needs time (add-path, then the first relay cycle) before any VSC is
// delivered. Without this gate a test could assert against a consumer that has
// launched but never synced. The grace (~150s) is sized to exceed this
// first-sync latency, so the consumer is not swept while we wait here.
func (s *baseTestSuite) waitForConsumerSync(consumerID string) {
	livenessURL := fmt.Sprintf("http://localhost:%s/vaas/provider/consumer_liveness/%s",
		s.cfg.providerRESTPort, consumerID)

	var first string
	s.Require().Eventuallyf(func() bool {
		body, err := httpGet(livenessURL)
		if err != nil {
			return false
		}
		var res struct {
			LastAckTime string `json:"last_ack_time"`
		}
		if json.Unmarshal(body, &res) != nil || res.LastAckTime == "" {
			return false
		}
		// Require two distinct lastAck readings: the first records a baseline,
		// the second proves lastAck is advancing (acks are flowing), not merely
		// sitting at the launch seed.
		if first == "" {
			first = res.LastAckTime
			s.T().Logf("consumer sync: baseline last_ack=%s", first)
			return false
		}
		if res.LastAckTime != first {
			s.T().Logf("consumer sync confirmed: last_ack advanced %s -> %s", first, res.LastAckTime)
			return true
		}
		return false
	}, 150*time.Second, 3*time.Second,
		"consumer %s never synced a VSC ack (lastAck did not advance past the launch seed)", consumerID)
}

// patchConfigToml lowers the CometBFT consensus timeouts so the chain produces
// blocks roughly every second instead of the ~5s default. Fast blocks matter
// for the liveness suite because every time-bounded step -- relayer add-path
// (which waits for client header heights), epoch/VSC cadence, and the VSC
// recv/ack round-trip -- is measured against the liveness grace. At the default
// block time these add up to minutes and the grace (which starts ticking at
// consumer launch) can expire before the consumer ever syncs.
func (s *baseTestSuite) patchConfigToml(path string) {
	bz, err := os.ReadFile(path)
	s.Require().NoError(err, "failed to read config.toml")
	out := string(bz)
	for from, to := range map[string]string{
		`timeout_propose = "3s"`:          `timeout_propose = "500ms"`,
		`timeout_commit = "5s"`:           `timeout_commit = "1s"`,
		`timeout_propose_delta = "500ms"`: `timeout_propose_delta = "200ms"`,
	} {
		s.Require().Containsf(out, from, "config.toml missing %q (default may have changed)", from)
		out = strings.ReplaceAll(out, from, to)
	}
	s.Require().NoError(os.WriteFile(path, []byte(out), 0o600), "failed to write config.toml")
}

// providerLogs returns the provider container's full stdout+stderr log.
func (s *baseTestSuite) providerLogs() string {
	var buf bytes.Buffer
	_ = s.dkrPool.Client.Logs(docker.LogsOptions{
		Container:    s.providerValRes[0].Container.ID,
		OutputStream: &buf,
		ErrorStream:  &buf,
		Stdout:       true,
		Stderr:       true,
	})
	return buf.String()
}

// consumerLogs returns the consumer container's full stdout+stderr log.
func (s *baseTestSuite) consumerLogs() string {
	var buf bytes.Buffer
	_ = s.dkrPool.Client.Logs(docker.LogsOptions{
		Container:    s.consumerValRes[0].Container.ID,
		OutputStream: &buf,
		ErrorStream:  &buf,
		Stdout:       true,
		Stderr:       true,
	})
	return buf.String()
}
