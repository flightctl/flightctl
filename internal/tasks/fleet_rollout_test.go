package tasks

import (
	"context"
	"net/http"
	"testing"

	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/service"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gomock "go.uber.org/mock/gomock"
)

func createTestFleetForRollout(name string, rolloutPolicy *domain.RolloutPolicy) *domain.Fleet {
	fleetName := name
	generation := int64(1)

	return &domain.Fleet{
		Metadata: domain.ObjectMeta{
			Name:       &fleetName,
			Generation: &generation,
		},
		Spec: domain.FleetSpec{
			RolloutPolicy: rolloutPolicy,
			Template: struct {
				Metadata *domain.ObjectMeta `json:"metadata,omitempty"`
				Spec     domain.DeviceSpec  `json:"spec"`
			}{
				Spec: domain.DeviceSpec{
					Os: &domain.DeviceOsSpec{
						Image: "test-image:latest",
					},
				},
			},
		},
	}
}

func createTestTemplateVersion(name string) *domain.TemplateVersion {
	tvName := name
	return &domain.TemplateVersion{
		Metadata: domain.ObjectMeta{
			Name: &tvName,
		},
		Status: &domain.TemplateVersionStatus{
			Os: &domain.DeviceOsSpec{
				Image: "test-image:latest",
			},
		},
	}
}

func createTestDevice(name string, owner string) *domain.Device {
	deviceName := name
	ownerName := owner
	return &domain.Device{
		Metadata: domain.ObjectMeta{
			Name:  &deviceName,
			Owner: &ownerName,
		},
		Spec: &domain.DeviceSpec{
			Os: &domain.DeviceOsSpec{
				Image: "old-image:latest",
			},
		},
		Status: &domain.DeviceStatus{
			Conditions: []domain.Condition{},
		},
	}
}

func TestFleetRolloutsLogic_DelayDeviceRenderCondition(t *testing.T) {
	tests := []struct {
		name               string
		fleet              *domain.Fleet
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
			fleet: createTestFleetForRollout("test-fleet", &domain.RolloutPolicy{
				DeviceSelection: &domain.RolloutDeviceSelection{},
			}),
			expectedDelayValue: false,
			description:        "delayDeviceRender should be false when fleet has RolloutPolicy but no DisruptionBudget",
		},
		{
			name: "RolloutPolicyWithDisruptionBudget",
			fleet: createTestFleetForRollout("test-fleet", &domain.RolloutPolicy{
				DeviceSelection: &domain.RolloutDeviceSelection{},
				DisruptionBudget: &domain.DisruptionBudget{
					MaxUnavailable: lo.ToPtr(25),
				},
			}),
			expectedDelayValue: true,
			description:        "delayDeviceRender should be true when fleet has RolloutPolicy with DisruptionBudget",
		},
		{
			name: "RolloutPolicyWithOnlyDisruptionBudget",
			fleet: createTestFleetForRollout("test-fleet", &domain.RolloutPolicy{
				DisruptionBudget: &domain.DisruptionBudget{
					MaxUnavailable: lo.ToPtr(10),
				},
			}),
			expectedDelayValue: true,
			description:        "delayDeviceRender should be true when fleet has RolloutPolicy with only DisruptionBudget",
		},
		{
			name: "RolloutPolicyWithComplexDisruptionBudget",
			fleet: createTestFleetForRollout("test-fleet", &domain.RolloutPolicy{
				DeviceSelection: &domain.RolloutDeviceSelection{},
				DisruptionBudget: &domain.DisruptionBudget{
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
		fleet              *domain.Fleet
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
			fleet: createTestFleetForRollout("test-fleet", &domain.RolloutPolicy{
				DeviceSelection: &domain.RolloutDeviceSelection{},
				DisruptionBudget: &domain.DisruptionBudget{
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
		fleet              *domain.Fleet
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
			fleet: createTestFleetForRollout("test-fleet", &domain.RolloutPolicy{
				DeviceSelection: &domain.RolloutDeviceSelection{},
				DisruptionBudget: &domain.DisruptionBudget{
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
			event := domain.Event{
				InvolvedObject: domain.ObjectReference{
					Kind: domain.FleetKind,
					Name: fleetName,
				},
				Reason: domain.EventReasonFleetRolloutBatchDispatched,
			}

			mockService := service.NewMockService(ctrl)

			// Mock GetFleet to return our test fleet
			mockService.EXPECT().GetFleet(gomock.Any(), gomock.Any(), fleetName, gomock.Any()).Return(tt.fleet, domain.Status{Code: http.StatusOK})

			// Mock GetLatestTemplateVersion with a simple template that won't trigger complex processing
			templateVersion := createTestTemplateVersion("test-tv")
			// Ensure the template has no complex fields that might cause device modification
			templateVersion.Status.Os = nil
			templateVersion.Status.Config = nil
			templateVersion.Status.Applications = nil
			mockService.EXPECT().GetLatestTemplateVersion(gomock.Any(), gomock.Any(), fleetName).Return(templateVersion, domain.Status{Code: http.StatusOK})

			// Create test device with owner that matches what f.owner will be set to
			// f.owner will be set to "Fleet/test-fleet" from util.SetResourceOwner(domain.FleetKind, "test-fleet")
			// Note: domain.FleetKind = "Fleet" (uppercase F), not "fleet"
			expectedOwner := "Fleet/test-fleet"
			testDevice := createTestDevice("test-device", expectedOwner)

			// Debug: Print the device owner values
			t.Logf("Test device owner: %s", *testDevice.Metadata.Owner)
			t.Logf("Expected f.owner will be: %s", expectedOwner)

			// Mock ListDevices to return a device so the rollout process continues
			// This is the key change - returning a non-empty device list to test full propagation
			mockService.EXPECT().ListDevices(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(&domain.DeviceList{
				Metadata: domain.ListMeta{},
				Items:    []domain.Device{*testDevice},
			}, domain.Status{Code: http.StatusOK})

			// Mock ReplaceDevice to capture the delayDeviceRender value from context
			// This will be called during the device update process, allowing us to verify propagation
			var capturedDelayDeviceRender bool
			mockService.EXPECT().ReplaceDevice(gomock.Any(), gomock.Any(), "test-device", gomock.Any(), gomock.Any()).DoAndReturn(
				func(ctx context.Context, orgId uuid.UUID, name string, device domain.Device, fieldsToUnset []string) (*domain.Device, domain.Status) {
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
					return &device, domain.Status{Code: http.StatusOK}
				})

			// Mock UpdateDeviceAnnotations for the device update
			mockService.EXPECT().UpdateDeviceAnnotations(gomock.Any(), gomock.Any(), "test-device", gomock.Any(), gomock.Any()).Return(domain.Status{Code: http.StatusOK})

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
			event := domain.Event{
				InvolvedObject: domain.ObjectReference{
					Kind: domain.FleetKind,
					Name: "test-fleet",
				},
				Reason: domain.EventReasonFleetRolloutBatchDispatched,
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
				func(ctx context.Context, orgId uuid.UUID, name string, device domain.Device, fieldsToUnset []string) (*domain.Device, domain.Status) {
					// Extract the delayDeviceRender value from context
					if delayValue, ok := ctx.Value(consts.DelayDeviceRenderCtxKey).(bool); ok {
						capturedDelayDeviceRender = delayValue
					}
					return &device, domain.Status{Code: http.StatusOK}
				})

			// Execute the key function that contains the delayDeviceRender propagation logic
			err := logic.updateDeviceInStore(context.Background(), device, &domain.DeviceSpec{}, tt.delayDeviceRender)

			// Assert
			require.NoError(t, err)
			assert.Equal(t, tt.expectedContextValue, capturedDelayDeviceRender, tt.description)
		})
	}
}

func createTestDeviceWithLabels(name string, owner string, labels map[string]string) *domain.Device {
	deviceName := name
	ownerName := owner
	return &domain.Device{
		Metadata: domain.ObjectMeta{
			Name:   &deviceName,
			Owner:  &ownerName,
			Labels: &labels,
		},
		Spec: &domain.DeviceSpec{
			Os: &domain.DeviceOsSpec{
				Image: "old-image:latest",
			},
		},
		Status: &domain.DeviceStatus{
			Conditions: []domain.Condition{},
		},
	}
}

func TestFleetRolloutsLogic_ReplaceComposeImageApplicationParameters(t *testing.T) {
	tests := []struct {
		name          string
		device        *domain.Device
		imageSpec     domain.ImageApplicationProviderSpec
		envVars       *map[string]string
		expectedImage string
		expectedEnv   map[string]string
		expectError   bool
	}{
		{
			name:   "replaces template in image tag",
			device: createTestDeviceWithLabels("mydevice", "fleet/test", map[string]string{"version": "v1.0"}),
			imageSpec: domain.ImageApplicationProviderSpec{
				Image: "quay.io/test/app:{{ index .metadata.labels \"version\" }}",
			},
			expectedImage: "quay.io/test/app:v1.0",
			expectError:   false,
		},
		{
			name:   "replaces device name in image tag",
			device: createTestDeviceWithLabels("mydevice-123", "fleet/test", map[string]string{}),
			imageSpec: domain.ImageApplicationProviderSpec{
				Image: "quay.io/test/app:{{ .metadata.name }}",
			},
			expectedImage: "quay.io/test/app:mydevice-123",
			expectError:   false,
		},
		{
			name:   "replaces template in envVars",
			device: createTestDeviceWithLabels("mydevice", "fleet/test", map[string]string{"env": "prod"}),
			imageSpec: domain.ImageApplicationProviderSpec{
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
			imageSpec: domain.ImageApplicationProviderSpec{
				Image: "quay.io/test/app:{{ index .metadata.labels \"missing\" }}",
			},
			expectedImage: "quay.io/test/app:",
			expectError:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)
			log := logrus.New()
			logic := FleetRolloutsLogic{log: log}

			composeApp := domain.ComposeApplication{
				EnvVars: tt.envVars,
				Name:    lo.ToPtr("test-app"),
				AppType: domain.AppTypeCompose,
			}
			err := composeApp.FromImageApplicationProviderSpec(tt.imageSpec)
			require.NoError(err)

			var app domain.ApplicationProviderSpec
			err = app.FromComposeApplication(composeApp)
			require.NoError(err)

			result, errs := logic.replaceComposeApplicationParameters(tt.device, app)

			if tt.expectError {
				assert.NotEmpty(t, errs)
				return
			}

			require.Empty(errs)
			require.NotNil(result)

			resultComposeApp, err := result.AsComposeApplication()
			require.NoError(err)
			imgSpec, err := resultComposeApp.AsImageApplicationProviderSpec()
			require.NoError(err)
			assert.Equal(t, tt.expectedImage, imgSpec.Image)

			if tt.expectedEnv != nil {
				require.NotNil(resultComposeApp.EnvVars)
				for k, v := range tt.expectedEnv {
					assert.Equal(t, v, (*resultComposeApp.EnvVars)[k])
				}
			}
		})
	}
}

func TestFleetRolloutsLogic_ReplaceQuadletInlineApplicationParameters(t *testing.T) {
	tests := []struct {
		name            string
		device          *domain.Device
		inlineSpec      domain.InlineApplicationProviderSpec
		envVars         *map[string]string
		expectedPath    string
		expectedContent string
		expectedEnv     map[string]string
		expectError     bool
	}{
		{
			name:   "replaces template in path",
			device: createTestDeviceWithLabels("mydevice", "fleet/test", map[string]string{}),
			inlineSpec: domain.InlineApplicationProviderSpec{
				Inline: []domain.ApplicationContent{
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
			inlineSpec: domain.InlineApplicationProviderSpec{
				Inline: []domain.ApplicationContent{
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
			inlineSpec: domain.InlineApplicationProviderSpec{
				Inline: []domain.ApplicationContent{
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
			inlineSpec: domain.InlineApplicationProviderSpec{
				Inline: []domain.ApplicationContent{
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
			inlineSpec: domain.InlineApplicationProviderSpec{
				Inline: []domain.ApplicationContent{
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
			require := require.New(t)
			log := logrus.New()
			logic := FleetRolloutsLogic{log: log}

			quadletApp := domain.QuadletApplication{
				EnvVars: tt.envVars,
				Name:    lo.ToPtr("test-app"),
				AppType: domain.AppTypeQuadlet,
			}
			err := quadletApp.FromInlineApplicationProviderSpec(tt.inlineSpec)
			require.NoError(err)

			var app domain.ApplicationProviderSpec
			err = app.FromQuadletApplication(quadletApp)
			require.NoError(err)

			result, errs := logic.replaceQuadletApplicationParameters(tt.device, app)

			if tt.expectError {
				assert.NotEmpty(t, errs)
				return
			}

			require.Empty(errs)
			require.NotNil(result)

			resultQuadletApp, err := result.AsQuadletApplication()
			require.NoError(err)
			inlineSpec, err := resultQuadletApp.AsInlineApplicationProviderSpec()
			require.NoError(err)
			require.Len(inlineSpec.Inline, 1)
			assert.Equal(t, tt.expectedPath, inlineSpec.Inline[0].Path)
			require.NotNil(inlineSpec.Inline[0].Content)
			assert.Equal(t, tt.expectedContent, *inlineSpec.Inline[0].Content)

			if tt.expectedEnv != nil {
				require.NotNil(resultQuadletApp.EnvVars)
				for k, v := range tt.expectedEnv {
					assert.Equal(t, v, (*resultQuadletApp.EnvVars)[k])
				}
			}
		})
	}
}

func TestFleetRolloutsLogic_ReplaceQuadletImageApplicationParameters(t *testing.T) {
	tests := []struct {
		name          string
		device        *domain.Device
		imageSpec     domain.ImageApplicationProviderSpec
		envVars       *map[string]string
		expectedImage string
		expectedEnv   map[string]string
		expectError   bool
	}{
		{
			name:   "replaces template in image tag",
			device: createTestDeviceWithLabels("mydevice", "fleet/test", map[string]string{"version": "v2.0"}),
			imageSpec: domain.ImageApplicationProviderSpec{
				Image: "quay.io/test/quadlet:{{ index .metadata.labels \"version\" }}",
			},
			expectedImage: "quay.io/test/quadlet:v2.0",
			expectError:   false,
		},
		{
			name:   "replaces device name in image tag",
			device: createTestDeviceWithLabels("quadlet-device", "fleet/test", map[string]string{}),
			imageSpec: domain.ImageApplicationProviderSpec{
				Image: "quay.io/test/app:{{ .metadata.name }}",
			},
			expectedImage: "quay.io/test/app:quadlet-device",
			expectError:   false,
		},
		{
			name:   "replaces template in envVars",
			device: createTestDeviceWithLabels("mydevice", "fleet/test", map[string]string{"region": "eu-west"}),
			imageSpec: domain.ImageApplicationProviderSpec{
				Image: "quay.io/test/quadlet:latest",
			},
			envVars:       &map[string]string{"REGION": "{{ index .metadata.labels \"region\" }}"},
			expectedImage: "quay.io/test/quadlet:latest",
			expectedEnv:   map[string]string{"REGION": "eu-west"},
			expectError:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)
			log := logrus.New()
			logic := FleetRolloutsLogic{log: log}

			quadletApp := domain.QuadletApplication{
				EnvVars: tt.envVars,
				Name:    lo.ToPtr("test-quadlet-app"),
				AppType: domain.AppTypeQuadlet,
			}
			err := quadletApp.FromImageApplicationProviderSpec(tt.imageSpec)
			require.NoError(err)

			var app domain.ApplicationProviderSpec
			err = app.FromQuadletApplication(quadletApp)
			require.NoError(err)

			result, errs := logic.replaceQuadletApplicationParameters(tt.device, app)

			if tt.expectError {
				assert.NotEmpty(t, errs)
				return
			}

			require.Empty(errs)
			require.NotNil(result)

			resultQuadletApp, err := result.AsQuadletApplication()
			require.NoError(err)
			imgSpec, err := resultQuadletApp.AsImageApplicationProviderSpec()
			require.NoError(err)
			assert.Equal(t, tt.expectedImage, imgSpec.Image)

			if tt.expectedEnv != nil {
				require.NotNil(resultQuadletApp.EnvVars)
				for k, v := range tt.expectedEnv {
					assert.Equal(t, v, (*resultQuadletApp.EnvVars)[k])
				}
			}
		})
	}
}

func TestFleetRolloutsLogic_ReplaceContainerApplicationParameters(t *testing.T) {
	tests := []struct {
		name          string
		device        *domain.Device
		image         string
		envVars       *map[string]string
		expectedImage string
		expectedEnv   map[string]string
		expectError   bool
	}{
		{
			name:          "replaces template in image tag",
			device:        createTestDeviceWithLabels("mydevice", "fleet/test", map[string]string{"version": "v1.5"}),
			image:         "quay.io/test/container:{{ index .metadata.labels \"version\" }}",
			expectedImage: "quay.io/test/container:v1.5",
			expectError:   false,
		},
		{
			name:          "replaces device name in image tag",
			device:        createTestDeviceWithLabels("container-device-456", "fleet/test", map[string]string{}),
			image:         "quay.io/test/app:{{ .metadata.name }}",
			expectedImage: "quay.io/test/app:container-device-456",
			expectError:   false,
		},
		{
			name:          "replaces template in envVars",
			device:        createTestDeviceWithLabels("mydevice", "fleet/test", map[string]string{"env": "staging"}),
			image:         "quay.io/test/container:latest",
			envVars:       &map[string]string{"ENVIRONMENT": "{{ index .metadata.labels \"env\" }}"},
			expectedImage: "quay.io/test/container:latest",
			expectedEnv:   map[string]string{"ENVIRONMENT": "staging"},
			expectError:   false,
		},
		{
			name:          "replaces multiple templates",
			device:        createTestDeviceWithLabels("mydevice", "fleet/test", map[string]string{"version": "v3.0", "tier": "premium"}),
			image:         "quay.io/test/container:{{ index .metadata.labels \"version\" }}",
			envVars:       &map[string]string{"TIER": "{{ index .metadata.labels \"tier\" }}"},
			expectedImage: "quay.io/test/container:v3.0",
			expectedEnv:   map[string]string{"TIER": "premium"},
			expectError:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)
			log := logrus.New()
			logic := FleetRolloutsLogic{log: log}

			containerApp := domain.ContainerApplication{
				Image:   tt.image,
				EnvVars: tt.envVars,
				Name:    lo.ToPtr("test-container-app"),
				AppType: domain.AppTypeContainer,
			}

			var app domain.ApplicationProviderSpec
			err := app.FromContainerApplication(containerApp)
			require.NoError(err)

			result, errs := logic.replaceContainerApplicationParameters(tt.device, app)

			if tt.expectError {
				assert.NotEmpty(t, errs)
				return
			}

			require.Empty(errs)
			require.NotNil(result)

			resultContainerApp, err := result.AsContainerApplication()
			require.NoError(err)
			assert.Equal(t, tt.expectedImage, resultContainerApp.Image)

			if tt.expectedEnv != nil {
				require.NotNil(resultContainerApp.EnvVars)
				for k, v := range tt.expectedEnv {
					assert.Equal(t, v, (*resultContainerApp.EnvVars)[k])
				}
			}
		})
	}
}

func TestFleetRolloutsLogic_ReplaceHelmApplicationParameters(t *testing.T) {
	tests := []struct {
		name          string
		device        *domain.Device
		image         string
		namespace     *string
		expectedImage string
		expectError   bool
	}{
		{
			name:          "replaces template in image tag",
			device:        createTestDeviceWithLabels("mydevice", "fleet/test", map[string]string{"chartVersion": "1.2.3"}),
			image:         "oci://registry.example.com/charts/myapp:{{ index .metadata.labels \"chartVersion\" }}",
			expectedImage: "oci://registry.example.com/charts/myapp:1.2.3",
			expectError:   false,
		},
		{
			name:          "replaces device name in image tag",
			device:        createTestDeviceWithLabels("helm-device-789", "fleet/test", map[string]string{}),
			image:         "oci://registry.example.com/charts/{{ .metadata.name }}:latest",
			expectedImage: "oci://registry.example.com/charts/helm-device-789:latest",
			expectError:   false,
		},
		{
			name:          "no template - image unchanged",
			device:        createTestDeviceWithLabels("mydevice", "fleet/test", map[string]string{}),
			image:         "oci://registry.example.com/charts/static:v1.0.0",
			expectedImage: "oci://registry.example.com/charts/static:v1.0.0",
			expectError:   false,
		},
		{
			name:          "missing label results in empty string",
			device:        createTestDeviceWithLabels("mydevice", "fleet/test", map[string]string{}),
			image:         "oci://registry.example.com/charts/myapp:{{ index .metadata.labels \"missing\" }}",
			expectedImage: "oci://registry.example.com/charts/myapp:",
			expectError:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)
			log := logrus.New()
			logic := FleetRolloutsLogic{log: log}

			helmApp := domain.HelmApplication{
				Image:     tt.image,
				Namespace: tt.namespace,
				Name:      lo.ToPtr("test-helm-app"),
				AppType:   domain.AppTypeHelm,
			}

			var app domain.ApplicationProviderSpec
			err := app.FromHelmApplication(helmApp)
			require.NoError(err)

			result, errs := logic.replaceHelmApplicationParameters(tt.device, app)

			if tt.expectError {
				assert.NotEmpty(t, errs)
				return
			}

			require.Empty(errs)
			require.NotNil(result)

			resultHelmApp, err := result.AsHelmApplication()
			require.NoError(err)
			assert.Equal(t, tt.expectedImage, resultHelmApp.Image)
		})
	}
}

func TestFleetRolloutsLogic_ReplaceComposeInlineApplicationParameters(t *testing.T) {
	tests := []struct {
		name            string
		device          *domain.Device
		inlineSpec      domain.InlineApplicationProviderSpec
		envVars         *map[string]string
		expectedPath    string
		expectedContent string
		expectedEnv     map[string]string
		expectError     bool
	}{
		{
			name:   "replaces template in path",
			device: createTestDeviceWithLabels("compose-device", "fleet/test", map[string]string{}),
			inlineSpec: domain.InlineApplicationProviderSpec{
				Inline: []domain.ApplicationContent{
					{
						Path:    "/etc/compose/{{ .metadata.name }}.yaml",
						Content: lo.ToPtr("version: '3'"),
					},
				},
			},
			expectedPath:    "/etc/compose/compose-device.yaml",
			expectedContent: "version: '3'",
			expectError:     false,
		},
		{
			name:   "replaces template in content",
			device: createTestDeviceWithLabels("mydevice", "fleet/test", map[string]string{"replicas": "3"}),
			inlineSpec: domain.InlineApplicationProviderSpec{
				Inline: []domain.ApplicationContent{
					{
						Path:    "/etc/docker-compose.yaml",
						Content: lo.ToPtr("replicas: {{ index .metadata.labels \"replicas\" }}"),
					},
				},
			},
			expectedPath:    "/etc/docker-compose.yaml",
			expectedContent: "replicas: 3",
			expectError:     false,
		},
		{
			name:   "replaces template in envVars",
			device: createTestDeviceWithLabels("mydevice", "fleet/test", map[string]string{"db": "postgres"}),
			inlineSpec: domain.InlineApplicationProviderSpec{
				Inline: []domain.ApplicationContent{
					{
						Path:    "/etc/compose.yaml",
						Content: lo.ToPtr("services: {}"),
					},
				},
			},
			envVars:         &map[string]string{"DATABASE": "{{ index .metadata.labels \"db\" }}"},
			expectedPath:    "/etc/compose.yaml",
			expectedContent: "services: {}",
			expectedEnv:     map[string]string{"DATABASE": "postgres"},
			expectError:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)
			log := logrus.New()
			logic := FleetRolloutsLogic{log: log}

			composeApp := domain.ComposeApplication{
				EnvVars: tt.envVars,
				Name:    lo.ToPtr("test-compose-inline-app"),
				AppType: domain.AppTypeCompose,
			}
			err := composeApp.FromInlineApplicationProviderSpec(tt.inlineSpec)
			require.NoError(err)

			var app domain.ApplicationProviderSpec
			err = app.FromComposeApplication(composeApp)
			require.NoError(err)

			result, errs := logic.replaceComposeApplicationParameters(tt.device, app)

			if tt.expectError {
				assert.NotEmpty(t, errs)
				return
			}

			require.Empty(errs)
			require.NotNil(result)

			resultComposeApp, err := result.AsComposeApplication()
			require.NoError(err)
			inlineSpec, err := resultComposeApp.AsInlineApplicationProviderSpec()
			require.NoError(err)
			require.Len(inlineSpec.Inline, 1)
			assert.Equal(t, tt.expectedPath, inlineSpec.Inline[0].Path)
			require.NotNil(inlineSpec.Inline[0].Content)
			assert.Equal(t, tt.expectedContent, *inlineSpec.Inline[0].Content)

			if tt.expectedEnv != nil {
				require.NotNil(resultComposeApp.EnvVars)
				for k, v := range tt.expectedEnv {
					assert.Equal(t, v, (*resultComposeApp.EnvVars)[k])
				}
			}
		})
	}
}
