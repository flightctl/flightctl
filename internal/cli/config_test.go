package cli

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
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
			name:           "set valid uuid unchanged",
			arg:            uuid,
			initialOrg:     uuid,
			expectedOutput: "Current organization unchanged: " + uuid + "\n",
			expectedFinal:  uuid,
		},
		{
			name:           "already unset",
			arg:            "",
			initialOrg:     "",
			expectedOutput: "Current organization already unset\n",
			expectedFinal:  "",
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
			require.True(t, strings.HasPrefix(gotOutput, tc.expectedOutput), "Expected output to start with %q, got %q", tc.expectedOutput, gotOutput)

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

func TestValidateOrganizationExists(t *testing.T) {
	tests := []struct {
		name           string
		organizationID string
		expectedError  string
	}{
		{
			name:           "empty organization ID is valid",
			organizationID: "",
		},
		{
			name:           "default organization ID",
			organizationID: "00000000-0000-0000-0000-000000000000",
		},
		{
			name:           "invalid organization ID format",
			organizationID: "invalid-org-id",
			expectedError:  "invalid organization ID",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			opts := DefaultConfigOptions()
			opts.ConfigFilePath = filepath.Join(t.TempDir(), "test.yaml")

			// Create a minimal valid config
			writeTestConfig(t, opts.ConfigFilePath, "")

			// Test the full RunSetOrganization function which includes both validations
			err := opts.RunSetOrganization(context.Background(), []string{tc.organizationID})
			if tc.expectedError != "" {
				require.Error(t, err)
				require.Contains(t, err.Error(), tc.expectedError)
			} else {
				// For empty organization ID and default organization ID, it should succeed
				// For other valid organization IDs, it will fail due to API call in test environment
				require.NoError(t, err)
				cfg, cfgErr := client.ParseConfigFile(opts.ConfigFilePath)
				require.NoError(t, cfgErr)
				require.Equal(t, tc.organizationID, cfg.Organization)
			}
		})
	}
}
