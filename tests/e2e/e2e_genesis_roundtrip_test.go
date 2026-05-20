package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/ory/dockertest/v3"
	"github.com/ory/dockertest/v3/docker"
)

// testGenesisRoundTrip stops the provider chain, exports its genesis, starts a
// new provider chain initialised from the exported genesis, and verifies the
// previously-registered consumer chain still exists in the new chain's state.
//
// This is the end-to-end counterpart of TestGenesisRoundTrip in
// x/vaas/provider/keeper/genesis_test.go: the unit test proves the keeper-level
// round-trip is a fixed point; this test proves the binary's export and init
// commands honour the same contract.
//
// MUST run last in TestVAAS — it stops and replaces the provider container,
// after which the relayer and the original consumer chain may produce noise
// but the suite is about to tear down anyway.
func (s *IntegrationTestSuite) testGenesisRoundTrip() {
	s.Run("genesis round-trip across provider restart", func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()

		oldRes := s.providerValRes[0]
		oldDataDir := s.provider.dataDir

		// 1. Stop the provider container gracefully so the data dir is in a
		//    consistent state for export.
		s.T().Log("stopping provider container for export...")
		s.Require().NoError(s.dkrPool.Client.StopContainer(oldRes.Container.ID, 30))

		// 2. Run an ephemeral container that exports the genesis to stdout
		//    with --for-zero-height so the resulting JSON is suitable for
		//    initialising a fresh chain.
		s.T().Log("exporting genesis via ephemeral container...")
		exportedJSON := s.exportProviderGenesis(ctx, oldDataDir)

		// 3. Parse + verify the exported JSON carries the new ConsumerState
		//    fields with populated values.
		s.T().Log("verifying exported genesis contains owner/metadata/init_params...")
		s.verifyExportedGenesis(exportedJSON)

		// 4. Bootstrap a new provider data dir from the old: same validator
		//    keys + config, but the exported JSON as genesis.json and a
		//    fresh data/ so the chain initialises from genesis.
		s.T().Log("bootstrapping new provider data dir from exported genesis...")
		newDataDir := s.bootstrapRestartedProviderDir(oldDataDir, exportedJSON)

		// 5. Purge the old container so its port bindings are released and
		//    the teardown step doesn't try to purge a stopped container.
		s.T().Log("purging old provider container...")
		s.Require().NoError(s.dkrPool.Purge(oldRes))
		s.providerValRes = s.providerValRes[:0]

		// 6. Start a new provider container from the new data dir on the
		//    same ports the old container used.
		s.T().Log("starting restarted provider container...")
		newRes := s.startRestartedProvider(newDataDir)
		s.providerValRes = append(s.providerValRes, newRes)

		// 7. Wait for the new provider to produce blocks.
		s.T().Log("waiting for restarted provider to produce blocks...")
		waitCtx, waitCancel := context.WithTimeout(ctx, 2*time.Minute)
		defer waitCancel()
		s.Require().NoError(s.waitForChainHeight(waitCtx, "http://localhost:26657", 3),
			"restarted provider failed to produce blocks")

		// 8. Verify the consumer chain is still registered.
		s.T().Log("verifying consumer chain is still registered after restart...")
		chainsOutput, err := s.queryProviderConsumerChains()
		s.Require().NoError(err, "failed to query consumer chains on restarted provider")
		s.Require().Contains(chainsOutput, consumerChainID,
			"consumer chain %s should still be registered after restart", consumerChainID)
	})
}

// exportProviderGenesis launches an ephemeral container that mounts the
// provider's data directory and runs `provider export --for-zero-height`.
// Returns the exported genesis JSON.
func (s *IntegrationTestSuite) exportProviderGenesis(ctx context.Context, dataDir string) []byte {
	exportRes, err := s.dkrPool.RunWithOptions(
		&dockertest.RunOptions{
			Name:       fmt.Sprintf("%s-export", providerChainID),
			Repository: e2eChainImage,
			NetworkID:  s.dkrNet.Network.ID,
			User:       "nonroot",
			Mounts: []string{
				fmt.Sprintf("%s:%s", dataDir, providerHomePath),
			},
			Cmd: []string{
				providerBinary, "export",
				"--home", providerHomePath,
				"--for-zero-height",
			},
		},
		func(config *docker.HostConfig) {
			config.RestartPolicy = docker.RestartPolicy{Name: "no"}
		},
	)
	s.Require().NoError(err, "failed to start export container")
	defer func() {
		if perr := s.dkrPool.Purge(exportRes); perr != nil {
			s.T().Logf("failed to purge export container: %v", perr)
		}
	}()

	exitCode, err := s.dkrPool.Client.WaitContainerWithContext(exportRes.Container.ID, ctx)
	s.Require().NoError(err, "export container wait failed")

	var stdoutBuf, stderrBuf bytes.Buffer
	err = s.dkrPool.Client.Logs(docker.LogsOptions{
		Container:    exportRes.Container.ID,
		OutputStream: &stdoutBuf,
		ErrorStream:  &stderrBuf,
		Stdout:       true,
		Stderr:       true,
	})
	s.Require().NoError(err, "failed to read export container logs")
	s.Require().Equalf(0, exitCode,
		"export container failed (exit=%d)\nstdout:\n%s\nstderr:\n%s",
		exitCode, stdoutBuf.String(), stderrBuf.String())

	// Some SDK versions log "exported genesis ..." lines before the JSON
	// body on stdout. Strip anything before the first '{'.
	raw := stdoutBuf.Bytes()
	if i := bytes.IndexByte(raw, '{'); i > 0 {
		raw = raw[i:]
	}
	return raw
}

// verifyExportedGenesis asserts that the exported JSON includes the consumer
// registered during setup along with the new ConsumerState fields populated.
func (s *IntegrationTestSuite) verifyExportedGenesis(exportedJSON []byte) {
	var top map[string]json.RawMessage
	s.Require().NoError(json.Unmarshal(exportedJSON, &top), "exported genesis is not valid JSON")

	appState, ok := top["app_state"]
	s.Require().True(ok, "exported genesis missing app_state")

	var app map[string]json.RawMessage
	s.Require().NoError(json.Unmarshal(appState, &app))

	providerState, ok := app["provider"]
	s.Require().True(ok, "app_state missing provider module")

	type consumerStateJSON struct {
		ConsumerId   uint64                 `json:"consumer_id,string"`
		ChainId      string                 `json:"chain_id"`
		Phase        string                 `json:"phase"`
		OwnerAddress string                 `json:"owner_address"`
		Metadata     map[string]interface{} `json:"metadata"`
		InitParams   map[string]interface{} `json:"init_params"`
	}
	var provider struct {
		ConsumerStates []consumerStateJSON `json:"consumer_states"`
	}
	s.Require().NoError(json.Unmarshal(providerState, &provider))
	s.Require().GreaterOrEqual(len(provider.ConsumerStates), 1, "no consumer states exported")

	var found *consumerStateJSON
	for i := range provider.ConsumerStates {
		if provider.ConsumerStates[i].ChainId == consumerChainID {
			found = &provider.ConsumerStates[i]
			break
		}
	}
	s.Require().NotNil(found, "expected consumer %s not found in export", consumerChainID)

	s.Require().NotEmpty(found.OwnerAddress, "owner_address must be exported for consumer %s", consumerChainID)
	s.Require().NotNil(found.Metadata, "metadata must be exported for consumer %s", consumerChainID)
	s.Require().NotNil(found.InitParams, "init_params must be exported for consumer %s", consumerChainID)
}

// bootstrapRestartedProviderDir builds a new provider data directory that
// reuses the old chain's validator and node keys + config files, but uses
// the exported JSON as its genesis.json and starts with an empty data/.
func (s *IntegrationTestSuite) bootstrapRestartedProviderDir(oldDir string, exportedJSON []byte) string {
	newDir, err := os.MkdirTemp("", "vaas-e2e-provider-restart-")
	s.Require().NoError(err)
	s.tmpDirs = append(s.tmpDirs, newDir)
	s.Require().NoError(os.Chmod(newDir, 0o777))
	s.Require().NoError(os.MkdirAll(filepath.Join(newDir, "config"), 0o777))
	s.Require().NoError(os.MkdirAll(filepath.Join(newDir, "data"), 0o777))

	// Copy validator + node keys and chain config files from the old dir.
	for _, name := range []string{
		"config/priv_validator_key.json",
		"config/node_key.json",
		"config/app.toml",
		"config/config.toml",
	} {
		data, err := os.ReadFile(filepath.Join(oldDir, name))
		s.Require().NoErrorf(err, "read %s from old data dir", name)
		s.Require().NoErrorf(os.WriteFile(filepath.Join(newDir, name), data, 0o644),
			"write %s to new data dir", name)
	}

	// Write the exported JSON as the new chain's genesis.
	s.Require().NoError(os.WriteFile(
		filepath.Join(newDir, "config", "genesis.json"),
		exportedJSON,
		0o644,
	), "failed to write exported genesis")

	// Reset priv_validator_state.json so the validator can sign blocks at
	// the new chain's initial height without tripping double-sign detection.
	s.Require().NoError(os.WriteFile(
		filepath.Join(newDir, "data", "priv_validator_state.json"),
		[]byte(`{"height":"0","round":0,"step":0}`),
		0o644,
	), "failed to write priv_validator_state.json")

	s.Require().NoError(os.Chmod(filepath.Join(newDir, "config"), 0o777))
	s.Require().NoError(os.Chmod(filepath.Join(newDir, "data"), 0o777))
	return newDir
}

// startRestartedProvider runs a new provider container on the same host ports
// the original used, mounting the new data dir.
func (s *IntegrationTestSuite) startRestartedProvider(dataDir string) *dockertest.Resource {
	// Update so subsequent helpers find the new data dir.
	s.provider.dataDir = dataDir

	resource, err := s.dkrPool.RunWithOptions(
		&dockertest.RunOptions{
			Name:       fmt.Sprintf("%s-val0", providerChainID),
			Repository: e2eChainImage,
			NetworkID:  s.dkrNet.Network.ID,
			Mounts: []string{
				fmt.Sprintf("%s:%s", dataDir, providerHomePath),
			},
			PortBindings: map[docker.Port][]docker.PortBinding{
				"26657/tcp": {{HostIP: "", HostPort: "26657"}},
				"9090/tcp":  {{HostIP: "", HostPort: "9090"}},
				"1317/tcp":  {{HostIP: "", HostPort: "1317"}},
				"26656/tcp": {{HostIP: "", HostPort: "26656"}},
			},
			Cmd: []string{providerBinary, "start", "--home", providerHomePath},
		},
		func(config *docker.HostConfig) {
			config.RestartPolicy = docker.RestartPolicy{Name: "no"}
		},
	)
	s.Require().NoError(err, "failed to start restarted provider container")
	return resource
}
