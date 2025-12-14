package common

import (
	"context"
	"testing"
	"time"

	api "github.com/flightctl/flightctl/api/v1beta1"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
)

func TestComputeDeviceStatusChanges_DeviceUpdateFailed(t *testing.T) {
	ctx := context.Background()
	orgId := uuid.New()

	// Create a device with an update error condition
	deviceWithError := &api.Device{
		Metadata: api.ObjectMeta{
			Name: lo.ToPtr("test-device"),
		},
		Status: &api.DeviceStatus{
			Updated: api.DeviceUpdatedStatus{
				Status: api.DeviceUpdatedStatusOutOfDate,
				Info:   lo.ToPtr("Device could not be updated to the fleet's latest device spec: update failed"),
			},
			Conditions: []api.Condition{
				{
					Type:    api.ConditionTypeDeviceUpdating,
					Status:  api.ConditionStatusFalse,
					Reason:  string(api.UpdateStateError),
					Message: "update failed",
				},
			},
		},
	}

	// Create a device without an update error condition
	deviceWithoutError := &api.Device{
		Metadata: api.ObjectMeta{
			Name: lo.ToPtr("test-device"),
		},
		Status: &api.DeviceStatus{
			Updated: api.DeviceUpdatedStatus{
				Status: api.DeviceUpdatedStatusOutOfDate,
				Info:   lo.ToPtr("Device has not been updated to the latest device spec."),
			},
		},
	}

	// Create an old device with UpToDate status for comparison
	oldDevice := &api.Device{
		Metadata: api.ObjectMeta{
			Name: lo.ToPtr("test-device"),
		},
		Status: &api.DeviceStatus{
			Updated: api.DeviceUpdatedStatus{
				Status: api.DeviceUpdatedStatusUpToDate,
				Info:   lo.ToPtr("Device was updated to the latest device spec."),
			},
		},
	}

	// Test case 1: Device with update error should emit DeviceUpdateFailed event
	updates := ComputeDeviceStatusChanges(ctx, oldDevice, deviceWithError, orgId, nil)
	assert.Len(t, updates, 1)
	assert.Equal(t, api.EventReasonDeviceUpdateFailed, updates[0].Reason)
	assert.Contains(t, updates[0].Details, "update failed")

	// Test case 2: Device without update error should emit DeviceContentOutOfDate event
	updates = ComputeDeviceStatusChanges(ctx, oldDevice, deviceWithoutError, orgId, nil)
	assert.Len(t, updates, 1)
	assert.Equal(t, api.EventReasonDeviceContentOutOfDate, updates[0].Reason)
	assert.Contains(t, updates[0].Details, "has not been updated")
}

func TestComputeDeviceStatusChanges_StatusTransition(t *testing.T) {
	ctx := context.Background()
	orgId := uuid.New()

	// Create old device with UpToDate status
	oldDevice := &api.Device{
		Metadata: api.ObjectMeta{
			Name: lo.ToPtr("test-device"),
		},
		Status: &api.DeviceStatus{
			Updated: api.DeviceUpdatedStatus{
				Status: api.DeviceUpdatedStatusUpToDate,
				Info:   lo.ToPtr("Device was updated to the latest device spec."),
			},
		},
	}

	// Create new device with OutOfDate status and update error
	newDevice := &api.Device{
		Metadata: api.ObjectMeta{
			Name: lo.ToPtr("test-device"),
		},
		Status: &api.DeviceStatus{
			Updated: api.DeviceUpdatedStatus{
				Status: api.DeviceUpdatedStatusOutOfDate,
				Info:   lo.ToPtr("Device could not be updated to the fleet's latest device spec: update failed"),
			},
			Conditions: []api.Condition{
				{
					Type:    api.ConditionTypeDeviceUpdating,
					Status:  api.ConditionStatusFalse,
					Reason:  string(api.UpdateStateError),
					Message: "update failed",
				},
			},
		},
	}

	// Test transition from UpToDate to OutOfDate with error
	updates := ComputeDeviceStatusChanges(ctx, oldDevice, newDevice, orgId, nil)
	assert.Len(t, updates, 1)
	assert.Equal(t, api.EventReasonDeviceUpdateFailed, updates[0].Reason)
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
		expectedStatus          api.DeviceSummaryStatusType
		expectedInfo            string
	}{
		{
			name:                  "Post-restore state: annotation=true, lastSeen=zero, should be AwaitingReconnect",
			hasAwaitingAnnotation: true,
			lastSeenTime:          time.Time{}, // Zero time (cleared by restore)
			expectedStatus:        api.DeviceSummaryStatusAwaitingReconnect,
			expectedInfo:          DeviceStatusInfoAwaitingReconnect,
		},
		{
			name:                  "Post-restore with resource errors: annotation should still take precedence",
			hasAwaitingAnnotation: true,
			lastSeenTime:          time.Time{}, // Zero time
			hasResourceErrors:     true,
			expectedStatus:        api.DeviceSummaryStatusAwaitingReconnect, // Should override resource errors
			expectedInfo:          DeviceStatusInfoAwaitingReconnect,
		},
		{
			name:                    "Post-restore with resource degradations: annotation should still take precedence",
			hasAwaitingAnnotation:   true,
			lastSeenTime:            time.Time{}, // Zero time
			hasResourceDegradations: true,
			expectedStatus:          api.DeviceSummaryStatusAwaitingReconnect, // Should override resource degradations
			expectedInfo:            DeviceStatusInfoAwaitingReconnect,
		},
		{
			name:                  "Post-restore with rebooting: annotation should still take precedence",
			hasAwaitingAnnotation: true,
			lastSeenTime:          time.Time{}, // Zero time
			isRebooting:           true,
			expectedStatus:        api.DeviceSummaryStatusAwaitingReconnect, // Should override rebooting
			expectedInfo:          DeviceStatusInfoAwaitingReconnect,
		},
		{
			name:                  "Without awaiting annotation: should be disconnected due to zero lastSeen",
			hasAwaitingAnnotation: false,
			lastSeenTime:          time.Time{},                    // Zero time
			expectedStatus:        api.DeviceSummaryStatusUnknown, // Should be disconnected
		},
		{
			name:                  "With awaiting annotation but recent lastSeen: should still be AwaitingReconnect",
			hasAwaitingAnnotation: true,
			lastSeenTime:          time.Now(), // Recent time
			expectedStatus:        api.DeviceSummaryStatusAwaitingReconnect,
			expectedInfo:          DeviceStatusInfoAwaitingReconnect,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup device with post-restore state
			annotations := make(map[string]string)
			if tt.hasAwaitingAnnotation {
				annotations[api.DeviceAnnotationAwaitingReconnect] = "true"
			}

			device := &api.Device{
				Metadata: api.ObjectMeta{
					Name:        lo.ToPtr("test-device"),
					Annotations: &annotations,
				},
				Status: &api.DeviceStatus{
					LastSeen: func() *time.Time {
						if tt.lastSeenTime.IsZero() {
							return nil
						}
						return lo.ToPtr(tt.lastSeenTime)
					}(),
					Summary: api.DeviceSummaryStatus{
						Status: api.DeviceSummaryStatusOnline, // Initial status (will be overridden)
						Info:   lo.ToPtr("Initial info"),
					},
					Resources: api.DeviceResourceStatus{
						Cpu:    api.DeviceResourceStatusHealthy,
						Memory: api.DeviceResourceStatusHealthy,
						Disk:   api.DeviceResourceStatusHealthy,
					},
					Conditions: []api.Condition{},
				},
			}

			// Set up resource errors/degradations if needed
			if tt.hasResourceErrors {
				device.Status.Resources.Cpu = api.DeviceResourceStatusCritical
			}
			if tt.hasResourceDegradations {
				device.Status.Resources.Memory = api.DeviceResourceStatusWarning
			}

			// Set up rebooting condition if needed
			if tt.isRebooting {
				rebootCondition := api.Condition{
					Type:   api.ConditionTypeDeviceUpdating,
					Status: api.ConditionStatusTrue,
					Reason: string(api.UpdateStateRebooting),
				}
				api.SetStatusCondition(&device.Status.Conditions, rebootCondition)
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
		deviceSummaryStatus api.ApplicationsSummaryStatusType
		deviceSummaryInfo   string
		appStatus           api.ApplicationStatusType
		expectedStatus      api.ApplicationsSummaryStatusType
		expectedInfo        string
	}{
		{
			name:                "Preserves Degraded status from device",
			deviceSummaryStatus: api.ApplicationsSummaryStatusDegraded,
			deviceSummaryInfo:   "app1 is in status Degraded",
			appStatus:           api.ApplicationStatusRunning,
			expectedStatus:      api.ApplicationsSummaryStatusDegraded,
			expectedInfo:        "app1 is in status Degraded",
		},
		{
			name:                "Preserves Error status from device",
			deviceSummaryStatus: api.ApplicationsSummaryStatusError,
			deviceSummaryInfo:   "app1 is in status Error",
			appStatus:           api.ApplicationStatusError,
			expectedStatus:      api.ApplicationsSummaryStatusError,
			expectedInfo:        "app1 is in status Error",
		},
		{
			name:                "Preserves Healthy status from device",
			deviceSummaryStatus: api.ApplicationsSummaryStatusHealthy,
			deviceSummaryInfo:   "",
			appStatus:           api.ApplicationStatusRunning,
			expectedStatus:      api.ApplicationsSummaryStatusHealthy,
			expectedInfo:        "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			device := &api.Device{
				Metadata: api.ObjectMeta{
					Name: lo.ToPtr("test-device"),
				},
				Status: &api.DeviceStatus{
					LastSeen: lo.ToPtr(time.Now()),
					ApplicationsSummary: api.DeviceApplicationsSummaryStatus{
						Status: tt.deviceSummaryStatus,
						Info:   lo.ToPtr(tt.deviceSummaryInfo),
					},
					Applications: []api.DeviceApplicationStatus{
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
