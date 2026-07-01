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
//   - Fast blocks (livenessPatchConfigToml lowers the CometBFT timeouts to
//     ~1s blocks). The provider seeds lastAck at consumer launch and starts the
//     grace clock then, so every setup step that precedes the first VSC sync --
//     relayer add-path, the first relay cycle, the recv/ack round-trip -- must
//     finish inside the grace. At the default ~5s block time these add up to
//     minutes and the consumer is swept before it ever syncs.
//   - A first-sync gate (livenessWaitForConsumerSync) that blocks setup until
//     the provider's lastAck has actually advanced past the launch seed, i.e.
//     the relayer is delivering and the consumer is acking. Assertions then run
//     from a known-synced state, independent of relayer startup jitter.
//
// The existing IntegrationTestSuite uses a ~21d provider unbonding, making the
// liveness sweep untestable in real time. This suite launches its own isolated
// set of containers (distinct chain IDs, Docker network, and host ports) and
// registers a single consumer via create_consumer_short_unbonding.json which
// carries a 200s consumer unbonding (200000000000 ns), 10m vaas_timeout
// (600000000000 ns, the MinVAASTimeoutPeriod floor), and 5s safe_mode_threshold
// (5000000000 ns).
//
// Test ordering within TestLivenessVAAS:
//   1. testRecoverBeforeGrace  - brief pause < grace (~150s); consumer stays LAUNCHED.
//   2. testRealSafeMode        - relayer paused > 5s; consumer enters restricted
//                                mode; bank send rejected; relayer unpaused; accepted.
//   3. testLivenessQuery       - QueryConsumerLiveness via CLI; last_ack_time recent,
//                                non-zero grace, removal_eta present.
//   4. testAutoSweepRemoval    - relayer stopped indefinitely; poll until STOPPED.
//
// The STOPPED -> DELETED edge (deletion at stop-time + unbonding) is unit-tested
// (TestSweepRemovesStaleConsumer), not exercised here -- see the note above
// testAutoSweepRemoval's trailing comment.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ory/dockertest/v3"
	"github.com/ory/dockertest/v3/docker"
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
	suite.Suite

	tmpDirs           []string
	provider          *chain
	consumer          *chain
	dkrPool           *dockertest.Pool
	dkrNet            *dockertest.Network
	providerValRes    []*dockertest.Resource
	consumerValRes    []*dockertest.Resource
	tsRelayerResource *dockertest.Resource
}

// TestLivenessIntegrationTestSuite is the entry point for the short-unbonding
// liveness e2e suite.
func TestLivenessIntegrationTestSuite(t *testing.T) {
	suite.Run(t, new(LivenessIntegrationTestSuite))
}

// SetupSuite brings up provider + consumer + ts-relayer with short timers.
func (s *LivenessIntegrationTestSuite) SetupSuite() {
	s.T().Log("setting up liveness e2e suite (short unbonding)...")

	var err error

	s.dkrPool, err = dockertest.NewPool("")
	s.Require().NoError(err, "failed to create docker pool")
	s.dkrPool.MaxWait = 5 * time.Minute

	s.livenessCleanupStaleContainers()

	s.dkrNet, err = s.dkrPool.CreateNetwork(livenessDockerNetwork)
	s.Require().NoError(err, "failed to create liveness docker network")

	s.T().Log("step 1: initializing liveness provider chain...")
	s.provider = &chain{id: livenessProviderChainID}
	s.livenessInitAndStartProvider()

	s.T().Log("step 2: registering consumer on liveness provider...")
	s.livenessRegisterConsumerOnProvider()

	s.T().Log("step 3: fetching consumer genesis from liveness provider...")
	consumerGenesisJSON := s.livenessFetchConsumerGenesis()

	s.T().Log("step 4: initializing liveness consumer chain...")
	s.consumer = &chain{id: livenessConsumerChainID}
	s.livenessInitAndStartConsumer(consumerGenesisJSON)

	s.T().Log("step 5: starting ts-relayer for liveness suite...")
	s.livenessSetupTSRelayer()

	s.T().Log("step 6: waiting for the consumer to sync its first VSC...")
	s.livenessWaitForConsumerSync("0")

	s.T().Log("liveness e2e suite setup complete!")
}

// livenessWaitForConsumerSync blocks until the provider has recorded a VSC ack
// from the consumer that is strictly newer than the launch seed, i.e. the
// relayer is actually delivering VSC packets and the consumer is acking them.
//
// This gate is what makes the timing-sensitive tests deterministic: the
// provider seeds lastAck at launch and starts the grace clock then, but the
// relayer needs time (add-path, then the first relay cycle) before any VSC is
// delivered. Without this gate a test could assert against a consumer that has
// launched but never synced. The grace (~150s) is sized to exceed this
// first-sync latency, so the consumer is not swept while we wait here.
func (s *LivenessIntegrationTestSuite) livenessWaitForConsumerSync(consumerID string) {
	livenessURL := fmt.Sprintf("http://localhost:%s/vaas/provider/consumer_liveness/%s",
		livenessProviderRESTPort, consumerID)

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

// TearDownSuite cleans up all Docker resources.
func (s *LivenessIntegrationTestSuite) TearDownSuite() {
	s.T().Log("tearing down liveness e2e suite...")

	if os.Getenv("VAAS_E2E_SKIP_CLEANUP") == "true" {
		s.T().Log("skipping cleanup (VAAS_E2E_SKIP_CLEANUP=true)")
		return
	}

	s.livenessStopTSRelayer()

	for _, r := range s.consumerValRes {
		if err := s.dkrPool.Purge(r); err != nil {
			s.T().Logf("failed to purge consumer container: %v", err)
		}
	}

	for _, r := range s.providerValRes {
		if err := s.dkrPool.Purge(r); err != nil {
			s.T().Logf("failed to purge provider container: %v", err)
		}
	}

	if s.dkrNet != nil {
		if err := s.dkrPool.RemoveNetwork(s.dkrNet); err != nil {
			s.T().Logf("failed to remove liveness network: %v", err)
		}
	}

	for _, dir := range s.tmpDirs {
		_ = os.RemoveAll(dir)
	}
}

// TestLivenessVAAS sequences the liveness tests in a safe order: non-destructive
// tests (recover, safe-mode, query) before the destructive sweep/delete tests.
func (s *LivenessIntegrationTestSuite) TestLivenessVAAS() {
	s.testRecoverBeforeGrace()
	s.testRealSafeMode()
	s.testLivenessQuery()
	s.testAutoSweepRemoval()
}

// ---- infrastructure helpers ------------------------------------------------

func (s *LivenessIntegrationTestSuite) livenessCleanupStaleContainers() {
	staleNames := []string{
		"liveness-provider-init",
		"liveness-consumer-init",
		fmt.Sprintf("%s-val0", livenessProviderChainID),
		fmt.Sprintf("%s-val0", livenessConsumerChainID),
		fmt.Sprintf("%s-%s-ts-relayer", livenessProviderChainID, livenessConsumerChainID),
	}
	for _, name := range staleNames {
		c, err := s.dkrPool.Client.InspectContainer(name)
		if err != nil {
			continue
		}
		s.T().Logf("removing stale liveness container: %s", name)
		_ = s.dkrPool.Client.RemoveContainer(docker.RemoveContainerOptions{
			ID:            c.ID,
			Force:         true,
			RemoveVolumes: true,
		})
	}
	_ = s.dkrPool.Client.RemoveNetwork(livenessDockerNetwork)
}

func (s *LivenessIntegrationTestSuite) livenessRunInitContainer(name, scriptPath, containerScriptPath, dataDir, homePath string, env []string) {
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

func (s *LivenessIntegrationTestSuite) livenessInitAndStartProvider() {
	providerDir, err := os.MkdirTemp("", "vaas-liveness-provider-")
	s.Require().NoError(err)
	s.tmpDirs = append(s.tmpDirs, providerDir)
	s.provider.dataDir = providerDir
	s.Require().NoError(os.Chmod(providerDir, 0o777))

	scriptPath := filepath.Join(testDir(), "scripts", "provider-init.sh")
	s.livenessRunInitContainer(
		"liveness-provider-init", scriptPath, "/scripts/provider-init.sh",
		providerDir, providerHomePath,
		[]string{
			"BINARY=" + providerBinary,
			"HOME_DIR=" + providerHomePath,
			"CHAIN_ID=" + livenessProviderChainID,
			"DENOM=" + bondDenom,
			"MNEMONIC=" + relayerMnemonic,
		},
	)

	genesisFile := filepath.Join(providerDir, "config", "genesis.json")
	s.livenessPatchGenesisJSON(genesisFile, func(genesis map[string]any) {
		appState := genesis["app_state"].(map[string]any)

		if gov, ok := appState["gov"].(map[string]any); ok {
			if params, ok := gov["params"].(map[string]any); ok {
				params["voting_period"] = "15s"
			}
		}

		if provider, ok := appState["provider"].(map[string]any); ok {
			if params, ok := provider["params"].(map[string]any); ok {
				// One block per epoch so a VSC packet (and its ack) flows every
				// block. Combined with the fast block time (see
				// livenessPatchConfigToml) this refreshes the provider's lastAck
				// every few seconds, well inside the grace, so a healthy
				// consumer is never swept between acks.
				params["blocks_per_epoch"] = "1"
				params["fees_per_block_amount"] = "1000"
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
				// asserting (see livenessWaitForConsumerSync).
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
	})

	// Fast blocks so add-path, epochs, and VSC delivery all complete well
	// inside the grace (see livenessPatchConfigToml).
	s.livenessPatchConfigToml(filepath.Join(providerDir, "config", "config.toml"))

	s.T().Log("starting liveness provider chain container...")
	resource, err := s.dkrPool.RunWithOptions(
		&dockertest.RunOptions{
			Name:       fmt.Sprintf("%s-val0", livenessProviderChainID),
			Repository: e2eChainImage,
			NetworkID:  s.dkrNet.Network.ID,
			Mounts: []string{
				fmt.Sprintf("%s:%s", providerDir, providerHomePath),
			},
			PortBindings: map[docker.Port][]docker.PortBinding{
				"26657/tcp": {{HostIP: "", HostPort: livenessProviderRPCPort}},
				"9090/tcp":  {{HostIP: "", HostPort: livenessProviderGRPCPort}},
				"1317/tcp":  {{HostIP: "", HostPort: livenessProviderRESTPort}},
				"26656/tcp": {{HostIP: "", HostPort: livenessProviderP2PPort}},
			},
			Cmd: []string{providerBinary, "start", "--home", providerHomePath},
		},
		func(config *docker.HostConfig) {
			config.RestartPolicy = docker.RestartPolicy{Name: "no"}
		},
	)
	s.Require().NoError(err, "failed to start liveness provider container")
	s.providerValRes = append(s.providerValRes, resource)
	s.T().Logf("liveness provider container started: %s", resource.Container.ID[:12])

	waitCtx, waitCancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer waitCancel()
	err = s.livenessWaitForChainHeight(waitCtx, "http://localhost:"+livenessProviderRPCPort, 3)
	s.Require().NoError(err, "liveness provider failed to produce blocks")
	s.T().Log("liveness provider chain is producing blocks")
}

func (s *LivenessIntegrationTestSuite) livenessRegisterConsumerOnProvider() {
	templatePath := filepath.Join(testDir(), "testdata", "create_consumer_short_unbonding.json")
	templateBytes, err := os.ReadFile(templatePath)
	s.Require().NoError(err, "failed to read create_consumer_short_unbonding.json template")

	createConsumerJSON := strings.ReplaceAll(string(templateBytes), "CONSUMER_SHORT_CHAIN_ID", livenessConsumerChainID)

	s.livenessdockerExecMust(s.providerValRes[0].Container.ID, []string{
		"sh", "-c", fmt.Sprintf("echo '%s' > /tmp/create_consumer.json", createConsumerJSON),
	})

	_, _, err = s.livenessdockerExec(s.providerValRes[0].Container.ID, []string{
		providerBinary, "tx", "provider", "create-consumer", "/tmp/create_consumer.json",
		"--from", "val",
		"--home", providerHomePath,
		"--keyring-backend", "test",
		"--chain-id", livenessProviderChainID,
		"--gas", "auto",
		"--gas-adjustment", "1.5",
		"--fees", "10000" + bondDenom,
		"-y",
	})
	s.Require().NoError(err, "failed to create consumer on liveness provider")
	time.Sleep(10 * time.Second)
}

func (s *LivenessIntegrationTestSuite) livenessFetchConsumerGenesis() []byte {
	var output string
	var lastErr error

	for range 30 {
		stdout, _, err := s.livenessdockerExec(s.providerValRes[0].Container.ID, []string{
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
	s.T().Logf("fetched liveness consumer genesis (%d bytes)", len(output))
	return []byte(output)
}

func (s *LivenessIntegrationTestSuite) livenessInitAndStartConsumer(consumerGenesisJSON []byte) {
	consumerDir, err := os.MkdirTemp("", "vaas-liveness-consumer-")
	s.Require().NoError(err)
	s.tmpDirs = append(s.tmpDirs, consumerDir)
	s.consumer.dataDir = consumerDir
	s.Require().NoError(os.Chmod(consumerDir, 0o777))

	scriptPath := filepath.Join(testDir(), "scripts", "consumer-init.sh")
	s.livenessRunInitContainer(
		"liveness-consumer-init", scriptPath, "/scripts/consumer-init.sh",
		consumerDir, consumerHomePath,
		[]string{
			"BINARY=" + consumerBinary,
			"HOME_DIR=" + consumerHomePath,
			"CHAIN_ID=" + livenessConsumerChainID,
			"DENOM=" + bondDenom,
			"MNEMONIC=" + relayerMnemonic,
		},
	)

	genesisFile := filepath.Join(consumerDir, "config", "genesis.json")
	err = patchConsumerGenesisWithProviderData(genesisFile, consumerGenesisJSON)
	s.Require().NoError(err, "failed to patch liveness consumer genesis")

	s.livenessPatchConsumerSlashingParams()

	// Fast blocks on the consumer too, so its side of add-path and the VSC
	// recv/ack round-trip keep pace with the provider (see livenessPatchConfigToml).
	s.livenessPatchConfigToml(filepath.Join(consumerDir, "config", "config.toml"))

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

	s.T().Log("starting liveness consumer chain container...")
	resource, err := s.dkrPool.RunWithOptions(
		&dockertest.RunOptions{
			Name:       fmt.Sprintf("%s-val0", livenessConsumerChainID),
			Repository: e2eChainImage,
			NetworkID:  s.dkrNet.Network.ID,
			Mounts: []string{
				fmt.Sprintf("%s:%s", consumerDir, consumerHomePath),
			},
			PortBindings: map[docker.Port][]docker.PortBinding{
				"26657/tcp": {{HostIP: "", HostPort: livenessConsumerRPCPort}},
				"9090/tcp":  {{HostIP: "", HostPort: livenessConsumerGRPCPort}},
				"1317/tcp":  {{HostIP: "", HostPort: livenessConsumerRESTPort}},
				"26656/tcp": {{HostIP: "", HostPort: livenessConsumerP2PPort}},
			},
			Cmd: []string{consumerBinary, "start", "--home", consumerHomePath},
		},
		func(config *docker.HostConfig) {
			config.RestartPolicy = docker.RestartPolicy{Name: "no"}
		},
	)
	s.Require().NoError(err, "failed to start liveness consumer container")
	s.consumerValRes = append(s.consumerValRes, resource)
	s.T().Logf("liveness consumer container started: %s", resource.Container.ID[:12])

	waitCtx, waitCancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer waitCancel()
	err = s.livenessWaitForChainHeight(waitCtx, "http://localhost:"+livenessConsumerRPCPort, 3)
	s.Require().NoError(err, "liveness consumer failed to produce blocks")
	s.T().Log("liveness consumer chain is producing blocks")
}

func (s *LivenessIntegrationTestSuite) livenessSetupTSRelayer() {
	s.livenessStartTSRelayer()
	s.livenesstsRelayerAddMnemonic(livenessProviderChainID, relayerMnemonic)
	s.livenesstsRelayerAddMnemonic(livenessConsumerChainID, relayerMnemonic)
	s.livenesstsRelayerAddGasPrice(livenessProviderChainID, "0.025"+bondDenom)
	s.livenesstsRelayerAddGasPrice(livenessConsumerChainID, "0.025"+bondDenom)
	s.livenesstsRelayerAddPath(IBCv2)
	s.livenesstsRelayerDumpPaths()
	s.livenessStartTSRelayerRelay()
	s.T().Log("liveness ts-relayer IBC v2 path configured")
}

func (s *LivenessIntegrationTestSuite) livenessStartTSRelayer() {
	s.T().Log("starting liveness ts-relayer container")
	resource, err := s.dkrPool.RunWithOptions(
		&dockertest.RunOptions{
			Name:       fmt.Sprintf("%s-%s-ts-relayer", livenessProviderChainID, livenessConsumerChainID),
			Repository: tsRelayerImage,
			Tag:        tsRelayerImageTag,
			NetworkID:  s.dkrNet.Network.ID,
			User:       "root",
			CapAdd:     []string{"IPC_LOCK"},
		},
		noRestart,
	)
	s.Require().NoError(err, "failed to start liveness ts-relayer container")
	s.tsRelayerResource = resource
	s.T().Logf("liveness ts-relayer started: %s", resource.Container.ID[:12])

	time.Sleep(3 * time.Second)

	providerURL := "http://" + s.providerValRes[0].Container.Name[1:] + ":26657"
	consumerURL := "http://" + s.consumerValRes[0].Container.Name[1:] + ":26657"

	s.livenessVerifyTSRelayerConnectivity("liveness-provider", providerURL)
	s.livenessVerifyTSRelayerConnectivity("liveness-consumer", consumerURL)
}

func (s *LivenessIntegrationTestSuite) livenessVerifyTSRelayerConnectivity(chainName, rpcURL string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	for attempt := range 10 {
		exec, err := s.dkrPool.Client.CreateExec(docker.CreateExecOptions{
			Context:      ctx,
			AttachStdout: true,
			AttachStderr: true,
			Container:    s.tsRelayerResource.Container.ID,
			User:         "root",
			Cmd:          []string{"wget", "-qO-", fmt.Sprintf("%s/status", rpcURL)},
		})
		s.Require().NoError(err)

		var out bytes.Buffer
		err = s.dkrPool.Client.StartExec(exec.ID, docker.StartExecOptions{
			Context:      ctx,
			Detach:       false,
			OutputStream: &out,
			ErrorStream:  &out,
		})
		_ = err

		for {
			inspectExec, err := s.dkrPool.Client.InspectExec(exec.ID)
			s.Require().NoError(err)
			if !inspectExec.Running {
				if inspectExec.ExitCode == 0 && strings.Contains(out.String(), "latest_block_height") {
					s.T().Logf("liveness ts-relayer can reach %s at %s", chainName, rpcURL)
					return
				}
				break
			}
		}
		s.T().Logf("liveness ts-relayer connectivity to %s attempt %d failed", chainName, attempt+1)
		time.Sleep(2 * time.Second)
	}
	s.Require().Fail("liveness ts-relayer cannot reach %s at %s", chainName, rpcURL)
}

func (s *LivenessIntegrationTestSuite) livenessExecuteTSRelayerCommand(ctx context.Context, args []string) []byte {
	tsRelayerBinary := []string{"/bin/with_keyring", "ibc-v2-ts-relayer"}
	cmd := append(tsRelayerBinary, args...)
	exec, err := s.dkrPool.Client.CreateExec(docker.CreateExecOptions{
		Context:      ctx,
		AttachStdout: true,
		AttachStderr: true,
		Container:    s.tsRelayerResource.Container.ID,
		User:         "root",
		Cmd:          cmd,
	})
	s.Require().NoError(err)

	var out bytes.Buffer
	err = s.dkrPool.Client.StartExec(exec.ID, docker.StartExecOptions{
		Context:      ctx,
		Detach:       false,
		OutputStream: &out,
		ErrorStream:  &out,
	})
	s.Require().NoError(err, "liveness ts-relayer startExec error: %s", out.String())

	for {
		inspectExec, err := s.dkrPool.Client.InspectExec(exec.ID)
		s.Require().NoError(err)
		if !inspectExec.Running {
			s.Require().Equal(0, inspectExec.ExitCode,
				"liveness ts-relayer cmd '%s' failed (exit=%d): %s",
				strings.Join(cmd, " "), inspectExec.ExitCode, out.String())
			break
		}
	}
	return out.Bytes()
}

func (s *LivenessIntegrationTestSuite) livenesstsRelayerAddMnemonic(chainID, mnemonic string) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	s.livenessExecuteTSRelayerCommand(ctx, []string{"add-mnemonic", "-c", chainID, "--mnemonic", mnemonic})
}

func (s *LivenessIntegrationTestSuite) livenesstsRelayerAddGasPrice(chainID, gasPrice string) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	s.livenessExecuteTSRelayerCommand(ctx, []string{"add-gas-price", "-c", chainID, "--gas-adjustment", "2.0", gasPrice})
}

func (s *LivenessIntegrationTestSuite) livenesstsRelayerAddPath(ibcVersion string) {
	s.T().Logf("liveness ts-relayer: adding IBCv%s path", ibcVersion)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	s.livenessExecuteTSRelayerCommand(ctx, []string{
		"add-path",
		"-s", livenessProviderChainID,
		"-d", livenessConsumerChainID,
		"--surl", "http://" + s.providerValRes[0].Container.Name[1:] + ":26657",
		"--durl", "http://" + s.consumerValRes[0].Container.Name[1:] + ":26657",
		"--st", "cosmos",
		"--dt", "cosmos",
		"--ibc-version", ibcVersion,
	})
}

func (s *LivenessIntegrationTestSuite) livenesstsRelayerDumpPaths() {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	s.livenessExecuteTSRelayerCommand(ctx, []string{"dump-paths"})
}

func (s *LivenessIntegrationTestSuite) livenessStartTSRelayerRelay() {
	s.T().Log("liveness ts-relayer: starting relay process")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	cmd := []string{"/bin/with_keyring", "ibc-v2-ts-relayer", "relay"}
	exec, err := s.dkrPool.Client.CreateExec(docker.CreateExecOptions{
		Context:      ctx,
		AttachStdout: true,
		AttachStderr: true,
		Container:    s.tsRelayerResource.Container.ID,
		User:         "root",
		Cmd:          cmd,
	})
	s.Require().NoError(err, "failed to create liveness relay exec")
	err = s.dkrPool.Client.StartExec(exec.ID, docker.StartExecOptions{Context: ctx, Detach: true})
	s.Require().NoError(err, "failed to start liveness relay process")
	time.Sleep(3 * time.Second)
	inspectExec, err := s.dkrPool.Client.InspectExec(exec.ID)
	s.Require().NoError(err)
	s.Require().True(inspectExec.Running, "liveness relay process is not running")
}

func (s *LivenessIntegrationTestSuite) livenessStopTSRelayer() {
	if s.tsRelayerResource != nil {
		s.T().Log("tearing down liveness ts-relayer...")
		s.Require().NoError(s.dkrPool.Purge(s.tsRelayerResource))
		s.tsRelayerResource = nil
	}
}

func (s *LivenessIntegrationTestSuite) livenessWaitForChainHeight(ctx context.Context, rpcEndpoint string, minHeight int64) error {
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

// ---- genesis helpers -------------------------------------------------------

func (s *LivenessIntegrationTestSuite) livenessPatchGenesisJSON(path string, mutate func(map[string]any)) {
	bz, err := os.ReadFile(path)
	s.Require().NoError(err, "failed to read genesis file")
	var genesis map[string]any
	s.Require().NoError(json.Unmarshal(bz, &genesis), "failed to unmarshal genesis")
	mutate(genesis)
	out, err := json.MarshalIndent(genesis, "", "  ")
	s.Require().NoError(err, "failed to marshal genesis")
	s.Require().NoError(os.WriteFile(path, out, 0o600), "failed to write genesis")
}

// livenessPatchConfigToml lowers the CometBFT consensus timeouts so the chain
// produces blocks roughly every second instead of the ~5s default. Fast blocks
// matter here because every time-bounded step in the suite -- relayer add-path
// (which waits for client header heights), epoch/VSC cadence, and the VSC
// recv/ack round-trip -- is measured against the liveness grace. At the default
// block time these add up to minutes and the grace (which starts ticking at
// consumer launch) can expire before the consumer ever syncs.
func (s *LivenessIntegrationTestSuite) livenessPatchConfigToml(path string) {
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

func (s *LivenessIntegrationTestSuite) livenessPatchConsumerSlashingParams() {
	s.livenessPatchGenesisJSON(s.consumer.dataDir+"/config/genesis.json", func(genesis map[string]any) {
		appState, ok := genesis["app_state"].(map[string]any)
		if !ok {
			return
		}
		slashing, ok := appState["slashing"].(map[string]any)
		if !ok {
			slashing = make(map[string]any)
		}
		params, ok := slashing["params"].(map[string]any)
		if !ok {
			params = make(map[string]any)
		}
		params["signed_blocks_window"] = "5"
		params["min_signed_per_window"] = "0.050000000000000000"
		params["slash_fraction_downtime"] = "0.000000000000000000"
		params["downtime_jail_duration"] = "60s"
		slashing["params"] = params
		appState["slashing"] = slashing
	})
}

// ---- exec helpers ----------------------------------------------------------

func (s *LivenessIntegrationTestSuite) livenessdockerExec(containerID string, cmd []string) (bytes.Buffer, bytes.Buffer, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var stdout, stderr bytes.Buffer
	exec, err := s.dkrPool.Client.CreateExec(docker.CreateExecOptions{
		Context:      ctx,
		AttachStdout: true,
		AttachStderr: true,
		Container:    containerID,
		User:         "nonroot",
		Cmd:          cmd,
	})
	if err != nil {
		return stdout, stderr, fmt.Errorf("failed to create exec: %w", err)
	}
	err = s.dkrPool.Client.StartExec(exec.ID, docker.StartExecOptions{
		Context:      ctx,
		Detach:       false,
		OutputStream: &stdout,
		ErrorStream:  &stderr,
	})
	if err != nil {
		return stdout, stderr, fmt.Errorf("failed to start exec: %w", err)
	}
	return stdout, stderr, nil
}

func (s *LivenessIntegrationTestSuite) livenessdockerExecMust(containerID string, cmd []string) {
	stdout, stderr, err := s.livenessdockerExec(containerID, cmd)
	if err != nil {
		s.T().Logf("cmd: %v", cmd)
		s.T().Logf("stdout: %s", stdout.String())
		s.T().Logf("stderr: %s", stderr.String())
	}
	s.Require().NoError(err, "docker exec failed for cmd: %v", cmd)
}

// ---- query helpers ---------------------------------------------------------

func (s *LivenessIntegrationTestSuite) livenessQueryConsumerPhase(consumerID string) string {
	stdout, _, err := s.livenessdockerExec(s.providerValRes[0].Container.ID, []string{
		providerBinary, "query", "provider", "consumer-chain", consumerID,
		"--home", providerHomePath,
		"--output", "json",
	})
	if err != nil {
		s.T().Logf("livenessQueryConsumerPhase(%s): exec error: %v", consumerID, err)
		return ""
	}
	var res struct {
		Phase string `json:"phase"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &res); err != nil {
		s.T().Logf("livenessQueryConsumerPhase(%s): decode error: %v (raw: %s)", consumerID, err, stdout.String())
		return ""
	}
	return res.Phase
}

func (s *LivenessIntegrationTestSuite) livenessConsumerUserBech32() string {
	stdout, _, err := s.livenessdockerExec(s.consumerValRes[0].Container.ID, []string{
		consumerBinary, "keys", "show", "user", "-a",
		"--home", consumerHomePath,
		"--keyring-backend", "test",
	})
	s.Require().NoError(err, "failed to get liveness consumer user address")
	return strings.TrimSpace(stdout.String())
}

func (s *LivenessIntegrationTestSuite) livenessConsumerBankSendDryRun() (string, error) {
	user := s.livenessConsumerUserBech32()
	_, stderr, err := s.livenessdockerExec(s.consumerValRes[0].Container.ID, []string{
		consumerBinary, "tx", "bank", "send", user, user, "1" + bondDenom,
		"--home", consumerHomePath,
		"--keyring-backend", "test",
		"--chain-id", livenessConsumerChainID,
		"--fees", "1000" + bondDenom,
		"--dry-run",
		"-y",
	})
	return stderr.String(), err
}

func (s *LivenessIntegrationTestSuite) livenessQueryGovAuthority() string {
	stdout, _, err := s.livenessdockerExec(s.providerValRes[0].Container.ID, []string{
		providerBinary, "query", "auth", "module-account", "gov",
		"--home", providerHomePath,
		"--output", "json",
	})
	s.Require().NoError(err, "failed to query gov module account")

	var res struct {
		Account struct {
			Address     string `json:"address"`
			BaseAccount struct {
				Address string `json:"address"`
			} `json:"base_account"`
			Value struct {
				Address     string `json:"address"`
				BaseAccount struct {
					Address string `json:"address"`
				} `json:"base_account"`
			} `json:"value"`
		} `json:"account"`
	}
	s.Require().NoError(json.Unmarshal(stdout.Bytes(), &res),
		"failed to decode module-account response: %s", stdout.String())

	candidates := []string{
		res.Account.BaseAccount.Address,
		res.Account.Value.Address,
		res.Account.Value.BaseAccount.Address,
		res.Account.Address,
	}
	for _, addr := range candidates {
		if addr != "" {
			return addr
		}
	}
	s.Require().Fail("gov authority address not found in module-account response")
	return ""
}

func (s *LivenessIntegrationTestSuite) livenessSubmitAndPassProposal(proposalJSON string) {
	containerID := s.providerValRes[0].Container.ID

	payload := base64.StdEncoding.EncodeToString([]byte(proposalJSON))
	_, _, err := s.livenessdockerExec(containerID, []string{
		"sh", "-c",
		fmt.Sprintf("echo %s | base64 -d > /tmp/proposal.json", payload),
	})
	s.Require().NoError(err, "failed to write proposal.json")

	stdout, stderr, err := s.livenessdockerExec(containerID, []string{
		providerBinary, "tx", "gov", "submit-proposal", "/tmp/proposal.json",
		"--from", "val",
		"--home", providerHomePath,
		"--keyring-backend", "test",
		"--chain-id", livenessProviderChainID,
		"--fees", "10000" + bondDenom,
		"--yes",
		"-o", "json",
	})
	s.Require().NoErrorf(err, "failed to submit proposal: stdout=%s stderr=%s",
		stdout.String(), stderr.String())

	var submitRes struct {
		TxHash string `json:"txhash"`
		Code   int    `json:"code"`
		RawLog string `json:"raw_log"`
	}
	s.Require().NoError(json.Unmarshal(stdout.Bytes(), &submitRes),
		"failed to decode submit-proposal response: %s", stdout.String())
	s.Require().Equalf(0, submitRes.Code, "submit-proposal failed: %s", submitRes.RawLog)
	s.Require().NotEmpty(submitRes.TxHash, "submit-proposal returned empty txhash")

	var proposalID uint64
	var lastTxOut string
	s.Require().Eventuallyf(func() bool {
		txOut, _, qerr := s.livenessdockerExec(containerID, []string{
			providerBinary, "query", "tx", submitRes.TxHash,
			"--home", providerHomePath,
			"--output", "json",
		})
		if qerr != nil || txOut.Len() == 0 {
			return false
		}
		lastTxOut = txOut.String()
		proposalID = extractProposalID(txOut.Bytes())
		return proposalID != 0
	}, 30*time.Second, 2*time.Second,
		"could not parse proposal_id from tx %s: last stdout=%s",
		submitRes.TxHash, lastTxOut)

	voteStdout, voteStderr, err := s.livenessdockerExec(containerID, []string{
		providerBinary, "tx", "gov", "vote", fmt.Sprintf("%d", proposalID), "yes",
		"--from", "val",
		"--home", providerHomePath,
		"--keyring-backend", "test",
		"--chain-id", livenessProviderChainID,
		"--fees", "10000" + bondDenom,
		"--yes",
	})
	s.Require().NoErrorf(err, "failed to vote on proposal %d: stdout=%s stderr=%s",
		proposalID, voteStdout.String(), voteStderr.String())

	s.Require().Eventuallyf(func() bool {
		txOut, _, qerr := s.livenessdockerExec(containerID, []string{
			providerBinary, "query", "gov", "proposal", strconv.FormatUint(proposalID, 10),
			"--home", providerHomePath,
			"--output", "json",
		})
		if qerr != nil || txOut.Len() == 0 {
			return false
		}
		var res struct {
			Status   string `json:"status"`
			Proposal struct {
				Status string `json:"status"`
			} `json:"proposal"`
		}
		if json.Unmarshal(txOut.Bytes(), &res) != nil {
			return false
		}
		status := res.Status
		if status == "" {
			status = res.Proposal.Status
		}
		switch status {
		case "PROPOSAL_STATUS_PASSED":
			return true
		case "PROPOSAL_STATUS_REJECTED", "PROPOSAL_STATUS_FAILED":
			s.Require().Failf("proposal terminated unsuccessfully",
				"proposal %d ended with status %s", proposalID, status)
			return true
		}
		return false
	}, 30*time.Second, 2*time.Second, "proposal %d did not pass within timeout", proposalID)
}

func (s *LivenessIntegrationTestSuite) livenessFundConsumerFeePool(consumerID, amount string) {
	stdout, stderr, err := s.livenessdockerExec(s.providerValRes[0].Container.ID, []string{
		providerBinary, "tx", "provider", "fund-consumer-fee-pool",
		consumerID, amount,
		"--from", "val",
		"--home", providerHomePath,
		"--keyring-backend", "test",
		"--chain-id", livenessProviderChainID,
		"--fees", "10000" + bondDenom,
		"-y",
		"-o", "json",
	})
	s.Require().NoErrorf(err, "failed to fund consumer %s fee pool: stdout=%s stderr=%s",
		consumerID, stdout.String(), stderr.String())

	var bres struct {
		TxHash string `json:"txhash"`
		Code   int    `json:"code"`
		RawLog string `json:"raw_log"`
	}
	s.Require().NoError(json.Unmarshal(stdout.Bytes(), &bres))
	s.Require().Equalf(0, bres.Code, "fund-consumer-fee-pool failed: %s", bres.RawLog)
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
			livenessProviderRESTPort, consumerID)
		if body, err := httpGet(livenessURL); err == nil {
			s.T().Logf("diagnostic: initial consumer liveness: %s", string(body))
		}

		phase := s.livenessQueryConsumerPhase(consumerID)
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

		phase = s.livenessQueryConsumerPhase(consumerID)
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
		s.livenessFundConsumerFeePool(consumerID, "20000000"+feeDenom)

		// Verify normal mode before freezing delivery.
		s.Require().Eventuallyf(func() bool {
			out, err := s.livenessConsumerBankSendDryRun()
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
			out, err := s.livenessConsumerBankSendDryRun()
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
			out, err := s.livenessConsumerBankSendDryRun()
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
		phase := s.livenessQueryConsumerPhase(consumerID)
		s.Require().Equalf("CONSUMER_PHASE_LAUNCHED", phase,
			"consumer must be LAUNCHED for liveness query test")

		// Diagnostic: log provider staking params and consumer chain init_params so
		// the controller run can confirm the genesis patches actually took effect.
		stakingOut, _, _ := s.livenessdockerExec(s.providerValRes[0].Container.ID, []string{
			providerBinary, "query", "staking", "params",
			"--home", providerHomePath, "--output", "json",
		})
		s.T().Logf("diagnostic: provider staking params: %s", stakingOut.String())

		chainOut, _, _ := s.livenessdockerExec(s.providerValRes[0].Container.ID, []string{
			providerBinary, "query", "provider", "consumer-chain", consumerID,
			"--home", providerHomePath, "--output", "json",
		})
		s.T().Logf("diagnostic: consumer chain (init_params): %s", chainOut.String())

		// Query liveness via the gRPC-gateway REST path.
		// The response Timestamps are RFC3339 strings; Duration is a string like "19.8s".
		livenessURL := fmt.Sprintf("http://localhost:%s/vaas/provider/consumer_liveness/%s",
			livenessProviderRESTPort, consumerID)

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

// testAutoSweepRemoval stops the relayer (and pauses the consumer) so no VSC
// acks return to the provider. After the liveness grace period (~150s) expires,
// the provider's SweepUnresponsiveConsumers moves the consumer to
// CONSUMER_PHASE_STOPPED. Polls with a ~2min window.
func (s *LivenessIntegrationTestSuite) testAutoSweepRemoval() {
	s.Run("auto sweep removal: sustained outage causes LAUNCHED -> STOPPED", func() {
		const consumerID = "0"

		phase := s.livenessQueryConsumerPhase(consumerID)
		s.Require().Equalf("CONSUMER_PHASE_LAUNCHED", phase,
			"consumer %s must be LAUNCHED before auto-sweep test", consumerID)

		// Diagnostic: log provider staking params and consumer liveness state so
		// the controller run can confirm the genesis patches took effect.
		stakingOut, _, _ := s.livenessdockerExec(s.providerValRes[0].Container.ID, []string{
			providerBinary, "query", "staking", "params",
			"--home", providerHomePath, "--output", "json",
		})
		s.T().Logf("diagnostic: provider staking params: %s", stakingOut.String())

		chainOut, _, _ := s.livenessdockerExec(s.providerValRes[0].Container.ID, []string{
			providerBinary, "query", "provider", "consumer-chain", consumerID,
			"--home", providerHomePath, "--output", "json",
		})
		s.T().Logf("diagnostic: consumer chain (init_params): %s", chainOut.String())

		livenessURL := fmt.Sprintf("http://localhost:%s/vaas/provider/consumer_liveness/%s",
			livenessProviderRESTPort, consumerID)
		if body, err := httpGet(livenessURL); err == nil {
			s.T().Logf("diagnostic: consumer liveness: %s", string(body))
		}

		s.T().Log("purging ts-relayer so no VSC acks are returned to provider...")
		s.livenessStopTSRelayer()

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
			p := s.livenessQueryConsumerPhase(consumerID)
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
