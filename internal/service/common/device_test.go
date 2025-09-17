package common

import (
	"context"
	"testing"
	"time"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
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

func TestUpdateServerSideDeviceAnnotations_PauseLogic(t *testing.T) {
	tests := []struct {
		name                   string
		isInternalRequest      bool
		hasWaitingAnnotation   bool
		waitingAnnotationValue string
		serviceVersion         string
		deviceReportedVersion  string
		expectedPaused         bool
		expectedWaitingRemoved bool
		expectedChanged        bool
	}{
		{
			name:                   "Internal request - should skip",
			isInternalRequest:      true,
			hasWaitingAnnotation:   true,
			waitingAnnotationValue: "true",
			serviceVersion:         "1",
			deviceReportedVersion:  "2",
			expectedPaused:         false,
			expectedWaitingRemoved: false,
			expectedChanged:        false,
		},
		{
			name:                   "No waiting annotation - should skip",
			isInternalRequest:      false,
			hasWaitingAnnotation:   false,
			serviceVersion:         "1",
			deviceReportedVersion:  "2",
			expectedPaused:         false,
			expectedWaitingRemoved: false,
			expectedChanged:        false,
		},
		{
			name:                   "Waiting annotation false - should skip",
			isInternalRequest:      false,
			hasWaitingAnnotation:   true,
			waitingAnnotationValue: "false",
			serviceVersion:         "1",
			deviceReportedVersion:  "2",
			expectedPaused:         false,
			expectedWaitingRemoved: false,
			expectedChanged:        false,
		},
		{
			name:                   "Device version lower - should remove waiting only",
			isInternalRequest:      false,
			hasWaitingAnnotation:   true,
			waitingAnnotationValue: "true",
			serviceVersion:         "2",
			deviceReportedVersion:  "1",
			expectedPaused:         false,
			expectedWaitingRemoved: true,
			expectedChanged:        true,
		},
		{
			name:                   "Device version equal - should remove waiting only",
			isInternalRequest:      false,
			hasWaitingAnnotation:   true,
			waitingAnnotationValue: "true",
			serviceVersion:         "2",
			deviceReportedVersion:  "2",
			expectedPaused:         false,
			expectedWaitingRemoved: true,
			expectedChanged:        true,
		},
		{
			name:                   "Device version higher - should pause and remove waiting",
			isInternalRequest:      false,
			hasWaitingAnnotation:   true,
			waitingAnnotationValue: "true",
			serviceVersion:         "1",
			deviceReportedVersion:  "2",
			expectedPaused:         true,
			expectedWaitingRemoved: true,
			expectedChanged:        true,
		},
		{
			name:                   "No service version - should pause and remove waiting",
			isInternalRequest:      false,
			hasWaitingAnnotation:   true,
			waitingAnnotationValue: "true",
			serviceVersion:         "",
			deviceReportedVersion:  "2",
			expectedPaused:         true,
			expectedWaitingRemoved: true,
			expectedChanged:        true,
		},
		{
			name:                   "No device version - should remove waiting only",
			isInternalRequest:      false,
			hasWaitingAnnotation:   true,
			waitingAnnotationValue: "true",
			serviceVersion:         "1",
			deviceReportedVersion:  "",
			expectedPaused:         false,
			expectedWaitingRemoved: true,
			expectedChanged:        true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup context
			ctx := context.Background()
			if tt.isInternalRequest {
				ctx = context.WithValue(ctx, consts.InternalRequestCtxKey, true)
			}

			// Setup device
			annotations := make(map[string]string)
			if tt.hasWaitingAnnotation {
				annotations[api.DeviceAnnotationAwaitingReconnect] = tt.waitingAnnotationValue
			}
			if tt.serviceVersion != "" {
				annotations[api.DeviceAnnotationRenderedVersion] = tt.serviceVersion
			}

			device := &api.Device{
				Metadata: api.ObjectMeta{
					Name:        lo.ToPtr("test-device"),
					Annotations: &annotations,
				},
				Status: &api.DeviceStatus{
					Config: api.DeviceConfigStatus{
						RenderedVersion: tt.deviceReportedVersion,
					},
				},
			}

			// Mock logger
			logger := logrus.New()
			logger.SetLevel(logrus.ErrorLevel) // Suppress log output during tests

			// Call function
			changed := updateServerSideDeviceAnnotations(ctx, device, logger)

			// Verify results
			assert.Equal(t, tt.expectedChanged, changed, "Changed result mismatch")

			if tt.expectedPaused {
				assert.Equal(t, "true", (*device.Metadata.Annotations)[api.DeviceAnnotationConflictPaused], "ConflictPaused annotation should be set")
			} else {
				_, exists := (*device.Metadata.Annotations)[api.DeviceAnnotationConflictPaused]
				assert.False(t, exists, "ConflictPaused annotation should not be set")
			}

			if tt.expectedWaitingRemoved {
				_, exists := (*device.Metadata.Annotations)[api.DeviceAnnotationAwaitingReconnect]
				assert.False(t, exists, "AwaitingReconnect annotation should be removed")
			} else if tt.hasWaitingAnnotation {
				assert.Equal(t, tt.waitingAnnotationValue, (*device.Metadata.Annotations)[api.DeviceAnnotationAwaitingReconnect], "AwaitingReconnect annotation should be preserved")
			}
		})
	}
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
					LastSeen: lo.ToPtr(tt.lastSeenTime),
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
