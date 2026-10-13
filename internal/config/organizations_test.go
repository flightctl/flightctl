package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOrganizations_InitialLabelSyncMappingsFile(t *testing.T) {
	t.Run("When configuration is not supplied it should use the default path", func(t *testing.T) {
		cfg := NewDefault()

		require.NotNil(t, cfg.Organizations)
		require.Equal(t, DefaultInitialLabelSyncMappingsFile, cfg.Organizations.InitialLabelSyncMappingsFile)
	})

	t.Run("When a custom path is configured it should preserve that path", func(t *testing.T) {
		cfg, err := Load(writeTempConfig(t, "organizations:\n  initialLabelSyncMappingsFile: /tmp/operator-mappings.yaml\n"))
		require.NoError(t, err)
		require.NotNil(t, cfg.Organizations)
		require.Equal(t, "/tmp/operator-mappings.yaml", cfg.Organizations.InitialLabelSyncMappingsFile)
	})
}
