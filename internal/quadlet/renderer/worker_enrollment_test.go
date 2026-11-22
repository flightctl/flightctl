package renderer

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/flightctl/flightctl/pkg/template"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"
)

func TestWorkerEnrollmentRendering(t *testing.T) {
	for _, paths := range [][]string{nil, {"/etc/flightctl/tpm-cas/manufacturer.pem"}} {
		t.Run("When worker trust is configured it should preserve existing paths", func(t *testing.T) {
			data := map[string]any{"db": map[string]any{"type": "builtin"}, "service": map[string]any{"tpmCAPaths": paths}}
			if paths == nil {
				delete(data, "service")
			}
			input := filepath.Join(repositoryRoot(t), "deploy/podman/flightctl-worker/flightctl-worker-config/config.yaml.template")
			output := filepath.Join(t.TempDir(), "config.yaml")
			require.NoError(t, template.RenderWithData(data, input, output))
			contents, err := os.ReadFile(output)
			require.NoError(t, err)
			var document map[string]any
			require.NoError(t, yaml.Unmarshal(contents, &document))
			require.NotNil(t, document["service"])
			var cfg struct {
				Service struct {
					TPMCAPaths []string `json:"tpmCAPaths"`
				} `json:"service"`
			}
			require.NoError(t, yaml.Unmarshal(contents, &cfg))
			require.Equal(t, paths, cfg.Service.TPMCAPaths)
		})
	}
	input := filepath.Join(repositoryRoot(t), "deploy/podman/flightctl-worker/flightctl-worker.container")
	output := filepath.Join(t.TempDir(), "worker.container")
	require.NoError(t, template.RenderWithData(map[string]any{"Worker": map[string]any{"Image": "worker", "Tag": "test"}}, input, output))
	contents, err := os.ReadFile(output)
	require.NoError(t, err)
	require.Contains(t, string(contents), "Volume=/run/flightctl-worker-enrollment/signing:/root/.flightctl/certs/enrollment:ro,z")
	require.Contains(t, string(contents), "ExecStartPre=/usr/bin/flightctl-standalone prepare-worker-enrollment")
	require.Contains(t, string(contents), "RuntimeDirectory=flightctl-worker-enrollment")
	require.Contains(t, string(contents), "RuntimeDirectoryMode=0700")
	require.NotContains(t, string(contents), "Volume=/etc/flightctl/pki/flightctl-api")
	require.Contains(t, string(contents), "Volume=/etc/flightctl/tpm-cas:/etc/flightctl/tpm-cas:ro,z")
	require.Contains(t, string(contents), "After=flightctl-db-wait.service flightctl-db-migrate.service flightctl-kv.service flightctl-certs-init.service")
	require.Contains(t, string(contents), "Wants=flightctl-db-wait.service flightctl-db-migrate.service flightctl-kv.service flightctl-certs-init.service")
	require.NotContains(t, string(contents), "server.key")
}
