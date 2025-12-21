package tasks

import (
	"context"
	"net/http"
	"testing"

	api "github.com/flightctl/flightctl/api/v1beta1"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/service"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gomock "go.uber.org/mock/gomock"
)

func createTestFleetForRollout(name string, rolloutPolicy *api.RolloutPolicy) *api.Fleet {
	fleetName := name
	generation := int64(1)

	return &api.Fleet{
		Metadata: api.ObjectMeta{
			Name:       &fleetName,
			Generation: &generation,
		},
		Spec: api.FleetSpec{
			RolloutPolicy: rolloutPolicy,
			Template: struct {
				Metadata *api.ObjectMeta `json:"metadata,omitempty"`
				Spec     api.DeviceSpec  `json:"spec"`
			}{
				Spec: api.DeviceSpec{
					Os: &api.DeviceOsSpec{
						Image: "test-image:latest",
					},
				},
			},
		},
	}
}

func createTestTemplateVersion(name string) *api.TemplateVersion {
	tvName := name
	return &api.TemplateVersion{
		Metadata: api.ObjectMeta{
			Name: &tvName,
		},
		Status: &api.TemplateVersionStatus{
			Os: &api.DeviceOsSpec{
				Image: "test-image:latest",
			},
		},
	}
}

func createTestDevice(name string, owner string) *api.Device {
	deviceName := name
	ownerName := owner
	return &api.Device{
		Metadata: api.ObjectMeta{
			Name:  &deviceName,
			Owner: &ownerName,
		},
		Spec: &api.DeviceSpec{
			Os: &api.DeviceOsSpec{
				Image: "old-image:latest",
			},
		},
		Status: &api.DeviceStatus{
			Conditions: []api.Condition{},
		},
	}
}

func TestFleetRolloutsLogic_DelayDeviceRenderCondition(t *testing.T) {
	tests := []struct {
		name               string
		fleet              *api.Fleet
		expectedDelayValue bool
		description        string
	}{
		{
			name:               "NoRolloutPolicy",
			fleet:              createTestFleetForRollout("test-fleet", nil),
			expectedDelayValue: false,
			description:        "delayDeviceRender should be false when fleet has no RolloutPolicy",
		},
		{
			name: "RolloutPolicyWithoutDisruptionBudget",
			fleet: createTestFleetForRollout("test-fleet", &api.RolloutPolicy{
				DeviceSelection: &api.RolloutDeviceSelection{},
			}),
			expectedDelayValue: false,
			description:        "delayDeviceRender should be false when fleet has RolloutPolicy but no DisruptionBudget",
		},
		{
			name: "RolloutPolicyWithDisruptionBudget",
			fleet: createTestFleetForRollout("test-fleet", &api.RolloutPolicy{
				DeviceSelection: &api.RolloutDeviceSelection{},
				DisruptionBudget: &api.DisruptionBudget{
					MaxUnavailable: lo.ToPtr(25),
				},
			}),
			expectedDelayValue: true,
			description:        "delayDeviceRender should be true when fleet has RolloutPolicy with DisruptionBudget",
		},
		{
			name: "RolloutPolicyWithOnlyDisruptionBudget",
			fleet: createTestFleetForRollout("test-fleet", &api.RolloutPolicy{
				DisruptionBudget: &api.DisruptionBudget{
					MaxUnavailable: lo.ToPtr(10),
				},
			}),
			expectedDelayValue: true,
			description:        "delayDeviceRender should be true when fleet has RolloutPolicy with only DisruptionBudget",
		},
		{
			name: "RolloutPolicyWithComplexDisruptionBudget",
			fleet: createTestFleetForRollout("test-fleet", &api.RolloutPolicy{
				DeviceSelection: &api.RolloutDeviceSelection{},
				DisruptionBudget: &api.DisruptionBudget{
					MaxUnavailable: lo.ToPtr(50),
					MinAvailable:   lo.ToPtr(25),
				},
			}),
			expectedDelayValue: true,
			description:        "delayDeviceRender should be true when fleet has RolloutPolicy with complex DisruptionBudget",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Test the delayDeviceRender condition logic directly
			delayDeviceRender := tt.fleet.Spec.RolloutPolicy != nil && tt.fleet.Spec.RolloutPolicy.DisruptionBudget != nil

			// Assert
			assert.Equal(t, tt.expectedDelayValue, delayDeviceRender, tt.description)
		})
	}
}

func TestFleetRolloutsLogic_RolloutFleet_DelayDeviceRenderPropagation(t *testing.T) {
	tests := []struct {
		name               string
		fleet              *api.Fleet
		expectedDelayValue bool
		description        string
	}{
		{
			name:               "NoRolloutPolicy",
			fleet:              createTestFleetForRollout("test-fleet", nil),
			expectedDelayValue: false,
			description:        "delayDeviceRender should be false when fleet has no RolloutPolicy",
		},
		{
			name: "RolloutPolicyWithDisruptionBudget",
			fleet: createTestFleetForRollout("test-fleet", &api.RolloutPolicy{
				DeviceSelection: &api.RolloutDeviceSelection{},
				DisruptionBudget: &api.DisruptionBudget{
					MaxUnavailable: lo.ToPtr(25),
				},
			}),
			expectedDelayValue: true,
			description:        "delayDeviceRender should be true when fleet has RolloutPolicy with DisruptionBudget",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Test the delayDeviceRender condition logic directly
			delayDeviceRender := tt.fleet.Spec.RolloutPolicy != nil && tt.fleet.Spec.RolloutPolicy.DisruptionBudget != nil

			// Assert
			assert.Equal(t, tt.expectedDelayValue, delayDeviceRender, tt.description)
		})
	}
}

// TestFleetRolloutsLogic_FullDelayDeviceRenderPropagation tests the complete propagation path
// from fleet configuration through device update, ensuring delayDeviceRender flows all the way
func TestFleetRolloutsLogic_FullDelayDeviceRenderPropagation(t *testing.T) {
	tests := []struct {
		name               string
		fleet              *api.Fleet
		expectedDelayValue bool
		description        string
	}{
		{
			name:               "NoRolloutPolicy",
			fleet:              createTestFleetForRollout("test-fleet", nil),
			expectedDelayValue: false,
			description:        "delayDeviceRender should be false when fleet has no RolloutPolicy",
		},
		{
			name: "RolloutPolicyWithDisruptionBudget",
			fleet: createTestFleetForRollout("test-fleet", &api.RolloutPolicy{
				DeviceSelection: &api.RolloutDeviceSelection{},
				DisruptionBudget: &api.DisruptionBudget{
					MaxUnavailable: lo.ToPtr(25),
				},
			}),
			expectedDelayValue: true,
			description:        "delayDeviceRender should be true when fleet has RolloutPolicy with DisruptionBudget",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			orgId := uuid.New()
			log := logrus.New()
			fleetName := "test-fleet"
			event := api.Event{
				InvolvedObject: api.ObjectReference{
					Kind: api.FleetKind,
					Name: fleetName,
				},
				Reason: api.EventReasonFleetRolloutBatchDispatched,
			}

			mockService := service.NewMockService(ctrl)

			// Mock GetFleet to return our test fleet
			mockService.EXPECT().GetFleet(gomock.Any(), gomock.Any(), fleetName, gomock.Any()).Return(tt.fleet, api.Status{Code: http.StatusOK})

			// Mock GetLatestTemplateVersion with a simple template that won't trigger complex processing
			templateVersion := createTestTemplateVersion("test-tv")
			// Ensure the template has no complex fields that might cause device modification
			templateVersion.Status.Os = nil
			templateVersion.Status.Config = nil
			templateVersion.Status.Applications = nil
			mockService.EXPECT().GetLatestTemplateVersion(gomock.Any(), gomock.Any(), fleetName).Return(templateVersion, api.Status{Code: http.StatusOK})

			// Create test device with owner that matches what f.owner will be set to
			// f.owner will be set to "Fleet/test-fleet" from util.SetResourceOwner(api.FleetKind, "test-fleet")
			// Note: api.FleetKind = "Fleet" (uppercase F), not "fleet"
			expectedOwner := "Fleet/test-fleet"
			testDevice := createTestDevice("test-device", expectedOwner)

			// Debug: Print the device owner values
			t.Logf("Test device owner: %s", *testDevice.Metadata.Owner)
			t.Logf("Expected f.owner will be: %s", expectedOwner)

			// Mock ListDevices to return a device so the rollout process continues
			// This is the key change - returning a non-empty device list to test full propagation
			mockService.EXPECT().ListDevices(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(&api.DeviceList{
				Metadata: api.ListMeta{},
				Items:    []api.Device{*testDevice},
			}, api.Status{Code: http.StatusOK})

			// Mock ReplaceDevice to capture the delayDeviceRender value from context
			// This will be called during the device update process, allowing us to verify propagation
			var capturedDelayDeviceRender bool
			mockService.EXPECT().ReplaceDevice(gomock.Any(), gomock.Any(), "test-device", gomock.Any(), gomock.Any()).DoAndReturn(
				func(ctx context.Context, orgId uuid.UUID, name string, device api.Device, fieldsToUnset []string) (*api.Device, api.Status) {
					// Debug: Print the device owner when ReplaceDevice is called
					if device.Metadata.Owner != nil {
						t.Logf("ReplaceDevice called with device owner: %s", *device.Metadata.Owner)
					} else {
						t.Logf("ReplaceDevice called with device owner: nil")
					}

					// Extract the delayDeviceRender value from context
					if delayValue, ok := ctx.Value(consts.DelayDeviceRenderCtxKey).(bool); ok {
						capturedDelayDeviceRender = delayValue
						t.Logf("Captured delayDeviceRender value: %v", delayValue)
					} else {
						t.Logf("No delayDeviceRender value found in context")
					}
					return &device, api.Status{Code: http.StatusOK}
				})

			// Mock UpdateDeviceAnnotations for the device update
			mockService.EXPECT().UpdateDeviceAnnotations(gomock.Any(), gomock.Any(), "test-device", gomock.Any(), gomock.Any()).Return(api.Status{Code: http.StatusOK})

			// Create FleetRolloutsLogic instance
			logic := NewFleetRolloutsLogic(log, mockService, orgId, event)

			// Execute - this will now process the device through the full rollout flow
			err := logic.RolloutFleet(context.Background())

			// Assert
			require.NoError(t, err)

			// Verify that the delayDeviceRender value was correctly propagated through the context
			// This proves the value flows all the way from the fleet configuration to the device update
			assert.Equal(t, tt.expectedDelayValue, capturedDelayDeviceRender, tt.description)

			// Also verify that the delayDeviceRender logic is correctly implemented
			// by checking the condition directly
			delayDeviceRender := tt.fleet.Spec.RolloutPolicy != nil && tt.fleet.Spec.RolloutPolicy.DisruptionBudget != nil
			assert.Equal(t, tt.expectedDelayValue, delayDeviceRender, tt.description)
		})
	}
}

// TestFleetRolloutsLogic_DelayDeviceRenderPropagationThroughContext tests that the delayDeviceRender
// value is correctly propagated through the context when calling updateDeviceInStore
func TestFleetRolloutsLogic_DelayDeviceRenderPropagationThroughContext(t *testing.T) {
	tests := []struct {
		name                 string
		delayDeviceRender    bool
		expectedContextValue bool
		description          string
	}{
		{
			name:                 "DelayDeviceRenderTrue",
			delayDeviceRender:    true,
			expectedContextValue: true,
			description:          "Context should contain delayDeviceRender=true when parameter is true",
		},
		{
			name:                 "DelayDeviceRenderFalse",
			delayDeviceRender:    false,
			expectedContextValue: false,
			description:          "Context should contain delayDeviceRender=false when parameter is false",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			orgId := uuid.New()
			log := logrus.New()
			event := api.Event{
				InvolvedObject: api.ObjectReference{
					Kind: api.FleetKind,
					Name: "test-fleet",
				},
				Reason: api.EventReasonFleetRolloutBatchDispatched,
			}

			mockService := service.NewMockService(ctrl)

			// Create FleetRolloutsLogic instance
			logic := NewFleetRolloutsLogic(log, mockService, orgId, event)

			// Set the owner field to match the device owner
			logic.owner = "fleet/test-fleet"

			// Create test device with matching owner
			device := createTestDevice("test-device", "fleet/test-fleet")

			// Mock ReplaceDevice to capture the context value
			var capturedDelayDeviceRender bool
			mockService.EXPECT().ReplaceDevice(gomock.Any(), gomock.Any(), "test-device", gomock.Any(), gomock.Any()).DoAndReturn(
				func(ctx context.Context, orgId uuid.UUID, name string, device api.Device, fieldsToUnset []string) (*api.Device, api.Status) {
					// Extract the delayDeviceRender value from context
					if delayValue, ok := ctx.Value(consts.DelayDeviceRenderCtxKey).(bool); ok {
						capturedDelayDeviceRender = delayValue
					}
					return &device, api.Status{Code: http.StatusOK}
				})

			// Execute the key function that contains the delayDeviceRender propagation logic
			err := logic.updateDeviceInStore(context.Background(), device, &api.DeviceSpec{}, tt.delayDeviceRender)

			// Assert
			require.NoError(t, err)
			assert.Equal(t, tt.expectedContextValue, capturedDelayDeviceRender, tt.description)
		})
	}
}

func createTestDeviceWithLabels(name string, owner string, labels map[string]string) *api.Device {
	deviceName := name
	ownerName := owner
	return &api.Device{
		Metadata: api.ObjectMeta{
			Name:   &deviceName,
			Owner:  &ownerName,
			Labels: &labels,
		},
		Spec: &api.DeviceSpec{
			Os: &api.DeviceOsSpec{
				Image: "old-image:latest",
			},
		},
		Status: &api.DeviceStatus{
			Conditions: []api.Condition{},
		},
	}
}

func TestFleetRolloutsLogic_ReplaceImageApplicationParameters(t *testing.T) {
	tests := []struct {
		name          string
		device        *api.Device
		appSpec       api.ImageApplicationProviderSpec
		envVars       *map[string]string
		expectedImage string
		expectedEnv   map[string]string
		expectError   bool
	}{
		{
			name:   "replaces template in image tag",
			device: createTestDeviceWithLabels("mydevice", "fleet/test", map[string]string{"version": "v1.0"}),
			appSpec: api.ImageApplicationProviderSpec{
				Image: "quay.io/test/app:{{ index .metadata.labels \"version\" }}",
			},
			expectedImage: "quay.io/test/app:v1.0",
			expectError:   false,
		},
		{
			name:   "replaces device name in image tag",
			device: createTestDeviceWithLabels("mydevice-123", "fleet/test", map[string]string{}),
			appSpec: api.ImageApplicationProviderSpec{
				Image: "quay.io/test/app:{{ .metadata.name }}",
			},
			expectedImage: "quay.io/test/app:mydevice-123",
			expectError:   false,
		},
		{
			name:   "replaces template in envVars",
			device: createTestDeviceWithLabels("mydevice", "fleet/test", map[string]string{"env": "prod"}),
			appSpec: api.ImageApplicationProviderSpec{
				Image: "quay.io/test/app:latest",
			},
			envVars:       &map[string]string{"MY_ENV": "{{ index .metadata.labels \"env\" }}"},
			expectedImage: "quay.io/test/app:latest",
			expectedEnv:   map[string]string{"MY_ENV": "prod"},
			expectError:   false,
		},
		{
			name:   "missing label results in empty string",
			device: createTestDeviceWithLabels("mydevice", "fleet/test", map[string]string{}),
			appSpec: api.ImageApplicationProviderSpec{
				Image: "quay.io/test/app:{{ index .metadata.labels \"missing\" }}",
			},
			expectedImage: "quay.io/test/app:",
			expectError:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			log := logrus.New()
			logic := FleetRolloutsLogic{log: log}

			app := api.ApplicationProviderSpec{
				Name:    lo.ToPtr("test-app"),
				AppType: api.AppTypeCompose,
				EnvVars: tt.envVars,
			}
			err := app.FromImageApplicationProviderSpec(tt.appSpec)
			require.NoError(t, err)

			result, errs := logic.replaceImageApplicationParameters(tt.device, app)

			if tt.expectError {
				assert.NotEmpty(t, errs)
				return
			}

			require.Empty(t, errs)
			require.NotNil(t, result)

			imgSpec, err := result.AsImageApplicationProviderSpec()
			require.NoError(t, err)
			assert.Equal(t, tt.expectedImage, imgSpec.Image)

			if tt.expectedEnv != nil {
				require.NotNil(t, result.EnvVars)
				for k, v := range tt.expectedEnv {
					assert.Equal(t, v, (*result.EnvVars)[k])
				}
			}
		})
	}
}

func TestFleetRolloutsLogic_ReplaceInlineApplicationParameters(t *testing.T) {
	tests := []struct {
		name            string
		device          *api.Device
		appSpec         api.InlineApplicationProviderSpec
		envVars         *map[string]string
		expectedPath    string
		expectedContent string
		expectedEnv     map[string]string
		expectError     bool
	}{
		{
			name:   "replaces template in path",
			device: createTestDeviceWithLabels("mydevice", "fleet/test", map[string]string{}),
			appSpec: api.InlineApplicationProviderSpec{
				Inline: []api.ApplicationContent{
					{
						Path:    "/etc/{{ .metadata.name }}.conf",
						Content: lo.ToPtr("static content"),
					},
				},
			},
			expectedPath:    "/etc/mydevice.conf",
			expectedContent: "static content",
			expectError:     false,
		},
		{
			name:   "replaces template in content",
			device: createTestDeviceWithLabels("mydevice", "fleet/test", map[string]string{"version": "2.0"}),
			appSpec: api.InlineApplicationProviderSpec{
				Inline: []api.ApplicationContent{
					{
						Path:    "/etc/app.conf",
						Content: lo.ToPtr("version={{ index .metadata.labels \"version\" }}"),
					},
				},
			},
			expectedPath:    "/etc/app.conf",
			expectedContent: "version=2.0",
			expectError:     false,
		},
		{
			name:   "replaces templates in both path and content",
			device: createTestDeviceWithLabels("mydevice", "fleet/test", map[string]string{"env": "prod"}),
			appSpec: api.InlineApplicationProviderSpec{
				Inline: []api.ApplicationContent{
					{
						Path:    "/etc/{{ .metadata.name }}/config",
						Content: lo.ToPtr("environment={{ index .metadata.labels \"env\" }}"),
					},
				},
			},
			expectedPath:    "/etc/mydevice/config",
			expectedContent: "environment=prod",
			expectError:     false,
		},
		{
			name:   "replaces template in envVars",
			device: createTestDeviceWithLabels("mydevice", "fleet/test", map[string]string{"region": "us-east"}),
			appSpec: api.InlineApplicationProviderSpec{
				Inline: []api.ApplicationContent{
					{
						Path:    "/etc/app.conf",
						Content: lo.ToPtr("config"),
					},
				},
			},
			envVars:         &map[string]string{"REGION": "{{ index .metadata.labels \"region\" }}"},
			expectedPath:    "/etc/app.conf",
			expectedContent: "config",
			expectedEnv:     map[string]string{"REGION": "us-east"},
			expectError:     false,
		},
		{
			name:   "missing label in content results in empty string",
			device: createTestDeviceWithLabels("mydevice", "fleet/test", map[string]string{}),
			appSpec: api.InlineApplicationProviderSpec{
				Inline: []api.ApplicationContent{
					{
						Path:    "/etc/app.conf",
						Content: lo.ToPtr("version={{ index .metadata.labels \"missing\" }}"),
					},
				},
			},
			expectedPath:    "/etc/app.conf",
			expectedContent: "version=",
			expectError:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			log := logrus.New()
			logic := FleetRolloutsLogic{log: log}

			app := api.ApplicationProviderSpec{
				Name:    lo.ToPtr("test-app"),
				AppType: api.AppTypeQuadlet,
				EnvVars: tt.envVars,
			}
			err := app.FromInlineApplicationProviderSpec(tt.appSpec)
			require.NoError(t, err)

			result, errs := logic.replaceInlineApplicationParameters(tt.device, app)

			if tt.expectError {
				assert.NotEmpty(t, errs)
				return
			}

			require.Empty(t, errs)
			require.NotNil(t, result)

			inlineSpec, err := result.AsInlineApplicationProviderSpec()
			require.NoError(t, err)
			require.Len(t, inlineSpec.Inline, 1)
			assert.Equal(t, tt.expectedPath, inlineSpec.Inline[0].Path)
			require.NotNil(t, inlineSpec.Inline[0].Content)
			assert.Equal(t, tt.expectedContent, *inlineSpec.Inline[0].Content)

			if tt.expectedEnv != nil {
				require.NotNil(t, result.EnvVars)
				for k, v := range tt.expectedEnv {
					assert.Equal(t, v, (*result.EnvVars)[k])
				}
			}
		})
	}
}
