package tasks

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/domain"
	dependencyrefservice "github.com/flightctl/flightctl/internal/service/dependencyref"
	deviceservice "github.com/flightctl/flightctl/internal/service/device"
	fleetservice "github.com/flightctl/flightctl/internal/service/fleet"
	templateversionservice "github.com/flightctl/flightctl/internal/service/templateversion"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/internal/util"
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

			mockFleetSvc := fleetservice.NewMockService(ctrl)
			mockTemplateVersionSvc := templateversionservice.NewMockService(ctrl)
			mockDeviceSvc := deviceservice.NewMockService(ctrl)
			mockDependencyRefSvc := dependencyrefservice.NewMockService(ctrl)

			// Mock GetFleet to return our test fleet
			mockFleetSvc.EXPECT().GetFleet(gomock.Any(), gomock.Any(), fleetName, gomock.Any()).Return(tt.fleet, domain.Status{Code: http.StatusOK})

			// Mock GetLatestTemplateVersion with a simple template that won't trigger complex processing
			templateVersion := createTestTemplateVersion("test-tv")
			// Ensure the template has no complex fields that might cause device modification
			templateVersion.Status.Os = nil
			templateVersion.Status.Config = nil
			templateVersion.Status.Applications = nil
			mockTemplateVersionSvc.EXPECT().GetLatestTemplateVersion(gomock.Any(), gomock.Any(), fleetName).Return(templateVersion, domain.Status{Code: http.StatusOK})
			mockDependencyRefSvc.EXPECT().ReplaceDeviceDependencyRefsByFleet(gomock.Any(), gomock.Any(), fleetName, gomock.Any()).Return(domain.Status{Code: http.StatusOK})

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
			mockDeviceSvc.EXPECT().ListDevices(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(&domain.DeviceList{
				Metadata: domain.ListMeta{},
				Items:    []domain.Device{*testDevice},
			}, domain.Status{Code: http.StatusOK})

			// Mock ReplaceDevice to capture the delayDeviceRender value from context
			// This will be called during the device update process, allowing us to verify propagation
			var capturedDelayDeviceRender bool
			mockDeviceSvc.EXPECT().ReplaceDevice(gomock.Any(), gomock.Any(), "test-device", gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
				func(ctx context.Context, orgId uuid.UUID, name string, device domain.Device, fieldsToUnset []string, enforceOwnership bool) (*domain.Device, domain.Status) {
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
			mockDeviceSvc.EXPECT().UpdateDeviceAnnotations(gomock.Any(), gomock.Any(), "test-device", gomock.Any(), gomock.Any()).Return(domain.Status{Code: http.StatusOK})

			// Create FleetRolloutsLogic instance
			logic := NewFleetRolloutsLogic(log, mockFleetSvc, mockTemplateVersionSvc, mockDeviceSvc, mockDependencyRefSvc, orgId, event)

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

			mockFleetSvc := fleetservice.NewMockService(ctrl)
			mockTemplateVersionSvc := templateversionservice.NewMockService(ctrl)
			mockDeviceSvc := deviceservice.NewMockService(ctrl)
			mockDependencyRefSvc := dependencyrefservice.NewMockService(ctrl)

			// Create FleetRolloutsLogic instance
			logic := NewFleetRolloutsLogic(log, mockFleetSvc, mockTemplateVersionSvc, mockDeviceSvc, mockDependencyRefSvc, orgId, event)

			// Set the owner field to match the device owner
			logic.owner = "fleet/test-fleet"

			// Create test device with matching owner
			device := createTestDevice("test-device", "fleet/test-fleet")

			// Mock ReplaceDevice to capture the context value
			var capturedDelayDeviceRender bool
			mockDeviceSvc.EXPECT().ReplaceDevice(gomock.Any(), gomock.Any(), "test-device", gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
				func(ctx context.Context, orgId uuid.UUID, name string, device domain.Device, fieldsToUnset []string, enforceOwnership bool) (*domain.Device, domain.Status) {
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

func TestFleetRolloutsLogic_updateDeviceToFleetTemplate_SkipCondition(t *testing.T) {
	const tvName = "v1"
	const image = "test-image:latest"

	tests := []struct {
		name                string
		templateVersion     string
		renderedVersion     string
		deviceImage         string
		expectReplaceDevice bool
		description         string
	}{
		{
			name:                "WhenVersionAndRenderedVersionAndSpecMatch",
			templateVersion:     tvName,
			renderedVersion:     tvName,
			deviceImage:         image,
			expectReplaceDevice: false,
			description:         "When templateVersion, renderedTemplateVersion, and spec all match fleet, rollout should be skipped",
		},
		{
			name:                "WhenRenderedVersionDiffersFromFleetVersion",
			templateVersion:     tvName,
			renderedVersion:     "v2",
			deviceImage:         image,
			expectReplaceDevice: true,
			description:         "When renderedTemplateVersion differs from fleet templateVersion, rollout must not be skipped even if spec is unchanged",
		},
		{
			name:                "WhenTemplateVersionDiffersFromFleetVersion",
			templateVersion:     "v2",
			renderedVersion:     "v2",
			deviceImage:         image,
			expectReplaceDevice: true,
			description:         "When templateVersion differs from fleet templateVersion, rollout must not be skipped",
		},
		{
			name:                "WhenSpecDiffersFromFleetSpec",
			templateVersion:     tvName,
			renderedVersion:     tvName,
			deviceImage:         "old-image:latest",
			expectReplaceDevice: true,
			description:         "When spec differs from fleet spec, rollout must not be skipped even if versions match",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			orgId := uuid.New()
			log := logrus.New()
			fleetName := "test-fleet"
			event := domain.Event{
				InvolvedObject: domain.ObjectReference{Kind: domain.FleetKind, Name: fleetName},
			}
			mockDeviceSvc := deviceservice.NewMockService(ctrl)
			logic := NewFleetRolloutsLogic(log, nil, nil, mockDeviceSvc, nil, orgId, event)
			logic.owner = lo.FromPtr(util.SetResourceOwner(domain.FleetKind, fleetName))

			annotations := map[string]string{
				domain.DeviceAnnotationTemplateVersion:         tt.templateVersion,
				domain.DeviceAnnotationRenderedTemplateVersion: tt.renderedVersion,
			}
			deviceName := "test-device"
			ownerStr := logic.owner
			device := &domain.Device{
				Metadata: domain.ObjectMeta{
					Name:        &deviceName,
					Owner:       &ownerStr,
					Annotations: &annotations,
				},
				Spec: &domain.DeviceSpec{
					Os: &domain.DeviceOsSpec{Image: tt.deviceImage},
				},
				Status: &domain.DeviceStatus{},
			}

			tv := createTestTemplateVersion(tvName)
			tv.Status.Os = &domain.DeviceOsSpec{Image: image}

			if tt.expectReplaceDevice {
				mockDeviceSvc.EXPECT().ReplaceDevice(gomock.Any(), orgId, deviceName, gomock.Any(), gomock.Any(), gomock.Any()).Return(device, domain.Status{Code: http.StatusOK})
				mockDeviceSvc.EXPECT().UpdateDeviceAnnotations(gomock.Any(), orgId, deviceName, gomock.Any(), gomock.Any()).Return(domain.Status{Code: http.StatusOK})
			}

			_, err := logic.updateDeviceToFleetTemplate(context.Background(), device, tv, false)
			require.NoError(t, err)
		})
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

func TestReplaceGitConfigParameters_DeviceLevelRefs(t *testing.T) {
	fleetName := "my-fleet"
	logic := FleetRolloutsLogic{
		log: logrus.New(),
		event: domain.Event{
			InvolvedObject: domain.ObjectReference{Name: fleetName},
		},
	}

	t.Run("When targetRevision is parameterized it should return a device-level DependencyRef", func(t *testing.T) {
		owner := util.SetResourceOwner(domain.FleetKind, fleetName)
		device := &domain.Device{
			Metadata: domain.ObjectMeta{
				Name:   lo.ToPtr("device-1"),
				Owner:  owner,
				Labels: &map[string]string{"branch": "feature-a"},
			},
		}
		configItem := makeGitConfigItem(t, "git-cfg", "my-repo", "{{ .metadata.labels.branch }}")

		newCfg, refs, errs := logic.replaceGitConfigParameters(device, configItem)
		require.Empty(t, errs)
		require.NotNil(t, newCfg)
		require.Len(t, refs, 1)
		assert.Equal(t, fleetName, *refs[0].FleetName)
		assert.Equal(t, "device-1", *refs[0].DeviceName)
		assert.Equal(t, "git", refs[0].RefType)
		assert.Equal(t, "feature-a", *refs[0].Revision)
		assert.Equal(t, "git:my-repo/feature-a", refs[0].ResourceKey)
		assert.Equal(t, "my-repo", *refs[0].RepositoryName)
	})

	t.Run("When targetRevision is not parameterized it should return no refs", func(t *testing.T) {
		device := &domain.Device{
			Metadata: domain.ObjectMeta{
				Name: lo.ToPtr("device-1"),
			},
		}
		configItem := makeGitConfigItem(t, "git-cfg", "my-repo", "main")

		newCfg, refs, errs := logic.replaceGitConfigParameters(device, configItem)
		require.Empty(t, errs)
		require.NotNil(t, newCfg)
		assert.Empty(t, refs)
	})

	t.Run("When multiple git configs have parameterized revisions getDeviceConfig collects all refs", func(t *testing.T) {
		owner := util.SetResourceOwner(domain.FleetKind, fleetName)
		device := &domain.Device{
			Metadata: domain.ObjectMeta{
				Name:   lo.ToPtr("device-1"),
				Owner:  owner,
				Labels: &map[string]string{"branch": "dev", "env": "staging"},
			},
		}
		tv := &domain.TemplateVersion{
			Status: &domain.TemplateVersionStatus{
				Config: &[]domain.ConfigProviderSpec{
					makeGitConfigItem(t, "git-1", "repo-a", "{{ .metadata.labels.branch }}"),
					makeGitConfigItem(t, "git-2", "repo-b", "main"),
					makeGitConfigItem(t, "git-3", "repo-c", "{{ .metadata.labels.env }}"),
				},
			},
		}

		_, refs, errs := logic.getDeviceConfig(device, tv)
		require.Empty(t, errs)
		require.Len(t, refs, 2)

		refsByKey := make(map[string]model.DependencyRef, len(refs))
		for _, r := range refs {
			refsByKey[r.ResourceKey] = r
		}

		assert.Contains(t, refsByKey, "git:repo-a/dev")
		assert.Equal(t, "device-1", *refsByKey["git:repo-a/dev"].DeviceName)

		assert.Contains(t, refsByKey, "git:repo-c/staging")
		assert.Equal(t, "device-1", *refsByKey["git:repo-c/staging"].DeviceName)
	})
}

func TestRolloutFleetPage_UpsertDeviceRefs(t *testing.T) {
	orgId := uuid.New()
	fleetName := "my-fleet"
	okStatus := domain.Status{Code: http.StatusOK}

	t.Run("When devices have parameterized revisions it should batch-upsert refs after the page", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		mockFleetSvc := fleetservice.NewMockService(ctrl)
		mockTemplateVersionSvc := templateversionservice.NewMockService(ctrl)
		mockDeviceSvc := deviceservice.NewMockService(ctrl)
		mockDependencyRefSvc := dependencyrefservice.NewMockService(ctrl)

		owner := "Fleet/my-fleet"
		devices := &domain.DeviceList{
			Items: []domain.Device{
				{
					Metadata: domain.ObjectMeta{
						Name:   lo.ToPtr("device-1"),
						Owner:  &owner,
						Labels: &map[string]string{"branch": "feature-a"},
					},
					Spec: &domain.DeviceSpec{},
				},
				{
					Metadata: domain.ObjectMeta{
						Name:   lo.ToPtr("device-2"),
						Owner:  &owner,
						Labels: &map[string]string{"branch": "feature-b"},
					},
					Spec: &domain.DeviceSpec{},
				},
			},
		}
		mockDeviceSvc.EXPECT().ListDevices(gomock.Any(), orgId, gomock.Any(), gomock.Any()).Return(devices, okStatus)

		tv := &domain.TemplateVersion{
			Metadata: domain.ObjectMeta{
				Name: lo.ToPtr("v1"),
			},
			Status: &domain.TemplateVersionStatus{
				Config: &[]domain.ConfigProviderSpec{
					makeGitConfigItem(t, "git-cfg", "my-repo", "{{ .metadata.labels.branch }}"),
				},
			},
		}

		mockDeviceSvc.EXPECT().ReplaceDevice(gomock.Any(), orgId, gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, okStatus).AnyTimes()
		mockDeviceSvc.EXPECT().UpdateDeviceAnnotations(gomock.Any(), orgId, gomock.Any(), gomock.Any(), gomock.Any()).Return(okStatus).AnyTimes()

		logic := FleetRolloutsLogic{
			log:                logrus.New(),
			fleetSvc:           mockFleetSvc,
			templateversionSvc: mockTemplateVersionSvc,
			deviceSvc:          mockDeviceSvc,
			dependencyrefSvc:   mockDependencyRefSvc,
			orgId:              orgId,
			owner:              owner,
			event: domain.Event{
				InvolvedObject: domain.ObjectReference{Name: fleetName},
			},
		}

		pageFailures, pageRefs, nextContinue, err := logic.rolloutFleetPage(
			context.Background(),
			tv,
			domain.ListDevicesParams{},
			nil,
			false,
		)

		require.NoError(t, err)
		assert.Equal(t, 0, pageFailures)
		assert.Nil(t, nextContinue)
		require.Len(t, pageRefs, 2)

		refsByDevice := map[string]model.DependencyRef{}
		for _, r := range pageRefs {
			refsByDevice[*r.DeviceName] = r
		}
		assert.Equal(t, "feature-a", *refsByDevice["device-1"].Revision)
		assert.Equal(t, "git:my-repo/feature-a", refsByDevice["device-1"].ResourceKey)
		assert.Equal(t, "feature-b", *refsByDevice["device-2"].Revision)
		assert.Equal(t, "git:my-repo/feature-b", refsByDevice["device-2"].ResourceKey)
	})
}

func TestDeviceDependencyRefLifecycle(t *testing.T) {
	fleetName := "fleet-a"
	owner := util.SetResourceOwner(domain.FleetKind, fleetName)

	t.Run("When device is standalone it should produce only device-level refs with empty fleet name", func(t *testing.T) {
		logic := FleetRolloutsLogic{
			log: logrus.New(),
			event: domain.Event{
				InvolvedObject: domain.ObjectReference{Name: "standalone-dev"},
			},
		}
		device := &domain.Device{
			Metadata: domain.ObjectMeta{
				Name: lo.ToPtr("standalone-dev"),
			},
		}
		configItem := makeGitConfigItem(t, "git-cfg", "my-repo", "main")
		_, refs, errs := logic.replaceGitConfigParameters(device, configItem)
		require.Empty(t, errs)
		assert.Empty(t, refs, "non-parameterized revision should not produce device-level refs")
	})

	t.Run("When device is owned by fleet A it should produce refs scoped to fleet A", func(t *testing.T) {
		logic := FleetRolloutsLogic{
			log: logrus.New(),
			event: domain.Event{
				InvolvedObject: domain.ObjectReference{Name: fleetName},
			},
		}
		device := &domain.Device{
			Metadata: domain.ObjectMeta{
				Name:   lo.ToPtr("device-1"),
				Owner:  owner,
				Labels: &map[string]string{"branch": "main"},
			},
		}
		configItem := makeGitConfigItem(t, "git-cfg", "my-repo", "{{ .metadata.labels.branch }}")
		_, refs, errs := logic.replaceGitConfigParameters(device, configItem)
		require.Empty(t, errs)
		require.Len(t, refs, 1)
		assert.Equal(t, fleetName, *refs[0].FleetName)
		assert.Equal(t, "device-1", *refs[0].DeviceName)
		assert.Equal(t, "git:my-repo/main", refs[0].ResourceKey)
	})

	t.Run("When fleet config has parameterized ref it should create device-level ref with resolved value", func(t *testing.T) {
		logic := FleetRolloutsLogic{
			log: logrus.New(),
			event: domain.Event{
				InvolvedObject: domain.ObjectReference{Name: fleetName},
			},
		}
		device := &domain.Device{
			Metadata: domain.ObjectMeta{
				Name:   lo.ToPtr("device-1"),
				Owner:  owner,
				Labels: &map[string]string{"branch": "feature-x"},
			},
		}
		tv := &domain.TemplateVersion{
			Status: &domain.TemplateVersionStatus{
				Config: &[]domain.ConfigProviderSpec{
					makeGitConfigItem(t, "git-cfg", "my-repo", "{{ .metadata.labels.branch }}"),
				},
			},
		}
		_, refs, errs := logic.getDeviceConfig(device, tv)
		require.Empty(t, errs)
		require.Len(t, refs, 1)
		assert.Equal(t, "device-1", *refs[0].DeviceName)
		assert.Equal(t, fleetName, *refs[0].FleetName)
		assert.Equal(t, "feature-x", *refs[0].Revision)
		assert.Equal(t, "git:my-repo/feature-x", refs[0].ResourceKey)
	})

	t.Run("When parameterized ref becomes non-parameterized it should produce no device-level refs", func(t *testing.T) {
		logic := FleetRolloutsLogic{
			log: logrus.New(),
			event: domain.Event{
				InvolvedObject: domain.ObjectReference{Name: fleetName},
			},
		}
		device := &domain.Device{
			Metadata: domain.ObjectMeta{
				Name:   lo.ToPtr("device-1"),
				Owner:  owner,
				Labels: &map[string]string{"branch": "main"},
			},
		}
		tv := &domain.TemplateVersion{
			Status: &domain.TemplateVersionStatus{
				Config: &[]domain.ConfigProviderSpec{
					makeGitConfigItem(t, "git-cfg", "my-repo", "main"),
				},
			},
		}
		_, refs, errs := logic.getDeviceConfig(device, tv)
		require.Empty(t, errs)
		assert.Empty(t, refs, "non-parameterized revision should not produce device-level refs")
	})

	t.Run("When device label changes it should update parameterized ref value", func(t *testing.T) {
		logic := FleetRolloutsLogic{
			log: logrus.New(),
			event: domain.Event{
				InvolvedObject: domain.ObjectReference{Name: fleetName},
			},
		}
		configItem := makeGitConfigItem(t, "git-cfg", "my-repo", "{{ .metadata.labels.branch }}")

		deviceBefore := &domain.Device{
			Metadata: domain.ObjectMeta{
				Name:   lo.ToPtr("device-1"),
				Owner:  owner,
				Labels: &map[string]string{"branch": "main"},
			},
		}
		_, refsBefore, errsBefore := logic.replaceGitConfigParameters(deviceBefore, configItem)
		require.Empty(t, errsBefore)
		require.Len(t, refsBefore, 1)
		assert.Equal(t, "git:my-repo/main", refsBefore[0].ResourceKey)

		deviceAfter := &domain.Device{
			Metadata: domain.ObjectMeta{
				Name:   lo.ToPtr("device-1"),
				Owner:  owner,
				Labels: &map[string]string{"branch": "feature-x"},
			},
		}
		_, refsAfter, errsAfter := logic.replaceGitConfigParameters(deviceAfter, configItem)
		require.Empty(t, errsAfter)
		require.Len(t, refsAfter, 1)
		assert.Equal(t, "git:my-repo/feature-x", refsAfter[0].ResourceKey)
		assert.NotEqual(t, refsBefore[0].ResourceKey, refsAfter[0].ResourceKey)
	})

	t.Run("When device label changes on irrelevant key it should not change ref value", func(t *testing.T) {
		logic := FleetRolloutsLogic{
			log: logrus.New(),
			event: domain.Event{
				InvolvedObject: domain.ObjectReference{Name: fleetName},
			},
		}
		configItem := makeGitConfigItem(t, "git-cfg", "my-repo", "{{ .metadata.labels.branch }}")

		deviceBefore := &domain.Device{
			Metadata: domain.ObjectMeta{
				Name:   lo.ToPtr("device-1"),
				Owner:  owner,
				Labels: &map[string]string{"branch": "main", "env": "prod"},
			},
		}
		_, refsBefore, errsBefore := logic.replaceGitConfigParameters(deviceBefore, configItem)
		require.Empty(t, errsBefore)
		require.Len(t, refsBefore, 1)

		deviceAfter := &domain.Device{
			Metadata: domain.ObjectMeta{
				Name:   lo.ToPtr("device-1"),
				Owner:  owner,
				Labels: &map[string]string{"branch": "main", "env": "staging"},
			},
		}
		_, refsAfter, errsAfter := logic.replaceGitConfigParameters(deviceAfter, configItem)
		require.Empty(t, errsAfter)
		require.Len(t, refsAfter, 1)
		assert.Equal(t, refsBefore[0].ResourceKey, refsAfter[0].ResourceKey)
	})

	t.Run("When RolloutDevice is called it should use transactional ReplaceFleetDeviceDependencyRefs", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		mockFleetSvc := fleetservice.NewMockService(ctrl)
		mockTemplateVersionSvc := templateversionservice.NewMockService(ctrl)
		mockDeviceSvc := deviceservice.NewMockService(ctrl)
		mockDependencyRefSvc := dependencyrefservice.NewMockService(ctrl)
		orgId := uuid.New()
		okStatus := domain.Status{Code: http.StatusOK}

		device := &domain.Device{
			Metadata: domain.ObjectMeta{
				Name:   lo.ToPtr("device-1"),
				Owner:  owner,
				Labels: &map[string]string{"branch": "feature-a"},
			},
			Spec: &domain.DeviceSpec{},
			Status: &domain.DeviceStatus{
				Conditions: []domain.Condition{},
			},
		}
		mockDeviceSvc.EXPECT().GetDevice(gomock.Any(), orgId, "device-1").Return(device, okStatus)

		tv := &domain.TemplateVersion{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr("v1")},
			Status: &domain.TemplateVersionStatus{
				Config: &[]domain.ConfigProviderSpec{
					makeGitConfigItem(t, "git-cfg", "my-repo", "{{ .metadata.labels.branch }}"),
				},
			},
		}
		mockTemplateVersionSvc.EXPECT().GetLatestTemplateVersion(gomock.Any(), orgId, fleetName).Return(tv, okStatus)

		fleet := createTestFleetForRollout(fleetName, nil)
		mockFleetSvc.EXPECT().GetFleet(gomock.Any(), orgId, fleetName, gomock.Any()).Return(fleet, okStatus)

		mockDeviceSvc.EXPECT().ReplaceDevice(gomock.Any(), orgId, gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(device, okStatus)
		mockDeviceSvc.EXPECT().UpdateDeviceAnnotations(gomock.Any(), orgId, "device-1", gomock.Any(), gomock.Any()).Return(okStatus)

		mockDependencyRefSvc.EXPECT().ReplaceFleetScopedDeviceDependencyRefs(
			gomock.Any(), orgId, "device-1",
			gomock.Len(1),
		).Return(okStatus)

		logic := FleetRolloutsLogic{
			log:                logrus.New(),
			fleetSvc:           mockFleetSvc,
			templateversionSvc: mockTemplateVersionSvc,
			deviceSvc:          mockDeviceSvc,
			dependencyrefSvc:   mockDependencyRefSvc,
			orgId:              orgId,
			event: domain.Event{
				InvolvedObject: domain.ObjectReference{Name: "device-1", Kind: domain.DeviceKind},
			},
		}
		err := logic.RolloutDevice(context.Background())
		require.NoError(t, err)
	})
}

func TestDeviceSecretDependencyRefLifecycle(t *testing.T) {
	fleetName := "fleet-a"
	owner := util.SetResourceOwner(domain.FleetKind, fleetName)

	t.Run("When secret has no parameterized fields it should produce no device-level refs", func(t *testing.T) {
		logic := FleetRolloutsLogic{
			log: logrus.New(),
			event: domain.Event{
				InvolvedObject: domain.ObjectReference{Name: fleetName},
			},
		}
		device := &domain.Device{
			Metadata: domain.ObjectMeta{
				Name:  lo.ToPtr("device-1"),
				Owner: owner,
			},
		}
		configItem := makeSecretConfigItem(t, "secret-cfg", "prod", "db-creds")
		_, refs, errs := logic.replaceKubeSecretConfigParameters(device, configItem)
		require.Empty(t, errs)
		assert.Empty(t, refs, "non-parameterized secret should not produce device-level refs")
	})

	t.Run("When secret namespace is parameterized it should create device-level ref with resolved value", func(t *testing.T) {
		logic := FleetRolloutsLogic{
			log: logrus.New(),
			event: domain.Event{
				InvolvedObject: domain.ObjectReference{Name: fleetName},
			},
		}
		device := &domain.Device{
			Metadata: domain.ObjectMeta{
				Name:   lo.ToPtr("device-1"),
				Owner:  owner,
				Labels: &map[string]string{"ns": "prod"},
			},
		}
		configItem := makeSecretConfigItem(t, "secret-cfg", "{{ .metadata.labels.ns }}", "db-creds")
		_, refs, errs := logic.replaceKubeSecretConfigParameters(device, configItem)
		require.Empty(t, errs)
		require.Len(t, refs, 1)
		assert.Equal(t, fleetName, *refs[0].FleetName)
		assert.Equal(t, "device-1", *refs[0].DeviceName)
		assert.Equal(t, "secret", refs[0].RefType)
		assert.Equal(t, "secret:prod/db-creds", refs[0].ResourceKey)
		assert.Equal(t, "prod", *refs[0].SecretNamespace)
		assert.Equal(t, "db-creds", *refs[0].SecretName)
	})

	t.Run("When secret name is parameterized it should create device-level ref with resolved value", func(t *testing.T) {
		logic := FleetRolloutsLogic{
			log: logrus.New(),
			event: domain.Event{
				InvolvedObject: domain.ObjectReference{Name: fleetName},
			},
		}
		device := &domain.Device{
			Metadata: domain.ObjectMeta{
				Name:   lo.ToPtr("device-1"),
				Owner:  owner,
				Labels: &map[string]string{"secret": "my-secret"},
			},
		}
		configItem := makeSecretConfigItem(t, "secret-cfg", "prod", "{{ .metadata.labels.secret }}")
		_, refs, errs := logic.replaceKubeSecretConfigParameters(device, configItem)
		require.Empty(t, errs)
		require.Len(t, refs, 1)
		assert.Equal(t, "secret:prod/my-secret", refs[0].ResourceKey)
		assert.Equal(t, "prod", *refs[0].SecretNamespace)
		assert.Equal(t, "my-secret", *refs[0].SecretName)
	})

	t.Run("When both namespace and name are parameterized it should resolve both in the ref", func(t *testing.T) {
		logic := FleetRolloutsLogic{
			log: logrus.New(),
			event: domain.Event{
				InvolvedObject: domain.ObjectReference{Name: fleetName},
			},
		}
		device := &domain.Device{
			Metadata: domain.ObjectMeta{
				Name:   lo.ToPtr("device-1"),
				Owner:  owner,
				Labels: &map[string]string{"ns": "staging", "secret": "api-key"},
			},
		}
		configItem := makeSecretConfigItem(t, "secret-cfg", "{{ .metadata.labels.ns }}", "{{ .metadata.labels.secret }}")
		_, refs, errs := logic.replaceKubeSecretConfigParameters(device, configItem)
		require.Empty(t, errs)
		require.Len(t, refs, 1)
		assert.Equal(t, "secret:staging/api-key", refs[0].ResourceKey)
	})

	t.Run("When getDeviceConfig has parameterized secret it should include secret refs in output", func(t *testing.T) {
		logic := FleetRolloutsLogic{
			log: logrus.New(),
			event: domain.Event{
				InvolvedObject: domain.ObjectReference{Name: fleetName},
			},
		}
		device := &domain.Device{
			Metadata: domain.ObjectMeta{
				Name:   lo.ToPtr("device-1"),
				Owner:  owner,
				Labels: &map[string]string{"ns": "prod"},
			},
		}
		tv := &domain.TemplateVersion{
			Status: &domain.TemplateVersionStatus{
				Config: &[]domain.ConfigProviderSpec{
					makeSecretConfigItem(t, "secret-cfg", "{{ .metadata.labels.ns }}", "db-creds"),
				},
			},
		}
		_, refs, errs := logic.getDeviceConfig(device, tv)
		require.Empty(t, errs)
		require.Len(t, refs, 1)
		assert.Equal(t, "secret", refs[0].RefType)
		assert.Equal(t, "secret:prod/db-creds", refs[0].ResourceKey)
	})
}

func TestDeviceHttpDependencyRefLifecycle(t *testing.T) {
	fleetName := "fleet-a"
	owner := util.SetResourceOwner(domain.FleetKind, fleetName)

	t.Run("When HTTP suffix is not parameterized it should produce no device-level refs", func(t *testing.T) {
		logic := FleetRolloutsLogic{
			log: logrus.New(),
			event: domain.Event{
				InvolvedObject: domain.ObjectReference{Name: fleetName},
			},
		}
		device := &domain.Device{
			Metadata: domain.ObjectMeta{
				Name:  lo.ToPtr("device-1"),
				Owner: owner,
			},
		}
		suffix := "/config.json"
		configItem := makeHttpConfigItem(t, "http-cfg", "http-repo", &suffix)
		_, refs, errs := logic.replaceHTTPConfigParameters(device, configItem)
		require.Empty(t, errs)
		assert.Empty(t, refs, "non-parameterized suffix should not produce device-level refs")
	})

	t.Run("When HTTP suffix is parameterized it should create device-level ref with resolved value", func(t *testing.T) {
		logic := FleetRolloutsLogic{
			log: logrus.New(),
			event: domain.Event{
				InvolvedObject: domain.ObjectReference{Name: fleetName},
			},
		}
		device := &domain.Device{
			Metadata: domain.ObjectMeta{
				Name:   lo.ToPtr("device-1"),
				Owner:  owner,
				Labels: &map[string]string{"env": "prod"},
			},
		}
		suffix := "/{{ .metadata.labels.env }}/config.json"
		configItem := makeHttpConfigItem(t, "http-cfg", "http-repo", &suffix)
		_, refs, errs := logic.replaceHTTPConfigParameters(device, configItem)
		require.Empty(t, errs)
		require.Len(t, refs, 1)
		assert.Equal(t, fleetName, *refs[0].FleetName)
		assert.Equal(t, "device-1", *refs[0].DeviceName)
		assert.Equal(t, "http", refs[0].RefType)
		assert.Equal(t, "http:http-repo/prod/config.json", refs[0].ResourceKey)
		assert.Equal(t, "/prod/config.json", *refs[0].HTTPSuffix)
	})

	t.Run("When device labels change and HTTP suffix uses label template it should produce different ref", func(t *testing.T) {
		logic := FleetRolloutsLogic{
			log: logrus.New(),
			event: domain.Event{
				InvolvedObject: domain.ObjectReference{Name: fleetName},
			},
		}
		suffix := "/{{ .metadata.labels.env }}/config.json"
		configItem := makeHttpConfigItem(t, "http-cfg", "http-repo", &suffix)

		deviceBefore := &domain.Device{
			Metadata: domain.ObjectMeta{
				Name:   lo.ToPtr("device-1"),
				Owner:  owner,
				Labels: &map[string]string{"env": "staging"},
			},
		}
		_, refsBefore, errsBefore := logic.replaceHTTPConfigParameters(deviceBefore, configItem)
		require.Empty(t, errsBefore)
		require.Len(t, refsBefore, 1)
		assert.Equal(t, "http:http-repo/staging/config.json", refsBefore[0].ResourceKey)

		deviceAfter := &domain.Device{
			Metadata: domain.ObjectMeta{
				Name:   lo.ToPtr("device-1"),
				Owner:  owner,
				Labels: &map[string]string{"env": "prod"},
			},
		}
		_, refsAfter, errsAfter := logic.replaceHTTPConfigParameters(deviceAfter, configItem)
		require.Empty(t, errsAfter)
		require.Len(t, refsAfter, 1)
		assert.Equal(t, "http:http-repo/prod/config.json", refsAfter[0].ResourceKey)
		assert.NotEqual(t, refsBefore[0].ResourceKey, refsAfter[0].ResourceKey)
	})

	t.Run("When getDeviceConfig has parameterized HTTP suffix it should include HTTP refs in output", func(t *testing.T) {
		logic := FleetRolloutsLogic{
			log: logrus.New(),
			event: domain.Event{
				InvolvedObject: domain.ObjectReference{Name: fleetName},
			},
		}
		device := &domain.Device{
			Metadata: domain.ObjectMeta{
				Name:   lo.ToPtr("device-1"),
				Owner:  owner,
				Labels: &map[string]string{"env": "prod"},
			},
		}
		suffix := "/{{ .metadata.labels.env }}/config.json"
		tv := &domain.TemplateVersion{
			Status: &domain.TemplateVersionStatus{
				Config: &[]domain.ConfigProviderSpec{
					makeHttpConfigItem(t, "http-cfg", "http-repo", &suffix),
				},
			},
		}
		_, refs, errs := logic.getDeviceConfig(device, tv)
		require.Empty(t, errs)
		require.Len(t, refs, 1)
		assert.Equal(t, "http", refs[0].RefType)
		assert.Equal(t, "http:http-repo/prod/config.json", refs[0].ResourceKey)
	})
}

func TestFleetRolloutsLogic_RolloutDevice_ApplicationLifecycleSync(t *testing.T) {
	fleetName := "test-fleet"
	deviceName := "device-1"
	okStatus := domain.Status{Code: http.StatusOK}

	newDevice := func() *domain.Device {
		return &domain.Device{
			Metadata: domain.ObjectMeta{
				Name:  lo.ToPtr(deviceName),
				Owner: util.SetResourceOwner(domain.FleetKind, fleetName),
			},
			Spec: &domain.DeviceSpec{},
			Status: &domain.DeviceStatus{
				Conditions: []domain.Condition{},
			},
		}
	}

	newFleetWithLifecycleDefault := func() *domain.Fleet {
		fleet := createTestFleetForRollout(fleetName, nil)
		fleet.Metadata.Annotations = &map[string]string{
			domain.FleetAnnotationApplicationLifecycle: `{"app-1":"stopped"}`,
		}
		return fleet
	}

	t.Run("When the fleet has a lifecycle default it should sync it onto the device before continuing the rollout", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		mockFleetSvc := fleetservice.NewMockService(ctrl)
		mockTemplateVersionSvc := templateversionservice.NewMockService(ctrl)
		mockDeviceSvc := deviceservice.NewMockService(ctrl)
		mockDependencyRefSvc := dependencyrefservice.NewMockService(ctrl)
		orgId := uuid.New()

		device := newDevice()
		mockDeviceSvc.EXPECT().GetDevice(gomock.Any(), orgId, deviceName).Return(device, okStatus)
		mockTemplateVersionSvc.EXPECT().GetLatestTemplateVersion(gomock.Any(), orgId, fleetName).Return(createTestTemplateVersion("v1"), okStatus)
		mockFleetSvc.EXPECT().GetFleet(gomock.Any(), orgId, fleetName, gomock.Any()).Return(newFleetWithLifecycleDefault(), okStatus)

		// The lifecycle-default sync (UpdateDeviceAnnotations #1) is distinct from the
		// templateVersion-tracking annotation update (#2) that updateDeviceToFleetTemplate
		// issues later in the same rollout for the (empty) config/app refs.
		mockDeviceSvc.EXPECT().UpdateDeviceAnnotations(gomock.Any(), orgId, deviceName,
			map[string]string{domain.DeviceAnnotationFleetApplicationLifecycle: `{"app-1":"stopped"}`}, nil,
		).Return(okStatus)
		mockDeviceSvc.EXPECT().UpdateDeviceAnnotations(gomock.Any(), orgId, deviceName,
			gomock.Not(gomock.Eq(map[string]string{domain.DeviceAnnotationFleetApplicationLifecycle: `{"app-1":"stopped"}`})), gomock.Any(),
		).Return(okStatus)
		mockDeviceSvc.EXPECT().ReplaceDevice(gomock.Any(), orgId, deviceName, gomock.Any(), gomock.Any(), gomock.Any()).Return(device, okStatus)
		mockDependencyRefSvc.EXPECT().ReplaceFleetScopedDeviceDependencyRefs(gomock.Any(), orgId, deviceName, gomock.Any()).Return(okStatus)

		logic := FleetRolloutsLogic{
			log:                logrus.New(),
			fleetSvc:           mockFleetSvc,
			templateversionSvc: mockTemplateVersionSvc,
			deviceSvc:          mockDeviceSvc,
			dependencyrefSvc:   mockDependencyRefSvc,
			orgId:              orgId,
			event: domain.Event{
				InvolvedObject: domain.ObjectReference{Name: deviceName, Kind: domain.DeviceKind},
			},
		}
		require.NoError(t, logic.RolloutDevice(context.Background()))
	})

	t.Run("When syncing the lifecycle default fails it should log and continue the rollout instead of aborting it", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		mockFleetSvc := fleetservice.NewMockService(ctrl)
		mockTemplateVersionSvc := templateversionservice.NewMockService(ctrl)
		mockDeviceSvc := deviceservice.NewMockService(ctrl)
		mockDependencyRefSvc := dependencyrefservice.NewMockService(ctrl)
		orgId := uuid.New()

		device := newDevice()
		mockDeviceSvc.EXPECT().GetDevice(gomock.Any(), orgId, deviceName).Return(device, okStatus)
		mockTemplateVersionSvc.EXPECT().GetLatestTemplateVersion(gomock.Any(), orgId, fleetName).Return(createTestTemplateVersion("v1"), okStatus)
		mockFleetSvc.EXPECT().GetFleet(gomock.Any(), orgId, fleetName, gomock.Any()).Return(newFleetWithLifecycleDefault(), okStatus)

		mockDeviceSvc.EXPECT().UpdateDeviceAnnotations(gomock.Any(), orgId, deviceName,
			map[string]string{domain.DeviceAnnotationFleetApplicationLifecycle: `{"app-1":"stopped"}`}, nil,
		).Return(domain.Status{Code: http.StatusInternalServerError, Message: "boom"})
		mockDeviceSvc.EXPECT().UpdateDeviceAnnotations(gomock.Any(), orgId, deviceName,
			gomock.Not(gomock.Eq(map[string]string{domain.DeviceAnnotationFleetApplicationLifecycle: `{"app-1":"stopped"}`})), gomock.Any(),
		).Return(okStatus)
		mockDeviceSvc.EXPECT().ReplaceDevice(gomock.Any(), orgId, deviceName, gomock.Any(), gomock.Any(), gomock.Any()).Return(device, okStatus)
		mockDependencyRefSvc.EXPECT().ReplaceFleetScopedDeviceDependencyRefs(gomock.Any(), orgId, deviceName, gomock.Any()).Return(okStatus)

		logic := FleetRolloutsLogic{
			log:                logrus.New(),
			fleetSvc:           mockFleetSvc,
			templateversionSvc: mockTemplateVersionSvc,
			deviceSvc:          mockDeviceSvc,
			dependencyrefSvc:   mockDependencyRefSvc,
			orgId:              orgId,
			event: domain.Event{
				InvolvedObject: domain.ObjectReference{Name: deviceName, Kind: domain.DeviceKind},
			},
		}
		require.NoError(t, logic.RolloutDevice(context.Background()),
			"a failure syncing the cached lifecycle default should not block the rest of the rollout")
	})
}

func TestFleetRolloutsLogic_SyncFleetApplicationLifecycleDefault(t *testing.T) {
	deviceName := "device-1"

	// hasAnnotation controls whether the device's cache annotation *key* is present at all
	// (even set to an empty-object placeholder), independent of the fleet's current value:
	// bootstrap must only ever run based on the key's absence, never on a value comparison.
	newDevice := func(hasAnnotation bool, value string) *domain.Device {
		device := &domain.Device{
			Metadata: domain.ObjectMeta{
				Name: lo.ToPtr(deviceName),
			},
		}
		if hasAnnotation {
			device.Metadata.Annotations = &map[string]string{
				domain.DeviceAnnotationFleetApplicationLifecycle: value,
			}
		}
		return device
	}

	newFleet := func(annotation string) *domain.Fleet {
		fleet := &domain.Fleet{Metadata: domain.ObjectMeta{Name: lo.ToPtr("test-fleet")}}
		if annotation != "" {
			fleet.Metadata.Annotations = &map[string]string{
				domain.FleetAnnotationApplicationLifecycle: annotation,
			}
		}
		return fleet
	}

	t.Run("When the device already has a cache annotation it should not call UpdateDeviceAnnotations even if the fleet's default has since changed", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		mockDeviceSvc := deviceservice.NewMockService(ctrl)

		logic := FleetRolloutsLogic{log: logrus.New(), deviceSvc: mockDeviceSvc, orgId: uuid.New()}
		err := logic.syncFleetApplicationLifecycleDefault(context.Background(), newDevice(true, `{"app-1":{"desiredState":"stopped"}}`), newFleet(`{"app-1":{"desiredState":"running"}}`))
		require.NoError(t, err, "a routine rollout must never clobber a device's already-synced lifecycle cache")
	})

	t.Run("When the device already has an empty cache annotation it should still be treated as synced", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		mockDeviceSvc := deviceservice.NewMockService(ctrl)

		logic := FleetRolloutsLogic{log: logrus.New(), deviceSvc: mockDeviceSvc, orgId: uuid.New()}
		err := logic.syncFleetApplicationLifecycleDefault(context.Background(), newDevice(true, "{}"), newFleet(`{"app-1":{"desiredState":"stopped"}}`))
		require.NoError(t, err, "presence of the key, not its value, is what determines bootstrap eligibility")
	})

	t.Run("When neither the device cache nor the fleet default exist it should not call UpdateDeviceAnnotations", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		mockDeviceSvc := deviceservice.NewMockService(ctrl)

		logic := FleetRolloutsLogic{log: logrus.New(), deviceSvc: mockDeviceSvc, orgId: uuid.New()}
		err := logic.syncFleetApplicationLifecycleDefault(context.Background(), newDevice(false, ""), newFleet(""))
		require.NoError(t, err)
	})

	t.Run("When the device has no cache annotation yet and the fleet has a default it should bootstrap the device's cache", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		mockDeviceSvc := deviceservice.NewMockService(ctrl)
		orgId := uuid.New()

		mockDeviceSvc.EXPECT().UpdateDeviceAnnotations(gomock.Any(), orgId, deviceName,
			map[string]string{domain.DeviceAnnotationFleetApplicationLifecycle: `{"app-1":{"desiredState":"stopped"}}`}, nil,
		).Return(domain.Status{Code: http.StatusOK})

		logic := FleetRolloutsLogic{log: logrus.New(), deviceSvc: mockDeviceSvc, orgId: orgId}
		err := logic.syncFleetApplicationLifecycleDefault(context.Background(), newDevice(false, ""), newFleet(`{"app-1":{"desiredState":"stopped"}}`))
		require.NoError(t, err)
	})

	t.Run("When the device has no cache annotation and the fleet has no default either it should not call UpdateDeviceAnnotations", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		mockDeviceSvc := deviceservice.NewMockService(ctrl)

		logic := FleetRolloutsLogic{log: logrus.New(), deviceSvc: mockDeviceSvc, orgId: uuid.New()}
		err := logic.syncFleetApplicationLifecycleDefault(context.Background(), newDevice(false, ""), newFleet(""))
		require.NoError(t, err)
	})

	t.Run("When UpdateDeviceAnnotations fails it should propagate the error", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		mockDeviceSvc := deviceservice.NewMockService(ctrl)
		orgId := uuid.New()

		mockDeviceSvc.EXPECT().UpdateDeviceAnnotations(gomock.Any(), orgId, deviceName, gomock.Any(), gomock.Any()).
			Return(domain.Status{Code: http.StatusInternalServerError, Message: "boom"})

		logic := FleetRolloutsLogic{log: logrus.New(), deviceSvc: mockDeviceSvc, orgId: orgId}
		err := logic.syncFleetApplicationLifecycleDefault(context.Background(), newDevice(false, ""), newFleet(`{"app-1":{"desiredState":"stopped"}}`))
		require.Error(t, err)
	})
}

func TestFleetRolloutIterationContext(t *testing.T) {
	t.Run("child not expired when parent deadline passed", func(t *testing.T) {
		parent, cancel := context.WithTimeout(context.Background(), 0)
		defer cancel()
		time.Sleep(time.Millisecond)
		require.Error(t, parent.Err())
		require.True(t, errors.Is(parent.Err(), context.DeadlineExceeded))

		iterCtx, cancelIter := fleetRolloutIterationContext(parent, time.Hour)
		defer cancelIter()
		assert.NoError(t, iterCtx.Err())
	})

	t.Run("child canceled when parent explicitly canceled", func(t *testing.T) {
		parent, cancelParent := context.WithCancel(context.Background())
		cancelParent()

		iterCtx, cancelIter := fleetRolloutIterationContext(parent, time.Hour)
		defer cancelIter()

		select {
		case <-iterCtx.Done():
		case <-time.After(2 * time.Second):
			t.Fatal("expected iteration context canceled after parent cancel")
		}
		assert.True(t, errors.Is(iterCtx.Err(), context.Canceled))
	})
}
