package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/ory/dockertest/v3"
	"github.com/ory/dockertest/v3/docker"
)

// testGenesisRoundTrip stops the provider chain, exports its genesis, starts a
// new provider chain initialised from the exported genesis, and verifies the
// previously-registered consumer chain still exists in the new chain's state.
// The export is taken while an equivocation punishment is pending and the
// undelegation behind it is on hold, so the round trip has to carry a live
// hold as well: the exported genesis must list it and the restarted provider
// must still hold it. testEquivocationCancelledByRemoval then watches it
// release through the unbonding index x/staking rebuilds at import.
//
// This is the end-to-end counterpart of TestGenesisRoundTrip in
// x/vaas/provider/keeper/genesis_test.go: the unit test proves the keeper-level
// round-trip is a fixed point; this test proves the binary's export and init
// commands honour the same contract.
//
// Runs after every scenario that needs the consumer: it stops and replaces
// the provider container, and the restarted chain keeps no history at the
// consumer client's trusted height, so nothing crosses IBC afterwards. Only
// provider-local scenarios follow it.
func (s *IntegrationTestSuite) testGenesisRoundTrip() {
	s.Run("genesis round-trip across provider restart", func() {
		const consumerID = "0"
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()

		oldRes := s.providerValRes[0]
		oldDataDir := s.provider.dataDir

		// The state the export has to carry: eqval2's pending punishment and
		// the undelegation held behind it, both left live by
		// testEquivocationQueuedBeforeRemoval.
		s.Require().Len(s.mustPendingEquivocations(consumerID), 1,
			"the export needs exactly one pending punishment")
		s.Require().True(s.singleUnbondingEntryHas(s.eqval2AccAddr, s.eqval2Valoper, "1"),
			"the export needs eqval2's undelegation on hold")

		// Keys added since init are the node user's private files; open them
		// up the way provider-init.sh does for the initial ones, so the host
		// can carry the keyring into the new data dir.
		s.dockerExecMust(oldRes.Container.ID, []string{"chmod", "-R", "777", providerHomePath + "/keyring-test"})

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
		s.T().Log("verifying exported genesis carries the pending punishment and its hold...")
		s.verifyExportedLiveHold(exportedJSON)

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
		if err := s.waitForChainHeight(waitCtx, "http://localhost:26657", 3); err != nil {
			// A chain that never produces a block died in InitChain, and the
			// reason exists only in the container log: surface it instead of
			// failing on a silent timeout.
			var logs bytes.Buffer
			_ = s.dkrPool.Client.Logs(docker.LogsOptions{
				Container:    newRes.Container.ID,
				OutputStream: &logs,
				ErrorStream:  &logs,
				Stdout:       true,
				Stderr:       true,
				Tail:         "120",
			})
			s.T().Logf("restarted provider container log tail:\n%s", logs.String())
			s.Require().NoError(err, "restarted provider failed to produce blocks")
		}

		// 8. Verify the consumer chain is still registered.
		s.T().Log("verifying consumer chain is still registered after restart...")
		chainsOutput, err := s.queryProviderConsumerChains()
		s.Require().NoError(err, "failed to query consumer chains on restarted provider")
		s.Require().Contains(chainsOutput, consumerChainID,
			"consumer chain %s should still be registered after restart", consumerChainID)

		// 9. The hold came through the import: the punishment is still
		//    pending and the undelegation behind it still on hold, for
		//    testEquivocationCancelledByRemoval to release.
		s.T().Log("verifying the pending punishment and its hold survived the restart...")
		s.Require().Eventuallyf(func() bool {
			pending, err := s.queryPendingEquivocations(consumerID)
			return err == nil && len(pending) == 1 &&
				s.singleUnbondingEntryHas(s.eqval2AccAddr, s.eqval2Valoper, "1")
		}, time.Minute, 2*time.Second,
			"the restarted provider does not hold eqval2's undelegation behind its pending punishment")
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

	providerState, ok := app["vaasprovider"]
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

// verifyExportedLiveHold asserts the exported JSON carries the pending
// punishment, the held operation ids, and the staking entry on hold behind
// them: the state the restarted provider has to resume with.
func (s *IntegrationTestSuite) verifyExportedLiveHold(exportedJSON []byte) {
	var top struct {
		AppState struct {
			Provider struct {
				PendingEquivocationPunishments []json.RawMessage `json:"pending_equivocation_punishments"`
				HeldUnbondingOps               []struct {
					UnbondingOpIds []string `json:"unbonding_op_ids"`
				} `json:"held_unbonding_ops"`
			} `json:"vaasprovider"`
			Staking struct {
				UnbondingDelegations []struct {
					DelegatorAddress string `json:"delegator_address"`
					ValidatorAddress string `json:"validator_address"`
					Entries          []struct {
						UnbondingID string `json:"unbonding_id"`
						RefCount    string `json:"unbonding_on_hold_ref_count"`
					} `json:"entries"`
				} `json:"unbonding_delegations"`
			} `json:"staking"`
		} `json:"app_state"`
	}
	s.Require().NoError(json.Unmarshal(exportedJSON, &top), "exported genesis is not valid JSON")
	s.Require().Len(top.AppState.Provider.PendingEquivocationPunishments, 1,
		"exported genesis must carry the pending punishment")
	s.Require().Len(top.AppState.Provider.HeldUnbondingOps, 1,
		"exported genesis must carry the held operation")
	heldIDs := top.AppState.Provider.HeldUnbondingOps[0].UnbondingOpIds
	s.Require().NotEmpty(heldIDs, "the exported hold carries no operation ids")

	var held bool
	for _, ubd := range top.AppState.Staking.UnbondingDelegations {
		if ubd.DelegatorAddress != s.eqval2AccAddr || ubd.ValidatorAddress != s.eqval2Valoper {
			continue
		}
		for _, e := range ubd.Entries {
			if slices.Contains(heldIDs, e.UnbondingID) {
				s.Require().Equalf("1", e.RefCount, "held entry %s must be exported on hold", e.UnbondingID)
				held = true
			}
		}
	}
	s.Require().Truef(held, "the staking export carries no entry of eqval2's undelegation for the held ids %v", heldIDs)
}

// bootstrapRestartedProviderDir builds a new provider data directory that
// reuses the old chain's validator and node keys, keyring and config files,
// but uses the exported JSON as its genesis.json and starts with an empty
// data/.
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
		s.Require().NoErrorf(os.WriteFile(filepath.Join(newDir, name), data, 0o644), //nolint:gosec // the node runs as nonroot in its container and must read this bind-mounted file
			"write %s to new data dir", name)
	}

	// The keyring too: the scenarios after the restart sign transactions
	// with the keys the old chain created.
	const keyringDir = "keyring-test"
	keyringEntries, err := os.ReadDir(filepath.Join(oldDir, keyringDir))
	s.Require().NoError(err, "read the keyring from the old data dir")
	s.Require().NoError(os.MkdirAll(filepath.Join(newDir, keyringDir), 0o777))
	for _, entry := range keyringEntries {
		if entry.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(oldDir, keyringDir, entry.Name()))
		s.Require().NoErrorf(err, "read keyring file %s from old data dir", entry.Name())
		s.Require().NoErrorf(os.WriteFile(filepath.Join(newDir, keyringDir, entry.Name()), data, 0o644), //nolint:gosec // the node runs as nonroot in its container and must read this bind-mounted file
			"write keyring file %s to new data dir", entry.Name())
	}
	s.Require().NoError(os.Chmod(filepath.Join(newDir, keyringDir), 0o777))

	// Write the exported JSON as the new chain's genesis.
	s.Require().NoError(os.WriteFile( //nolint:gosec // the node runs as nonroot in its container and must read this bind-mounted file
		filepath.Join(newDir, "config", "genesis.json"),
		exportedJSON,
		0o644,
	), "failed to write exported genesis")

	// Reset priv_validator_state.json so the validator can sign blocks at
	// the new chain's initial height without tripping double-sign detection.
	s.Require().NoError(os.WriteFile( //nolint:gosec // the node runs as nonroot in its container and must read this bind-mounted file
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
