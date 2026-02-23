package e2e

import (
	"bytes"
	"context"
	"fmt"
	"time"

	"github.com/ory/dockertest/v3/docker"
)

// dockerExec runs a command in the specified Docker container and returns stdout/stderr.
func (s *IntegrationTestSuite) dockerExec(containerID string, cmd []string) (bytes.Buffer, bytes.Buffer, error) {
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

// executeHermesCommand runs a command in the Hermes relayer container.
// Uses the container's default user instead of "nonroot".
func (s *IntegrationTestSuite) executeHermesCommand(cmd []string) (bytes.Buffer, bytes.Buffer, error) {
	s.Require().NotNil(s.hermesResource, "hermes container not started")

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	var stdout, stderr bytes.Buffer

	exec, err := s.dkrPool.Client.CreateExec(docker.CreateExecOptions{
		Context:      ctx,
		AttachStdout: true,
		AttachStderr: true,
		Container:    s.hermesResource.Container.ID,
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


// dockerExecMust runs a command in a Docker container, failing the test on error.
func (s *IntegrationTestSuite) dockerExecMust(containerID string, cmd []string) {
	stdout, stderr, err := s.dockerExec(containerID, cmd)
	if err != nil {
		s.T().Logf("cmd: %v", cmd)
		s.T().Logf("stdout: %s", stdout.String())
		s.T().Logf("stderr: %s", stderr.String())
	}
	s.Require().NoError(err, "docker exec failed for cmd: %v", cmd)
}
