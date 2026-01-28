package util

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/flightctl/flightctl/pkg/executer"
)

// SafeExecuter wraps a real executer and blocks dangerous system commands
// that should never be executed during integration tests (like systemctl reboot).
type SafeExecuter struct {
	wrapped executer.Executer
}

// NewSafeExecuter creates a new SafeExecuter that wraps the given executer
func NewSafeExecuter(wrapped executer.Executer) *SafeExecuter {
	return &SafeExecuter{wrapped: wrapped}
}

// NewDefaultSafeExecuter creates a SafeExecuter wrapping CommonExecuter
func NewDefaultSafeExecuter() *SafeExecuter {
	return &SafeExecuter{wrapped: executer.NewCommonExecuter()}
}

// isDangerousCommand checks if a command should be blocked in tests
func isDangerousCommand(command string, args ...string) bool {
	// Block systemctl reboot/poweroff/halt
	if command == "systemctl" || strings.HasSuffix(command, "/systemctl") {
		// Check if ANY arg contains a dangerous action (conservative approach for test safety)
		for _, arg := range args {
			switch arg {
			case "reboot", "poweroff", "halt", "kexec":
				return true
			}
		}
	}

	// Block direct shutdown commands
	switch command {
	case "reboot", "poweroff", "halt", "shutdown":
		return true
	case "/sbin/reboot", "/sbin/poweroff", "/sbin/halt", "/sbin/shutdown":
		return true
	case "/usr/sbin/reboot", "/usr/sbin/poweroff", "/usr/sbin/halt", "/usr/sbin/shutdown":
		return true
	}

	return false
}

func (s *SafeExecuter) CommandContext(ctx context.Context, command string, args ...string) *exec.Cmd {
	if isDangerousCommand(command, args...) {
		// Return a no-op command that will fail with a clear error
		cmd := exec.CommandContext(ctx, "echo", "BLOCKED: dangerous command prevented in test")
		return cmd
	}
	return s.wrapped.CommandContext(ctx, command, args...)
}

func (s *SafeExecuter) Execute(command string, args ...string) (stdout string, stderr string, exitCode int) {
	if isDangerousCommand(command, args...) {
		return "", fmt.Sprintf("SafeExecuter blocked dangerous command: %s", command), 0
	}
	return s.wrapped.Execute(command, args...)
}

func (s *SafeExecuter) ExecuteWithContext(ctx context.Context, command string, args ...string) (stdout string, stderr string, exitCode int) {
	if isDangerousCommand(command, args...) {
		return "", fmt.Sprintf("SafeExecuter blocked dangerous command: %s", command), 0
	}
	return s.wrapped.ExecuteWithContext(ctx, command, args...)
}

func (s *SafeExecuter) ExecuteWithContextFromDir(ctx context.Context, workingDir string, command string, args []string, env ...string) (stdout string, stderr string, exitCode int) {
	if isDangerousCommand(command, args...) {
		return "", fmt.Sprintf("SafeExecuter blocked dangerous command: %s", command), 0
	}
	return s.wrapped.ExecuteWithContextFromDir(ctx, workingDir, command, args, env...)
}
