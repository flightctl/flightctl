package v1beta1

import (
	"encoding/json"
	"testing"

	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOsModeTypeConstants(t *testing.T) {
	require.Equal(t, OsModeType("image"), OsModeImage)
	require.Equal(t, OsModeType("package"), OsModePackage)
}

func TestDeviceStatusCapabilitiesJSON(t *testing.T) {
	tests := []struct {
		name              string
		jsonInput         string
		wantCapabilities  bool
		wantOsMode        *OsModeType
		marshalSource     *DeviceStatus
		wantMarshalOmit   bool
		wantMarshalOsMode string
	}{
		{
			name:             "When capabilities is absent it should leave Capabilities nil",
			jsonInput:        `{"os":{"image":"","imageDigest":""}}`,
			wantCapabilities: false,
		},
		{
			name:             "When capabilities.osMode is image it should unmarshal OsModeImage",
			jsonInput:        `{"os":{"image":"","imageDigest":""},"capabilities":{"osMode":"image"}}`,
			wantCapabilities: true,
			wantOsMode:       lo.ToPtr(OsModeImage),
		},
		{
			name:             "When capabilities.osMode is package it should unmarshal OsModePackage",
			jsonInput:        `{"os":{"image":"","imageDigest":""},"capabilities":{"osMode":"package"}}`,
			wantCapabilities: true,
			wantOsMode:       lo.ToPtr(OsModePackage),
		},
		{
			name:            "When Capabilities is nil it should omit capabilities from JSON",
			marshalSource:   statusWithCapabilities(nil),
			wantMarshalOmit: true,
		},
		{
			name:              "When Capabilities.osMode is set it should include capabilities.osMode in JSON",
			marshalSource:     statusWithCapabilities(lo.ToPtr(OsModePackage)),
			wantMarshalOsMode: "package",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.jsonInput != "" {
				var status DeviceStatus
				require.NoError(t, json.Unmarshal([]byte(tt.jsonInput), &status))
				if !tt.wantCapabilities {
					assert.Nil(t, status.Capabilities)
					return
				}
				require.NotNil(t, status.Capabilities)
				require.NotNil(t, status.Capabilities.OsMode)
				assert.Equal(t, *tt.wantOsMode, *status.Capabilities.OsMode)
				return
			}

			require.NotNil(t, tt.marshalSource)
			data, err := json.Marshal(tt.marshalSource)
			require.NoError(t, err)
			raw := string(data)
			if tt.wantMarshalOmit {
				assert.NotContains(t, raw, `"capabilities"`)
				return
			}
			assert.Contains(t, raw, `"capabilities"`)
			assert.Contains(t, raw, `"osMode":"`+tt.wantMarshalOsMode+`"`)
		})
	}
}

func TestEnrollmentRequestSpecOsModeJSON(t *testing.T) {
	tests := []struct {
		name       string
		jsonInput  string
		wantOsMode *OsModeType
	}{
		{
			name:       "When osMode is absent it should leave OsMode nil",
			jsonInput:  `{"csr":"pem-data"}`,
			wantOsMode: nil,
		},
		{
			name:       "When osMode is image it should unmarshal OsModeImage",
			jsonInput:  `{"csr":"pem-data","osMode":"image"}`,
			wantOsMode: lo.ToPtr(OsModeImage),
		},
		{
			name:       "When osMode is package it should unmarshal OsModePackage",
			jsonInput:  `{"csr":"pem-data","osMode":"package"}`,
			wantOsMode: lo.ToPtr(OsModePackage),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var spec EnrollmentRequestSpec
			require.NoError(t, json.Unmarshal([]byte(tt.jsonInput), &spec))
			if tt.wantOsMode == nil {
				assert.Nil(t, spec.OsMode)
				return
			}
			require.NotNil(t, spec.OsMode)
			assert.Equal(t, *tt.wantOsMode, *spec.OsMode)
		})
	}
}

func statusWithCapabilities(mode *OsModeType) *DeviceStatus {
	status := NewDeviceStatus()
	if mode != nil {
		status.Capabilities = &DeviceCapabilities{OsMode: mode}
	}
	return &status
}
