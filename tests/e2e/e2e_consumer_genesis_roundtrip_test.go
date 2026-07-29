package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/ory/dockertest/v3"
	"github.com/ory/dockertest/v3/docker"

	cmtservice "github.com/cosmos/cosmos-sdk/client/grpc/cmtservice"
)

// testConsumerGenesisRoundTrip stops the consumer chain, exports its genesis
// at a continuing height, starts a new consumer chain initialised from the
// exported genesis, and verifies the restarted consumer picks the VSC flow
// back up where the old one left off.
//
// It is the consumer-side counterpart of testGenesisRoundTrip and the
// end-to-end companion of TestGenesisRoundTrip in
// x/vaas/consumer/keeper/genesis_test.go and of the export test in
// app/consumer/export_test.go: the exported genesis must carry the
// cross-chain validator set with usable consensus pubkeys (a null pubkey
// panics CometBFT's GenesisDoc.ValidateAndComplete on reload) and the
// out-of-order dedup watermark (highest_valset_update_id), and the restarted
// chain -- same chain id, same IBC clients, heights continuing from the
// export -- must keep converging on the provider's validator set through the
// relayer, which is never restarted (only paused briefly to quiesce the
// consumer before the halt).
//
// Runs immediately before testLivenessRemoval: consumer "0" must still be
// LAUNCHED here, and it remains LAUNCHED afterwards (the outage lasts minutes
// against the suite's ~13-day liveness grace).
func (s *IntegrationTestSuite) testConsumerGenesisRoundTrip() {
	s.Run("genesis round-trip across consumer restart", func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()

		oldRes := s.consumerValRes[0]
		oldDataDir := s.consumer.dataDir

		// Record the consumer's pre-restart consensus valset and height; the
		// export and the restarted chain are checked against both.
		preVals, err := s.queryConsumerNetValidators()
		s.Require().NoError(err, "failed to query consumer validators before restart")
		preValSet := s.valPowerMap(preVals)
		s.Require().NotEmpty(preValSet, "consumer has no validators before restart")
		preHeight, err := s.queryConsumerBlockHeight()
		s.Require().NoError(err, "failed to query consumer height before restart")

		// 1. Pause the relayer and let the consumer commit a few more blocks
		//    so its mempool drains. The node logs one applied-VSC line per
		//    handler *execution*, and a tx executes in proposal, process, and
		//    finalize modes (plus simulations) with only finalize committing;
		//    without this quiesce, a stop landing mid-proposal could leave the
		//    log ahead of the committed state the export reads. After the
		//    drain, the last logged id is the committed dedup watermark.
		s.T().Log("pausing relayer and draining the consumer mempool...")
		s.Require().NoError(s.dkrPool.Client.PauseContainer(s.tsRelayerResource.Container.ID),
			"failed to pause relayer container")
		// Safety net: never leave the relayer paused if anything below fails
		// (errors ignored: the happy path already unpaused it).
		defer func() { _ = s.dkrPool.Client.UnpauseContainer(s.tsRelayerResource.Container.ID) }()
		quiesceCtx, quiesceCancel := context.WithTimeout(ctx, time.Minute)
		defer quiesceCancel()
		s.Require().NoError(
			s.waitForChainHeight(quiesceCtx, "http://localhost:"+s.cfg.consumerRPCPort, preHeight+3),
			"consumer did not commit blocks after the relayer pause")

		// 2. Stop the consumer container gracefully so the data dir is in a
		//    consistent state for export.
		s.T().Log("stopping consumer container for export...")
		s.Require().NoError(s.dkrPool.Client.StopContainer(oldRes.Container.ID, 30))

		// 3. With the container stopped and its recv traffic drained, the log
		//    is final: the highest vscID the consumer ever applied is the
		//    dedup watermark the export must carry.
		appliedIDs := appliedVscIDs(s.consumerLogs())
		s.Require().NotEmpty(appliedIDs, "consumer log contains no applied VSC packets before restart")
		watermark := appliedIDs[len(appliedIDs)-1]
		s.Require().Positive(watermark, "pre-restart dedup watermark must be positive")
		s.T().Logf("pre-restart dedup watermark: vscID=%d", watermark)

		// The consumer is down from here until the restart, so the relayer
		// has nothing to deliver to it; resume it now and leave it running
		// through the entire export/restart/convergence flow.
		s.T().Log("unpausing relayer...")
		s.Require().NoError(s.dkrPool.Client.UnpauseContainer(s.tsRelayerResource.Container.ID),
			"failed to unpause relayer container")

		// 4. Export at the last committed height (no --for-zero-height): the
		//    restarted chain continues at the next height, keeping the
		//    provider's IBC client for the consumer verifiable across the
		//    restart.
		s.T().Log("exporting consumer genesis via ephemeral container...")
		exportedJSON := s.exportConsumerGenesis(ctx, oldDataDir)

		// 5. Parse + verify the exported JSON: continuing height, a validator
		//    set with usable pubkeys, and the dedup watermark.
		s.T().Log("verifying exported genesis carries the valset and the dedup watermark...")
		exportedValSet, initialHeight := s.verifyExportedConsumerGenesis(exportedJSON, watermark)
		s.Require().Greater(initialHeight, preHeight,
			"exported initial_height must continue past the pre-restart height")
		s.Require().Equal(preValSet, exportedValSet,
			"exported validator set must match the consumer's pre-restart consensus valset")

		// 6. Bootstrap a new consumer data dir from the old: same validator
		//    keys + config, but the exported JSON as genesis.json and a fresh
		//    data/ so the chain initialises from genesis.
		s.T().Log("bootstrapping new consumer data dir from exported genesis...")
		newDataDir := s.bootstrapRestartedConsumerDir(oldDataDir, exportedJSON)

		// 7. Purge the old container so its name and port bindings are freed
		//    for the replacement (the relayer reaches the consumer by
		//    container name, so the replacement must reuse it).
		s.T().Log("purging old consumer container...")
		s.Require().NoError(s.dkrPool.Purge(oldRes))
		s.consumerValRes = s.consumerValRes[:0]

		// 8. Start a new consumer container from the new data dir on the same
		//    name and ports the old container used.
		s.T().Log("starting restarted consumer container...")
		newRes := s.startRestartedConsumer(newDataDir)
		s.consumerValRes = append(s.consumerValRes, newRes)

		// 9. Wait for the new consumer to produce blocks past the continuing
		//    initial height.
		s.T().Log("waiting for restarted consumer to produce blocks...")
		waitCtx, waitCancel := context.WithTimeout(ctx, 2*time.Minute)
		defer waitCancel()
		s.Require().NoError(
			s.waitForChainHeight(waitCtx, "http://localhost:"+s.cfg.consumerRPCPort, initialHeight+1),
			"restarted consumer failed to produce blocks from the exported genesis")

		// 10. The restarted chain must boot with exactly the exported valset.
		postVals, err := s.queryConsumerNetValidators()
		s.Require().NoError(err, "failed to query consumer validators after restart")
		s.Require().Equal(exportedValSet, s.valPowerMap(postVals),
			"restarted consumer's validator set must match the exported one")

		// The provider must not have noticed anything fatal: the consumer is
		// still LAUNCHED (testLivenessRemoval relies on this next).
		phase := s.queryProviderConsumerPhase("0")
		s.Require().Equal("CONSUMER_PHASE_LAUNCHED", phase,
			"consumer must remain LAUNCHED across the restart")

		// 11. VSC flow must resume over the pre-existing IBC clients: change
		//     the provider's valset and require the restarted consumer to
		//     converge on it.
		providerValsBefore, err := s.queryProviderNetValidators()
		s.Require().NoError(err, "failed to query provider validators before the post-restart delegation")
		powerBefore := powerSum(s.valPowerMap(providerValsBefore))

		s.T().Log("delegating on the provider to force a post-restart valset change...")
		stdout, _, err := s.dockerExec(s.providerValRes[0].Container.ID, []string{
			providerBinary, "keys", "show", "val", "--bech", "val", "-a",
			"--home", providerHomePath,
			"--keyring-backend", "test",
		})
		s.Require().NoError(err, "failed to get validator operator address")
		valoperAddr := strings.TrimSpace(stdout.String())

		stdout, stderr, err := s.dockerExec(s.providerValRes[0].Container.ID, []string{
			// At least one unit of voting power (tokens / powerReduction,
			// default 1e6) so the validator's integer power actually changes.
			providerBinary, "tx", "staking", "delegate", valoperAddr, "100000000" + bondDenom,
			"--from", "user",
			"--home", providerHomePath,
			"--keyring-backend", "test",
			"--chain-id", providerChainID,
			"--fees", "10000" + bondDenom,
			"-y",
			"-o", "json",
		})
		s.Require().NoErrorf(err, "failed to delegate on provider after consumer restart: stderr=%s", stderr.String())
		s.requireTxCommitted(stdout.Bytes())

		s.Require().Eventuallyf(func() bool {
			vals, err := s.queryProviderNetValidators()
			if err != nil {
				return false
			}
			return powerSum(s.valPowerMap(vals)) > powerBefore
		}, 30*time.Second, 2*time.Second,
			"provider VP did not increase after the post-restart delegation")

		s.T().Log("waiting for restarted consumer to converge on the provider's new valset...")
		// Compare by consensus address, translating each provider validator
		// through the provider's key-assignment mapping: a validator that has
		// assigned a consumer key appears in the consumer set under the
		// assigned address, so a raw pubkey-map comparison cannot match once
		// any assignment exists. The assignments are settled before this test
		// runs, so the pairs are read once up front.
		assignedPairs := s.queryAllPairsValConsAddr("0")
		s.Require().Eventuallyf(func() bool {
			consumerVals, err := s.queryConsumerNetValidators()
			if err != nil {
				return false
			}
			providerVals, err := s.queryProviderNetValidators()
			if err != nil {
				return false
			}
			consumerSet := s.valAddrPowerMap(consumerVals)
			expectedSet := make(map[string]int64, len(providerVals))
			for _, v := range providerVals {
				addr := v.Address
				if assigned, ok := assignedPairs[addr]; ok {
					addr = assigned
				}
				expectedSet[addr] = v.VotingPower
			}
			s.T().Logf("consumer valset: %v (want %v)", consumerSet, expectedSet)
			return len(consumerSet) > 0 && maps.Equal(consumerSet, expectedSet)
		}, 3*time.Minute, 3*time.Second,
			"restarted consumer never converged on the provider's post-delegation valset")

		// 12. The convergence above can only have come from applied VSC
		//     packets; with the watermark restored, every one of them must
		//     carry an id strictly greater than the pre-restart watermark
		//     (an id at or below it would be a replayed stale packet).
		postIDs := appliedVscIDs(s.consumerLogs())
		s.Require().NotEmpty(postIDs, "restarted consumer log contains no applied VSC packets")
		for _, id := range postIDs {
			s.Require().Greaterf(id, watermark,
				"restarted consumer applied VSC %d at or below the restored dedup watermark %d", id, watermark)
		}
		s.T().Logf("post-restart applied vscIDs %v all above restored watermark %d", postIDs, watermark)
	})
}

// vscAppliedLogRegexp matches the log line OnRecvVSCPacketV2 emits when it
// actually applies a packet (skipped out-of-order packets log a different
// line), capturing the packet's valset-update id.
var vscAppliedLogRegexp = regexp.MustCompile(`finished receiving/handling VSCPacket.*?vscID=(\d+)`)

// appliedVscIDs extracts, in log order, the id of every VSC packet the
// consumer applied according to its container log. Ids repeat and interleave
// (the handler runs -- and logs -- once per execution mode of the same tx,
// and a batched tx logs each of its packets per mode), but modes run in
// chronological order and packets within a tx in increasing-id order, so once
// recv traffic is drained the last entry is the committed watermark. The
// chain logger colorizes field keys, so ANSI escapes are stripped before
// matching.
func appliedVscIDs(logs string) []uint64 {
	logs = ansiEscapeRegex.ReplaceAllString(logs, "")
	var ids []uint64
	for _, m := range vscAppliedLogRegexp.FindAllStringSubmatch(logs, -1) {
		id, err := strconv.ParseUint(m[1], 10, 64)
		if err != nil {
			continue
		}
		ids = append(ids, id)
	}
	return ids
}

// valPowerMap flattens a consensus valset query result into a
// pubkey(base64) -> voting power map, the address-independent form the
// round-trip assertions compare.
func (s *IntegrationTestSuite) valPowerMap(vals []*cmtservice.Validator) map[string]uint64 {
	keys, powers := s.extractPubKeys(vals)
	m := make(map[string]uint64, len(keys))
	for i, k := range keys {
		m[k] = powers[i]
	}
	return m
}

// valAddrPowerMap maps each validator's bech32 consensus address to its voting
// power. Address-keyed (rather than pubkey-keyed like valPowerMap) so a
// provider-side set can be compared against a consumer-side one after
// rewriting assigned consumer keys through the provider's key-assignment
// pairs.
func (s *IntegrationTestSuite) valAddrPowerMap(vals []*cmtservice.Validator) map[string]int64 {
	m := make(map[string]int64, len(vals))
	for _, v := range vals {
		m[v.Address] = v.VotingPower
	}
	return m
}

// queryAllPairsValConsAddr returns the provider-to-consumer consensus address
// pairs the provider tracks for a consumer, keyed by provider address. A
// validator with no assigned consumer key has no pair.
func (s *IntegrationTestSuite) queryAllPairsValConsAddr(consumerID string) map[string]string {
	stdout, _, err := s.dockerExec(s.providerValRes[0].Container.ID, []string{
		providerBinary, "query", "provider", "all-pairs-valconsensus-address", consumerID,
		"--home", providerHomePath,
		"--output", "json",
	})
	s.Require().NoError(err, "failed to query address pairs for consumer %s", consumerID)

	var res struct {
		PairValConAddr []struct {
			ProviderAddress string `json:"provider_address"`
			ConsumerAddress string `json:"consumer_address"`
		} `json:"pair_val_con_addr"`
	}
	s.Require().NoErrorf(json.Unmarshal(stdout.Bytes(), &res),
		"failed to decode address pairs response: %s", stdout.String())

	pairs := make(map[string]string, len(res.PairValConAddr))
	for _, p := range res.PairValConAddr {
		pairs[p.ProviderAddress] = p.ConsumerAddress
	}
	return pairs
}

// powerSum returns the total voting power of a pubkey -> power map.
func powerSum(set map[string]uint64) uint64 {
	var sum uint64
	for _, p := range set {
		sum += p
	}
	return sum
}

// exportConsumerGenesis launches an ephemeral container that mounts the
// consumer's data directory and runs `consumer export` (without
// --for-zero-height: the restarted chain continues at the next height).
// Returns the exported genesis JSON.
func (s *IntegrationTestSuite) exportConsumerGenesis(ctx context.Context, dataDir string) []byte {
	exportRes, err := s.dkrPool.RunWithOptions(
		&dockertest.RunOptions{
			Name:       fmt.Sprintf("%s-export", consumerChainID),
			Repository: e2eChainImage,
			NetworkID:  s.dkrNet.Network.ID,
			User:       "nonroot",
			Mounts: []string{
				fmt.Sprintf("%s:%s", dataDir, consumerHomePath),
			},
			Cmd: []string{
				consumerBinary, "export",
				"--home", consumerHomePath,
			},
		},
		func(config *docker.HostConfig) {
			config.RestartPolicy = docker.RestartPolicy{Name: "no"}
		},
	)
	s.Require().NoError(err, "failed to start consumer export container")
	defer func() {
		if perr := s.dkrPool.Purge(exportRes); perr != nil {
			s.T().Logf("failed to purge consumer export container: %v", perr)
		}
	}()

	exitCode, err := s.dkrPool.Client.WaitContainerWithContext(exportRes.Container.ID, ctx)
	s.Require().NoError(err, "consumer export container wait failed")

	var stdoutBuf, stderrBuf bytes.Buffer
	err = s.dkrPool.Client.Logs(docker.LogsOptions{
		Container:    exportRes.Container.ID,
		OutputStream: &stdoutBuf,
		ErrorStream:  &stderrBuf,
		Stdout:       true,
		Stderr:       true,
	})
	s.Require().NoError(err, "failed to read consumer export container logs")
	s.Require().Equalf(0, exitCode,
		"consumer export container failed (exit=%d)\nstdout:\n%s\nstderr:\n%s",
		exitCode, stdoutBuf.String(), stderrBuf.String())

	// Some SDK versions log "exported genesis ..." lines before the JSON
	// body on stdout. Strip anything before the first '{'.
	raw := stdoutBuf.Bytes()
	if i := bytes.IndexByte(raw, '{'); i > 0 {
		raw = raw[i:]
	}
	return raw
}

// verifyExportedConsumerGenesis asserts that the exported JSON is a restart
// genesis for the same chain id whose consensus validator set is non-empty
// with a usable pubkey on every entry, whose vaasconsumer module state agrees
// with that set, and whose dedup watermark equals the one the stopped
// consumer last logged. Returns the exported pubkey -> power map and the
// continuing initial height.
func (s *IntegrationTestSuite) verifyExportedConsumerGenesis(exportedJSON []byte, wantWatermark uint64) (map[string]uint64, int64) {
	var exported struct {
		ChainID       string `json:"chain_id"`
		InitialHeight int64  `json:"initial_height"`
		Consensus     struct {
			Validators []struct {
				Address string `json:"address"`
				PubKey  *struct {
					Type  string `json:"type"`
					Value string `json:"value"`
				} `json:"pub_key"`
				Power string `json:"power"`
			} `json:"validators"`
		} `json:"consensus"`
		AppState struct {
			VaasConsumer struct {
				NewChain              bool   `json:"new_chain"`
				ProviderClientID      string `json:"provider_client_id"`
				HighestValsetUpdateID uint64 `json:"highest_valset_update_id,string"`
				Provider              struct {
					InitialValSet []struct {
						PubKey map[string]string `json:"pub_key"`
						Power  string            `json:"power"`
					} `json:"initial_val_set"`
				} `json:"provider"`
			} `json:"vaasconsumer"`
		} `json:"app_state"`
	}
	s.Require().NoError(json.Unmarshal(exportedJSON, &exported), "exported consumer genesis is not valid JSON")

	s.Require().Equal(consumerChainID, exported.ChainID, "exported genesis must keep the consumer chain id")

	// The consensus block is what CometBFT boots from; a null pub_key here
	// panics GenesisDoc.ValidateAndComplete on load.
	s.Require().NotEmpty(exported.Consensus.Validators, "exported genesis has no consensus validators")
	consensusSet := make(map[string]uint64, len(exported.Consensus.Validators))
	for _, v := range exported.Consensus.Validators {
		s.Require().NotNilf(v.PubKey, "exported consensus validator %s has a null pub_key", v.Address)
		s.Require().NotEmptyf(v.PubKey.Value, "exported consensus validator %s has an empty pub_key value", v.Address)
		power, err := strconv.ParseUint(v.Power, 10, 64)
		s.Require().NoErrorf(err, "exported consensus validator %s has unparseable power %q", v.Address, v.Power)
		s.Require().Positivef(power, "exported consensus validator %s has zero power", v.Address)
		consensusSet[v.PubKey.Value] = power
	}

	// The module state must be a restart genesis whose initial valset agrees
	// with the consensus block entry for entry.
	vc := exported.AppState.VaasConsumer
	s.Require().False(vc.NewChain, "exported genesis must be a restart genesis (new_chain=false)")
	s.Require().NotEmpty(vc.ProviderClientID, "exported genesis must carry the provider client id")

	moduleSet := make(map[string]uint64, len(vc.Provider.InitialValSet))
	for i, v := range vc.Provider.InitialValSet {
		key, ok := v.PubKey["ed25519"]
		s.Require().Truef(ok && key != "", "exported initial_val_set entry %d has no ed25519 pub_key: %v", i, v.PubKey)
		power, err := strconv.ParseUint(v.Power, 10, 64)
		s.Require().NoErrorf(err, "exported initial_val_set entry %d has unparseable power %q", i, v.Power)
		moduleSet[key] = power
	}
	s.Require().Equal(consensusSet, moduleSet,
		"vaasconsumer initial_val_set must agree with the exported consensus validators")

	// The dedup watermark must round-trip: it is what keeps a replayed stale
	// VSC from being applied over a newer set after the restart.
	s.Require().Equal(wantWatermark, vc.HighestValsetUpdateID,
		"exported highest_valset_update_id must equal the watermark the stopped consumer last applied")

	return consensusSet, exported.InitialHeight
}

// bootstrapRestartedConsumerDir builds a new consumer data directory that
// reuses the old chain's validator and node keys + config files, but uses
// the exported JSON as its genesis.json and starts with an empty data/.
func (s *IntegrationTestSuite) bootstrapRestartedConsumerDir(oldDir string, exportedJSON []byte) string {
	newDir, err := os.MkdirTemp("", "vaas-e2e-consumer-restart-")
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
		s.Require().NoErrorf(err, "read %s from old consumer data dir", name)
		s.Require().NoErrorf(os.WriteFile(filepath.Join(newDir, name), data, 0o644),
			"write %s to new consumer data dir", name)
	}

	// Write the exported JSON as the new chain's genesis.
	s.Require().NoError(os.WriteFile(
		filepath.Join(newDir, "config", "genesis.json"),
		exportedJSON,
		0o644,
	), "failed to write exported consumer genesis")

	// Reset priv_validator_state.json: the restarted chain's heights start
	// past anything the old chain signed, so a zeroed state cannot double-sign.
	s.Require().NoError(os.WriteFile(
		filepath.Join(newDir, "data", "priv_validator_state.json"),
		[]byte(`{"height":"0","round":0,"step":0}`),
		0o644,
	), "failed to write priv_validator_state.json")

	s.Require().NoError(os.Chmod(filepath.Join(newDir, "config"), 0o777))
	s.Require().NoError(os.Chmod(filepath.Join(newDir, "data"), 0o777))
	return newDir
}

// startRestartedConsumer runs a new consumer container on the same name and
// host ports the original used (the relayer reaches the consumer by container
// name), mounting the new data dir.
func (s *IntegrationTestSuite) startRestartedConsumer(dataDir string) *dockertest.Resource {
	// Update so subsequent helpers find the new data dir.
	s.consumer.dataDir = dataDir

	resource, err := s.dkrPool.RunWithOptions(
		&dockertest.RunOptions{
			Name:       fmt.Sprintf("%s-val0", consumerChainID),
			Repository: e2eChainImage,
			NetworkID:  s.dkrNet.Network.ID,
			Mounts: []string{
				fmt.Sprintf("%s:%s", dataDir, consumerHomePath),
			},
			PortBindings: map[docker.Port][]docker.PortBinding{
				"26657/tcp": {{HostIP: "", HostPort: s.cfg.consumerRPCPort}},
				"9090/tcp":  {{HostIP: "", HostPort: s.cfg.consumerGRPCPort}},
				"1317/tcp":  {{HostIP: "", HostPort: s.cfg.consumerRESTPort}},
				"26656/tcp": {{HostIP: "", HostPort: s.cfg.consumerP2PPort}},
			},
			Cmd: []string{consumerBinary, "start", "--home", consumerHomePath},
		},
		func(config *docker.HostConfig) {
			config.RestartPolicy = docker.RestartPolicy{Name: "no"}
		},
	)
	s.Require().NoError(err, "failed to start restarted consumer container")
	return resource
}
