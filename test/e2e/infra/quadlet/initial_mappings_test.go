package quadlet

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestInitialLabelSyncMappingsQuadletConsumers(t *testing.T) {
	repoRoot := findRepoRoot(t)
	consumers := []struct {
		name       string
		unitPath   string
		configPath string
	}{
		{
			name:       "API",
			unitPath:   "deploy/podman/flightctl-api/flightctl-api.container",
			configPath: "deploy/podman/flightctl-api/flightctl-api-config/config.yaml.template",
		},
		{
			name:       "imagebuilder API",
			unitPath:   "deploy/podman/flightctl-imagebuilder-api/flightctl-imagebuilder-api.container",
			configPath: "deploy/podman/flightctl-imagebuilder-api/flightctl-imagebuilder-api-config/config.yaml.template",
		},
		{
			name:       "alertmanager proxy",
			unitPath:   "deploy/podman/flightctl-alertmanager-proxy/flightctl-alertmanager-proxy.container",
			configPath: "deploy/podman/flightctl-alertmanager-proxy/flightctl-alertmanager-proxy-config/config.yaml.template",
		},
		{
			name:       "remote access",
			unitPath:   "deploy/podman/flightctl-remote-access/flightctl-remote-access.container",
			configPath: "deploy/podman/flightctl-remote-access/flightctl-remote-access-config/config.yaml.template",
		},
		{
			name:       "database migration",
			unitPath:   "deploy/podman/flightctl-db-migrate/flightctl-db-migrate.container",
			configPath: "deploy/podman/flightctl-db-migrate/flightctl-db-migrate-config/config.yaml.template",
		},
	}

	for _, consumer := range consumers {
		t.Run(consumer.name, func(t *testing.T) {
			unit, err := os.ReadFile(filepath.Join(repoRoot, consumer.unitPath))
			require.NoError(t, err)
			require.Contains(t, string(unit), "Volume=/etc/flightctl/label-sync:/etc/flightctl/label-sync:ro,z")

			configTemplate, err := os.ReadFile(filepath.Join(repoRoot, consumer.configPath))
			require.NoError(t, err)
			require.Contains(t, string(configTemplate), "initialLabelSyncMappingsFile: /etc/flightctl/label-sync/mappings.yaml")
		})
	}

	packagedManifest, err := os.ReadFile(filepath.Join(repoRoot, "packaging/flightctl/label-sync/mappings.yaml"))
	require.NoError(t, err)
	require.Contains(t, string(packagedManifest), "name: system-info")
	require.Contains(t, string(packagedManifest), `{"feature.flightctl.io/systemInfo." + k: v}`)
	require.Contains(t, string(packagedManifest), "name: custom-info")
	require.Contains(t, string(packagedManifest), `{"feature.flightctl.io/customInfo." + k: v}`)

	rpmSpec, err := os.ReadFile(filepath.Join(repoRoot, "packaging/rpm/flightctl.spec"))
	require.NoError(t, err)
	require.True(t, strings.Contains(string(rpmSpec), "%config(noreplace) %{_sysconfdir}/flightctl/label-sync/mappings.yaml"), "RPM services package should preserve an existing operator mapping file")
}
