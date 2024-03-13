package agent

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

var yamlConfig = `management-endpoint: https://management.endpoint
enrollment-endpoint: https://management.endpoint
enrollment-ui-endpoint: https://ui.management.endpoint
spec-fetch-interval: 0m10s
status-update-interval: 0m10s`

func TestParseConfigFile(t *testing.T) {
	require := require.New(t)

	tmpDir := t.TempDir()
	filePath := tmpDir + "/config.yaml"
	err := os.WriteFile(filePath, []byte(yamlConfig), 0600)
	require.NoError(err)

	cfg := NewDefault()
	err = cfg.ParseConfigFile(filePath)
	require.NoError(err)

	// ensure override
	require.Equal("https://management.endpoint", cfg.ManagementEndpoint)
	require.Equal("https://management.endpoint", cfg.EnrollmentEndpoint)
	require.Equal("https://ui.management.endpoint", cfg.EnrollmentUIEndpoint)
	require.Equal("10s", cfg.SpecFetchInterval.String())
	require.Equal("10s", cfg.StatusUpdateInterval.String())

	// ensure defaults
	require.Equal("/etc/flightctl", cfg.ConfigDir)
}
