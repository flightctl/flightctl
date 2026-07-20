package common

import (
	"context"
	"testing"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/google/uuid"
	"github.com/samber/lo"
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
	updates := ComputeDeviceStatusChanges(ctx, oldDevice, deviceWithError, orgId)
	assert.Len(t, updates, 1)
	assert.Equal(t, domain.EventReasonDeviceUpdateFailed, updates[0].Reason)
	assert.Contains(t, updates[0].Details, "update failed")

	// Test case 2: Device without update error should emit DeviceContentOutOfDate event
	updates = ComputeDeviceStatusChanges(ctx, oldDevice, deviceWithoutError, orgId)
	assert.Len(t, updates, 1)
	assert.Equal(t, domain.EventReasonDeviceContentOutOfDate, updates[0].Reason)
	assert.Contains(t, updates[0].Details, "has not been updated")
}

func TestComputeDeviceStatusChanges_OSImageChanged_EDM3986(t *testing.T) {
	ctx := context.Background()
	orgId := uuid.New()

	tests := []struct {
		name            string
		oldDigest       string
		newDigest       string
		expectEvent     bool
		expectedDetails string
	}{
		{
			name:            "When initial OS image is reported it should emit event with 'Initial OS image detected' message",
			oldDigest:       "",
			newDigest:       "sha256:abc123",
			expectEvent:     true,
			expectedDetails: "Initial OS image detected: sha256:abc123",
		},
		{
			name:            "When OS image changes it should emit event with from/to message",
			oldDigest:       "sha256:old111",
			newDigest:       "sha256:new222",
			expectEvent:     true,
			expectedDetails: "OS image changed from sha256:old111 to sha256:new222",
		},
		{
			name:        "When OS image is unchanged it should not emit event",
			oldDigest:   "sha256:same",
			newDigest:   "sha256:same",
			expectEvent: false,
		},
		{
			name:        "When new digest is empty it should not emit event",
			oldDigest:   "sha256:old",
			newDigest:   "",
			expectEvent: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			oldDevice := &domain.Device{
				Metadata: domain.ObjectMeta{Name: lo.ToPtr("test-device")},
				Status: &domain.DeviceStatus{
					Os: domain.DeviceOsStatus{ImageDigest: tt.oldDigest},
				},
			}
			newDevice := &domain.Device{
				Metadata: domain.ObjectMeta{Name: lo.ToPtr("test-device")},
				Status: &domain.DeviceStatus{
					Os: domain.DeviceOsStatus{ImageDigest: tt.newDigest},
				},
			}

			updates := ComputeDeviceStatusChanges(ctx, oldDevice, newDevice, orgId)

			var osImageEvents []ResourceUpdate
			for _, u := range updates {
				if u.Reason == domain.EventReasonDeviceOSImageChanged {
					osImageEvents = append(osImageEvents, u)
				}
			}

			if tt.expectEvent {
				assert.Len(t, osImageEvents, 1)
				assert.Equal(t, tt.expectedDetails, osImageEvents[0].Details)
			} else {
				assert.Empty(t, osImageEvents)
			}
		})
	}
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
	updates := ComputeDeviceStatusChanges(ctx, oldDevice, newDevice, orgId)
	assert.Len(t, updates, 1)
	assert.Equal(t, domain.EventReasonDeviceUpdateFailed, updates[0].Reason)
	assert.Contains(t, updates[0].Details, "update failed")
}
