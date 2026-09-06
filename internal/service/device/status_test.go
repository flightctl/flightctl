package device

import (
	"context"
	"testing"
	"time"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/service/common"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
)

func TestUpdateServerSideDeviceStatus_PostRestoreState(t *testing.T) {
	// This test validates the critical post-restore state where ALL three conditions must be true:
	// 1. awaitingReconnect annotation = "true"
	// 2. lastSeen = zero time (cleared by restore)
	// 3. status summary = AwaitingReconnect
	//
	// This ensures that after restore, the AwaitingReconnect status takes precedence over
	// disconnection logic (which would normally trigger due to zero lastSeen time)

	tests := []struct {
		name                    string
		hasAwaitingAnnotation   bool
		lastSeenTime            time.Time
		hasResourceErrors       bool
		hasResourceDegradations bool
		isRebooting             bool
		expectedStatus          domain.DeviceSummaryStatusType
		expectedInfo            string
	}{
		{
			name:                  "Post-restore state: annotation=true, lastSeen=zero, should be AwaitingReconnect",
			hasAwaitingAnnotation: true,
			lastSeenTime:          time.Time{}, // Zero time (cleared by restore)
			expectedStatus:        domain.DeviceSummaryStatusAwaitingReconnect,
			expectedInfo:          common.DeviceStatusInfoAwaitingReconnect,
		},
		{
			name:                  "Post-restore with resource errors: annotation should still take precedence",
			hasAwaitingAnnotation: true,
			lastSeenTime:          time.Time{}, // Zero time
			hasResourceErrors:     true,
			expectedStatus:        domain.DeviceSummaryStatusAwaitingReconnect, // Should override resource errors
			expectedInfo:          common.DeviceStatusInfoAwaitingReconnect,
		},
		{
			name:                    "Post-restore with resource degradations: annotation should still take precedence",
			hasAwaitingAnnotation:   true,
			lastSeenTime:            time.Time{}, // Zero time
			hasResourceDegradations: true,
			expectedStatus:          domain.DeviceSummaryStatusAwaitingReconnect, // Should override resource degradations
			expectedInfo:            common.DeviceStatusInfoAwaitingReconnect,
		},
		{
			name:                  "Post-restore with rebooting: annotation should still take precedence",
			hasAwaitingAnnotation: true,
			lastSeenTime:          time.Time{}, // Zero time
			isRebooting:           true,
			expectedStatus:        domain.DeviceSummaryStatusAwaitingReconnect, // Should override rebooting
			expectedInfo:          common.DeviceStatusInfoAwaitingReconnect,
		},
		{
			name:                  "Without awaiting annotation: should be disconnected due to zero lastSeen",
			hasAwaitingAnnotation: false,
			lastSeenTime:          time.Time{},                       // Zero time
			expectedStatus:        domain.DeviceSummaryStatusUnknown, // Should be disconnected
		},
		{
			name:                  "With awaiting annotation but recent lastSeen: should still be AwaitingReconnect",
			hasAwaitingAnnotation: true,
			lastSeenTime:          time.Now(), // Recent time
			expectedStatus:        domain.DeviceSummaryStatusAwaitingReconnect,
			expectedInfo:          common.DeviceStatusInfoAwaitingReconnect,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup device with post-restore state
			annotations := make(map[string]string)
			if tt.hasAwaitingAnnotation {
				annotations[domain.DeviceAnnotationAwaitingReconnect] = "true"
			}

			device := &domain.Device{
				Metadata: domain.ObjectMeta{
					Name:        lo.ToPtr("test-device"),
					Annotations: &annotations,
				},
				Status: &domain.DeviceStatus{
					LastSeen: func() *time.Time {
						if tt.lastSeenTime.IsZero() {
							return nil
						}
						return lo.ToPtr(tt.lastSeenTime)
					}(),
					Summary: domain.DeviceSummaryStatus{
						Status: domain.DeviceSummaryStatusOnline, // Initial status (will be overridden)
						Info:   lo.ToPtr("Initial info"),
					},
					Resources: domain.DeviceResourceStatus{
						Cpu:    domain.DeviceResourceStatusHealthy,
						Memory: domain.DeviceResourceStatusHealthy,
						Disk:   domain.DeviceResourceStatusHealthy,
					},
					Conditions: []domain.Condition{},
				},
			}

			// Set up resource errors/degradations if needed
			if tt.hasResourceErrors {
				device.Status.Resources.Cpu = domain.DeviceResourceStatusCritical
			}
			if tt.hasResourceDegradations {
				device.Status.Resources.Memory = domain.DeviceResourceStatusWarning
			}

			// Set up rebooting condition if needed
			if tt.isRebooting {
				rebootCondition := domain.Condition{
					Type:   domain.ConditionTypeDeviceUpdating,
					Status: domain.ConditionStatusTrue,
					Reason: string(domain.UpdateStateRebooting),
				}
				domain.SetStatusCondition(&device.Status.Conditions, rebootCondition)
			}

			// Call the function under test
			initialStatus := device.Status.Summary.Status
			changed := updateServerSideDeviceStatus(device)

			// Verify the status was set correctly
			assert.Equal(t, tt.expectedStatus, device.Status.Summary.Status, "Status should match expected")

			if tt.expectedInfo != "" {
				assert.NotNil(t, device.Status.Summary.Info, "Info should not be nil")
				assert.Equal(t, tt.expectedInfo, *device.Status.Summary.Info, "Info should match expected")
			}

			// Verify changed flag is correct
			expectedChanged := initialStatus != tt.expectedStatus
			assert.Equal(t, expectedChanged, changed, "Changed flag should be correct")
		})
	}
}

func TestUpdateServerSideApplicationStatus_PreservesDeviceStatus(t *testing.T) {
	tests := []struct {
		name                string
		deviceSummaryStatus domain.ApplicationsSummaryStatusType
		deviceSummaryInfo   string
		appStatus           domain.ApplicationStatusType
		expectedStatus      domain.ApplicationsSummaryStatusType
		expectedInfo        string
	}{
		{
			name:                "Preserves Degraded status from device",
			deviceSummaryStatus: domain.ApplicationsSummaryStatusDegraded,
			deviceSummaryInfo:   "app1 is in status Degraded",
			appStatus:           domain.ApplicationStatusRunning,
			expectedStatus:      domain.ApplicationsSummaryStatusDegraded,
			expectedInfo:        "app1 is in status Degraded",
		},
		{
			name:                "Preserves Error status from device",
			deviceSummaryStatus: domain.ApplicationsSummaryStatusError,
			deviceSummaryInfo:   "app1 is in status Error",
			appStatus:           domain.ApplicationStatusError,
			expectedStatus:      domain.ApplicationsSummaryStatusError,
			expectedInfo:        "app1 is in status Error",
		},
		{
			name:                "Preserves Healthy status from device",
			deviceSummaryStatus: domain.ApplicationsSummaryStatusHealthy,
			deviceSummaryInfo:   "",
			appStatus:           domain.ApplicationStatusRunning,
			expectedStatus:      domain.ApplicationsSummaryStatusHealthy,
			expectedInfo:        "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			device := &domain.Device{
				Metadata: domain.ObjectMeta{
					Name: lo.ToPtr("test-device"),
				},
				Status: &domain.DeviceStatus{
					LastSeen: lo.ToPtr(time.Now()),
					ApplicationsSummary: domain.DeviceApplicationsSummaryStatus{
						Status: tt.deviceSummaryStatus,
						Info:   lo.ToPtr(tt.deviceSummaryInfo),
					},
					Applications: []domain.DeviceApplicationStatus{
						{Name: "app1", Status: tt.appStatus},
					},
				},
			}

			updateServerSideApplicationStatus(device)

			assert.Equal(t, tt.expectedStatus, device.Status.ApplicationsSummary.Status, "Status should be preserved from device")
			if tt.expectedInfo != "" {
				assert.NotNil(t, device.Status.ApplicationsSummary.Info)
				assert.Equal(t, tt.expectedInfo, *device.Status.ApplicationsSummary.Info, "Info should be preserved from device")
			}
		})
	}
}

func TestUpdateServerSideDeviceUpdatedStatus_OsImageMismatch(t *testing.T) {
	ctx := context.Background()
	orgId := uuid.New()
	log := logrus.NewEntry(logrus.StandardLogger())

	tests := []struct {
		name               string
		specOsImage        string
		specCatalogItemRef *domain.CatalogItemRefSpec
		statusOsImage      string
		capabilities       *domain.DeviceCapabilities
		expectedStatus     domain.DeviceUpdatedStatusType
		expectInfoContains string
	}{
		{
			name:           "When image-mode device has matching OS images it should remain UpToDate",
			specOsImage:    "quay.io/flightctl/device:v7",
			statusOsImage:  "quay.io/flightctl/device:v7",
			capabilities:   &domain.DeviceCapabilities{OsMode: lo.ToPtr(domain.OsModeImage)},
			expectedStatus: domain.DeviceUpdatedStatusUpToDate,
		},
		{
			name:               "When image-mode device has mismatching OS images it should override to OutOfDate",
			specOsImage:        "quay.io/flightctl/device:v7",
			statusOsImage:      "quay.io/flightctl/device:base",
			capabilities:       &domain.DeviceCapabilities{OsMode: lo.ToPtr(domain.OsModeImage)},
			expectedStatus:     domain.DeviceUpdatedStatusOutOfDate,
			expectInfoContains: "OS image mismatch",
		},
		{
			name:               "When package-mode device has spec OS image it should override to OutOfDate",
			specOsImage:        "quay.io/flightctl/device:v7",
			statusOsImage:      "",
			capabilities:       &domain.DeviceCapabilities{OsMode: lo.ToPtr(domain.OsModePackage)},
			expectedStatus:     domain.DeviceUpdatedStatusOutOfDate,
			expectInfoContains: "OS image mismatch",
		},
		{
			name:           "When package-mode device has no spec OS image it should remain UpToDate",
			specOsImage:    "",
			statusOsImage:  "",
			capabilities:   &domain.DeviceCapabilities{OsMode: lo.ToPtr(domain.OsModePackage)},
			expectedStatus: domain.DeviceUpdatedStatusUpToDate,
		},
		{
			name:           "When legacy device without capabilities has empty status OS image it should remain UpToDate",
			specOsImage:    "quay.io/flightctl/device:v7",
			statusOsImage:  "",
			capabilities:   nil,
			expectedStatus: domain.DeviceUpdatedStatusUpToDate,
		},
		{
			name:           "When legacy device without capabilities has mismatching OS images it should remain UpToDate",
			specOsImage:    "quay.io/flightctl/device:v7",
			statusOsImage:  "quay.io/flightctl/device:base",
			capabilities:   nil,
			expectedStatus: domain.DeviceUpdatedStatusUpToDate,
		},
		{
			name:           "When device has capabilities with nil osMode it should remain UpToDate",
			specOsImage:    "quay.io/flightctl/device:v7",
			statusOsImage:  "quay.io/flightctl/device:base",
			capabilities:   &domain.DeviceCapabilities{OsMode: nil},
			expectedStatus: domain.DeviceUpdatedStatusUpToDate,
		},
		{
			name:           "When no spec OS image is set it should remain UpToDate regardless of capabilities",
			specOsImage:    "",
			statusOsImage:  "quay.io/flightctl/device:base",
			capabilities:   &domain.DeviceCapabilities{OsMode: lo.ToPtr(domain.OsModeImage)},
			expectedStatus: domain.DeviceUpdatedStatusUpToDate,
		},
		{
			name:               "When package-mode device has catalogItemRef only it should override to OutOfDate",
			specCatalogItemRef: &domain.CatalogItemRefSpec{Catalog: "cat", Item: "os", Version: "v1"},
			capabilities:       &domain.DeviceCapabilities{OsMode: lo.ToPtr(domain.OsModePackage)},
			expectedStatus:     domain.DeviceUpdatedStatusOutOfDate,
			expectInfoContains: "catalog OS target",
		},
		{
			name:               "When image-mode device has catalogItemRef only it should remain UpToDate",
			specCatalogItemRef: &domain.CatalogItemRefSpec{Catalog: "cat", Item: "os", Version: "v1"},
			capabilities:       &domain.DeviceCapabilities{OsMode: lo.ToPtr(domain.OsModeImage)},
			expectedStatus:     domain.DeviceUpdatedStatusUpToDate,
		},
		{
			name:               "When legacy device without capabilities has catalogItemRef it should remain UpToDate",
			specCatalogItemRef: &domain.CatalogItemRefSpec{Catalog: "cat", Item: "os", Version: "v1"},
			capabilities:       nil,
			expectedStatus:     domain.DeviceUpdatedStatusUpToDate,
		},
		{
			name:               "When device has capabilities with nil osMode and catalogItemRef it should remain UpToDate",
			specCatalogItemRef: &domain.CatalogItemRefSpec{Catalog: "cat", Item: "os", Version: "v1"},
			capabilities:       &domain.DeviceCapabilities{OsMode: nil},
			expectedStatus:     domain.DeviceUpdatedStatusUpToDate,
		},
		{
			name:           "When package-mode device has no OS target it should remain UpToDate",
			capabilities:   &domain.DeviceCapabilities{OsMode: lo.ToPtr(domain.OsModePackage)},
			expectedStatus: domain.DeviceUpdatedStatusUpToDate,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			annotations := map[string]string{
				domain.DeviceAnnotationRenderedVersion: "4",
			}
			device := &domain.Device{
				Metadata: domain.ObjectMeta{
					Name:        lo.ToPtr("test-device"),
					Annotations: &annotations,
				},
				Spec: &domain.DeviceSpec{},
				Status: &domain.DeviceStatus{
					LastSeen: lo.ToPtr(time.Now()),
					Updated: domain.DeviceUpdatedStatus{
						Status: domain.DeviceUpdatedStatusUpToDate,
					},
					Config: domain.DeviceConfigStatus{
						RenderedVersion: "4",
					},
					Os: domain.DeviceOsStatus{
						Image: tt.statusOsImage,
					},
					Capabilities: tt.capabilities,
				},
			}
			if tt.specOsImage != "" {
				device.Spec.Os = &domain.DeviceOsSpec{Image: tt.specOsImage}
			}
			if tt.specCatalogItemRef != nil {
				if device.Spec.Os == nil {
					device.Spec.Os = &domain.DeviceOsSpec{}
				}
				device.Spec.Os.CatalogItemRef = tt.specCatalogItemRef
			}

			updateServerSideDeviceUpdatedStatus(device, ctx, nil, log, orgId)

			assert.Equal(t, tt.expectedStatus, device.Status.Updated.Status)
			if tt.expectInfoContains != "" {
				assert.Contains(t, *device.Status.Updated.Info, tt.expectInfoContains)
			}
		})
	}
}
