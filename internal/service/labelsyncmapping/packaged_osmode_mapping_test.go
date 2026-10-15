package labelsyncmapping

import (
	"encoding/json"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
)

const osModeMappingName = "os-mode"

// packagedMappingsPath resolves the mapping manifest that ships with the
// product so these tests exercise the shipped expression rather than a copy
// that can silently drift from it.
func packagedMappingsPath(t *testing.T) string {
	t.Helper()
	_, sourceFile, _, ok := runtime.Caller(0)
	require.True(t, ok, "could not locate the test source file")
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(sourceFile), "../../.."))
	return filepath.Join(repoRoot, "packaging/flightctl/label-sync/mappings.yaml")
}

// packagedMapping decodes one shipped mapping by name the same way the initial
// provisioner decodes it at runtime.
func packagedMapping(t *testing.T, name string) domain.LabelSyncMapping {
	t.Helper()
	entries, err := ReadInitialLabelSyncMappingEntries(packagedMappingsPath(t))
	require.NoError(t, err)

	for _, entry := range entries {
		var mapping domain.LabelSyncMapping
		require.NoError(t, json.Unmarshal(entry, &mapping))
		if lo.FromPtr(mapping.Metadata.Name) == name {
			return mapping
		}
	}
	t.Fatalf("shipped mappings do not contain a mapping named %q", name)
	return domain.LabelSyncMapping{}
}

// osModeStatus builds the device status shape an agent reports: systemInfo is a
// struct value, capabilities is an optional pointer that older agents populate.
func osModeStatus(systemInfoMode *domain.OsModeType, capabilities *domain.DeviceCapabilities) *domain.DeviceStatus {
	status := &domain.DeviceStatus{}
	status.SystemInfo.OsMode = systemInfoMode
	status.Capabilities = capabilities
	return status
}

func osModeDevice(status *domain.DeviceStatus) domain.Device {
	return domain.Device{
		Metadata: domain.ObjectMeta{Name: lo.ToPtr("edge-01")},
		Status:   status,
	}
}

// TestPackagedOsModeMappingMatchesInternalFallback pins the shipped os-mode
// mapping to the service-side resolution order in domain.DeviceStatusOsMode:
// a non-empty status.systemInfo.osMode wins, the deprecated
// status.capabilities.osMode is the fallback for older agents, and a device
// that reports neither gets no label instead of an evaluation error.
func TestPackagedOsModeMappingMatchesInternalFallback(t *testing.T) {
	image := domain.OsModeImage
	packageMode := domain.OsModePackage
	empty := domain.OsModeType("")

	testCases := []struct {
		name   string
		status *domain.DeviceStatus
		want   string
	}{
		{
			name:   "When only systemInfo reports image it should derive the image label",
			status: osModeStatus(&image, nil),
			want:   string(domain.OsModeImage),
		},
		{
			name:   "When only systemInfo reports package it should derive the package label",
			status: osModeStatus(&packageMode, nil),
			want:   string(domain.OsModePackage),
		},
		{
			name:   "When only the deprecated capabilities report image it should fall back to image",
			status: osModeStatus(nil, &domain.DeviceCapabilities{OsMode: &image}),
			want:   string(domain.OsModeImage),
		},
		{
			name:   "When only the deprecated capabilities report package it should fall back to package",
			status: osModeStatus(nil, &domain.DeviceCapabilities{OsMode: &packageMode}),
			want:   string(domain.OsModePackage),
		},
		{
			name:   "When systemInfo reports image and capabilities report package it should prefer systemInfo",
			status: osModeStatus(&image, &domain.DeviceCapabilities{OsMode: &packageMode}),
			want:   string(domain.OsModeImage),
		},
		{
			name:   "When systemInfo reports package and capabilities report image it should prefer systemInfo",
			status: osModeStatus(&packageMode, &domain.DeviceCapabilities{OsMode: &image}),
			want:   string(domain.OsModePackage),
		},
		{
			name:   "When systemInfo reports an empty mode and capabilities report package it should fall back to package",
			status: osModeStatus(&empty, &domain.DeviceCapabilities{OsMode: &packageMode}),
			want:   string(domain.OsModePackage),
		},
		{
			name:   "When systemInfo reports an empty mode and capabilities report image it should fall back to image",
			status: osModeStatus(&empty, &domain.DeviceCapabilities{OsMode: &image}),
			want:   string(domain.OsModeImage),
		},
		{
			name:   "When neither systemInfo nor capabilities report a mode it should derive no label",
			status: osModeStatus(nil, nil),
			want:   "",
		},
		{
			name:   "When capabilities are present without a mode it should derive no label",
			status: osModeStatus(nil, &domain.DeviceCapabilities{}),
			want:   "",
		},
		{
			name:   "When both reported modes are empty it should derive no label",
			status: osModeStatus(&empty, &domain.DeviceCapabilities{OsMode: &empty}),
			want:   "",
		},
		{
			name:   "When the device has no status it should derive no label",
			status: nil,
			want:   "",
		},
	}

	mapping := packagedMapping(t, osModeMappingName)
	require.Equal(t, domain.LabelSyncMappingDevice, mapping.Spec.ResourceType)
	require.Equal(t, "feature.flightctl.io/os.mode", lo.FromPtr(mapping.Spec.Key))

	evaluator, err := NewEvaluator()
	require.NoError(t, err)
	require.NoError(t, evaluator.ValidateExpressionIs(mapping.Spec.Expression, ResultKindScalar))

	for _, tt := range testCases {
		t.Run(tt.name, func(t *testing.T) {
			device := osModeDevice(tt.status)

			outputs, err := mappingOutputs(evaluator, mapping, testActivation(t, device))
			require.NoError(t, err)

			if tt.want == "" {
				require.Empty(t, outputs)
			} else {
				require.Equal(t, map[string]string{*mapping.Spec.Key: tt.want}, outputs)
			}

			// The derived label must agree with the service-side resolution
			// that field selectors and summary counts already use.
			require.Equal(t, tt.want, string(lo.FromPtr(domain.DeviceStatusOsMode(tt.status))),
				"shipped mapping and domain.DeviceStatusOsMode must agree")
		})
	}
}

// TestPackagedOsModeMappingHandlesExplicitNulls covers activations that carry
// explicit JSON nulls. ActivateDevice omits unset pointers rather than encoding
// null, so these shapes are built directly to prove the shipped expression
// tolerates a null primary, a null capabilities object, and a null legacy
// field without failing the mapping.
func TestPackagedOsModeMappingHandlesExplicitNulls(t *testing.T) {
	testCases := []struct {
		name   string
		status string
		want   string
	}{
		{
			name:   "When systemInfo osMode is null and capabilities are absent it should derive no label",
			status: `{"systemInfo":{"osMode":null}}`,
			want:   "",
		},
		{
			name:   "When systemInfo osMode is null and capabilities are null it should derive no label",
			status: `{"systemInfo":{"osMode":null},"capabilities":null}`,
			want:   "",
		},
		{
			name:   "When both reported modes are null it should derive no label",
			status: `{"systemInfo":{"osMode":null},"capabilities":{"osMode":null}}`,
			want:   "",
		},
		{
			name:   "When systemInfo osMode is null and capabilities report package it should fall back to package",
			status: `{"systemInfo":{"osMode":null},"capabilities":{"osMode":"package"}}`,
			want:   string(domain.OsModePackage),
		},
		{
			name:   "When the whole status is null it should derive no label",
			status: `null`,
			want:   "",
		},
	}

	mapping := packagedMapping(t, osModeMappingName)
	evaluator, err := NewEvaluator()
	require.NoError(t, err)

	for _, tt := range testCases {
		t.Run(tt.name, func(t *testing.T) {
			var status any
			require.NoError(t, json.Unmarshal([]byte(tt.status), &status))
			activation := map[string]any{
				"metadata": map[string]any{"name": "edge-01"},
				"spec":     nil,
				"status":   status,
			}

			outputs, err := mappingOutputs(evaluator, mapping, Activation(activation))
			require.NoError(t, err)

			if tt.want == "" {
				require.Empty(t, outputs)
			} else {
				require.Equal(t, map[string]string{*mapping.Spec.Key: tt.want}, outputs)
			}
		})
	}
}
