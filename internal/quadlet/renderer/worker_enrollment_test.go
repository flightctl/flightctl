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
	for _, path := range []string{"client-signer.crt", "client-signer.key"} {
		require.Contains(t, string(contents), "Volume=/etc/flightctl/pki/flightctl-api/"+path+":/root/.flightctl/certs/"+path+":ro,z")
	}
	require.Contains(t, string(contents), "Volume=/etc/flightctl/pki/ca-bundle.crt:/root/.flightctl/certs/ca-bundle.crt:ro,z")
	require.Contains(t, string(contents), "Volume=/etc/flightctl/tpm-cas:/etc/flightctl/tpm-cas:ro,z")
	require.Contains(t, string(contents), "After=flightctl-db-wait.service flightctl-db-migrate.service flightctl-kv.service flightctl-certs-init.service")
	require.Contains(t, string(contents), "Wants=flightctl-db-wait.service flightctl-db-migrate.service flightctl-kv.service flightctl-certs-init.service")
	require.NotContains(t, string(contents), "server.key")
}
