package v1beta1

import (
	"encoding/json"
	"strings"
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

func TestEnrollmentRequestSpecCapabilitiesJSON(t *testing.T) {
	tests := []struct {
		name              string
		jsonInput         string
		wantOsMode        *OsModeType
		wantDeltaEligible *bool
	}{
		{
			name:       "When capabilities is absent it should leave Capabilities nil",
			jsonInput:  `{"csr":"pem-data"}`,
			wantOsMode: nil,
		},
		{
			name:       "When capabilities.osMode is image it should unmarshal OsModeImage",
			jsonInput:  `{"csr":"pem-data","capabilities":{"osMode":"image"}}`,
			wantOsMode: lo.ToPtr(OsModeImage),
		},
		{
			name:       "When capabilities.osMode is package it should unmarshal OsModePackage",
			jsonInput:  `{"csr":"pem-data","capabilities":{"osMode":"package"}}`,
			wantOsMode: lo.ToPtr(OsModePackage),
		},
		{
			name:              "When capabilities.deltaEligible is true it should unmarshal true",
			jsonInput:         `{"csr":"pem-data","capabilities":{"osMode":"image","deltaEligible":true}}`,
			wantOsMode:        lo.ToPtr(OsModeImage),
			wantDeltaEligible: lo.ToPtr(true),
		},
		{
			name:              "When capabilities.deltaEligible is false it should unmarshal false",
			jsonInput:         `{"csr":"pem-data","capabilities":{"osMode":"image","deltaEligible":false}}`,
			wantOsMode:        lo.ToPtr(OsModeImage),
			wantDeltaEligible: lo.ToPtr(false),
		},
		{
			name:       "When top-level osMode is present it should leave Capabilities nil",
			jsonInput:  `{"csr":"pem-data","osMode":"image"}`,
			wantOsMode: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var spec EnrollmentRequestSpec
			require.NoError(t, json.Unmarshal([]byte(tt.jsonInput), &spec))
			if tt.wantOsMode == nil && tt.wantDeltaEligible == nil {
				assert.Nil(t, spec.Capabilities)
				return
			}
			require.NotNil(t, spec.Capabilities)
			if tt.wantOsMode == nil {
				assert.Nil(t, spec.Capabilities.OsMode)
			} else {
				require.NotNil(t, spec.Capabilities.OsMode)
				assert.Equal(t, *tt.wantOsMode, *spec.Capabilities.OsMode)
			}
			assert.Equal(t, tt.wantDeltaEligible, spec.Capabilities.DeltaEligible)
		})
	}
}

func TestEnrollmentRequestSpecMarshalCapabilities(t *testing.T) {
	spec := EnrollmentRequestSpec{
		Csr: "pem-data",
		Capabilities: &DeviceCapabilities{
			OsMode:        lo.ToPtr(OsModeImage),
			DeltaEligible: lo.ToPtr(true),
		},
	}
	data, err := json.Marshal(spec)
	require.NoError(t, err)

	var raw map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(data, &raw))
	_, hasTopLevelOsMode := raw["osMode"]
	assert.False(t, hasTopLevelOsMode)
	require.Contains(t, raw, "capabilities")
}

type enrollmentRequestValidateOsModeCase struct {
	name      string
	osMode    *OsModeType
	wantError bool
}

func enrollmentRequestWithOsMode(mode *OsModeType) EnrollmentRequest {
	er := EnrollmentRequest{
		Metadata: ObjectMeta{Name: lo.ToPtr("test-er")},
		Spec:     EnrollmentRequestSpec{Csr: "pem-data"},
	}
	if mode != nil {
		er.Spec.Capabilities = &DeviceCapabilities{OsMode: mode}
	}
	return er
}

// TestEnrollmentRequestValidateOsMode verifies EnrollmentRequest.Validate accepts
// nil/image/package capabilities.osMode and rejects other values with a spec.capabilities.osMode error.
func TestEnrollmentRequestValidateOsMode(t *testing.T) {
	tests := []enrollmentRequestValidateOsModeCase{
		{
			name:   "When osMode is absent it should pass validation",
			osMode: nil,
		},
		{
			name:   "When osMode is image it should pass validation",
			osMode: lo.ToPtr(OsModeImage),
		},
		{
			name:   "When osMode is package it should pass validation",
			osMode: lo.ToPtr(OsModePackage),
		},
		{
			name:      "When osMode is invalid it should return an error",
			osMode:    lo.ToPtr(OsModeType("foo")),
			wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			er := enrollmentRequestWithOsMode(tt.osMode)
			errs := er.Validate()
			if tt.wantError {
				require.NotEmpty(t, errs)
				found := false
				for _, e := range errs {
					if strings.Contains(e.Error(), "spec.capabilities.osMode") {
						found = true
						break
					}
				}
				assert.True(t, found, "expected an error mentioning spec.capabilities.osMode, got: %v", errs)
			} else {
				for _, e := range errs {
					if strings.Contains(e.Error(), "spec.capabilities.osMode") {
						t.Errorf("unexpected osMode error: %v", e)
					}
				}
			}
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
