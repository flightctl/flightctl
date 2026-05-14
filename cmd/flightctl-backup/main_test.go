package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/stretchr/testify/require"
)

// createTestConfig creates a minimal valid config file for testing
func createTestConfig(t *testing.T, path string) {
	t.Helper()

	// Ensure directory exists
	dir := filepath.Dir(path)
	err := os.MkdirAll(dir, 0755)
	require.NoError(t, err)

	// Use config.NewDefault() and Save() to create a valid config file
	cfg := config.NewDefault()
	err = config.Save(cfg, path)
	require.NoError(t, err)
}

// executeCommand executes a Cobra command with args and captures output
func executeCommand(t *testing.T, args ...string) (string, error) {
	t.Helper()

	var buf bytes.Buffer
	cmd := NewFlightCtlBackupCommand()
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs(args)

	err := cmd.Execute()
	return buf.String(), err
}

func TestFlagDefaults(t *testing.T) {
	cmd := NewFlightCtlBackupCommand()

	// Get flag defaults
	outputFlag := cmd.Flags().Lookup("output")
	require.NotNil(t, outputFlag)
	require.Equal(t, ".", outputFlag.DefValue)

	configFlag := cmd.Flags().Lookup("config")
	require.NotNil(t, configFlag)
	require.Equal(t, config.ConfigFile(), configFlag.DefValue)
}

func TestFlagParsing(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantOutput string
		wantConfig string
	}{
		{
			name:       "custom output and config",
			args:       []string{"--output", "/tmp/backup", "--config", "/etc/flightctl.yaml"},
			wantOutput: "/tmp/backup",
			wantConfig: "/etc/flightctl.yaml",
		},
		{
			name:       "only custom output",
			args:       []string{"--output", "/var/backups"},
			wantOutput: "/var/backups",
			wantConfig: config.ConfigFile(),
		},
		{
			name:       "only custom config",
			args:       []string{"--config", "/custom/config.yaml"},
			wantOutput: ".",
			wantConfig: "/custom/config.yaml",
		},
		{
			name:       "short flag for output",
			args:       []string{"-o", "/tmp/test"},
			wantOutput: "",
			wantConfig: config.ConfigFile(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := NewFlightCtlBackupCommand()

			// Parse flags without executing
			err := cmd.ParseFlags(tt.args)

			if tt.wantOutput == "" {
				// Short flag -o should not be supported
				require.Error(t, err)
			} else {
				require.NoError(t, err)

				outputFlag, err := cmd.Flags().GetString("output")
				require.NoError(t, err)
				require.Equal(t, tt.wantOutput, outputFlag)

				configFlag, err := cmd.Flags().GetString("config")
				require.NoError(t, err)
				require.Equal(t, tt.wantConfig, configFlag)
			}
		})
	}
}

func TestOutputPathValidation(t *testing.T) {
	tests := []struct {
		name        string
		setupOutput func(t *testing.T) string
		wantErr     bool
		errContains string
	}{
		{
			name: "valid directory",
			setupOutput: func(t *testing.T) string {
				t.Setenv("KUBERNETES_SERVICE_HOST", "kubernetes.default.svc")
				return t.TempDir()
			},
			wantErr: false,
		},
		{
			name: "non-existent directory",
			setupOutput: func(t *testing.T) string {
				return filepath.Join(t.TempDir(), "nonexistent")
			},
			wantErr:     true,
			errContains: "output directory does not exist",
		},
		{
			name: "file instead of directory",
			setupOutput: func(t *testing.T) string {
				dir := t.TempDir()
				file := filepath.Join(dir, "file.txt")
				require.NoError(t, os.WriteFile(file, []byte("test"), 0600))
				return file
			},
			wantErr:     true,
			errContains: "output path is not a directory",
		},
		{
			name: "current directory",
			setupOutput: func(t *testing.T) string {
				t.Setenv("KUBERNETES_SERVICE_HOST", "kubernetes.default.svc")
				return "."
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			outputPath := tt.setupOutput(t)

			// Create temp config file
			configDir := t.TempDir()
			configPath := filepath.Join(configDir, "config.yaml")
			createTestConfig(t, configPath)

			// Execute command
			args := []string{"--output", outputPath, "--config", configPath}
			_, err := executeCommand(t, args...)

			if tt.wantErr {
				require.Error(t, err)
				require.Contains(t, err.Error(), tt.errContains)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestConfigPathValidation(t *testing.T) {
	tests := []struct {
		name        string
		setupConfig func(t *testing.T) string
		wantErr     bool
		errContains string
	}{
		{
			name: "valid config file",
			setupConfig: func(t *testing.T) string {
				t.Setenv("KUBERNETES_SERVICE_HOST", "kubernetes.default.svc")
				dir := t.TempDir()
				configPath := filepath.Join(dir, "config.yaml")
				createTestConfig(t, configPath)
				return configPath
			},
			wantErr: false,
		},
		{
			name: "non-existent config file - LoadOrGenerate should create it",
			setupConfig: func(t *testing.T) string {
				t.Setenv("KUBERNETES_SERVICE_HOST", "kubernetes.default.svc")
				dir := t.TempDir()
				return filepath.Join(dir, "new-config.yaml")
			},
			wantErr: false, // LoadOrGenerate creates the file
		},
		{
			name: "invalid config file - malformed YAML",
			setupConfig: func(t *testing.T) string {
				dir := t.TempDir()
				configPath := filepath.Join(dir, "bad-config.yaml")
				// Write invalid YAML
				err := os.WriteFile(configPath, []byte("invalid: yaml: content: [unclosed"), 0600)
				require.NoError(t, err)
				return configPath
			},
			wantErr:     true,
			errContains: "reading configuration",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			configPath := tt.setupConfig(t)

			// Use current directory as output (always valid)
			outputPath := "."

			// Execute command
			args := []string{"--output", outputPath, "--config", configPath}
			_, err := executeCommand(t, args...)

			if tt.wantErr {
				require.Error(t, err)
				require.Contains(t, err.Error(), tt.errContains)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestVersionCommand(t *testing.T) {
	output, err := executeCommand(t, "version")

	require.NoError(t, err)
	require.Contains(t, output, "Flight Control Backup Version:")
}

func TestCommandStructure(t *testing.T) {
	cmd := NewFlightCtlBackupCommand()

	// Verify command use string
	require.Equal(t, "flightctl-backup [flags]", cmd.Use)

	// Verify short/long descriptions are non-empty
	require.NotEmpty(t, cmd.Short)
	require.NotEmpty(t, cmd.Long)

	// Verify flags are registered
	outputFlag := cmd.Flags().Lookup("output")
	require.NotNil(t, outputFlag, "output flag should be registered")

	configFlag := cmd.Flags().Lookup("config")
	require.NotNil(t, configFlag, "config flag should be registered")

	// Verify version subcommand exists
	versionCmd, _, err := cmd.Find([]string{"version"})
	require.NoError(t, err)
	require.NotNil(t, versionCmd)
	require.Equal(t, "version", versionCmd.Use)
}

func TestHelpOutput(t *testing.T) {
	output, err := executeCommand(t, "--help")

	require.NoError(t, err)
	require.Contains(t, output, "flightctl-backup [flags]")
	require.Contains(t, output, "--output")
	require.Contains(t, output, "--config")
	require.Contains(t, output, "Directory path where backup files will be written")
	require.Contains(t, output, "Path to the service configuration file")
}

func TestRunBackupPlaceholder(t *testing.T) {
	// Set up Kubernetes environment for deployment detection
	t.Setenv("KUBERNETES_SERVICE_HOST", "kubernetes.default.svc")

	// Create temp directory for output
	outputDir := t.TempDir()

	// Create temp config file
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	createTestConfig(t, configPath)

	// Execute command
	args := []string{"--output", outputDir, "--config", configPath}
	output, err := executeCommand(t, args...)

	// Command should succeed (placeholder implementation)
	require.NoError(t, err)

	// Verify logs contain placeholder messages (output is captured)
	// Note: log output goes to configured logger, not to cmd.SetOut
	// This test primarily verifies the command executes without error
	_ = output
}
