package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// isolateDeploymentDetection prevents accidental Kubernetes detection from the
// developer's kubeconfig during CLI tests.
func isolateDeploymentDetection(t *testing.T) {
	t.Helper()
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "missing-kubeconfig"))
}

// backupCommandArgs returns CLI args that pin deployment type to Podman so tests
// do not depend on the host environment.
func backupCommandArgs(t *testing.T, extra ...string) []string {
	t.Helper()
	isolateDeploymentDetection(t)
	args := []string{"--deployment-type", "podman"}
	return append(args, extra...)
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

	outputFlag := cmd.Flags().Lookup("output")
	require.NotNil(t, outputFlag)
	require.Equal(t, ".", outputFlag.DefValue)

	// --config flag should no longer exist
	configFlag := cmd.Flags().Lookup("config")
	require.Nil(t, configFlag, "config flag should not exist after refactor")

	namespaceFlag := cmd.Flags().Lookup("namespace")
	require.NotNil(t, namespaceFlag)
	require.Equal(t, "", namespaceFlag.DefValue)

	internalNamespaceFlag := cmd.Flags().Lookup("internal-namespace")
	require.NotNil(t, internalNamespaceFlag)
	require.Equal(t, "", internalNamespaceFlag.DefValue)

	helmReleaseNameFlag := cmd.Flags().Lookup("helm-release-name")
	require.NotNil(t, helmReleaseNameFlag)
	require.Equal(t, "", helmReleaseNameFlag.DefValue)

	dbContainerNameFlag := cmd.Flags().Lookup("db-container-name")
	require.NotNil(t, dbContainerNameFlag)
	require.Equal(t, "", dbContainerNameFlag.DefValue)

	dbNameFlag := cmd.Flags().Lookup("db-name")
	require.NotNil(t, dbNameFlag)
	require.Equal(t, "", dbNameFlag.DefValue)

	kvContainerNameFlag := cmd.Flags().Lookup("kv-container-name")
	require.NotNil(t, kvContainerNameFlag)
	require.Equal(t, "", kvContainerNameFlag.DefValue)
}

func TestFlagParsing(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantOutput string
		wantErr    bool
	}{
		{
			name:       "custom output",
			args:       []string{"--output", "/tmp/backup"},
			wantOutput: "/tmp/backup",
		},
		{
			name:       "default output",
			args:       []string{},
			wantOutput: ".",
		},
		{
			name:    "short flag for output should not be supported",
			args:    []string{"-o", "/tmp/test"},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := NewFlightCtlBackupCommand()

			err := cmd.ParseFlags(tt.args)

			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)

			outputFlag, err := cmd.Flags().GetString("output")
			require.NoError(t, err)
			require.Equal(t, tt.wantOutput, outputFlag)
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
				return t.TempDir()
			},
			wantErr:     true,
			errContains: "backup failed",
		},
		{
			name: "non-existent directory",
			setupOutput: func(t *testing.T) string {
				return filepath.Join(t.TempDir(), "nonexistent")
			},
			wantErr:     true,
			errContains: "backup failed",
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
			errContains: "failed to create output directory",
		},
		{
			name: "current directory",
			setupOutput: func(t *testing.T) string {
				return "."
			},
			wantErr:     true,
			errContains: "backup failed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			outputPath := tt.setupOutput(t)

			args := backupCommandArgs(t, "--output", outputPath)
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

	require.Equal(t, "flightctl-backup [flags]", cmd.Use)

	require.NotEmpty(t, cmd.Short)
	require.NotEmpty(t, cmd.Long)

	outputFlag := cmd.Flags().Lookup("output")
	require.NotNil(t, outputFlag, "output flag should be registered")

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
	require.Contains(t, output, "--namespace")
	require.Contains(t, output, "--internal-namespace")
	require.Contains(t, output, "--helm-release-name")
	require.Contains(t, output, "--db-container-name")
	require.Contains(t, output, "--db-name")
	require.Contains(t, output, "--kv-container-name")
	require.Contains(t, output, "Directory path where backup files will be written")
	// --config flag should no longer appear in help
	require.NotContains(t, output, "--config")
}

func TestRunBackupPlaceholder(t *testing.T) {
	outputDir := t.TempDir()

	args := backupCommandArgs(t, "--output", outputDir)
	output, err := executeCommand(t, args...)

	// Full backup requires Podman/Kubernetes infrastructure; verify the command
	// reaches the backup workflow rather than failing during flag handling.
	require.Error(t, err)
	require.Contains(t, err.Error(), "backup failed")
	_ = output
}
