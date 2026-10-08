package labelsyncmapping

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"
)

func TestDefaultCapabilityMappingsPreserveMapBehavior(t *testing.T) {
	const (
		gpuLabel = "feature.flightctl.io/gpu.present"
		kvmLabel = "feature.flightctl.io/kvm.enabled"
		osLabel  = "feature.flightctl.io/os.mode"
	)
	legacyExpressions := map[string]string{
		"gpu-present": `has(status.systemInfo.gpus) && status.systemInfo.gpus != null
			? {"feature.flightctl.io/gpu.present": size(status.systemInfo.gpus) > 0 ? "true" : "false"}
			: {}`,
		"kvm-enabled": `has(status.systemInfo.kvm) && status.systemInfo.kvm != null
			&& has(status.systemInfo.kvm.enabled) && status.systemInfo.kvm.enabled != null
			? {"feature.flightctl.io/kvm.enabled": status.systemInfo.kvm.enabled ? "true" : "false"}
			: {}`,
		"os-mode": `has(status.systemInfo.osMode) && status.systemInfo.osMode != null
			? {"feature.flightctl.io/os.mode": status.systemInfo.osMode}
			: {}`,
	}
	expectedKeys := map[string]string{
		"gpu-present": gpuLabel,
		"kvm-enabled": kvmLabel,
		"os-mode":     osLabel,
	}
	testCases := []struct {
		name       string
		mapping    string
		systemInfo map[string]any
		want       map[string]string
	}{
		{name: "When GPU information is absent it should emit no label", mapping: "gpu-present", systemInfo: map[string]any{}, want: map[string]string{}},
		{name: "When GPU information is null it should emit no label", mapping: "gpu-present", systemInfo: map[string]any{"gpus": nil}, want: map[string]string{}},
		{name: "When the GPU list is empty it should emit false", mapping: "gpu-present", systemInfo: map[string]any{"gpus": []any{}}, want: map[string]string{gpuLabel: "false"}},
		{name: "When the GPU list is populated it should emit true", mapping: "gpu-present", systemInfo: map[string]any{"gpus": []any{map[string]any{"index": 0}}}, want: map[string]string{gpuLabel: "true"}},
		{name: "When KVM information is absent it should emit no label", mapping: "kvm-enabled", systemInfo: map[string]any{}, want: map[string]string{}},
		{name: "When KVM information is null it should emit no label", mapping: "kvm-enabled", systemInfo: map[string]any{"kvm": nil}, want: map[string]string{}},
		{name: "When the KVM enabled field is absent it should emit no label", mapping: "kvm-enabled", systemInfo: map[string]any{"kvm": map[string]any{}}, want: map[string]string{}},
		{name: "When the KVM enabled field is null it should emit no label", mapping: "kvm-enabled", systemInfo: map[string]any{"kvm": map[string]any{"enabled": nil}}, want: map[string]string{}},
		{name: "When KVM is disabled it should emit false", mapping: "kvm-enabled", systemInfo: map[string]any{"kvm": map[string]any{"enabled": false}}, want: map[string]string{kvmLabel: "false"}},
		{name: "When KVM is enabled it should emit true", mapping: "kvm-enabled", systemInfo: map[string]any{"kvm": map[string]any{"enabled": true}}, want: map[string]string{kvmLabel: "true"}},
		{name: "When OS mode is absent it should emit no label", mapping: "os-mode", systemInfo: map[string]any{}, want: map[string]string{}},
		{name: "When OS mode is null it should emit no label", mapping: "os-mode", systemInfo: map[string]any{"osMode": nil}, want: map[string]string{}},
		{name: "When OS mode is empty it should emit no label", mapping: "os-mode", systemInfo: map[string]any{"osMode": ""}, want: map[string]string{}},
		{name: "When OS mode is image it should emit image", mapping: "os-mode", systemInfo: map[string]any{"osMode": "image"}, want: map[string]string{osLabel: "image"}},
		{name: "When OS mode is package it should emit package", mapping: "os-mode", systemInfo: map[string]any{"osMode": "package"}, want: map[string]string{osLabel: "package"}},
	}

	mappings := readPackagedCapabilityMappings(t)
	mappingsByName := make(map[string]domain.LabelSyncMapping, len(legacyExpressions))
	for _, mapping := range mappings {
		if mapping.Metadata.Name != nil {
			if _, isCapabilityMapping := legacyExpressions[*mapping.Metadata.Name]; isCapabilityMapping {
				mappingsByName[*mapping.Metadata.Name] = mapping
			}
		}
	}
	for name := range legacyExpressions {
		mapping, ok := mappingsByName[name]
		require.Truef(t, ok, "packaged default mapping %q is missing", name)
		require.NotNil(t, mapping.Spec.Key)
		require.Equal(t, expectedKeys[name], *mapping.Spec.Key)
	}

	evaluator, err := NewEvaluator()
	require.NoError(t, err)
	for _, tt := range testCases {
		t.Run(tt.name, func(t *testing.T) {
			mapping := mappingsByName[tt.mapping]
			require.NoError(t, evaluator.ValidateExpressionIs(mapping.Spec.Expression, ResultKindScalar))
			activation := Activation(map[string]any{"status": map[string]any{"systemInfo": tt.systemInfo}})

			legacyResult, err := evaluator.Evaluate(legacyExpressions[tt.mapping], activation)
			require.NoError(t, err)
			legacyLabels, ok := legacyResult.(MapResult)
			require.Truef(t, ok, "legacy expression returned %T", legacyResult)
			require.Equal(t, tt.want, map[string]string(legacyLabels))

			got, err := mappingOutputs(evaluator, mapping, activation)
			require.NoError(t, err)
			require.Equal(t, map[string]string(legacyLabels), got)
		})
	}
}

func readPackagedCapabilityMappings(t *testing.T) []domain.LabelSyncMapping {
	t.Helper()

	_, sourceFile, _, ok := runtime.Caller(0)
	require.True(t, ok, "could not locate test source")
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(sourceFile), "../../.."))
	manifestPath := filepath.Join(repoRoot, "packaging/flightctl/label-sync/mappings.yaml")
	contents, err := os.ReadFile(manifestPath)
	require.NoError(t, err)

	var mappings []domain.LabelSyncMapping
	require.NoError(t, yaml.Unmarshal(contents, &mappings))
	return mappings
}
