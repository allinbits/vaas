package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/ory/dockertest/v3"
	"github.com/ory/dockertest/v3/docker"
)

const (
	tsRelayerImage    = "ghcr.io/allinbits/ibc-v2-ts-relayer"
	tsRelayerImageTag = "latest"
	IBCv2             = "2"
)

type tsRelayerPath struct {
	ID       int    `json:"id"`
	Version  int    `json:"version"`
	ChainIdA string `json:"chainIdA"`
	ChainIdB string `json:"chainIdB"`
	ClientA  string `json:"clientA"`
	ClientB  string `json:"clientB"`
	NodeA    string `json:"nodeA"`
	NodeB    string `json:"nodeB"`
}

func noRestart(config *docker.HostConfig) {
	config.RestartPolicy = docker.RestartPolicy{Name: "no"}
}

func (s *IntegrationTestSuite) startTSRelayer() {
	s.T().Log("starting ts-relayer container")

	resource, err := s.dkrPool.RunWithOptions(
		&dockertest.RunOptions{
			Name:       fmt.Sprintf("%s-%s-ts-relayer", providerChainID, consumerChainID),
			Repository: tsRelayerImage,
			Tag:        tsRelayerImageTag,
			NetworkID:  s.dkrNet.Network.ID,
			User:       "root",
			CapAdd:     []string{"IPC_LOCK"},
		},
		noRestart,
	)
	s.Require().NoError(err, "failed to start ts-relayer container")
	s.tsRelayerResource = resource
	s.T().Logf("ts-relayer container started: %s", resource.Container.ID[:12])
}

func (s *IntegrationTestSuite) stopTSRelayer() {
	if s.tsRelayerResource != nil {
		s.T().Log("tearing down ts-relayer...")
		s.Require().NoError(s.dkrPool.Purge(s.tsRelayerResource))
		s.tsRelayerResource = nil
	}
}

func (s *IntegrationTestSuite) executeTSRelayerCommand(ctx context.Context, args []string) []byte {
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
	s.Require().NoError(err, "ts-relayer startExec error: %s", out.String())

	for {
		inspectExec, err := s.dkrPool.Client.InspectExec(exec.ID)
		s.Require().NoError(err, "ts-relayer inspectExec error: %s", out.String())
		if !inspectExec.Running {
			s.Require().Equal(0, inspectExec.ExitCode, "ts-relayer cmd '%s' failed (exit=%d): %s", strings.Join(cmd, " "), inspectExec.ExitCode, out.String())
			break
		}
	}
	return out.Bytes()
}

func (s *IntegrationTestSuite) tsRelayerAddMnemonic(chainID, mnemonic string) {
	s.T().Logf("ts-relayer: adding mnemonic for chain %s", chainID)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	s.executeTSRelayerCommand(ctx, []string{
		"add-mnemonic",
		"-c", chainID,
		"--mnemonic", mnemonic,
	})
}

func (s *IntegrationTestSuite) tsRelayerAddGasPrice(chainID, gasPrice string) {
	s.T().Logf("ts-relayer: adding gas-price for chain %s", chainID)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	s.executeTSRelayerCommand(ctx, []string{
		"add-gas-price",
		"-c", chainID,
		gasPrice,
	})
}

func (s *IntegrationTestSuite) tsRelayerAddPath(ibcVersion string) {
	s.T().Logf("ts-relayer: adding IBCv%s path between %s and %s", ibcVersion, providerChainID, consumerChainID)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	s.executeTSRelayerCommand(ctx, []string{
		"add-path",
		"-s", providerChainID,
		"-d", consumerChainID,
		"--surl", "http://" + s.providerValRes[0].Container.Name[1:] + ":26657",
		"--durl", "http://" + s.consumerValRes[0].Container.Name[1:] + ":26657",
		"--ibc-version", ibcVersion,
	})
}

func (s *IntegrationTestSuite) tsRelayerDumpPaths() []tsRelayerPath {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	out := s.executeTSRelayerCommand(ctx, []string{"dump-paths"})
	var paths []tsRelayerPath
	s.Require().NoError(json.Unmarshal(out, &paths), "parsing ts-relayer dump-paths: %s", out)
	return paths
}
