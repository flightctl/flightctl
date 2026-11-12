package v1beta1

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestFirmwareSpec(t *testing.T) FirmwareSpec {
	t.Helper()
	var spec FirmwareSpec
	require.NoError(t, json.Unmarshal([]byte(testFirmwareSpecJSON), &spec))
	return spec
}

func newTestFirmwareStatus(t *testing.T) FirmwareStatus {
	t.Helper()
	var status FirmwareStatus
	require.NoError(t, json.Unmarshal([]byte(testFirmwareStatusJSON), &status))
	return status
}

func firmwareConfigResource(f *FirmwareSpec, index int) map[string]any {
	return f.Updater.Config["resources"].([]any)[index].(map[string]any)
}

// requireFirmwareErrors asserts that errs holds exactly one error per entry in
// want, each containing the corresponding text.
func requireFirmwareErrors(t *testing.T, errs []error, want []string) {
	t.Helper()
	joined := errors.Join(errs...)
	if len(want) == 0 {
		require.Empty(t, errs, "unexpected errors: %v", joined)
		return
	}
	require.Len(t, errs, len(want), "errors: %v", joined)
	for _, text := range want {
		assert.Contains(t, joined.Error(), text)
	}
}

func TestFirmwareSpecValidateStructure(t *testing.T) {
	const tooLongName = "a123456789b123456789c123456789d123456789e123456789f123456789g1234"

	tests := []struct {
		name     string
		mutate   func(f *FirmwareSpec)
		wantErrs []string
	}{
		{
			name:   "When the firmware spec is complete it should pass",
			mutate: func(f *FirmwareSpec) {},
		},
		{
			name:     "When profile is empty it should be required",
			mutate:   func(f *FirmwareSpec) { f.Profile = "" },
			wantErrs: []string{"spec.firmware.profile: Required value"},
		},
		{
			name:     "When the updater name is empty it should be required",
			mutate:   func(f *FirmwareSpec) { f.Updater.Name = "" },
			wantErrs: []string{"spec.firmware.updater.name: Required value"},
		},
		{
			name:     "When the updater version is empty it should be required",
			mutate:   func(f *FirmwareSpec) { f.Updater.Version = "" },
			wantErrs: []string{"spec.firmware.updater.version: Required value"},
		},
		{
			name:     "When the updater config is absent it should require a JSON object",
			mutate:   func(f *FirmwareSpec) { f.Updater.Config = nil },
			wantErrs: []string{"spec.firmware.updater.config: Required value"},
		},
		{
			name:   "When the updater config is an empty object it should pass",
			mutate: func(f *FirmwareSpec) { f.Updater.Config = map[string]any{} },
		},
		{
			name:     "When retryGeneration is negative it should be rejected",
			mutate:   func(f *FirmwareSpec) { f.RetryGeneration = lo.ToPtr(int64(-1)) },
			wantErrs: []string{"spec.firmware.retryGeneration: Invalid value: -1"},
		},
		{
			name:   "When retryGeneration is zero it should pass",
			mutate: func(f *FirmwareSpec) { f.RetryGeneration = lo.ToPtr(int64(0)) },
		},
		{
			name:   "When retryGeneration is omitted it should pass",
			mutate: func(f *FirmwareSpec) { f.RetryGeneration = nil },
		},
		{
			name:     "When components are absent it should require at least one component",
			mutate:   func(f *FirmwareSpec) { f.Components = nil },
			wantErrs: []string{"spec.firmware.components: Required value"},
		},
		{
			name:     "When components are empty it should require at least one component",
			mutate:   func(f *FirmwareSpec) { f.Components = []FirmwareComponentSpec{} },
			wantErrs: []string{"spec.firmware.components: Required value"},
		},
		{
			name:     "When overlays are absent it should require the overlay list",
			mutate:   func(f *FirmwareSpec) { f.Overlays = nil },
			wantErrs: []string{"spec.firmware.overlays: Required value"},
		},
		{
			name:   "When overlays are an empty list it should pass",
			mutate: func(f *FirmwareSpec) { f.Overlays = []FirmwareOverlaySpec{} },
		},
		{
			name:     "When a component name is empty it should be required",
			mutate:   func(f *FirmwareSpec) { f.Components[0].Name = "" },
			wantErrs: []string{"spec.firmware.components[0].name: Required value"},
		},
		{
			name:     "When a component name is not a lowercase label it should be rejected",
			mutate:   func(f *FirmwareSpec) { f.Components[0].Name = "System_1" },
			wantErrs: []string{`spec.firmware.components[0].name: Invalid value: "System_1"`},
		},
		{
			name:     "When a component name ends with a hyphen it should be rejected",
			mutate:   func(f *FirmwareSpec) { f.Components[1].Name = "bootloader-" },
			wantErrs: []string{`spec.firmware.components[1].name: Invalid value: "bootloader-"`},
		},
		{
			name:     "When a component name exceeds 63 characters it should be rejected",
			mutate:   func(f *FirmwareSpec) { f.Components[0].Name = tooLongName },
			wantErrs: []string{"spec.firmware.components[0].name: Invalid value"},
		},
		{
			name:     "When two components share a name it should report the duplicate",
			mutate:   func(f *FirmwareSpec) { f.Components[1].Name = "system" },
			wantErrs: []string{`spec.firmware.components[1].name: Duplicate value: "system"`},
		},
		{
			name:     "When an overlay reuses a component name it should report the duplicate",
			mutate:   func(f *FirmwareSpec) { f.Overlays[0].Name = "bootloader" },
			wantErrs: []string{`spec.firmware.overlays[0].name: Duplicate value: "bootloader"`},
		},
		{
			name: "When two overlays share a name it should report the duplicate",
			mutate: func(f *FirmwareSpec) {
				f.Overlays = append(f.Overlays, f.Overlays[0])
			},
			wantErrs: []string{`spec.firmware.overlays[1].name: Duplicate value: "camera"`},
		},
		{
			name: "When the artifact digest is not a sha256 digest it should be rejected",
			mutate: func(f *FirmwareSpec) {
				f.Components[0].ArtifactDigest = lo.ToPtr("md5:0123456789abcdef0123456789abcdef")
			},
			wantErrs: []string{`spec.firmware.components[0].artifactDigest: Invalid value: "md5:0123456789abcdef0123456789abcdef"`},
		},
		{
			name:     "When the artifact digest uses uppercase hex it should be rejected",
			mutate:   func(f *FirmwareSpec) { f.Components[0].ArtifactDigest = lo.ToPtr(strings.ToUpper(testFirmwareDigest)) },
			wantErrs: []string{"spec.firmware.components[0].artifactDigest: Invalid value"},
		},
		{
			name: "When the artifact digest is truncated it should be rejected",
			mutate: func(f *FirmwareSpec) {
				f.Components[0].ArtifactDigest = lo.ToPtr(testFirmwareDigest[:len(testFirmwareDigest)-1])
			},
			wantErrs: []string{"spec.firmware.components[0].artifactDigest: Invalid value"},
		},
		{
			name:     "When a component has no version constraint it should be rejected",
			mutate:   func(f *FirmwareSpec) { f.Components[0].Version = FirmwareVersionConstraints{} },
			wantErrs: []string{"spec.firmware.components[0].version: Required value"},
		},
		{
			name:     "When latest is false and no other constraint is set it should be rejected",
			mutate:   func(f *FirmwareSpec) { f.Components[0].Version = FirmwareVersionConstraints{Latest: lo.ToPtr(false)} },
			wantErrs: []string{"spec.firmware.components[0].version: Required value"},
		},
		{
			name:     "When an exact constraint is empty it should be rejected",
			mutate:   func(f *FirmwareSpec) { f.Components[0].Version.Exact = lo.ToPtr("") },
			wantErrs: []string{`spec.firmware.components[0].version.exact: Invalid value: ""`},
		},
		{
			name:     "When a minimum constraint is empty it should be rejected",
			mutate:   func(f *FirmwareSpec) { f.Components[0].Version.Minimum = lo.ToPtr("") },
			wantErrs: []string{`spec.firmware.components[0].version.minimum: Invalid value: ""`},
		},
		{
			name:     "When a maximum constraint is empty it should be rejected",
			mutate:   func(f *FirmwareSpec) { f.Components[0].Version.Maximum = lo.ToPtr("") },
			wantErrs: []string{`spec.firmware.components[0].version.maximum: Invalid value: ""`},
		},
		{
			name:     "When an overlay name is empty it should be required",
			mutate:   func(f *FirmwareSpec) { f.Overlays[0].Name = "" },
			wantErrs: []string{"spec.firmware.overlays[0].name: Required value"},
		},
		{
			name:     "When an overlay name is not an RFC 1123 label it should be rejected",
			mutate:   func(f *FirmwareSpec) { f.Overlays[0].Name = "camera.dtbo" },
			wantErrs: []string{"spec.firmware.overlays[0].name: Invalid value"},
		},
		{
			name:   "When an overlay name is an RFC 1123 label it should pass",
			mutate: func(f *FirmwareSpec) { f.Overlays[0].Name = "camera-overlay" },
		},
		{
			name:     "When an overlay lists no compatible firmware it should be rejected",
			mutate:   func(f *FirmwareSpec) { f.Overlays[0].CompatibleFirmware = []FirmwareOverlayCompatibility{} },
			wantErrs: []string{"spec.firmware.overlays[0].compatibleFirmware: Required value"},
		},
		{
			name:     "When an overlay omits compatible firmware it should be rejected",
			mutate:   func(f *FirmwareSpec) { f.Overlays[0].CompatibleFirmware = nil },
			wantErrs: []string{"spec.firmware.overlays[0].compatibleFirmware: Required value"},
		},
		{
			name:     "When a compatible firmware entry has no component it should be required",
			mutate:   func(f *FirmwareSpec) { f.Overlays[0].CompatibleFirmware[0].Component = "" },
			wantErrs: []string{"spec.firmware.overlays[0].compatibleFirmware[0].component: Required value"},
		},
		{
			name:     "When a compatible firmware bound is empty it should be rejected",
			mutate:   func(f *FirmwareSpec) { f.Overlays[0].CompatibleFirmware[1].Maximum = lo.ToPtr("") },
			wantErrs: []string{`spec.firmware.overlays[0].compatibleFirmware[1].maximum: Invalid value: ""`},
		},
		{
			name: "When a compatible firmware entry has only a component it should pass",
			mutate: func(f *FirmwareSpec) {
				f.Overlays[0].CompatibleFirmware = []FirmwareOverlayCompatibility{{Component: "system"}}
			},
		},
		{
			name: "When several structural errors exist it should report each of them",
			mutate: func(f *FirmwareSpec) {
				f.Profile = ""
				f.Components[1].Name = "system"
				f.Components[0].ArtifactDigest = lo.ToPtr("sha256:short")
				f.Overlays[0].CompatibleFirmware = nil
			},
			wantErrs: []string{
				"spec.firmware.profile: Required value",
				`spec.firmware.components[1].name: Duplicate value: "system"`,
				"spec.firmware.components[0].artifactDigest: Invalid value",
				"spec.firmware.overlays[0].compatibleFirmware: Required value",
			},
		},
	}

	for _, fleetTemplate := range []bool{false, true} {
		for _, tt := range tests {
			name := tt.name
			if fleetTemplate {
				name += " in a fleet template"
			}
			t.Run(name, func(t *testing.T) {
				spec := newTestFirmwareSpec(t)
				tt.mutate(&spec)
				requireFirmwareErrors(t, spec.Validate(fleetTemplate), tt.wantErrs)
			})
		}
	}
}

func TestFirmwareConfigTemplateValidationBounds(t *testing.T) {
	t.Run("When configuration nesting exceeds the depth limit it should be rejected", func(t *testing.T) {
		value := any("{{ .metadata.labels.value }}")
		for range 65 {
			value = map[string]any{"nested": value}
		}
		spec := newTestFirmwareSpec(t)
		spec.Updater.Config = map[string]any{"deep": value}

		errs := spec.Validate(true)
		requireFirmwareErrors(t, errs, []string{"spec.firmware.updater.config.deep"})
		assert.Contains(t, errors.Join(errs...).Error(), "must not nest deeper than 64 levels")
	})

	t.Run("When configuration size exceeds the node limit it should be rejected", func(t *testing.T) {
		config := make(map[string]any, firmwareConfigMaxNodes+1)
		for i := range firmwareConfigMaxNodes + 1 {
			config[fmt.Sprintf("key-%d", i)] = "value"
		}
		spec := newTestFirmwareSpec(t)
		spec.Updater.Config = config

		errs := spec.Validate(true)
		require.Len(t, errs, 1)
		assert.Contains(t, errors.Join(errs...).Error(), "must contain at most 10000 values")
	})

	t.Run("When configuration validation produces too many errors it should cap them", func(t *testing.T) {
		config := make(map[string]any, firmwareConfigMaxErrors+1)
		for i := range firmwareConfigMaxErrors + 1 {
			config[fmt.Sprintf("{{ .metadata.labels.key%d }}", i)] = "value"
		}
		spec := newTestFirmwareSpec(t)
		spec.Updater.Config = config

		errs := spec.Validate(true)
		require.Len(t, errs, firmwareConfigMaxErrors)
	})
}

func TestFirmwareSpecValidateVersionConstraintShapes(t *testing.T) {
	tests := []struct {
		name        string
		constraints FirmwareVersionConstraints
	}{
		{name: "When only exact is set it should pass", constraints: FirmwareVersionConstraints{Exact: lo.ToPtr("36.4.4")}},
		{name: "When only minimum is set it should pass", constraints: FirmwareVersionConstraints{Minimum: lo.ToPtr("36.4.2")}},
		{name: "When only maximum is set it should pass", constraints: FirmwareVersionConstraints{Maximum: lo.ToPtr("36.4.4")}},
		{name: "When only latest is set it should pass", constraints: FirmwareVersionConstraints{Latest: lo.ToPtr(true)}},
		{
			name:        "When exact, minimum and maximum are combined it should keep all three",
			constraints: FirmwareVersionConstraints{Exact: lo.ToPtr("36.4.4"), Minimum: lo.ToPtr("36.4.2"), Maximum: lo.ToPtr("36.4.4")},
		},
		{
			name:        "When minimum, maximum and latest are combined it should pass",
			constraints: FirmwareVersionConstraints{Minimum: lo.ToPtr("36.4.2"), Maximum: lo.ToPtr("36.4.4"), Latest: lo.ToPtr(true)},
		},
		{
			name:        "When minimum exceeds maximum it should pass because the updater compares versions",
			constraints: FirmwareVersionConstraints{Minimum: lo.ToPtr("36.5.0"), Maximum: lo.ToPtr("36.4.4")},
		},
		{
			name:        "When versions do not follow semantic versioning it should pass unchanged",
			constraints: FirmwareVersionConstraints{Exact: lo.ToPtr("r36 rev 4.4 (gcid-1)")},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := newTestFirmwareSpec(t)
			spec.Components[0].Version = tt.constraints
			requireFirmwareErrors(t, spec.Validate(false), nil)

			raw, err := json.Marshal(spec)
			require.NoError(t, err)
			var decoded FirmwareSpec
			require.NoError(t, json.Unmarshal(raw, &decoded))
			assert.Equal(t, tt.constraints, decoded.Components[0].Version, "constraints must be kept exactly as requested")
		})
	}
}

func TestFirmwareSpecValidateTemplates(t *testing.T) {
	const (
		labelParameter   = "{{ .metadata.labels.board }}"
		unclosedTemplate = "{{ .metadata.labels.board"
		standaloneDetail = "template parameters are only supported in fleet templates"
		forbiddenDetail  = "template parameters are not supported in this field"
	)

	tests := []struct {
		name          string
		fleetTemplate bool
		mutate        func(f *FirmwareSpec)
		wantErrs      []string
		wantText      []string
	}{
		{
			name:          "When profile and updater identity use template parameters in a fleet template it should pass",
			fleetTemplate: true,
			mutate: func(f *FirmwareSpec) {
				f.Profile = "{{ .metadata.labels.profile }}"
				f.Updater.Name = "{{ .metadata.labels.updater }}"
				f.Updater.Version = "{{ .metadata.labels.version }}"
			},
		},
		{
			name:          "When a templated profile mixes literal text and parameters in a fleet template it should pass",
			fleetTemplate: true,
			mutate:        func(f *FirmwareSpec) { f.Profile = "orin-" + labelParameter },
		},
		{
			name:          "When a templated field uses template functions in a fleet template it should pass",
			fleetTemplate: true,
			mutate: func(f *FirmwareSpec) {
				f.Updater.Version = `{{ getOrDefault .metadata.labels "updater-version" "0.1.0" }}`
			},
		},
		{
			name:          "When a templated profile has invalid syntax in a fleet template it should be rejected",
			fleetTemplate: true,
			mutate:        func(f *FirmwareSpec) { f.Profile = unclosedTemplate },
			wantErrs:      []string{"spec.firmware.profile: Invalid value"},
			wantText:      []string{"invalid parameter syntax"},
		},
		{
			name:          "When a templated updater name accesses an unsupported field in a fleet template it should be rejected",
			fleetTemplate: true,
			mutate:        func(f *FirmwareSpec) { f.Updater.Name = "{{ .metadata.annotations.updater }}" },
			wantErrs:      []string{"spec.firmware.updater.name: Invalid value"},
		},
		{
			name:          "When a component name uses template parameters in a fleet template it should be rejected",
			fleetTemplate: true,
			mutate:        func(f *FirmwareSpec) { f.Components[0].Name = labelParameter },
			wantErrs:      []string{"spec.firmware.components[0].name: Invalid value"},
			wantText:      []string{forbiddenDetail},
		},
		{
			name:          "When an artifact digest uses template parameters in a fleet template it should be rejected",
			fleetTemplate: true,
			mutate:        func(f *FirmwareSpec) { f.Components[0].ArtifactDigest = lo.ToPtr("sha256:" + labelParameter) },
			wantErrs:      []string{"spec.firmware.components[0].artifactDigest: Invalid value"},
			wantText:      []string{forbiddenDetail},
		},
		{
			name:          "When version constraints use template parameters in a fleet template it should reject each of them",
			fleetTemplate: true,
			mutate: func(f *FirmwareSpec) {
				f.Components[0].Version = FirmwareVersionConstraints{Exact: lo.ToPtr(labelParameter), Minimum: lo.ToPtr(labelParameter), Maximum: lo.ToPtr(labelParameter)}
			},
			wantErrs: []string{
				"spec.firmware.components[0].version.exact: Invalid value",
				"spec.firmware.components[0].version.minimum: Invalid value",
				"spec.firmware.components[0].version.maximum: Invalid value",
			},
		},
		{
			name:          "When an overlay name uses template parameters in a fleet template it should be rejected",
			fleetTemplate: true,
			mutate:        func(f *FirmwareSpec) { f.Overlays[0].Name = labelParameter },
			wantErrs:      []string{"spec.firmware.overlays[0].name: Invalid value"},
			wantText:      []string{forbiddenDetail},
		},
		{
			name:          "When compatible firmware fields use template parameters in a fleet template it should reject each of them",
			fleetTemplate: true,
			mutate: func(f *FirmwareSpec) {
				f.Overlays[0].CompatibleFirmware[0].Component = labelParameter
				f.Overlays[0].CompatibleFirmware[0].Exact = lo.ToPtr(labelParameter)
			},
			wantErrs: []string{
				"spec.firmware.overlays[0].compatibleFirmware[0].component: Invalid value",
				"spec.firmware.overlays[0].compatibleFirmware[0].exact: Invalid value",
			},
		},
		{
			name:          "When a config string uses template parameters in a fleet template it should leave placement to the updater release",
			fleetTemplate: true,
			mutate: func(f *FirmwareSpec) {
				firmwareConfigResource(f, 0)["targetGuid"] = "{{ .metadata.labels.guid }}"
			},
		},
		{
			name:          "When a config string has invalid template syntax in a fleet template it should be rejected",
			fleetTemplate: true,
			mutate:        func(f *FirmwareSpec) { firmwareConfigResource(f, 0)["targetGuid"] = unclosedTemplate },
			wantErrs:      []string{"spec.firmware.updater.config.resources[0].targetGuid: Invalid value"},
			wantText:      []string{"invalid parameter syntax"},
		},
		{
			name:          "When a config key uses template parameters in a fleet template it should be rejected",
			fleetTemplate: true,
			mutate:        func(f *FirmwareSpec) { f.Updater.Config[labelParameter] = "value" },
			wantErrs:      []string{"template parameters are not supported in configuration keys"},
			wantText:      []string{"spec.firmware.updater.config.{{ .metadata.labels.board }}"},
		},
		{
			name:          "When template syntax is allowed it should still report every structural error",
			fleetTemplate: true,
			mutate: func(f *FirmwareSpec) {
				f.Profile = labelParameter
				f.Components[1].Name = "system"
				f.Components[0].ArtifactDigest = lo.ToPtr("sha256:short")
				f.Updater.Config = nil
			},
			wantErrs: []string{
				`spec.firmware.components[1].name: Duplicate value: "system"`,
				"spec.firmware.components[0].artifactDigest: Invalid value",
				"spec.firmware.updater.config: Required value",
			},
		},
		{
			name:     "When profile uses template parameters on a standalone device it should be rejected",
			mutate:   func(f *FirmwareSpec) { f.Profile = labelParameter },
			wantErrs: []string{"spec.firmware.profile: Invalid value"},
			wantText: []string{standaloneDetail},
		},
		{
			name: "When the updater identity uses template parameters on a standalone device it should be rejected",
			mutate: func(f *FirmwareSpec) {
				f.Updater.Name = "{{ .metadata.labels.updater }}"
				f.Updater.Version = "{{ .metadata.labels.version }}"
			},
			wantErrs: []string{
				"spec.firmware.updater.name: Invalid value",
				"spec.firmware.updater.version: Invalid value",
			},
		},
		{
			name:     "When a config string uses template parameters on a standalone device it should be rejected",
			mutate:   func(f *FirmwareSpec) { firmwareConfigResource(f, 0)["targetGuid"] = "{{ .metadata.labels.guid }}" },
			wantErrs: []string{"spec.firmware.updater.config.resources[0].targetGuid: Invalid value"},
			wantText: []string{standaloneDetail},
		},
		{
			name: "When a nested config string uses an unclosed template on a standalone device it should be rejected",
			mutate: func(f *FirmwareSpec) {
				f.Updater.Config["releases"].([]any)[0].(map[string]any)["profiles"] = []any{unclosedTemplate}
			},
			wantErrs: []string{"spec.firmware.updater.config.releases[0].profiles[0]: Invalid value"},
			wantText: []string{standaloneDetail},
		},
		{
			name:     "When a config key uses template parameters on a standalone device it should be rejected",
			mutate:   func(f *FirmwareSpec) { f.Updater.Config[labelParameter] = "value" },
			wantErrs: []string{"template parameters are not supported in configuration keys"},
		},
		{
			name:     "When a component name uses template parameters on a standalone device it should be rejected",
			mutate:   func(f *FirmwareSpec) { f.Components[0].Name = labelParameter },
			wantErrs: []string{"spec.firmware.components[0].name: Invalid value"},
			wantText: []string{forbiddenDetail},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := newTestFirmwareSpec(t)
			tt.mutate(&spec)
			errs := spec.Validate(tt.fleetTemplate)
			requireFirmwareErrors(t, errs, tt.wantErrs)
			for _, text := range tt.wantText {
				assert.Contains(t, errors.Join(errs...).Error(), text)
			}
		})
	}
}

func TestFirmwareSpecUpdaterIdentityTemplated(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(f *FirmwareSpec)
		want   bool
	}{
		{name: "When the updater name and version are literal it should report a concrete identity", mutate: func(f *FirmwareSpec) {}},
		{
			name:   "When the updater name uses template parameters it should report a templated identity",
			mutate: func(f *FirmwareSpec) { f.Updater.Name = "{{ .metadata.labels.updater }}" },
			want:   true,
		},
		{
			name:   "When the updater version uses template parameters it should report a templated identity",
			mutate: func(f *FirmwareSpec) { f.Updater.Version = "0.{{ .metadata.labels.minor }}.0" },
			want:   true,
		},
		{
			name:   "When only the profile uses template parameters it should report a concrete identity",
			mutate: func(f *FirmwareSpec) { f.Profile = "{{ .metadata.labels.profile }}" },
		},
		{
			name:   "When only a config string uses template parameters it should report a concrete identity",
			mutate: func(f *FirmwareSpec) { firmwareConfigResource(f, 0)["targetGuid"] = "{{ .metadata.labels.guid }}" },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := newTestFirmwareSpec(t)
			tt.mutate(&spec)
			assert.Equal(t, tt.want, spec.UpdaterIdentityTemplated())
		})
	}
}

func TestResourceValidationIncludesFirmware(t *testing.T) {
	templatedProfile := func(t *testing.T) *FirmwareSpec {
		spec := newTestFirmwareSpec(t)
		spec.Profile = "{{ .metadata.labels.profile }}"
		return &spec
	}

	t.Run("When a device spec has invalid firmware it should report the firmware errors", func(t *testing.T) {
		firmware := newTestFirmwareSpec(t)
		firmware.Components[0].ArtifactDigest = lo.ToPtr("sha256:short")
		requireFirmwareErrors(t, DeviceSpec{Firmware: &firmware}.Validate(false), []string{"spec.firmware.components[0].artifactDigest: Invalid value"})
	})

	t.Run("When a device spec has no firmware it should not report firmware errors", func(t *testing.T) {
		requireFirmwareErrors(t, DeviceSpec{}.Validate(false), nil)
	})

	t.Run("When a standalone device uses firmware template parameters it should be rejected", func(t *testing.T) {
		device := Device{Metadata: ObjectMeta{Name: lo.ToPtr("orin-1")}, Spec: &DeviceSpec{Firmware: templatedProfile(t)}}
		requireFirmwareErrors(t, device.Validate(), []string{"spec.firmware.profile: Invalid value"})
	})

	t.Run("When a fleet template uses firmware template parameters it should pass", func(t *testing.T) {
		fleet := Fleet{
			Metadata: ObjectMeta{Name: lo.ToPtr("orin-fleet")},
			Spec: FleetSpec{
				Selector: &LabelSelector{MatchLabels: &map[string]string{"board": "orin"}},
			},
		}
		fleet.Spec.Template.Spec.Firmware = templatedProfile(t)
		requireFirmwareErrors(t, fleet.Validate(), nil)
	})
}

func TestFirmwareStatusValidate(t *testing.T) {
	tests := []struct {
		name     string
		mutate   func(s *FirmwareStatus)
		wantErrs []string
	}{
		{
			name:   "When the firmware status is complete it should pass",
			mutate: func(s *FirmwareStatus) {},
		},
		{
			name:   "When no attempt is reported it should pass",
			mutate: func(s *FirmwareStatus) { s.Attempt = nil },
		},
		{
			name:     "When the attempt phase is not a known phase it should be rejected",
			mutate:   func(s *FirmwareStatus) { s.Attempt.Phase = "Flashing" },
			wantErrs: []string{`status.firmware.attempt.phase: Unsupported value: "Flashing"`},
		},
		{
			name:     "When the attempt phase is empty it should be required",
			mutate:   func(s *FirmwareStatus) { s.Attempt.Phase = "" },
			wantErrs: []string{"status.firmware.attempt.phase: Required value"},
		},
		{
			name: "When a canceled attempt has no cancellation reason it should be rejected",
			mutate: func(s *FirmwareStatus) {
				s.Attempt.Phase = FirmwareAttemptPhaseCanceled
				s.Attempt.CancellationReason = nil
			},
			wantErrs: []string{"status.firmware.attempt.cancellationReason: Required value"},
		},
		{
			name: "When a canceled attempt was superseded it should pass",
			mutate: func(s *FirmwareStatus) {
				s.Attempt.Phase = FirmwareAttemptPhaseCanceled
				s.Attempt.CancellationReason = lo.ToPtr(FirmwareCancellationReasonSuperseded)
			},
		},
		{
			name: "When the cancellation reason is not a known reason it should be rejected",
			mutate: func(s *FirmwareStatus) {
				s.Attempt.Phase = FirmwareAttemptPhaseCanceled
				s.Attempt.CancellationReason = lo.ToPtr(FirmwareCancellationReason("OperatorRequest"))
			},
			wantErrs: []string{`status.firmware.attempt.cancellationReason: Unsupported value: "OperatorRequest"`},
		},
		{
			name:     "When the attempt retry generation is negative it should be rejected",
			mutate:   func(s *FirmwareStatus) { s.Attempt.RetryGeneration = -1 },
			wantErrs: []string{"status.firmware.attempt.retryGeneration: Invalid value: -1"},
		},
		{
			name:     "When the attempt error has no reason it should be required",
			mutate:   func(s *FirmwareStatus) { s.Attempt.Error.Reason = "" },
			wantErrs: []string{"status.firmware.attempt.error.reason: Required value"},
		},
		{
			name:     "When the attempt error has no message it should be required",
			mutate:   func(s *FirmwareStatus) { s.Attempt.Error.Message = "" },
			wantErrs: []string{"status.firmware.attempt.error.message: Required value"},
		},
		{
			name:     "When the support contract version is below 1 it should be rejected",
			mutate:   func(s *FirmwareStatus) { s.Support.ApiVersion = 0 },
			wantErrs: []string{"status.firmware.support.apiVersion: Invalid value: 0"},
		},
		{
			name:     "When a component type is not firmware or overlay it should be rejected",
			mutate:   func(s *FirmwareStatus) { s.Components[0].Type = "bootloader" },
			wantErrs: []string{`status.firmware.components[0].type: Unsupported value: "bootloader"`},
		},
		{
			name:     "When a component type is empty it should be required",
			mutate:   func(s *FirmwareStatus) { s.Components[1].Type = "" },
			wantErrs: []string{"status.firmware.components[1].type: Required value"},
		},
		{
			name:     "When a component state is not a known state it should be rejected",
			mutate:   func(s *FirmwareStatus) { s.Components[0].State = "Flashed" },
			wantErrs: []string{`status.firmware.components[0].state: Unsupported value: "Flashed"`},
		},
		{
			name:     "When a component state is empty it should be required",
			mutate:   func(s *FirmwareStatus) { s.Components[0].State = "" },
			wantErrs: []string{"status.firmware.components[0].state: Required value"},
		},
		{
			name: "When a component error lacks its reason and message it should require both",
			mutate: func(s *FirmwareStatus) {
				s.Components[0].Error.Reason = ""
				s.Components[0].Error.Message = ""
			},
			wantErrs: []string{
				"status.firmware.components[0].error.reason: Required value",
				"status.firmware.components[0].error.message: Required value",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status := newTestFirmwareStatus(t)
			tt.mutate(&status)
			requireFirmwareErrors(t, status.Validate(), tt.wantErrs)
		})
	}
}

func TestFirmwareStatusValidateExceededBudgetPresence(t *testing.T) {
	type location struct {
		name  string
		path  string
		error func(s *FirmwareStatus) *FirmwareError
	}
	locations := []location{
		{name: "attempt", path: "status.firmware.attempt.error.exceededBudget", error: func(s *FirmwareStatus) *FirmwareError { return s.Attempt.Error }},
		{name: "component", path: "status.firmware.components[0].error.exceededBudget", error: func(s *FirmwareStatus) *FirmwareError { return s.Components[0].Error }},
	}
	tests := []struct {
		name    string
		reason  string
		budget  *string
		wantErr string
	}{
		{name: "a limit failure that names its budget", reason: FirmwareErrorReasonResourceLimitExceeded, budget: lo.ToPtr("maxArtifactBytes")},
		{name: "a limit failure without a budget", reason: FirmwareErrorReasonResourceLimitExceeded, wantErr: "Required value"},
		{name: "a limit failure with an empty budget", reason: FirmwareErrorReasonResourceLimitExceeded, budget: lo.ToPtr(""), wantErr: "Required value"},
		{name: "an unauthenticated artifact failure that names a budget", reason: "ArtifactUnauthenticated", budget: lo.ToPtr("maxArtifactBytes"), wantErr: "Forbidden"},
		{name: "a digest mismatch that names a budget", reason: "ArtifactDigestMismatch", budget: lo.ToPtr("maxArtifactBytes"), wantErr: "Forbidden"},
		{name: "a failure with another reason that names an unknown budget", reason: "StagingFailed", budget: lo.ToPtr("noSuchBudget"), wantErr: "Forbidden"},
		{name: "a failure with another reason and an empty budget", reason: "ArtifactUnauthenticated", budget: lo.ToPtr(""), wantErr: "Forbidden"},
		{name: "an unauthenticated artifact failure without a budget", reason: "ArtifactUnauthenticated"},
		{name: "a failure with an unlisted reason and no budget", reason: "VendorSpecificFailure"},
	}
	for _, loc := range locations {
		for _, tt := range tests {
			outcome := "it should pass"
			if tt.wantErr != "" {
				outcome = "it should reject the exceeded budget"
			}
			t.Run("When the "+loc.name+" reports "+tt.name+" "+outcome, func(t *testing.T) {
				status := newTestFirmwareStatus(t)
				status.Attempt.Error = &FirmwareError{Reason: "StagingFailed", Message: "staging failed", Retryable: false}
				status.Components[0].Error = &FirmwareError{Reason: "ArtifactDigestMismatch", Message: "digest mismatch", Retryable: false}
				firmwareErr := loc.error(&status)
				firmwareErr.Reason = tt.reason
				firmwareErr.ExceededBudget = tt.budget
				if tt.wantErr == "" {
					requireFirmwareErrors(t, status.Validate(), nil)
					return
				}
				requireFirmwareErrors(t, status.Validate(), []string{loc.path + ": " + tt.wantErr})
			})
		}
	}
}

func TestFirmwareStatusValidateAcceptsEveryEnumValue(t *testing.T) {
	phases := []FirmwareAttemptPhase{
		FirmwareAttemptPhasePreparing, FirmwareAttemptPhaseWaitingForPolicy, FirmwareAttemptPhaseStaging,
		FirmwareAttemptPhaseAwaitingReboot, FirmwareAttemptPhaseVerifying, FirmwareAttemptPhaseSucceeded,
		FirmwareAttemptPhaseFailed, FirmwareAttemptPhaseCanceled,
	}
	for _, phase := range phases {
		t.Run("When the attempt phase is "+string(phase)+" it should pass", func(t *testing.T) {
			status := newTestFirmwareStatus(t)
			status.Attempt.Phase = phase
			if phase == FirmwareAttemptPhaseCanceled {
				status.Attempt.CancellationReason = lo.ToPtr(FirmwareCancellationReasonSuperseded)
			}
			requireFirmwareErrors(t, status.Validate(), nil)
		})
	}

	for _, componentType := range []FirmwareComponentType{FirmwareComponentTypeFirmware, FirmwareComponentTypeOverlay} {
		for _, state := range []FirmwareComponentState{FirmwareComponentStateUnknown, FirmwareComponentStatePending, FirmwareComponentStateActive, FirmwareComponentStateFailed} {
			t.Run("When a "+string(componentType)+" component is "+string(state)+" it should pass", func(t *testing.T) {
				status := newTestFirmwareStatus(t)
				status.Components[0].Type = componentType
				status.Components[0].State = state
				requireFirmwareErrors(t, status.Validate(), nil)
			})
		}
	}
}

func TestFirmwareStatusIdentityValidation(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*FirmwareStatus)
		path   string
	}{
		{"When an attempt ID is empty it should reject it", func(s *FirmwareStatus) { s.Attempt.Id = "" }, "attempt.id"},
		{"When a target hash is empty it should reject it", func(s *FirmwareStatus) { s.Attempt.SpecHash = "" }, "attempt.specHash"},
		{"When a boot ID is empty it should reject it", func(s *FirmwareStatus) { s.BootID = "" }, "firmware.bootID"},
		{"When a component name is empty it should reject it", func(s *FirmwareStatus) { s.Components[0].Name = "" }, "components[0].name"},
		{"When a component name is invalid it should reject it", func(s *FirmwareStatus) { s.Components[0].Name = "Bad_Name" }, "components[0].name"},
		{"When status names repeat across component types it should reject them", func(s *FirmwareStatus) {
			c := s.Components[0]
			c.Type = FirmwareComponentTypeOverlay
			s.Components = append(s.Components, c)
		}, "name"},
		{"When an error phase is unknown it should reject it", func(s *FirmwareStatus) {
			s.Attempt.Error = &FirmwareError{Reason: "Failed", Message: "failure", Phase: lo.ToPtr(FirmwareAttemptPhase("Stagging"))}
		}, "attempt.error.phase"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status := newTestFirmwareStatus(t)
			tc.mutate(&status)
			requireFirmwareErrors(t, status.Validate(), []string{tc.path})
		})
	}
}
