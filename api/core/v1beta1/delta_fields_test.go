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
