package main

import (
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestDeltaGenerationCompletionTimeoutHelmRendering(t *testing.T) {
	helmPath, err := exec.LookPath("helm")
	if err != nil {
		t.Skip("helm is not installed; make lint-helm runs the chart rendering checks")
	}

	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not locate chart test source")
	}
	chartDir := filepath.Clean(filepath.Join(filepath.Dir(sourceFile), "../../flightctl"))

	tests := []struct {
		name  string
		value string
		want  string
	}{
		{name: "When completion timeout is omitted it should default to 10m", want: "10m"},
		{name: "When completion timeout is set it should render in both consumers", value: "3m", want: "3m"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			values := ""
			if tt.value != "" {
				values = writeHelmValues(t, "deltaGeneration:\n  completionTimeout: "+tt.value+"\n")
			}
			resources := renderChart(t, helmPath, chartDir, values, false)
			assertConfiguredCompletionTimeout(t, resources, "flightctl-delta-worker-config", tt.want)
			assertConfiguredCompletionTimeout(t, resources, "flightctl-periodic-config", tt.want)
		})
	}
}

func assertConfiguredCompletionTimeout(t *testing.T, resources []helmManifestResource, configMapName, want string) {
	t.Helper()
	for _, resource := range resources {
		if resource.Kind != "ConfigMap" || resource.Metadata.Name != configMapName {
			continue
		}
		var config struct {
			DeltaGeneration struct {
				CompletionTimeout string `yaml:"completionTimeout"`
			} `yaml:"deltaGeneration"`
		}
		if err := yaml.Unmarshal([]byte(resource.Data["config.yaml"]), &config); err != nil {
			t.Fatalf("decode %s config.yaml: %v", configMapName, err)
		}
		if config.DeltaGeneration.CompletionTimeout != want {
			t.Fatalf("%s completionTimeout = %q, want %q", configMapName, config.DeltaGeneration.CompletionTimeout, want)
		}
		return
	}
	t.Fatalf("missing ConfigMap %s", configMapName)
}
