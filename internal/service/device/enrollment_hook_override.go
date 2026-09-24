package device

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/service/common"
	devicestore "github.com/flightctl/flightctl/internal/store/device"
	"github.com/google/uuid"
)

var (
	errNoEnrollmentHooksCondition = errors.New("device has no EnrollmentHooks condition; nothing to override")
	errOverrideNotAllowed         = errors.New("ManualOverride is only allowed when EnrollmentHooks condition is Failed")
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

		var currentCondition *domain.Condition
		if m.Device.Status != nil {
			currentCondition = domain.FindStatusCondition(m.Device.Status.Conditions, domain.ConditionTypeDeviceEnrollmentHooks)
		}
		if currentCondition == nil {
			return errNoEnrollmentHooksCondition
		}

		// Only allow override when the condition indicates failure (Status=False, Reason=Failed).
		if currentCondition.Status != domain.ConditionStatusFalse || currentCondition.Reason != domain.EnrollmentHooksReasonFailed {
			return fmt.Errorf(
				"%w; current status=%s reason=%s", errOverrideNotAllowed,
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
		if errors.Is(err, errNoEnrollmentHooksCondition) || errors.Is(err, errOverrideNotAllowed) {
			return nil, domain.StatusBadRequest(err.Error())
		}
		status := common.StoreErrorToApiStatus(err, false, domain.DeviceKind, &name)
		if status.Code == http.StatusInternalServerError {
			h.log.WithError(err).Error("failed to override enrollment hook")
			return nil, domain.StatusInternalServerError("failed to override enrollment hook")
		}
		return nil, status
	}

	// Emit condition-change events (EnrollmentHookManualOverride).
	if result != nil {
		h.diffAndEmitConditionEvents(ctx, orgId, result, oldConditions, newConditions)
	}

	return result, domain.StatusOK()
}
