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

func TestDeviceOsStatusLastUpdateFallbackReasonJSON(t *testing.T) {
	tests := []struct {
		name            string
		jsonInput       string
		wantReason      *string
		marshalSource   DeviceOsStatus
		wantMarshalOmit bool
		wantMarshalVal  string
	}{
		{
			name:       "When lastUpdateFallbackReason is absent it should leave the field nil",
			jsonInput:  `{"image":"quay.io/acme/os:latest","imageDigest":"sha256:bbb"}`,
			wantReason: nil,
		},
		{
			name:       "When lastUpdateFallbackReason is set it should unmarshal the reason",
			jsonInput:  `{"image":"quay.io/acme/os:latest","imageDigest":"sha256:bbb","lastUpdateFallbackReason":"delta apply failed"}`,
			wantReason: lo.ToPtr("delta apply failed"),
		},
		{
			name:            "When LastUpdateFallbackReason is nil it should omit the field from JSON",
			marshalSource:   DeviceOsStatus{Image: "quay.io/acme/os:latest", ImageDigest: "sha256:bbb"},
			wantMarshalOmit: true,
		},
		{
			name:           "When LastUpdateFallbackReason is set it should include the field in JSON",
			marshalSource:  DeviceOsStatus{Image: "quay.io/acme/os:latest", ImageDigest: "sha256:bbb", LastUpdateFallbackReason: lo.ToPtr("delta apply failed")},
			wantMarshalVal: "delta apply failed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.jsonInput != "" {
				var status DeviceOsStatus
				require.NoError(t, json.Unmarshal([]byte(tt.jsonInput), &status))
				assert.Equal(t, tt.wantReason, status.LastUpdateFallbackReason)
				return
			}

			data, err := json.Marshal(tt.marshalSource)
			require.NoError(t, err)
			raw := string(data)
			if tt.wantMarshalOmit {
				assert.NotContains(t, raw, `"lastUpdateFallbackReason"`)
				return
			}
			assert.Contains(t, raw, `"lastUpdateFallbackReason":"`+tt.wantMarshalVal+`"`)
		})
	}
}

func TestDeviceCapabilitiesDeltaEligibleJSON(t *testing.T) {
	tests := []struct {
		name              string
		jsonInput         string
		wantDeltaEligible *bool
		marshalSource     DeviceCapabilities
		wantMarshalOmit   bool
		wantMarshalVal    string
	}{
		{
			name:              "When deltaEligible is absent it should leave DeltaEligible nil",
			jsonInput:         `{"osMode":"image"}`,
			wantDeltaEligible: nil,
		},
		{
			name:              "When deltaEligible is true it should unmarshal true",
			jsonInput:         `{"osMode":"image","deltaEligible":true}`,
			wantDeltaEligible: lo.ToPtr(true),
		},
		{
			name:              "When deltaEligible is false it should unmarshal false",
			jsonInput:         `{"osMode":"image","deltaEligible":false}`,
			wantDeltaEligible: lo.ToPtr(false),
		},
		{
			name:            "When DeltaEligible is nil it should omit deltaEligible from JSON",
			marshalSource:   DeviceCapabilities{OsMode: lo.ToPtr(OsModeImage)},
			wantMarshalOmit: true,
		},
		{
			name:           "When DeltaEligible is true it should include deltaEligible true in JSON",
			marshalSource:  DeviceCapabilities{OsMode: lo.ToPtr(OsModeImage), DeltaEligible: lo.ToPtr(true)},
			wantMarshalVal: "true",
		},
		{
			name:           "When DeltaEligible is false it should include deltaEligible false in JSON",
			marshalSource:  DeviceCapabilities{OsMode: lo.ToPtr(OsModeImage), DeltaEligible: lo.ToPtr(false)},
			wantMarshalVal: "false",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.jsonInput != "" {
				var caps DeviceCapabilities
				require.NoError(t, json.Unmarshal([]byte(tt.jsonInput), &caps))
				assert.Equal(t, tt.wantDeltaEligible, caps.DeltaEligible)
				return
			}

			data, err := json.Marshal(tt.marshalSource)
			require.NoError(t, err)
			raw := string(data)
			if tt.wantMarshalOmit {
				assert.NotContains(t, raw, `"deltaEligible"`)
				return
			}
			assert.Contains(t, raw, `"deltaEligible":`+tt.wantMarshalVal)
		})
	}
}

func TestDeviceUpdatedStatusSizeJSON(t *testing.T) {
	tests := []struct {
		name            string
		jsonInput       string
		wantSize        *string
		marshalSource   DeviceUpdatedStatus
		wantMarshalOmit bool
		wantMarshalVal  string
	}{
		{
			name:      "When size is absent it should leave Size nil",
			jsonInput: `{"status":"UpToDate"}`,
			wantSize:  nil,
		},
		{
			name:      "When size is set it should unmarshal the IEC size",
			jsonInput: `{"status":"Updating","size":"45 MiB"}`,
			wantSize:  lo.ToPtr("45 MiB"),
		},
		{
			name:            "When Size is nil it should omit size from JSON",
			marshalSource:   DeviceUpdatedStatus{Status: DeviceUpdatedStatusUpToDate},
			wantMarshalOmit: true,
		},
		{
			name:           "When Size is set it should include size in JSON",
			marshalSource:  DeviceUpdatedStatus{Status: DeviceUpdatedStatusUpdating, Size: lo.ToPtr("45 MiB")},
			wantMarshalVal: "45 MiB",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.jsonInput != "" {
				var updated DeviceUpdatedStatus
				require.NoError(t, json.Unmarshal([]byte(tt.jsonInput), &updated))
				assert.Equal(t, tt.wantSize, updated.Size)
				return
			}

			data, err := json.Marshal(tt.marshalSource)
			require.NoError(t, err)
			raw := string(data)
			if tt.wantMarshalOmit {
				assert.NotContains(t, raw, `"size"`)
				return
			}
			assert.Contains(t, raw, `"size":"`+tt.wantMarshalVal+`"`)
		})
	}
}

func TestDevicesSummaryCapabilitiesDeltaEligibleJSON(t *testing.T) {
	tests := []struct {
		name            string
		jsonInput       string
		wantNil         bool
		wantCounts      map[string]int64
		marshalSource   DevicesSummaryCapabilities
		wantMarshalOmit bool
	}{
		{
			name:      "When deltaEligible is absent it should leave DeltaEligible nil",
			jsonInput: `{"osMode":{"image":2}}`,
			wantNil:   true,
		},
		{
			name:       "When deltaEligible is set it should unmarshal true false and unknown counts",
			jsonInput:  `{"deltaEligible":{"true":1,"false":2,"unknown":3}}`,
			wantCounts: map[string]int64{"true": 1, "false": 2, "unknown": 3},
		},
		{
			name:            "When DeltaEligible is nil it should omit deltaEligible from JSON",
			marshalSource:   DevicesSummaryCapabilities{},
			wantMarshalOmit: true,
		},
		{
			name:          "When DeltaEligible is set it should include the counts in JSON",
			marshalSource: DevicesSummaryCapabilities{DeltaEligible: &map[string]int64{"true": 4, "false": 1}},
			wantCounts:    map[string]int64{"true": 4, "false": 1},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.jsonInput != "" {
				var caps DevicesSummaryCapabilities
				require.NoError(t, json.Unmarshal([]byte(tt.jsonInput), &caps))
				if tt.wantNil {
					assert.Nil(t, caps.DeltaEligible)
					return
				}
				require.NotNil(t, caps.DeltaEligible)
				assert.Equal(t, tt.wantCounts, *caps.DeltaEligible)
				return
			}

			data, err := json.Marshal(tt.marshalSource)
			require.NoError(t, err)
			raw := string(data)
			if tt.wantMarshalOmit {
				assert.NotContains(t, raw, `"deltaEligible"`)
				return
			}
			var caps DevicesSummaryCapabilities
			require.NoError(t, json.Unmarshal(data, &caps))
			require.NotNil(t, caps.DeltaEligible)
			assert.Equal(t, tt.wantCounts, *caps.DeltaEligible)
		})
	}
}

func TestNewDeviceStatusDoesNotInventDeltaFields(t *testing.T) {
	status := NewDeviceStatus()
	assert.Nil(t, status.Capabilities)
	assert.Nil(t, status.Os.LastUpdateFallbackReason)
	assert.Nil(t, status.Updated.Size)
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
