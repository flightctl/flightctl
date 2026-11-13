package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFirmwareUpdaterCatalogConfiguration(t *testing.T) {
	t.Run("When not configured it should leave the administrator catalog path empty", func(t *testing.T) { require.Empty(t, NewDefault().FirmwareUpdaterCatalogFile) })
	t.Run("When loaded it should preserve the administrator catalog path", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "config.yaml")
		require.NoError(t, os.WriteFile(path, []byte("firmwareUpdaterCatalogFile: /etc/flightctl/firmware/updaters.yaml\n"), 0o600))
		cfg, err := Load(path)
		require.NoError(t, err)
		require.Equal(t, "/etc/flightctl/firmware/updaters.yaml", cfg.FirmwareUpdaterCatalogFile)
		data, err := json.Marshal(cfg)
		require.NoError(t, err)
		require.Contains(t, string(data), `"firmwareUpdaterCatalogFile":"/etc/flightctl/firmware/updaters.yaml"`)
	})
}
