package agent

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

var yamlConfig = `management-endpoint: https://api.flightctl.edge-devices.net
enrollment-endpoint: https://api.flightctl.edge-devices.net
enrollment-ui-endpoint: https://ui.flightctl.edge-devices.net
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

	require.Equal("https://api.flightctl.edge-devices.net", cfg.ManagementEndpoint)
}
