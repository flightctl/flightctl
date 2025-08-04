package cli

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/flightctl/flightctl/internal/client"
	"github.com/stretchr/testify/require"
)

// writeTestConfig creates a minimal valid flightctl config at the given path
// and sets the provided current organization value.
func writeTestConfig(t *testing.T, path string, org string) {
	t.Helper()
	cfg := client.NewDefault()
	cfg.Service.Server = "https://api.example.com" // minimal valid value
	cfg.Organization = org
	require.NoError(t, cfg.Persist(path))
}

// captureOutput redirects stdout while fn is executed and returns what was written.
func captureOutput(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	require.NoError(t, err)

	old := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = old }()

	fn()

	require.NoError(t, w.Close())

	var buf bytes.Buffer
	_, err = io.Copy(&buf, r)
	require.NoError(t, err)
	return buf.String()
}

func TestRunCurrentOrganization(t *testing.T) {
	const uuid = "00000000-0000-0000-0000-000000000000"

	tests := []struct {
		name           string
		orgInConfig    string
		expectedOutput string
	}{
		{name: "org set", orgInConfig: uuid, expectedOutput: uuid + "\n"},
		{name: "org not set", orgInConfig: "", expectedOutput: "No current organization set\n"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tempDir := t.TempDir()
			configPath := filepath.Join(tempDir, "client.yaml")
			writeTestConfig(t, configPath, tc.orgInConfig)

			opts := DefaultConfigOptions()
			opts.ConfigFilePath = configPath

			got := captureOutput(t, func() {
				require.NoError(t, opts.RunCurrentOrganization(context.Background(), nil))
			})
			require.Equal(t, tc.expectedOutput, got)
		})
	}
}

func TestRunCurrentOrganization_NoConfigFile(t *testing.T) {
	opts := DefaultConfigOptions()
	// Point to a non-existing file inside temp dir
	opts.ConfigFilePath = filepath.Join(t.TempDir(), "missing.yaml")

	err := opts.RunCurrentOrganization(context.Background(), nil)
	require.Error(t, err)
}

func TestRunSetOrganization(t *testing.T) {
	const uuid = "00000000-0000-0000-0000-000000000000"

	tests := []struct {
		name           string
		arg            string
		initialOrg     string
		wantErr        bool
		expectedOutput string
		expectedFinal  string
	}{
		{
			name:           "unset",
			arg:            "",
			initialOrg:     uuid,
			expectedOutput: "Current organization unset\n",
			expectedFinal:  "",
		},
		{
			name:           "set valid uuid",
			arg:            uuid,
			initialOrg:     "",
			expectedOutput: "Current organization set to: " + uuid + "\n",
			expectedFinal:  uuid,
		},
		{
			name:           "set valid uuid",
			arg:            uuid,
			initialOrg:     uuid,
			expectedOutput: "Current organization unchanged\n",
			expectedFinal:  uuid,
		},
		{
			name:       "invalid org",
			arg:        "invalid",
			initialOrg: "",
			wantErr:    true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tempDir := t.TempDir()
			configPath := filepath.Join(tempDir, "client.yaml")
			writeTestConfig(t, configPath, tc.initialOrg)

			opts := DefaultConfigOptions()
			opts.ConfigFilePath = configPath

			var gotOutput string
			err := func() error {
				return func() error {
					var runErr error
					gotOutput = captureOutput(t, func() {
						runErr = opts.RunSetOrganization(context.Background(), []string{tc.arg})
					})
					return runErr
				}()
			}()

			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.expectedOutput, gotOutput)

			cfg, cfgErr := client.ParseConfigFile(configPath)
			require.NoError(t, cfgErr)
			require.Equal(t, tc.expectedFinal, cfg.Organization)
		})
	}
}

func TestRunSetOrganization_NoConfigFile(t *testing.T) {
	opts := DefaultConfigOptions()
	opts.ConfigFilePath = filepath.Join(t.TempDir(), "missing.yaml")

	err := opts.RunSetOrganization(context.Background(), []string{""})
	require.Error(t, err)
}
