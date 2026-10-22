package renderer

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/flightctl/flightctl/pkg/template"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"
)

func TestDeltaGenerationCompletionTimeoutPodmanRendering(t *testing.T) {
	tests := []struct {
		name  string
		value any
		want  string
	}{
		{name: "When completion timeout is omitted it should default to 10m", want: "10m"},
		{name: "When completion timeout is set it should render in both consumers", value: "3m", want: "3m"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			deltaGeneration := map[string]any{}
			if tt.value != nil {
				deltaGeneration["completionTimeout"] = tt.value
			}
			data := map[string]any{
				"db":                     map[string]any{"type": "builtin"},
				"deltaGeneration":        deltaGeneration,
				"periodic":               map[string]any{},
				"vulnerabilityReporting": map[string]any{},
			}
			repoRoot := repositoryRoot(t)
			renderedConfig := renderPodmanConfigTemplate(t, data, repoRoot, "flightctl-delta-worker")
			require.Equal(t, tt.want, renderedConfig.DeltaGeneration.CompletionTimeout)
			renderedConfig = renderPodmanConfigTemplate(t, data, repoRoot, "flightctl-periodic")
			require.Equal(t, tt.want, renderedConfig.DeltaGeneration.CompletionTimeout)
		})
	}
}

func renderPodmanConfigTemplate(t *testing.T, data map[string]any, repoRoot, service string) deltaGenerationPodmanConfig {
	t.Helper()
	templatePath := filepath.Join(repoRoot, "deploy", "podman", service, service+"-config", "config.yaml.template")
	outputPath := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, template.RenderWithData(data, templatePath, outputPath))

	contents, err := os.ReadFile(outputPath)
	require.NoError(t, err)
	var config deltaGenerationPodmanConfig
	require.NoError(t, yaml.Unmarshal(contents, &config))
	return config
}

type deltaGenerationPodmanConfig struct {
	DeltaGeneration struct {
		CompletionTimeout string `json:"completionTimeout"`
	} `json:"deltaGeneration"`
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, sourceFile, _, ok := runtime.Caller(0)
	require.True(t, ok, "could not locate template test source")
	return filepath.Clean(filepath.Join(filepath.Dir(sourceFile), "..", "..", ".."))
}
