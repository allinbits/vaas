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
	"strconv"
	"strings"
	"time"

	"github.com/ory/dockertest/v3"
	"github.com/ory/dockertest/v3/docker"
	"github.com/stretchr/testify/suite"

	"github.com/cosmos/cosmos-sdk/codec"
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

	// govVotingPeriod is the provider gov voting period patchProviderGenesis
	// sets; the proposal helpers size their tally wait from it.
	govVotingPeriod time.Duration

	// patchProviderConfigToml / patchConsumerConfigToml, when non-nil, mutate
	// the respective config.toml before the chain container starts (the
	// liveness suite uses this for fast blocks). Leaving them nil skips the
	// step.
	patchProviderConfigToml func(path string)
	patchConsumerConfigToml func(path string)

	// enableConsumerPhotonFees switches the consumer's photon-only fee policy
	// on in its genesis (the reference consumer app wires no governance, so
	// the flag cannot be flipped later) and launches the consumer node with
	// zero min-gas-prices, the launch configuration the policy assumes: the
	// photon ante polices the fee denom in consensus, while the relayer's
	// exempt traffic must stay admissible whatever it pays.
	enableConsumerPhotonFees bool

	// validators is the number of validator nodes each chain runs, one
	// consumer node per provider validator; zero means one. Only val0 binds
	// host ports and holds the keys the suite transacts from; the other
	// nodes are reached through docker exec.
	validators int
}

// baseTestSuite carries the shared Docker state and helper methods. Concrete
// suites embed it and supply their own cfg and SetupSuite.
type baseTestSuite struct {
	suite.Suite

	cfg baseSuiteConfig
	cdc codec.Codec

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

	// A failed run leaves no other trace of what the chains were doing; keep
	// the tail of both logs in the test output.
	if s.T().Failed() {
		if len(s.providerValRes) > 0 {
			s.T().Logf("provider log tail:\n%s", tailLines(s.providerLogs(), 300))
		}
		if len(s.consumerValRes) > 0 {
			s.T().Logf("consumer log tail:\n%s", tailLines(s.consumerLogs(), 100))
		}
	}

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
		fmt.Sprintf("%s-%s-ts-relayer", s.cfg.providerChainID, s.cfg.consumerChainID),
	}
	for i := range s.validatorCount() {
		staleNames = append(staleNames,
			fmt.Sprintf("%s-val%d", s.cfg.providerChainID, i),
			fmt.Sprintf("%s-val%d", s.cfg.consumerChainID, i),
		)
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

// genesisSection returns the object at path in a genesis app state, failing
// the suite when any step is missing: a patch that skipped a missing section
// would leave the suite running on the shipped defaults it exists to replace.
func (s *baseTestSuite) genesisSection(appState map[string]any, path ...string) map[string]any {
	section := appState
	for i, key := range path {
		next, ok := section[key].(map[string]any)
		s.Require().Truef(ok, "provider genesis has no %q object at %v", key, path[:i+1])
		section = next
	}
	return section
}

// validatorCount is the number of validator nodes each chain runs.
func (s *baseTestSuite) validatorCount() int {
	if s.cfg.validators < 1 {
		return 1
	}
	return s.cfg.validators
}

// providerHome returns the host home directory of provider node i.
func (s *baseTestSuite) providerHome(i int) string {
	return s.provider.homes[i]
}

// consumerHome returns the host home directory of consumer node i.
func (s *baseTestSuite) consumerHome(i int) string {
	return s.consumer.homes[i]
}

// newChainHomes creates the host directory an init container fills with one
// node home per validator, and returns it with the home paths it will hold
// (val0, val1, ...), in validator order.
func (s *baseTestSuite) newChainHomes(suffix string) (parent string, homes []string) {
	parent, err := os.MkdirTemp("", s.cfg.tmpDirPrefix+suffix)
	s.Require().NoError(err)
	s.tmpDirs = append(s.tmpDirs, parent)
	s.Require().NoError(os.Chmod(parent, 0o777))
	homes = make([]string, s.validatorCount())
	for i := range homes {
		homes[i] = filepath.Join(parent, fmt.Sprintf("val%d", i))
	}
	return parent, homes
}

// copyToAllHomes copies relPath from homes[0] over the same path in every
// other home, so a file patched on val0 reaches every node.
func (s *baseTestSuite) copyToAllHomes(homes []string, relPath string) {
	for _, home := range homes[1:] {
		s.Require().NoErrorf(copyFile(filepath.Join(homes[0], relPath), filepath.Join(home, relPath)),
			"failed to copy %s to %s", relPath, home)
	}
}

// nodePorts are the host ports a chain's val0 binds.
type nodePorts struct {
	rpc, grpc, rest, p2p string
}

func (s *baseTestSuite) providerPorts() nodePorts {
	return nodePorts{rpc: s.cfg.providerRPCPort, grpc: s.cfg.providerGRPCPort, rest: s.cfg.providerRESTPort, p2p: s.cfg.providerP2PPort}
}

func (s *baseTestSuite) consumerPorts() nodePorts {
	return nodePorts{rpc: s.cfg.consumerRPCPort, grpc: s.cfg.consumerGRPCPort, rest: s.cfg.consumerRESTPort, p2p: s.cfg.consumerP2PPort}
}

// startChainNode starts validator node index of a chain as the container
// <chainID>-val<index>, mounting hostHome at homePath. Only val0 binds host
// ports: the other nodes are reached through docker exec, and the relayer
// and the suite's queries address val0 by container name and host port.
func (s *baseTestSuite) startChainNode(chainID, binary, homePath, hostHome string, index int, ports nodePorts) *dockertest.Resource {
	opts := &dockertest.RunOptions{
		Name:       fmt.Sprintf("%s-val%d", chainID, index),
		Repository: e2eChainImage,
		NetworkID:  s.dkrNet.Network.ID,
		Mounts:     []string{fmt.Sprintf("%s:%s", hostHome, homePath)},
		Cmd:        []string{binary, "start", "--home", homePath},
	}
	if index == 0 {
		opts.PortBindings = map[docker.Port][]docker.PortBinding{
			"26657/tcp": {{HostIP: "", HostPort: ports.rpc}},
			"9090/tcp":  {{HostIP: "", HostPort: ports.grpc}},
			"1317/tcp":  {{HostIP: "", HostPort: ports.rest}},
			"26656/tcp": {{HostIP: "", HostPort: ports.p2p}},
		}
	}
	resource, err := s.dkrPool.RunWithOptions(opts, func(config *docker.HostConfig) {
		config.RestartPolicy = docker.RestartPolicy{Name: "no"}
	})
	s.Require().NoErrorf(err, "failed to start %s container", opts.Name)
	return resource
}

// nodeHeight reads a node's latest block height from its own status, through
// docker exec, which is how nodes without host ports are observed.
func (s *baseTestSuite) nodeHeight(containerID, binary, homePath string) (int64, error) {
	stdout, stderr, err := s.dockerExec(containerID, []string{binary, "status", "--home", homePath})
	if err != nil {
		return 0, fmt.Errorf("status: %w (stderr: %s)", err, stderr.String())
	}
	var res struct {
		SyncInfo struct {
			LatestBlockHeight string `json:"latest_block_height"`
		} `json:"sync_info"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &res); err != nil {
		return 0, fmt.Errorf("decode status: %w (stderr: %s)", err, stderr.String())
	}
	return strconv.ParseInt(res.SyncInfo.LatestBlockHeight, 10, 64)
}

// waitForNodesCaughtUp fails the suite unless every node of resources reaches
// height 3 and sits within two blocks of resources[0] before timeout, logging
// every node's last log lines first when it does fail.
func (s *baseTestSuite) waitForNodesCaughtUp(resources []*dockertest.Resource, binary, homePath string, timeout time.Duration) {
	if len(resources) < 2 {
		return
	}
	caughtUp := func() bool {
		head, err := s.nodeHeight(resources[0].Container.ID, binary, homePath)
		if err != nil || head < 3 {
			return false
		}
		for _, r := range resources[1:] {
			h, err := s.nodeHeight(r.Container.ID, binary, homePath)
			if err != nil || h < 3 || head-h > 2 {
				return false
			}
		}
		return true
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if caughtUp() {
			return
		}
		time.Sleep(3 * time.Second)
	}
	s.logNodeTails(resources, 80)
	s.Require().Failf("nodes not caught up", "not every node caught up with val0 within %s", timeout)
}

// logNodeTails logs the last lines of every node's container output. A
// bring-up that fails purges its nodes with the suite, so this is the only
// record of why they produced no blocks.
func (s *baseTestSuite) logNodeTails(resources []*dockertest.Resource, lines int) {
	for i, r := range resources {
		var buf bytes.Buffer
		_ = s.dkrPool.Client.Logs(docker.LogsOptions{
			Container:    r.Container.ID,
			OutputStream: &buf,
			ErrorStream:  &buf,
			Stdout:       true,
			Stderr:       true,
			Tail:         strconv.Itoa(lines),
		})
		s.T().Logf("val%d (%s), last %d log lines:\n%s", i, r.Container.ID[:12], lines, buf.String())
	}
}

// stopConsumerNode stops consumer node i, the way a validator's node goes
// offline; its container and data stay, so startConsumerNode brings the same
// node back.
func (s *baseTestSuite) stopConsumerNode(i int) {
	s.Require().NoErrorf(s.dkrPool.Client.StopContainer(s.consumerValRes[i].Container.ID, 30),
		"failed to stop consumer val%d", i)
}

// startConsumerNode starts consumer node i again and waits until every
// consumer node has caught up with val0.
func (s *baseTestSuite) startConsumerNode(i int) {
	s.Require().NoErrorf(s.dkrPool.Client.StartContainer(s.consumerValRes[i].Container.ID, nil),
		"failed to start consumer val%d", i)
	s.waitForNodesCaughtUp(s.consumerValRes, consumerBinary, consumerHomePath, 3*time.Minute)
}

// initAndStartProvider initializes the provider chain's node homes using a
// temporary Docker container that runs provider-init.sh, then starts one
// chain container per validator.
func (s *baseTestSuite) initAndStartProvider() {
	parentDir, homes := s.newChainHomes("provider-")
	s.provider.homes = homes
	s.provider.dataDir = homes[0]

	// Run init script in a temporary container
	scriptPath := filepath.Join(testDir(), "scripts", "provider-init.sh")
	s.runInitContainer(s.cfg.providerInitName, scriptPath, "/scripts/provider-init.sh", parentDir, initHomesPath, []string{
		"BINARY=" + providerBinary,
		"HOMES_DIR=" + initHomesPath,
		"NUM_VALIDATORS=" + strconv.Itoa(s.validatorCount()),
		"CHAIN_ID=" + s.cfg.providerChainID,
		"DENOM=" + bondDenom,
		"MNEMONIC=" + relayerMnemonic,
	})

	// Apply the suite-specific genesis mutations on val0 and hand the result
	// to every node.
	genesisFile := filepath.Join(homes[0], "config", "genesis.json")
	s.patchGenesisJSON(genesisFile, func(genesis map[string]any) {
		appState := genesis["app_state"].(map[string]any)
		s.cfg.patchProviderGenesis(appState)
	})
	s.copyToAllHomes(homes, filepath.Join("config", "genesis.json"))

	// Apply the suite-specific config.toml mutations (e.g. fast blocks).
	if s.cfg.patchProviderConfigToml != nil {
		for _, home := range homes {
			s.cfg.patchProviderConfigToml(filepath.Join(home, "config", "config.toml"))
		}
	}

	s.T().Logf("starting %d provider chain container(s)...", len(homes))
	for i, home := range homes {
		resource := s.startChainNode(s.cfg.providerChainID, providerBinary, providerHomePath, home, i, s.providerPorts())
		s.providerValRes = append(s.providerValRes, resource)
		s.T().Logf("provider container val%d started: %s", i, resource.Container.ID[:12])
	}

	// Wait for the provider to produce blocks and every node to follow.
	waitCtx, waitCancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer waitCancel()
	err := s.waitForChainHeight(waitCtx, "http://localhost:"+s.cfg.providerRPCPort, 3)
	if err != nil {
		s.logNodeTails(s.providerValRes, 80)
	}
	s.Require().NoError(err, "provider failed to produce blocks")
	s.waitForNodesCaughtUp(s.providerValRes, providerBinary, providerHomePath, 2*time.Minute)
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
		providerBinary, "tx", "vaasprovider", "create-consumer", "/tmp/create_consumer.json",
		// The submitter becomes the consumer's owner, and the owner is also
		// the account allowed to pin the provider client on the consumer
		// side. The owner key (HD index 1 of the shared mnemonic) exists on
		// both chains with the same address bytes, and unlike the relayer
		// key its account sequence is not being consumed concurrently by the
		// ts-relayer's own transactions.
		"--from", "owner",
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
			providerBinary, "query", "vaasprovider", "consumer-genesis", "0",
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

// initAndStartConsumer initializes the consumer chain's node homes using a
// temporary Docker container that runs consumer-init.sh, then starts one
// chain container per validator.
func (s *baseTestSuite) initAndStartConsumer(consumerGenesisJSON []byte) {
	parentDir, homes := s.newChainHomes("consumer-")
	s.consumer.homes = homes
	s.consumer.dataDir = homes[0]

	// Run init script in a temporary container
	scriptPath := filepath.Join(testDir(), "scripts", "consumer-init.sh")
	initEnv := []string{
		"BINARY=" + consumerBinary,
		"HOMES_DIR=" + initHomesPath,
		"NUM_VALIDATORS=" + strconv.Itoa(s.validatorCount()),
		"CHAIN_ID=" + s.cfg.consumerChainID,
		"DENOM=" + bondDenom,
		"MNEMONIC=" + relayerMnemonic,
	}
	if s.cfg.enableConsumerPhotonFees {
		initEnv = append(initEnv, "MIN_GAS_PRICE=0"+bondDenom)
	}
	s.runInitContainer(s.cfg.consumerInitName, scriptPath, "/scripts/consumer-init.sh", parentDir, initHomesPath, initEnv)

	// Patch consumer genesis with provider's consumer-genesis data on the host
	genesisFile := filepath.Join(homes[0], "config", "genesis.json")
	err := patchConsumerGenesisWithProviderData(genesisFile, consumerGenesisJSON)
	s.Require().NoError(err, "failed to patch consumer genesis")

	// The provider-authored consumer genesis always writes photon_fees_enabled
	// off; enabling it is a consumer-local genesis choice, made here the same
	// way a real core shard's operator would make it.
	if s.cfg.enableConsumerPhotonFees {
		s.patchGenesisJSON(genesisFile, func(genesis map[string]any) {
			appState, ok := genesis["app_state"].(map[string]any)
			s.Require().True(ok, "app_state not found in consumer genesis")
			vaasconsumer, ok := appState["vaasconsumer"].(map[string]any)
			s.Require().True(ok, "vaasconsumer not found in consumer genesis")
			params, ok := vaasconsumer["params"].(map[string]any)
			s.Require().True(ok, "vaasconsumer params not found in consumer genesis")
			params["photon_fees_enabled"] = true
		})
	}

	// Patch consumer slashing params for aggressive downtime detection, then
	// hand the finished genesis to every node.
	s.patchConsumerSlashingParams()
	s.copyToAllHomes(homes, filepath.Join("config", "genesis.json"))

	// Apply the suite-specific config.toml mutations (e.g. fast blocks).
	if s.cfg.patchConsumerConfigToml != nil {
		for _, home := range homes {
			s.cfg.patchConsumerConfigToml(filepath.Join(home, "config", "config.toml"))
		}
	}

	// Each consumer node validates under its provider validator's consensus
	// key. Its node key stays the one its init produced: the peer lists the
	// init step wrote name those.
	for i, home := range homes {
		err = copyFile(
			filepath.Join(s.providerHome(i), "config", "priv_validator_key.json"),
			filepath.Join(home, "config", "priv_validator_key.json"),
		)
		s.Require().NoErrorf(err, "failed to copy priv_validator_key.json for val%d", i)
	}

	s.T().Logf("starting %d consumer chain container(s)...", len(homes))
	for i, home := range homes {
		resource := s.startChainNode(s.cfg.consumerChainID, consumerBinary, consumerHomePath, home, i, s.consumerPorts())
		s.consumerValRes = append(s.consumerValRes, resource)
		s.T().Logf("consumer container val%d started: %s", i, resource.Container.ID[:12])
	}

	// Wait for the consumer to produce blocks and every node to follow.
	waitCtx, waitCancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer waitCancel()
	err = s.waitForChainHeight(waitCtx, "http://localhost:"+s.cfg.consumerRPCPort, 3)
	if err != nil {
		s.logNodeTails(s.consumerValRes, 80)
	}
	s.Require().NoError(err, "consumer failed to produce blocks")
	s.waitForNodesCaughtUp(s.consumerValRes, consumerBinary, consumerHomePath, 2*time.Minute)
	s.T().Log("consumer chain is producing blocks")
}

// setupTSRelayer starts the ts-relayer container, configures it with
// mnemonics and gas prices for both chains, and creates an IBC v2 path.
// The ts-relayer handles counterparty registration and packet relaying.
func (s *baseTestSuite) setupTSRelayer() {
	s.startTSRelayer()

	s.tsRelayerAddMnemonic(s.cfg.providerChainID)
	s.tsRelayerAddMnemonic(s.cfg.consumerChainID)
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

// tendermintClientTracking polls the given chain until an IBC tendermint
// client tracking counterpartyChainID exists, and returns its client id. The
// ts-relayer creates the clients shortly after add-path; a fresh chain has no
// other clients, but the filter keys on the tracked chain id rather than
// assuming an index.
func (s *baseTestSuite) tendermintClientTracking(containerID, binary, homePath, counterpartyChainID string) string {
	var clientID string
	s.Require().Eventually(func() bool {
		stdout, _, err := s.dockerExec(containerID, []string{
			binary, "query", "ibc", "client", "states",
			"--home", homePath,
			"-o", "json",
		})
		if err != nil {
			return false
		}
		var res struct {
			ClientStates []struct {
				ClientId    string `json:"client_id"`
				ClientState struct {
					ChainId string `json:"chain_id"`
				} `json:"client_state"`
			} `json:"client_states"`
		}
		if err := json.Unmarshal(stdout.Bytes(), &res); err != nil {
			return false
		}
		for _, cs := range res.ClientStates {
			if cs.ClientState.ChainId == counterpartyChainID {
				clientID = cs.ClientId
				return true
			}
		}
		return false
	}, 2*time.Minute, 2*time.Second,
		"no IBC client tracking %s appeared on %s", counterpartyChainID, binary)
	return clientID
}

// declareConsumerClients performs the two owner declarations that connect a
// launched consumer to its relayer-created IBC clients. Nothing is discovered
// on-chain: the provider learns its consumer client from the owner's
// update-consumer, and the consumer learns its provider client from the
// owner's set-provider-client. Until both land, VSC packets stay queued on the
// provider and rejected by the consumer.
func (s *baseTestSuite) declareConsumerClients(consumerID string) {
	providerClientID := s.tendermintClientTracking(
		s.providerValRes[0].Container.ID, providerBinary, providerHomePath, s.cfg.consumerChainID)
	s.T().Logf("declaring consumer client on the provider: %s", providerClientID)

	updateJSON := fmt.Sprintf(`{"consumer_id": %s, "client_id": %q}`, consumerID, providerClientID)
	s.dockerExecMust(s.providerValRes[0].Container.ID, []string{
		"sh", "-c", fmt.Sprintf("echo '%s' > /tmp/declare_client.json", updateJSON),
	})
	// The declaration validates the client against live IBC state during the
	// --gas auto simulation, and the relayer registers the counterparty in its
	// own time shortly after creating the client -- so a too-early attempt
	// fails simulation with an empty stdout. Retry until it broadcasts, and
	// log each failed attempt, since a simulation error only reaches stderr.
	s.Require().Eventuallyf(func() bool {
		stdout, stderr, err := s.dockerExec(s.providerValRes[0].Container.ID, []string{
			providerBinary, "tx", "vaasprovider", "update-consumer", "/tmp/declare_client.json",
			"--from", "owner",
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
		if err != nil || stdout.Len() == 0 {
			s.T().Logf("declare attempt not broadcast yet: err=%v stderr=%s", err, stderr.String())
			return false
		}
		s.requireTxCommitted(stdout.Bytes())
		return true
	}, 2*time.Minute, 3*time.Second,
		"declaring the consumer client never broadcast; see the attempt logs above")

	consumerClientID := s.tendermintClientTracking(
		s.consumerValRes[0].Container.ID, consumerBinary, consumerHomePath, s.cfg.providerChainID)
	s.T().Logf("pinning provider client on the consumer: %s", consumerClientID)

	s.Require().Eventuallyf(func() bool {
		stdout, stderr, err := s.dockerExec(s.consumerValRes[0].Container.ID, []string{
			consumerBinary, "tx", "vaasconsumer", "set-provider-client", consumerClientID,
			"--from", "owner",
			"--home", consumerHomePath,
			"--keyring-backend", "test",
			"--chain-id", s.cfg.consumerChainID,
			"--gas", "auto",
			"--gas-adjustment", "1.5",
			"--fees", "10000" + bondDenom,
			"--broadcast-mode", "sync",
			"-y",
			"-o", "json",
		})
		if err != nil || stdout.Len() == 0 {
			s.T().Logf("pin attempt not broadcast yet: err=%v stderr=%s", err, stderr.String())
			return false
		}
		s.requireTxCommittedOn(s.consumerValRes[0].Container.ID, consumerBinary, consumerHomePath, stdout.Bytes())
		return true
	}, 2*time.Minute, 3*time.Second,
		"pinning the provider client never broadcast; see the attempt logs above")
}

// tailLines returns the last n lines of text.
func tailLines(text string, n int) string {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
