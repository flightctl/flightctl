package device

import (
	"context"
	"fmt"
	"net/http"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/service/common"
	"github.com/flightctl/flightctl/internal/store"
	devicestore "github.com/flightctl/flightctl/internal/store/device"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/google/uuid"
)

// A device is held while rendering does not support firmware: its spec and
// rendered output are left as they are, so neither desired firmware nor an
// outstanding firmware attempt is dropped or overtaken, and only the hold
// condition is recorded. Every decision to hold is made on the device loaded
// for the mutation attempt that writes, so a concurrent write that adds or
// ends firmware state makes the attempt retry and decide again.

// firmwareHoldCondition is the SpecValid condition of a held device.
func firmwareHoldCondition() domain.Condition {
	return domain.Condition{
		Type:   domain.ConditionTypeDeviceSpecValid,
		Status: domain.ConditionStatusUnknown,
		Reason: domain.DeviceSpecValidReasonFirmwareUnsupported,
		Message: "The device keeps its current spec and rendered output because it has desired firmware or an outstanding firmware attempt, " +
			"or its template version carries firmware, and device rendering does not support firmware yet.",
	}
}

// firmwareHoldStatus is the result of a write that held the device instead.
func firmwareHoldStatus(name string) domain.Status {
	return domain.NewFailureStatus(http.StatusConflict, domain.DeviceSpecValidReasonFirmwareUnsupported,
		fmt.Sprintf("device %q is held because device rendering does not support its firmware state", name))
}

// IsFirmwareHold reports whether status reports that a device was held
// instead of having its spec or rendered output replaced, because rendering
// does not support its firmware state.
func IsFirmwareHold(status domain.Status) bool {
	return status.Code == http.StatusConflict && status.Reason == domain.DeviceSpecValidReasonFirmwareUnsupported
}

// holdForFirmware records the hold condition on device, the device of the
// current mutation attempt, and refreshes its service-side status. It returns
// the service conditions before and after the change, or
// store.ErrMutateSkipWrite when the condition is already recorded.
func (h *DeviceServiceHandler) holdForFirmware(ctx context.Context, orgId uuid.UUID, device *domain.Device) ([]domain.Condition, []domain.Condition, error) {
	existing := serviceConditionsFromDevice(device)
	merged, changed := common.MergeStatusConditions(existing, []domain.Condition{firmwareHoldCondition()})
	if !changed {
		return nil, nil, store.ErrMutateSkipWrite
	}
	replaceServiceConditionsOnDevice(device, merged)
	_ = common.UpdateServiceSideStatus(ctx, orgId, device, h.fleetStore, h.log)
	return existing, merged, nil
}

func (h *DeviceServiceHandler) HoldDeviceForFirmware(ctx context.Context, orgId uuid.UUID, name string, expectedOwner *string, sourceHasFirmware bool) (bool, domain.Status) {
	var held bool
	var oldConditions, newConditions []domain.Condition
	result, _, _, err := h.deviceStore.Mutate(ctx, orgId, name, nil, func(m *devicestore.DeviceMutation) error {
		held, oldConditions, newConditions = false, nil, nil
		if err := m.RequireExisting(); err != nil {
			return err
		}
		if err := rejectDecommissionedDevice(m.Device); err != nil {
			return err
		}
		if util.DefaultIfNil(m.Device.Metadata.Owner, "") != util.DefaultIfNil(expectedOwner, "") {
			return flterrors.ErrUpdatingResourceWithOwnerNotAllowed
		}
		if !sourceHasFirmware && !domain.DeviceRequiresFirmwareHold(m.Device) {
			return store.ErrMutateSkipWrite
		}
		held = true
		var err error
		oldConditions, newConditions, err = h.holdForFirmware(ctx, orgId, m.Device)
		return err
	}, devicestore.WithTimestamp())
	if err != nil {
		return false, common.StoreErrorToApiStatus(err, false, domain.DeviceKind, &name)
	}
	if result != nil && newConditions != nil {
		h.diffAndEmitConditionEvents(ctx, orgId, result, oldConditions, newConditions)
	}
	return held, domain.StatusOK()
}
