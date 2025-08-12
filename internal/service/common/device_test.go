package common

import (
	"context"
	"testing"

	api "github.com/flightctl/flightctl/api/v1alpha1"
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
