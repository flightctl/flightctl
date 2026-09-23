package device

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/service/common"
	devicestore "github.com/flightctl/flightctl/internal/store/device"
	"github.com/google/uuid"
)

// OverrideDeviceEnrollmentHook manually overrides a failed enrollment hook
// condition. It transitions EnrollmentHooks from False/Failed to True/ManualOverride,
// clearing the failure state and unblocking fleet match and rendered GET.
// Only admin and operator roles may perform this action.
func (h *DeviceServiceHandler) OverrideDeviceEnrollmentHook(ctx context.Context, orgId uuid.UUID, name string) (*domain.Device, domain.Status) {
	var oldConditions, newConditions []domain.Condition
	result, _, _, err := h.deviceStore.Mutate(ctx, orgId, name, nil, func(m *devicestore.DeviceMutation) error {
		if err := m.RequireExisting(); err != nil {
			return err
		}

		// Look up the current EnrollmentHooks condition.
		currentCondition := domain.FindStatusCondition(m.Device.Status.Conditions, domain.ConditionTypeDeviceEnrollmentHooks)
		if currentCondition == nil {
			return fmt.Errorf("device has no EnrollmentHooks condition; nothing to override")
		}

		// Only allow override when the condition indicates failure (Status=False, Reason=Failed).
		if currentCondition.Status != domain.ConditionStatusFalse || currentCondition.Reason != domain.EnrollmentHooksReasonFailed {
			return fmt.Errorf(
				"ManualOverride is only allowed when EnrollmentHooks condition is Failed; current status=%s reason=%s",
				currentCondition.Status, currentCondition.Reason,
			)
		}

		// Capture the old service conditions for event diffing.
		oldConditions = serviceConditionsFromDevice(m.Device)

		// Build the ManualOverride condition.
		overrideCondition := domain.Condition{
			Type:               domain.ConditionTypeDeviceEnrollmentHooks,
			Status:             domain.ConditionStatusTrue,
			Reason:             domain.EnrollmentHooksReasonManualOverride,
			Message:            "Enrollment hook failure was manually overridden by an operator.",
			LastTransitionTime: time.Now().UTC(),
		}

		// Merge the new condition into existing service conditions.
		existing := serviceConditionsFromDevice(m.Device)
		merged, _ := common.MergeStatusConditions(existing, []domain.Condition{overrideCondition})
		newConditions = merged
		replaceServiceConditionsOnDevice(m.Device, merged)

		return nil
	})
	if err != nil {
		status := common.StoreErrorToApiStatus(err, false, domain.DeviceKind, &name)
		if status.Code == http.StatusInternalServerError {
			// Transition-validation errors are user errors, not server errors.
			return nil, domain.StatusBadRequest(err.Error())
		}
		return nil, status
	}

	// Emit condition-change events (EnrollmentHookManualOverride).
	if result != nil {
		h.diffAndEmitConditionEvents(ctx, orgId, result, oldConditions, newConditions)
	}

	return result, domain.StatusOK()
}
