package util

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSafeExecuter_BlocksDangerousCommands(t *testing.T) {
	safeExec := NewDefaultSafeExecuter()
	ctx := context.Background()

	tests := []struct {
		name    string
		command string
		args    []string
	}{
		{
			name:    "systemctl reboot",
			command: "systemctl",
			args:    []string{"reboot"},
		},
		{
			name:    "systemctl poweroff",
			command: "systemctl",
			args:    []string{"poweroff"},
		},
		{
			name:    "systemctl halt",
			command: "systemctl",
			args:    []string{"halt"},
		},
		{
			name:    "systemctl --force reboot",
			command: "systemctl",
			args:    []string{"--force", "reboot"},
		},
		{
			name:    "systemctl -q poweroff",
			command: "systemctl",
			args:    []string{"-q", "poweroff"},
		},
		{
			name:    "systemctl --no-block halt",
			command: "systemctl",
			args:    []string{"--no-block", "halt"},
		},
		{
			name:    "systemctl with reboot after other args",
			command: "systemctl",
			args:    []string{"--message", "test", "reboot"},
		},
		{
			name:    "systemctl reboot with trailing flags",
			command: "systemctl",
			args:    []string{"reboot", "--force"},
		},
		{
			name:    "direct reboot",
			command: "reboot",
			args:    []string{},
		},
		{
			name:    "sbin shutdown",
			command: "/sbin/shutdown",
			args:    []string{"-h", "now"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stdout, stderr, exitCode := safeExec.ExecuteWithContext(ctx, tt.command, tt.args...)

			// Should block the command with exit code 0 (success blocking)
			assert.Equal(t, 0, exitCode, "Exit code should be 0 when blocking")
			assert.Empty(t, stdout, "Stdout should be empty")
			assert.Contains(t, stderr, "SafeExecuter blocked", "Stderr should indicate command was blocked")
			assert.Contains(t, stderr, tt.command, "Stderr should mention the blocked command")
		})
	}
}

func TestSafeExecuter_AllowsSafeCommands(t *testing.T) {
	safeExec := NewDefaultSafeExecuter()
	ctx := context.Background()

	// Test a safe command that should be allowed
	stdout, stderr, exitCode := safeExec.ExecuteWithContext(ctx, "echo", "hello")

	assert.Equal(t, 0, exitCode, "Exit code should be 0 for successful command")
	assert.Contains(t, stdout, "hello", "Stdout should contain the echo output")
	assert.Empty(t, stderr, "Stderr should be empty for successful command")
}

func TestSafeExecuter_AllowsSystemctlNonDangerousCommands(t *testing.T) {
	safeExec := NewDefaultSafeExecuter()
	ctx := context.Background()

	// systemctl commands that are safe should be allowed
	safeCommands := [][]string{
		{"systemctl", "status", "some-service"},
		{"systemctl", "start", "some-service"},
		{"systemctl", "stop", "some-service"},
		{"systemctl", "restart", "some-service"},
	}

	for _, args := range safeCommands {
		// We don't care if these fail (service doesn't exist), we just want to
		// verify they're not blocked by SafeExecuter
		_, stderr, _ := safeExec.ExecuteWithContext(ctx, args[0], args[1:]...)

		assert.NotContains(t, stderr, "SafeExecuter blocked",
			"Safe systemctl commands should not be blocked: %v", args)
	}
}
