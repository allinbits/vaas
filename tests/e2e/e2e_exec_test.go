package e2e

import (
	"bytes"
	"context"
	"fmt"

	"github.com/ory/dockertest/v3/docker"
)

// dockerExec runs a command in the specified Docker container and returns stdout/stderr.
func (s *IntegrationTestSuite) dockerExec(ctx context.Context, containerID string, cmd []string) (bytes.Buffer, bytes.Buffer, error) {
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

// executeHermesCommand runs a command in the Hermes relayer container.
func (s *IntegrationTestSuite) executeHermesCommand(ctx context.Context, cmd []string) (bytes.Buffer, bytes.Buffer, error) {
	s.Require().NotNil(s.hermesResource, "hermes container not started")
	return s.dockerExec(ctx, s.hermesResource.Container.ID, cmd)
}
