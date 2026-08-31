package v1beta1

import (
	"encoding/json"
	"testing"

	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDeviceOsSpecDeltaImageJSON(t *testing.T) {
	tests := []struct {
		name            string
		jsonInput       string
		wantDeltaImage  *string
		marshalSource   DeviceOsSpec
		wantMarshalOmit bool
		wantMarshalVal  string
	}{
		{
			name:           "When deltaImage is absent it should leave DeltaImage nil",
			jsonInput:      `{"image":"quay.io/acme/os:latest"}`,
			wantDeltaImage: nil,
		},
		{
			name:           "When deltaImage is set it should unmarshal the hint",
			jsonInput:      `{"image":"quay.io/acme/os@sha256:bbb","deltaImage":"quay.io/acme/os@sha256:ddd"}`,
			wantDeltaImage: lo.ToPtr("quay.io/acme/os@sha256:ddd"),
		},
		{
			name:            "When DeltaImage is nil it should omit deltaImage from JSON",
			marshalSource:   DeviceOsSpec{Image: "quay.io/acme/os:latest"},
			wantMarshalOmit: true,
		},
		{
			name:           "When DeltaImage is set it should include deltaImage in JSON",
			marshalSource:  DeviceOsSpec{Image: "quay.io/acme/os@sha256:bbb", DeltaImage: lo.ToPtr("quay.io/acme/os@sha256:ddd")},
			wantMarshalVal: "quay.io/acme/os@sha256:ddd",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.jsonInput != "" {
				var spec DeviceOsSpec
				require.NoError(t, json.Unmarshal([]byte(tt.jsonInput), &spec))
				assert.Equal(t, tt.wantDeltaImage, spec.DeltaImage)
				return
			}

			data, err := json.Marshal(tt.marshalSource)
			require.NoError(t, err)
			raw := string(data)
			if tt.wantMarshalOmit {
				assert.NotContains(t, raw, `"deltaImage"`)
				return
			}
			assert.Contains(t, raw, `"deltaImage":"`+tt.wantMarshalVal+`"`)
		})
	}
}

func TestDeviceDeltaApplyStatusJSON(t *testing.T) {
	tests := []struct {
		name            string
		jsonInput       string
		wantReason      *string
		wantSize        *string
		marshalSource   DeviceOsStatus
		wantMarshalOmit string
		wantMarshalJSON string
	}{
		{
			name:       "When lastDelta is absent it should leave LastDelta nil",
			jsonInput:  `{"image":"quay.io/acme/os:latest","imageDigest":"sha256:bbb"}`,
			wantReason: nil,
			wantSize:   nil,
		},
		{
			name:       "When lastDelta.fallbackReason is set it should unmarshal the reason",
			jsonInput:  `{"image":"quay.io/acme/os:latest","imageDigest":"sha256:bbb","lastDelta":{"fallbackReason":"delta apply failed"}}`,
			wantReason: lo.ToPtr("delta apply failed"),
		},
		{
			name:      "When lastDelta.size is set it should unmarshal the IEC size",
			jsonInput: `{"image":"quay.io/acme/os:latest","imageDigest":"sha256:bbb","lastDelta":{"size":"45 MiB"}}`,
			wantSize:  lo.ToPtr("45 MiB"),
		},
		{
			name:            "When LastDelta is nil it should omit lastDelta from JSON",
			marshalSource:   DeviceOsStatus{Image: "quay.io/acme/os:latest", ImageDigest: "sha256:bbb"},
			wantMarshalOmit: "lastDelta",
		},
		{
			name: "When LastDelta fallbackReason and size are set it should include lastDelta in JSON",
			marshalSource: DeviceOsStatus{
				Image: "quay.io/acme/os:latest", ImageDigest: "sha256:bbb",
				LastDelta: &DeviceDeltaApplyStatus{FallbackReason: lo.ToPtr("delta apply failed"), Size: lo.ToPtr("45 MiB")},
			},
			wantMarshalJSON: `"lastDelta":{"fallbackReason":"delta apply failed","size":"45 MiB"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.jsonInput != "" {
				var status DeviceOsStatus
				require.NoError(t, json.Unmarshal([]byte(tt.jsonInput), &status))
				if tt.wantReason == nil && tt.wantSize == nil {
					assert.Nil(t, status.LastDelta)
					return
				}
				require.NotNil(t, status.LastDelta)
				assert.Equal(t, tt.wantReason, status.LastDelta.FallbackReason)
				assert.Equal(t, tt.wantSize, status.LastDelta.Size)
				return
			}

			data, err := json.Marshal(tt.marshalSource)
			require.NoError(t, err)
			raw := string(data)
			if tt.wantMarshalOmit != "" {
				assert.NotContains(t, raw, `"`+tt.wantMarshalOmit+`"`)
				return
			}
			assert.Contains(t, raw, tt.wantMarshalJSON)
		})
	}
}

func TestDeviceSystemInfoDeltaFieldsJSON(t *testing.T) {
	tests := []struct {
		name              string
		jsonInput         string
		wantDeltaEligible *bool
		wantBootc         *string
		wantOCI           *string
		marshalSource     DeviceSystemInfo
		wantMarshalOmit   []string
		wantMarshalJSON   string
	}{
		{
			name:              "When deltaEligible is absent it should leave DeltaEligible nil",
			jsonInput:         `{"architecture":"amd64","bootID":"b","operatingSystem":"linux","agentVersion":"v1"}`,
			wantDeltaEligible: nil,
		},
		{
			name:              "When deltaEligible is true it should unmarshal true",
			jsonInput:         `{"architecture":"amd64","bootID":"b","operatingSystem":"linux","agentVersion":"v1","deltaEligible":true,"bootcVersion":"bootc 1.15.0","ociDeltaVersion":"oci-delta 0.2.1"}`,
			wantDeltaEligible: lo.ToPtr(true),
			wantBootc:         lo.ToPtr("bootc 1.15.0"),
			wantOCI:           lo.ToPtr("oci-delta 0.2.1"),
		},
		{
			name:            "When delta fields are nil it should omit them from JSON",
			marshalSource:   DeviceSystemInfo{Architecture: "amd64", BootID: "b", OperatingSystem: "linux", AgentVersion: "v1"},
			wantMarshalOmit: []string{"deltaEligible", "bootcVersion", "ociDeltaVersion"},
		},
		{
			name: "When delta fields are set it should include them in JSON",
			marshalSource: DeviceSystemInfo{
				Architecture:    "amd64",
				BootID:          "b",
				OperatingSystem: "linux",
				AgentVersion:    "v1",
				DeltaEligible:   lo.ToPtr(true),
				BootcVersion:    lo.ToPtr("bootc 1.15.0"),
				OciDeltaVersion: lo.ToPtr("oci-delta 0.2.1"),
			},
			wantMarshalJSON: `"deltaEligible":true`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.jsonInput != "" {
				var info DeviceSystemInfo
				require.NoError(t, json.Unmarshal([]byte(tt.jsonInput), &info))
				assert.Equal(t, tt.wantDeltaEligible, info.DeltaEligible)
				assert.Equal(t, tt.wantBootc, info.BootcVersion)
				assert.Equal(t, tt.wantOCI, info.OciDeltaVersion)
				return
			}

			data, err := json.Marshal(tt.marshalSource)
			require.NoError(t, err)
			raw := string(data)
			for _, omit := range tt.wantMarshalOmit {
				assert.NotContains(t, raw, `"`+omit+`"`)
			}
			if tt.wantMarshalJSON != "" {
				assert.Contains(t, raw, tt.wantMarshalJSON)
				assert.Contains(t, raw, `"bootcVersion":"bootc 1.15.0"`)
				assert.Contains(t, raw, `"ociDeltaVersion":"oci-delta 0.2.1"`)
			}
		})
	}
}

func TestNewDeviceStatusDoesNotInventDeltaFields(t *testing.T) {
	status := NewDeviceStatus()
	assert.Nil(t, status.Capabilities)
	assert.Nil(t, status.Os.LastDelta)
	assert.Nil(t, status.DeltaGeneration)
}

func TestPrepareDeltasDetailsJSON(t *testing.T) {
	tests := []struct {
		name            string
		jsonInput       string
		wantTV          *string
		marshalSource   PrepareDeltasDetails
		wantMarshalOmit bool
		wantMarshalTV   string
	}{
		{
			name:      "When templateVersion is set it should round-trip for a fleet prepare",
			jsonInput: `{"detailType":"PrepareDeltas","templateVersion":"tv-1"}`,
			wantTV:    lo.ToPtr("tv-1"),
		},
		{
			name:      "When templateVersion is omitted it should round-trip for a device prepare",
			jsonInput: `{"detailType":"PrepareDeltas"}`,
			wantTV:    nil,
		},
		{
			name:            "When TemplateVersion is nil it should omit templateVersion from JSON",
			marshalSource:   PrepareDeltasDetails{DetailType: PrepareDeltas},
			wantMarshalOmit: true,
		},
		{
			name:          "When TemplateVersion is set it should include templateVersion in JSON",
			marshalSource: PrepareDeltasDetails{DetailType: PrepareDeltas, TemplateVersion: lo.ToPtr("tv-2")},
			wantMarshalTV: "tv-2",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.jsonInput != "" {
				var details PrepareDeltasDetails
				require.NoError(t, json.Unmarshal([]byte(tt.jsonInput), &details))
				assert.Equal(t, tt.wantTV, details.TemplateVersion)
				assert.NotContains(t, tt.jsonInput, `"rolloutStrategy"`)
				return
			}

			data, err := json.Marshal(tt.marshalSource)
			require.NoError(t, err)
			raw := string(data)
			assert.NotContains(t, raw, `"rolloutStrategy"`)
			if tt.wantMarshalOmit {
				assert.NotContains(t, raw, `"templateVersion"`)
				return
			}
			assert.Contains(t, raw, `"templateVersion":"`+tt.wantMarshalTV+`"`)
		})
	}
}

func TestDeltaGenerationProgressDetailsJSON(t *testing.T) {
	t.Run("When phase is set it should round-trip an in-progress pair", func(t *testing.T) {
		in := `{"detailType":"DeltaGenerationProgress","imageRepository":"quay.io/acme/os","sourceDigest":"sha256:aaa","targetDigest":"sha256:bbb","generationStatus":"in_progress","phase":"createDelta","templateVersion":"tv-1"}`
		var details DeltaGenerationProgressDetails
		require.NoError(t, json.Unmarshal([]byte(in), &details))
		assert.Equal(t, DeltaGenerationProgress, details.DetailType)
		assert.Equal(t, "quay.io/acme/os", details.ImageRepository)
		assert.Equal(t, DeltaGenerationProgressInProgress, details.GenerationStatus)
		require.NotNil(t, details.Phase)
		assert.Equal(t, DeltaGenerationPhaseCreateDelta, *details.Phase)
		assert.Equal(t, lo.ToPtr("tv-1"), details.TemplateVersion)
		assert.NotContains(t, in, `"percent"`)
	})

	t.Run("When generationStatus is failed it should omit phase", func(t *testing.T) {
		src := DeltaGenerationProgressDetails{
			DetailType:       DeltaGenerationProgress,
			ImageRepository:  "quay.io/acme/os",
			SourceDigest:     "sha256:aaa",
			TargetDigest:     "sha256:bbb",
			GenerationStatus: DeltaGenerationProgressFailed,
		}
		data, err := json.Marshal(src)
		require.NoError(t, err)
		raw := string(data)
		assert.Contains(t, raw, `"generationStatus":"failed"`)
		assert.NotContains(t, raw, `"phase"`)
		assert.NotContains(t, raw, `"percent"`)
	})
}

func TestRolloutPolicyGenerateDeltaJSON(t *testing.T) {
	tests := []struct {
		name            string
		jsonInput       string
		want            *bool
		marshalSource   RolloutPolicy
		wantMarshalOmit bool
		wantMarshalVal  bool
	}{
		{
			name:      "When generateDelta is absent it should leave GenerateDelta nil",
			jsonInput: `{}`,
			want:      nil,
		},
		{
			name:      "When generateDelta is false it should unmarshal false",
			jsonInput: `{"generateDelta":false}`,
			want:      lo.ToPtr(false),
		},
		{
			name:      "When generateDelta is true it should unmarshal true",
			jsonInput: `{"generateDelta":true}`,
			want:      lo.ToPtr(true),
		},
		{
			name:            "When GenerateDelta is nil it should omit generateDelta from JSON",
			marshalSource:   RolloutPolicy{},
			wantMarshalOmit: true,
		},
		{
			name:           "When GenerateDelta is false it should include generateDelta in JSON",
			marshalSource:  RolloutPolicy{GenerateDelta: lo.ToPtr(false)},
			wantMarshalVal: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.jsonInput != "" {
				var policy RolloutPolicy
				require.NoError(t, json.Unmarshal([]byte(tt.jsonInput), &policy))
				assert.Equal(t, tt.want, policy.GenerateDelta)
				return
			}

			data, err := json.Marshal(tt.marshalSource)
			require.NoError(t, err)
			raw := string(data)
			if tt.wantMarshalOmit {
				assert.NotContains(t, raw, `"generateDelta"`)
				return
			}
			if tt.wantMarshalVal {
				assert.Contains(t, raw, `"generateDelta":true`)
				return
			}
			assert.Contains(t, raw, `"generateDelta":false`)
		})
	}
}

func TestRolloutPolicyWaitAndTimeoutJSON(t *testing.T) {
	tests := []struct {
		name            string
		jsonInput       string
		wantWait        *Duration
		wantTimeout     *Duration
		marshalSource   RolloutPolicy
		wantMarshalOmit []string
		wantMarshalWait string
	}{
		{
			name:        "When wait and timeout are absent it should leave both nil",
			jsonInput:   `{}`,
			wantWait:    nil,
			wantTimeout: nil,
		},
		{
			name:        "When maxWaitForDelta is 0s it should unmarshal zero duration",
			jsonInput:   `{"maxWaitForDelta":"0s"}`,
			wantWait:    lo.ToPtr(Duration("0s")),
			wantTimeout: nil,
		},
		{
			name:        "When both are set it should unmarshal the durations",
			jsonInput:   `{"maxWaitForDelta":"10m","deltaGenerationTimeout":"30m"}`,
			wantWait:    lo.ToPtr(Duration("10m")),
			wantTimeout: lo.ToPtr(Duration("30m")),
		},
		{
			name:            "When wait and timeout are nil it should omit them from JSON",
			marshalSource:   RolloutPolicy{},
			wantMarshalOmit: []string{`"maxWaitForDelta"`, `"deltaGenerationTimeout"`},
		},
		{
			name:            "When MaxWaitForDelta is 0s it should include maxWaitForDelta in JSON",
			marshalSource:   RolloutPolicy{MaxWaitForDelta: lo.ToPtr(Duration("0s"))},
			wantMarshalWait: "0s",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.jsonInput != "" {
				var policy RolloutPolicy
				require.NoError(t, json.Unmarshal([]byte(tt.jsonInput), &policy))
				assert.Equal(t, tt.wantWait, policy.MaxWaitForDelta)
				assert.Equal(t, tt.wantTimeout, policy.DeltaGenerationTimeout)
				return
			}

			data, err := json.Marshal(tt.marshalSource)
			require.NoError(t, err)
			raw := string(data)
			for _, key := range tt.wantMarshalOmit {
				assert.NotContains(t, raw, key)
			}
			if tt.wantMarshalWait != "" {
				assert.Contains(t, raw, `"maxWaitForDelta":"`+tt.wantMarshalWait+`"`)
			}
		})
	}
}

func TestDeltaGenerationStatusJSON(t *testing.T) {
	t.Run("When deltaGeneration is absent it should leave Fleet and Device status nil", func(t *testing.T) {
		var fleet FleetStatus
		require.NoError(t, json.Unmarshal([]byte(`{}`), &fleet))
		assert.Nil(t, fleet.DeltaGeneration)

		var device DeviceStatus
		require.NoError(t, json.Unmarshal([]byte(`{}`), &device))
		assert.Nil(t, device.DeltaGeneration)
	})

	t.Run("When deltaGeneration is set it should round-trip completed and total", func(t *testing.T) {
		in := `{"completed":1,"total":4}`
		var status DeltaGenerationStatus
		require.NoError(t, json.Unmarshal([]byte(in), &status))
		assert.Equal(t, int64(1), status.Completed)
		assert.Equal(t, int64(4), status.Total)

		data, err := json.Marshal(FleetStatus{Conditions: []Condition{}, DeltaGeneration: &status})
		require.NoError(t, err)
		assert.Contains(t, string(data), `"deltaGeneration"`)
		assert.Contains(t, string(data), `"completed":1`)
		assert.Contains(t, string(data), `"total":4`)
		assert.NotContains(t, string(data), `"phase"`)
		assert.NotContains(t, string(data), `"pairs"`)
	})

	t.Run("When DeltaGeneration is nil it should omit deltaGeneration from JSON", func(t *testing.T) {
		data, err := json.Marshal(FleetStatus{Conditions: []Condition{}})
		require.NoError(t, err)
		assert.NotContains(t, string(data), `"deltaGeneration"`)
	})
}

func TestDeltaPreparingConditionTypes(t *testing.T) {
	assert.Equal(t, ConditionType("FleetDeltaPreparing"), ConditionTypeFleetDeltaPreparing)
	assert.Equal(t, ConditionType("DeviceDeltaPreparing"), ConditionTypeDeviceDeltaPreparing)
}
