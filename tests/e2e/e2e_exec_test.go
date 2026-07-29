package e2e

import (
	"bytes"
	"context"
	"fmt"
	"time"

	"github.com/ory/dockertest/v3/docker"
)

// dockerExec runs a command in the specified Docker container and returns stdout/stderr.
func (s *baseTestSuite) dockerExec(containerID string, cmd []string) (bytes.Buffer, bytes.Buffer, error) {
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

	// StartExec reports how the exec itself went, not how the command inside it
	// exited, so a command that failed comes back with err == nil and an empty
	// stdout. Callers that unmarshal stdout then fail on the empty document
	// rather than on the reason -- a CLI route that no longer exists, a bad
	// flag, an unfunded key -- which is invisible unless the exit code and
	// stderr are surfaced. Log them and leave the error nil: several callers
	// deliberately run commands expected to fail and assert on stderr
	// themselves.
	if inspect, inspectErr := s.dkrPool.Client.InspectExec(exec.ID); inspectErr == nil && inspect.ExitCode != 0 {
		s.T().Logf("command exited %d: %v\nstdout: %s\nstderr: %s",
			inspect.ExitCode, cmd, stdout.String(), stderr.String())
	}

	return stdout, stderr, nil
}

// dockerExecMust runs a command in a Docker container, failing the test on error.
func (s *baseTestSuite) dockerExecMust(containerID string, cmd []string) {
	stdout, stderr, err := s.dockerExec(containerID, cmd)
	if err != nil {
		s.T().Logf("cmd: %v", cmd)
		s.T().Logf("stdout: %s", stdout.String())
		s.T().Logf("stderr: %s", stderr.String())
	}
	s.Require().NoError(err, "docker exec failed for cmd: %v", cmd)
}
