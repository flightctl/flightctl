package v1beta1

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// firmwareSubdocument round-trips value through JSON and returns the JSON
// found at path in the marshaled output.
func firmwareSubdocument(t *testing.T, value any, path ...string) string {
	t.Helper()
	raw, err := json.Marshal(value)
	require.NoError(t, err)
	var current any
	require.NoError(t, json.Unmarshal(raw, &current))
	for _, key := range path {
		object, ok := current.(map[string]any)
		require.True(t, ok, "expected an object before %q", key)
		current, ok = object[key]
		require.True(t, ok, "missing %q in %s", key, raw)
	}
	out, err := json.Marshal(current)
	require.NoError(t, err)
	return string(out)
}

func TestDeviceSpecFirmwareJSONRoundTrip(t *testing.T) {
	input := `{"firmware":` + testFirmwareSpecJSON + `}`

	var spec DeviceSpec
	require.NoError(t, json.Unmarshal([]byte(input), &spec))

	require.NotNil(t, spec.Firmware)
	firmware := spec.Firmware
	require.NotNil(t, firmware.RetryGeneration)
	assert.Equal(t, int64(1), *firmware.RetryGeneration)
	assert.Equal(t, "orin-example-profile", firmware.Profile)
	assert.Equal(t, "nvidia-orin", firmware.Updater.Name)
	assert.Equal(t, "0.1.0", firmware.Updater.Version)
	assert.Equal(t, 3.0, firmware.Updater.Config["maxStagingAttempts"])
	assert.Equal(t, true, firmware.Updater.Config["verifyActivation"])
	require.Len(t, firmware.Components, 2)
	assert.Equal(t, "system", firmware.Components[0].Name)
	assert.Equal(t, "36.5.2", *firmware.Components[0].Version.Exact)
	assert.Equal(t, "36.4.2", *firmware.Components[0].Version.Minimum)
	assert.Equal(t, "36.5.2", *firmware.Components[0].Version.Maximum)
	assert.Equal(t, testFirmwareDigest, *firmware.Components[0].ArtifactDigest)
	assert.True(t, *firmware.Components[1].Version.Latest)
	assert.Nil(t, firmware.Components[1].ArtifactDigest)
	require.Len(t, firmware.Overlays, 1)
	assert.Equal(t, "camera", firmware.Overlays[0].Name)
	require.Len(t, firmware.Overlays[0].CompatibleFirmware, 2)
	assert.Equal(t, "bootloader", firmware.Overlays[0].CompatibleFirmware[1].Component)
	assert.Equal(t, "36.4.2", *firmware.Overlays[0].CompatibleFirmware[1].Minimum)

	out, err := json.Marshal(spec)
	require.NoError(t, err)
	assert.JSONEq(t, input, string(out), "every firmware field, including updater-defined config, must survive a round trip")
}

func TestFleetTemplateFirmwareJSONRoundTrip(t *testing.T) {
	input := `{
	  "apiVersion": "v1beta1",
	  "kind": "Fleet",
	  "metadata": {"name": "orin-fleet"},
	  "spec": {
	    "selector": {"matchLabels": {"board": "orin"}},
	    "template": {
	      "metadata": {"labels": {"board": "orin"}},
	      "spec": {"firmware": ` + testFirmwareSpecJSON + `}
	    }
	  }
	}`

	var fleet Fleet
	require.NoError(t, json.Unmarshal([]byte(input), &fleet))
	require.NotNil(t, fleet.Spec.Template.Spec.Firmware)

	assert.JSONEq(t, testFirmwareSpecJSON, firmwareSubdocument(t, fleet, "spec", "template", "spec", "firmware"))
}

func TestDeviceStatusFirmwareJSONRoundTrip(t *testing.T) {
	var status DeviceStatus
	require.NoError(t, json.Unmarshal([]byte(`{"firmware":`+testFirmwareStatusJSON+`}`), &status))

	require.NotNil(t, status.Firmware)
	firmware := status.Firmware
	assert.Equal(t, 1, firmware.Support.ApiVersion)
	assert.True(t, firmware.Support.Supported)
	require.Len(t, firmware.Support.Updaters, 1)
	assert.True(t, *firmware.Support.Updaters[0].Cached)
	require.NotNil(t, firmware.Attempt)
	assert.Equal(t, FirmwareAttemptPhaseFailed, firmware.Attempt.Phase)
	assert.Equal(t, int64(1), firmware.Attempt.RetryGeneration)
	require.NotNil(t, firmware.Attempt.Updater)
	assert.Equal(t, testFirmwareDigest, *firmware.Attempt.Updater.ManifestDigest)
	require.NotNil(t, firmware.Attempt.Error)
	assert.Equal(t, "maxArtifactBytes", *firmware.Attempt.Error.ExceededBudget)
	require.Len(t, firmware.Attempt.Recovery, 1)
	assert.Equal(t, "https://example.com/firmware/recovery", *firmware.Attempt.Recovery[0].DocumentationURL)
	require.Len(t, firmware.Components, 2)
	system := firmware.Components[0]
	assert.Equal(t, FirmwareComponentTypeFirmware, system.Type)
	assert.Equal(t, FirmwareComponentStateFailed, system.State)
	assert.Equal(t, "36.5.2", *system.Selected.Version)
	assert.Equal(t, testFirmwareDigest, system.Selected.Source.Digest)
	assert.Equal(t, "36.4.4", *system.Actual.Version)
	require.NotNil(t, system.Observation)
	require.NotNil(t, system.Observation.Backend)
	assert.Contains(t, system.Observation.Backend.Data, "esrt", "updater-defined observation data must be retained")
	assert.Equal(t, FirmwareComponentTypeOverlay, firmware.Components[1].Type)
	assert.False(t, *firmware.Components[1].Actual.Active)

	assert.JSONEq(t, testFirmwareStatusJSON, firmwareSubdocument(t, status, "firmware"), "every firmware status field must survive a round trip")
}

func TestCanceledFirmwareAttemptJSONRoundTrip(t *testing.T) {
	var attempt FirmwareAttemptStatus
	require.NoError(t, json.Unmarshal([]byte(testCanceledFirmwareAttemptJSON), &attempt))

	assert.Equal(t, FirmwareAttemptPhaseCanceled, attempt.Phase)
	require.NotNil(t, attempt.CancellationReason)
	assert.Equal(t, FirmwareCancellationReasonSuperseded, *attempt.CancellationReason)
	assert.Nil(t, attempt.Updater, "an attempt rejected before updater selection has no pinned updater")

	out, err := json.Marshal(attempt)
	require.NoError(t, err)
	assert.JSONEq(t, testCanceledFirmwareAttemptJSON, string(out))
}

func TestAbsentFirmwareStaysAbsent(t *testing.T) {
	var spec DeviceSpec
	require.NoError(t, json.Unmarshal([]byte(`{"os":{"image":"quay.io/example/os:1"}}`), &spec))
	assert.Nil(t, spec.Firmware, "a spec without firmware leaves firmware unmanaged")
	out, err := json.Marshal(spec)
	require.NoError(t, err)
	assert.NotContains(t, string(out), "firmware")

	var status DeviceStatus
	require.NoError(t, json.Unmarshal([]byte(`{"conditions":[],"applications":[]}`), &status))
	assert.Nil(t, status.Firmware)
	out, err = json.Marshal(status)
	require.NoError(t, err)
	assert.NotContains(t, string(out), "firmware")
}

func TestFirmwareTypesRejectMismatchedJSONTypes(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(firmware map[string]any)
	}{
		{
			name:   "When updater.config is a JSON array it should fail to decode",
			mutate: func(f map[string]any) { f["updater"].(map[string]any)["config"] = []any{"resources"} },
		},
		{
			name:   "When updater.config is a JSON string it should fail to decode",
			mutate: func(f map[string]any) { f["updater"].(map[string]any)["config"] = "resources" },
		},
		{
			name: "When the latest constraint holds template syntax it should fail to decode",
			mutate: func(f map[string]any) {
				f["components"].([]any)[1].(map[string]any)["version"].(map[string]any)["latest"] = "{{ .metadata.labels.latest }}"
			},
		},
		{
			name:   "When retryGeneration holds template syntax it should fail to decode",
			mutate: func(f map[string]any) { f["retryGeneration"] = "{{ .metadata.labels.retry }}" },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var firmware map[string]any
			require.NoError(t, json.Unmarshal([]byte(testFirmwareSpecJSON), &firmware))
			tt.mutate(firmware)
			raw, err := json.Marshal(map[string]any{"firmware": firmware})
			require.NoError(t, err)

			var spec DeviceSpec
			assert.Error(t, json.Unmarshal(raw, &spec))
		})
	}
}
