package agent

import (
	"os"
	"testing"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

var yamlConfig = `enrollment-service:
  service:
    server: https://enrollment.endpoint
    certificate-authority-data: abcd
  authentication:
    client-certificate-data: efgh
    client-key-data: ijkl
  enrollment-ui-endpoint: https://ui.enrollment.endpoint
management-service:
  service:
    server: https://management.endpoint
    certificate-authority-data: abcd
  authentication:
    client-certificate-data: efgh
    client-key-data: ijkl
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
	require.Equal("https://enrollment.endpoint", cfg.EnrollmentService.Service.Server)
	require.Equal("https://ui.enrollment.endpoint", cfg.EnrollmentService.EnrollmentUIEndpoint)
	require.Equal("https://management.endpoint", cfg.ManagementService.Service.Server)
	require.Equal("10s", cfg.SpecFetchInterval.String())
	require.Equal("10s", cfg.StatusUpdateInterval.String())

	// ensure defaults
	require.Equal(DefaultConfigDir, cfg.ConfigDir)
	require.Equal(DefaultDataDir, cfg.DataDir)
	require.Equal(logrus.InfoLevel.String(), cfg.LogLevel)
}

func TestParseConfigFile_NoFile(t *testing.T) {
	require := require.New(t)

	tmpDir := t.TempDir()
	filePath := tmpDir + "/nonexistent.yaml"

	cfg := NewDefault()
	err := cfg.ParseConfigFile(filePath)

	// Expect an error because the file does not exist
	require.Error(err)
}
