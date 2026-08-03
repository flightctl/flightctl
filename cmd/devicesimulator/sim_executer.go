package main

import (
	"context"
	"os/exec"
	"strings"

	"github.com/flightctl/flightctl/pkg/executer"
)

// simulatorExecuter stubs host tooling that simulated agents must not invoke for real
// (notably podman), while delegating everything else to the normal executer.
type simulatorExecuter struct {
	wrapped executer.Executer
}

func newSimulatorExecuter() *simulatorExecuter {
	return &simulatorExecuter{wrapped: executer.NewCommonExecuter()}
}

func isPodmanCommand(command string) bool {
	return command == "podman" || strings.HasSuffix(command, "/podman")
}

func stubPodman(args []string) (stdout string, stderr string, exitCode int) {
	if len(args) > 0 && args[0] == "--version" {
		return "podman version 4.9.0\n", "", 0
	}
	return "", "", 0
}

func (s *simulatorExecuter) CommandContext(ctx context.Context, command string, args ...string) *exec.Cmd {
	if isPodmanCommand(command) {
		if len(args) > 0 && args[0] == "--version" {
			return exec.CommandContext(ctx, "echo", "podman version 4.9.0")
		}
		return exec.CommandContext(ctx, "true")
	}
	return s.wrapped.CommandContext(ctx, command, args...)
}

func (s *simulatorExecuter) Execute(command string, args ...string) (stdout string, stderr string, exitCode int) {
	if isPodmanCommand(command) {
		return stubPodman(args)
	}
	return s.wrapped.Execute(command, args...)
}

func (s *simulatorExecuter) ExecuteWithContext(ctx context.Context, command string, args ...string) (stdout string, stderr string, exitCode int) {
	if isPodmanCommand(command) {
		return stubPodman(args)
	}
	return s.wrapped.ExecuteWithContext(ctx, command, args...)
}

func (s *simulatorExecuter) ExecuteWithContextFromDir(ctx context.Context, workingDir string, command string, args []string, env ...string) (stdout string, stderr string, exitCode int) {
	if isPodmanCommand(command) {
		return stubPodman(args)
	}
	return s.wrapped.ExecuteWithContextFromDir(ctx, workingDir, command, args, env...)
}
