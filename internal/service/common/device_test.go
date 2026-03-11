package common

import (
	"context"
	"testing"
	"time"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
)

func TestComputeDeviceStatusChanges_DeviceUpdateFailed(t *testing.T) {
	ctx := context.Background()
	orgId := uuid.New()

	// Create a device with an update error condition
	deviceWithError := &domain.Device{
		Metadata: domain.ObjectMeta{
			Name: lo.ToPtr("test-device"),
		},
		Status: &domain.DeviceStatus{
			Updated: domain.DeviceUpdatedStatus{
				Status: domain.DeviceUpdatedStatusOutOfDate,
				Info:   lo.ToPtr("Device could not be updated to the fleet's latest device spec: update failed"),
			},
			Conditions: []domain.Condition{
				{
					Type:    domain.ConditionTypeDeviceUpdating,
					Status:  domain.ConditionStatusFalse,
					Reason:  string(domain.UpdateStateError),
					Message: "update failed",
				},
			},
		},
	}

	// Create a device without an update error condition
	deviceWithoutError := &domain.Device{
		Metadata: domain.ObjectMeta{
			Name: lo.ToPtr("test-device"),
		},
		Status: &domain.DeviceStatus{
			Updated: domain.DeviceUpdatedStatus{
				Status: domain.DeviceUpdatedStatusOutOfDate,
				Info:   lo.ToPtr("Device has not been updated to the latest device spec."),
			},
		},
	}

	// Create an old device with UpToDate status for comparison
	oldDevice := &domain.Device{
		Metadata: domain.ObjectMeta{
			Name: lo.ToPtr("test-device"),
		},
		Status: &domain.DeviceStatus{
			Updated: domain.DeviceUpdatedStatus{
				Status: domain.DeviceUpdatedStatusUpToDate,
				Info:   lo.ToPtr("Device was updated to the latest device spec."),
			},
		},
	}

	// Test case 1: Device with update error should emit DeviceUpdateFailed event
	updates := ComputeDeviceStatusChanges(ctx, oldDevice, deviceWithError, orgId, nil)
	assert.Len(t, updates, 1)
	assert.Equal(t, domain.EventReasonDeviceUpdateFailed, updates[0].Reason)
	assert.Contains(t, updates[0].Details, "update failed")

	// Test case 2: Device without update error should emit DeviceContentOutOfDate event
	updates = ComputeDeviceStatusChanges(ctx, oldDevice, deviceWithoutError, orgId, nil)
	assert.Len(t, updates, 1)
	assert.Equal(t, domain.EventReasonDeviceContentOutOfDate, updates[0].Reason)
	assert.Contains(t, updates[0].Details, "has not been updated")
}

func TestComputeDeviceStatusChanges_StatusTransition(t *testing.T) {
	ctx := context.Background()
	orgId := uuid.New()

	// Create old device with UpToDate status
	oldDevice := &domain.Device{
		Metadata: domain.ObjectMeta{
			Name: lo.ToPtr("test-device"),
		},
		Status: &domain.DeviceStatus{
			Updated: domain.DeviceUpdatedStatus{
				Status: domain.DeviceUpdatedStatusUpToDate,
				Info:   lo.ToPtr("Device was updated to the latest device spec."),
			},
		},
	}

	// Create new device with OutOfDate status and update error
	newDevice := &domain.Device{
		Metadata: domain.ObjectMeta{
			Name: lo.ToPtr("test-device"),
		},
		Status: &domain.DeviceStatus{
			Updated: domain.DeviceUpdatedStatus{
				Status: domain.DeviceUpdatedStatusOutOfDate,
				Info:   lo.ToPtr("Device could not be updated to the fleet's latest device spec: update failed"),
			},
			Conditions: []domain.Condition{
				{
					Type:    domain.ConditionTypeDeviceUpdating,
					Status:  domain.ConditionStatusFalse,
					Reason:  string(domain.UpdateStateError),
					Message: "update failed",
				},
			},
		},
	}

	// Test transition from UpToDate to OutOfDate with error
	updates := ComputeDeviceStatusChanges(ctx, oldDevice, newDevice, orgId, nil)
	assert.Len(t, updates, 1)
	assert.Equal(t, domain.EventReasonDeviceUpdateFailed, updates[0].Reason)
	assert.Contains(t, updates[0].Details, "update failed")
}

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
			expectedInfo:          DeviceStatusInfoAwaitingReconnect,
		},
		{
			name:                  "Post-restore with resource errors: annotation should still take precedence",
			hasAwaitingAnnotation: true,
			lastSeenTime:          time.Time{}, // Zero time
			hasResourceErrors:     true,
			expectedStatus:        domain.DeviceSummaryStatusAwaitingReconnect, // Should override resource errors
			expectedInfo:          DeviceStatusInfoAwaitingReconnect,
		},
		{
			name:                    "Post-restore with resource degradations: annotation should still take precedence",
			hasAwaitingAnnotation:   true,
			lastSeenTime:            time.Time{}, // Zero time
			hasResourceDegradations: true,
			expectedStatus:          domain.DeviceSummaryStatusAwaitingReconnect, // Should override resource degradations
			expectedInfo:            DeviceStatusInfoAwaitingReconnect,
		},
		{
			name:                  "Post-restore with rebooting: annotation should still take precedence",
			hasAwaitingAnnotation: true,
			lastSeenTime:          time.Time{}, // Zero time
			isRebooting:           true,
			expectedStatus:        domain.DeviceSummaryStatusAwaitingReconnect, // Should override rebooting
			expectedInfo:          DeviceStatusInfoAwaitingReconnect,
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
			expectedInfo:          DeviceStatusInfoAwaitingReconnect,
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
		name           string
		specOsImage    string
		statusOsImage  string
		expectedStatus domain.DeviceUpdatedStatusType
		expectMismatch bool
	}{
		{
			name:           "matching OS images remains UpToDate",
			specOsImage:    "quay.io/flightctl/device:v7",
			statusOsImage:  "quay.io/flightctl/device:v7",
			expectedStatus: domain.DeviceUpdatedStatusUpToDate,
		},
		{
			name:           "mismatching OS images overrides to OutOfDate",
			specOsImage:    "quay.io/flightctl/device:v7",
			statusOsImage:  "quay.io/flightctl/device:base",
			expectedStatus: domain.DeviceUpdatedStatusOutOfDate,
			expectMismatch: true,
		},
		{
			name:           "no spec OS image remains UpToDate",
			specOsImage:    "",
			statusOsImage:  "quay.io/flightctl/device:base",
			expectedStatus: domain.DeviceUpdatedStatusUpToDate,
		},
		{
			name:           "no status OS image remains UpToDate",
			specOsImage:    "quay.io/flightctl/device:v7",
			statusOsImage:  "",
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
				},
			}
			if tt.specOsImage != "" {
				device.Spec.Os = &domain.DeviceOsSpec{Image: tt.specOsImage}
			}

			updateServerSideDeviceUpdatedStatus(device, ctx, nil, log, orgId)

			assert.Equal(t, tt.expectedStatus, device.Status.Updated.Status)
			if tt.expectMismatch {
				assert.Contains(t, *device.Status.Updated.Info, "OS image mismatch")
			}
		})
	}
}
