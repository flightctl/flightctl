package device

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

// newEnrollmentHookTestDevice creates a device with the given EnrollmentHooks
// condition and registers it in a fresh DeviceServiceHandler backed by fake stores.
func newEnrollmentHookTestDevice(t *testing.T, condStatus domain.ConditionStatus, condReason string) (
	h Service, st *fakeStore, ev *fakeEvents, orgId uuid.UUID, deviceName string,
) {
	t.Helper()

	deviceName = "device-eh"
	status := domain.NewDeviceStatus()
	status.Conditions = append(status.Conditions, domain.Condition{
		Type:   domain.ConditionTypeDeviceEnrollmentHooks,
		Status: condStatus,
		Reason: condReason,
	})

	device := domain.Device{
		Metadata: domain.ObjectMeta{
			Name: lo.ToPtr(deviceName),
		},
		Spec:   &domain.DeviceSpec{Os: &domain.DeviceOsSpec{Image: "img"}},
		Status: &status,
	}

	st = newFakeStore()
	ev = &fakeEvents{}
	h = NewDeviceServiceHandler(st.device, st.catalog, st.fleet, ev, nil, "agent.example.com", logrus.New())
	orgId = uuid.New()
	_, err := st.device.Create(context.Background(), orgId, &device, nil)
	require.NoError(t, err)

	return h, st, ev, orgId, deviceName
}

func enrollmentHooksConditionPatchPath(t *testing.T, h Service, orgId uuid.UUID, deviceName string) string {
	t.Helper()
	device, status := h.GetDevice(context.Background(), orgId, deviceName)
	require.Equal(t, int32(http.StatusOK), status.Code)
	for index, condition := range device.Status.Conditions {
		if condition.Type == domain.ConditionTypeDeviceEnrollmentHooks {
			return fmt.Sprintf("/status/conditions/%d", index)
		}
	}
	t.Fatal("device has no EnrollmentHooks condition")
	return ""
}

func TestOverrideDeviceEnrollmentHook(t *testing.T) {
	ctx := context.Background()

	t.Run("When device condition is Failed it should transition to ManualOverride", func(t *testing.T) {
		require := require.New(t)
		h, _, ev, orgId, deviceName := newEnrollmentHookTestDevice(t,
			domain.ConditionStatusFalse, domain.EnrollmentHooksReasonFailed)

		dev, status := h.OverrideDeviceEnrollmentHook(ctx, orgId, deviceName)
		require.Equal(int32(http.StatusOK), status.Code)
		require.NotNil(dev)

		// Verify condition was updated.
		cond := domain.FindStatusCondition(dev.Status.Conditions, domain.ConditionTypeDeviceEnrollmentHooks)
		require.NotNil(cond)
		require.Equal(domain.ConditionStatusTrue, cond.Status)
		require.Equal(domain.EnrollmentHooksReasonManualOverride, cond.Reason)

		// Clearing the gate triggers ownership reconciliation as well as the override event.
		require.Len(ev.created, 2)
		require.Equal(domain.EventReasonResourceUpdated, ev.created[0].Reason)
		require.Equal(domain.EventReasonEnrollmentHookManualOverride, ev.created[1].Reason)
	})

	t.Run("When device condition is Pending it should reject the override", func(t *testing.T) {
		require := require.New(t)
		h, _, _, orgId, deviceName := newEnrollmentHookTestDevice(t,
			domain.ConditionStatusFalse, domain.EnrollmentHooksReasonPending)

		dev, status := h.OverrideDeviceEnrollmentHook(ctx, orgId, deviceName)
		require.Equal(int32(http.StatusBadRequest), status.Code)
		require.Nil(dev)
	})

	t.Run("When device condition is NotifyPending it should reject the override", func(t *testing.T) {
		require := require.New(t)
		h, _, _, orgId, deviceName := newEnrollmentHookTestDevice(t,
			domain.ConditionStatusFalse, domain.EnrollmentHooksReasonNotifyPending)

		dev, status := h.OverrideDeviceEnrollmentHook(ctx, orgId, deviceName)
		require.Equal(int32(http.StatusBadRequest), status.Code)
		require.Nil(dev)
	})

	t.Run("When device condition is already Succeeded it should reject the override", func(t *testing.T) {
		require := require.New(t)
		h, _, _, orgId, deviceName := newEnrollmentHookTestDevice(t,
			domain.ConditionStatusTrue, domain.EnrollmentHooksReasonSucceeded)

		dev, status := h.OverrideDeviceEnrollmentHook(ctx, orgId, deviceName)
		require.Equal(int32(http.StatusBadRequest), status.Code)
		require.Nil(dev)
	})

	t.Run("When device has no EnrollmentHooks condition it should reject the override", func(t *testing.T) {
		require := require.New(t)
		h, st, _, orgId, deviceName := newEnrollmentHookTestDevice(t,
			domain.ConditionStatusFalse, domain.EnrollmentHooksReasonFailed)
		st.device.devices[deviceName].Status.Conditions = nil

		dev, status := h.OverrideDeviceEnrollmentHook(ctx, orgId, deviceName)
		require.Equal(int32(http.StatusBadRequest), status.Code)
		require.Nil(dev)
	})

	t.Run("When device status is absent it should reject the override", func(t *testing.T) {
		require := require.New(t)
		h, st, _, orgId, deviceName := newEnrollmentHookTestDevice(t,
			domain.ConditionStatusFalse, domain.EnrollmentHooksReasonFailed)
		st.device.devices[deviceName].Status = nil

		dev, status := h.OverrideDeviceEnrollmentHook(ctx, orgId, deviceName)
		require.Equal(int32(http.StatusBadRequest), status.Code)
		require.Nil(dev)
	})

	t.Run("When device does not exist it should return not found", func(t *testing.T) {
		require := require.New(t)
		h, _, _, orgId, _ := newEnrollmentHookTestDevice(t,
			domain.ConditionStatusFalse, domain.EnrollmentHooksReasonFailed)

		dev, status := h.OverrideDeviceEnrollmentHook(ctx, orgId, "nonexistent")
		require.Equal(int32(http.StatusNotFound), status.Code)
		require.Nil(dev)
	})

	t.Run("When the store reports a conflict it should preserve that status", func(t *testing.T) {
		require := require.New(t)
		h, st, _, orgId, deviceName := newEnrollmentHookTestDevice(t,
			domain.ConditionStatusFalse, domain.EnrollmentHooksReasonFailed)
		st.device.mutateErr = flterrors.ErrNoRowsUpdated

		dev, status := h.OverrideDeviceEnrollmentHook(ctx, orgId, deviceName)
		require.Equal(int32(http.StatusConflict), status.Code)
		require.Nil(dev)
	})

	t.Run("When the store fails it should return 500 without exposing details", func(t *testing.T) {
		require := require.New(t)
		h, st, ev, orgId, deviceName := newEnrollmentHookTestDevice(t,
			domain.ConditionStatusFalse, domain.EnrollmentHooksReasonFailed)
		st.device.mutateErr = errors.New("database connection detail")

		dev, status := h.OverrideDeviceEnrollmentHook(ctx, orgId, deviceName)
		require.Equal(int32(http.StatusInternalServerError), status.Code)
		require.NotContains(status.Message, "database connection detail")
		require.Nil(dev)
		require.Empty(ev.created)
	})
}

func TestPatchDeviceStatusEnrollmentHooks(t *testing.T) {
	ctx := context.Background()

	t.Run("When the override is already set it should allow unrelated status patches", func(t *testing.T) {
		require := require.New(t)
		h, _, _, orgId, deviceName := newEnrollmentHookTestDevice(t,
			domain.ConditionStatusFalse, domain.EnrollmentHooksReasonFailed)
		_, status := h.OverrideDeviceEnrollmentHook(ctx, orgId, deviceName)
		require.Equal(int32(http.StatusOK), status.Code)

		var agentVersion any = "v2"
		patch := domain.PatchRequest{{Op: "replace", Path: "/status/systemInfo/agentVersion", Value: &agentVersion}}
		dev, status := h.PatchDeviceStatus(ctx, orgId, deviceName, patch, false)
		require.Equal(int32(http.StatusOK), status.Code)
		require.Equal("v2", dev.Status.SystemInfo.AgentVersion)
		cond := domain.FindStatusCondition(dev.Status.Conditions, domain.ConditionTypeDeviceEnrollmentHooks)
		require.NotNil(cond)
		require.Equal(domain.ConditionStatusTrue, cond.Status)
		require.Equal(domain.EnrollmentHooksReasonManualOverride, cond.Reason)
	})

	t.Run("When the condition is unchanged it should allow unrelated status patches", func(t *testing.T) {
		require := require.New(t)
		h, _, _, orgId, deviceName := newEnrollmentHookTestDevice(t,
			domain.ConditionStatusFalse, domain.EnrollmentHooksReasonFailed)
		var agentVersion any = "v2"
		patch := domain.PatchRequest{{Op: "replace", Path: "/status/systemInfo/agentVersion", Value: &agentVersion}}

		dev, status := h.PatchDeviceStatus(ctx, orgId, deviceName, patch, false)
		require.Equal(int32(http.StatusOK), status.Code)
		require.Equal("v2", dev.Status.SystemInfo.AgentVersion)
		cond := domain.FindStatusCondition(dev.Status.Conditions, domain.ConditionTypeDeviceEnrollmentHooks)
		require.NotNil(cond)
		require.Equal(domain.ConditionStatusFalse, cond.Status)
		require.Equal(domain.EnrollmentHooksReasonFailed, cond.Reason)
	})

	t.Run("When a generic patch forges success from Failed it should reject without changing the device", func(t *testing.T) {
		require := require.New(t)
		h, _, _, orgId, deviceName := newEnrollmentHookTestDevice(t,
			domain.ConditionStatusFalse, domain.EnrollmentHooksReasonFailed)
		conditionPath := enrollmentHooksConditionPatchPath(t, h, orgId, deviceName)
		var succeededStatus any = domain.ConditionStatusTrue
		var succeededReason any = domain.EnrollmentHooksReasonSucceeded
		patch := domain.PatchRequest{
			{Op: "replace", Path: conditionPath + "/status", Value: &succeededStatus},
			{Op: "replace", Path: conditionPath + "/reason", Value: &succeededReason},
		}

		dev, status := h.PatchDeviceStatus(ctx, orgId, deviceName, patch, false)
		require.Equal(int32(http.StatusBadRequest), status.Code)
		require.Contains(status.Message, "EnrollmentHooks condition cannot be modified via status patch")
		require.Nil(dev)
		stored, getStatus := h.GetDevice(ctx, orgId, deviceName)
		require.Equal(int32(http.StatusOK), getStatus.Code)
		cond := domain.FindStatusCondition(stored.Status.Conditions, domain.ConditionTypeDeviceEnrollmentHooks)
		require.NotNil(cond)
		require.Equal(domain.ConditionStatusFalse, cond.Status)
		require.Equal(domain.EnrollmentHooksReasonFailed, cond.Reason)
	})

	t.Run("When a generic patch reports Succeeded from Pending it should accept", func(t *testing.T) {
		require := require.New(t)
		h, _, _, orgId, deviceName := newEnrollmentHookTestDevice(t,
			domain.ConditionStatusFalse, domain.EnrollmentHooksReasonPending)
		conditionPath := enrollmentHooksConditionPatchPath(t, h, orgId, deviceName)
		var succeededStatus any = domain.ConditionStatusTrue
		var succeededReason any = domain.EnrollmentHooksReasonSucceeded
		var message any = "hooks completed successfully"
		patch := domain.PatchRequest{
			{Op: "replace", Path: conditionPath + "/status", Value: &succeededStatus},
			{Op: "replace", Path: conditionPath + "/reason", Value: &succeededReason},
			{Op: "replace", Path: conditionPath + "/message", Value: &message},
		}

		dev, status := h.PatchDeviceStatus(ctx, orgId, deviceName, patch, false)
		require.Equal(int32(http.StatusOK), status.Code)
		require.NotNil(dev)
		cond := domain.FindStatusCondition(dev.Status.Conditions, domain.ConditionTypeDeviceEnrollmentHooks)
		require.NotNil(cond)
		require.Equal(domain.ConditionStatusTrue, cond.Status)
		require.Equal(domain.EnrollmentHooksReasonSucceeded, cond.Reason)
	})

	t.Run("When a generic patch reports Failed from Pending it should accept", func(t *testing.T) {
		require := require.New(t)
		h, _, _, orgId, deviceName := newEnrollmentHookTestDevice(t,
			domain.ConditionStatusFalse, domain.EnrollmentHooksReasonPending)
		conditionPath := enrollmentHooksConditionPatchPath(t, h, orgId, deviceName)
		var failedStatus any = domain.ConditionStatusFalse
		var failedReason any = domain.EnrollmentHooksReasonFailed
		var message any = "hooks failed"
		patch := domain.PatchRequest{
			{Op: "replace", Path: conditionPath + "/status", Value: &failedStatus},
			{Op: "replace", Path: conditionPath + "/reason", Value: &failedReason},
			{Op: "replace", Path: conditionPath + "/message", Value: &message},
		}

		dev, status := h.PatchDeviceStatus(ctx, orgId, deviceName, patch, false)
		require.Equal(int32(http.StatusOK), status.Code)
		require.NotNil(dev)
		cond := domain.FindStatusCondition(dev.Status.Conditions, domain.ConditionTypeDeviceEnrollmentHooks)
		require.NotNil(cond)
		require.Equal(domain.ConditionStatusFalse, cond.Status)
		require.Equal(domain.EnrollmentHooksReasonFailed, cond.Reason)
	})

	t.Run("When a generic patch removes the condition it should reject", func(t *testing.T) {
		require := require.New(t)
		h, _, _, orgId, deviceName := newEnrollmentHookTestDevice(t,
			domain.ConditionStatusFalse, domain.EnrollmentHooksReasonFailed)
		conditionPath := enrollmentHooksConditionPatchPath(t, h, orgId, deviceName)
		patch := domain.PatchRequest{{Op: "remove", Path: conditionPath}}

		dev, status := h.PatchDeviceStatus(ctx, orgId, deviceName, patch, false)
		require.Equal(int32(http.StatusBadRequest), status.Code)
		require.Contains(status.Message, "EnrollmentHooks condition cannot be modified via status patch")
		require.Nil(dev)
		stored, getStatus := h.GetDevice(ctx, orgId, deviceName)
		require.Equal(int32(http.StatusOK), getStatus.Code)
		require.NotNil(domain.FindStatusCondition(stored.Status.Conditions, domain.ConditionTypeDeviceEnrollmentHooks))
	})

	t.Run("When a generic patch sets ManualOverride it should reject", func(t *testing.T) {
		require := require.New(t)
		h, _, _, orgId, deviceName := newEnrollmentHookTestDevice(t,
			domain.ConditionStatusFalse, domain.EnrollmentHooksReasonFailed)
		conditionPath := enrollmentHooksConditionPatchPath(t, h, orgId, deviceName)
		var overriddenStatus any = domain.ConditionStatusTrue
		var overriddenReason any = domain.EnrollmentHooksReasonManualOverride
		patch := domain.PatchRequest{
			{Op: "replace", Path: conditionPath + "/status", Value: &overriddenStatus},
			{Op: "replace", Path: conditionPath + "/reason", Value: &overriddenReason},
		}

		dev, status := h.PatchDeviceStatus(ctx, orgId, deviceName, patch, false)
		require.Equal(int32(http.StatusBadRequest), status.Code)
		require.Contains(status.Message, "EnrollmentHooks condition cannot be modified via status patch")
		require.Nil(dev)
	})
}

func TestPatchDeviceStatusEnrollmentHooksFleetReconciliation(t *testing.T) {
	tests := []struct {
		name          string
		beforeStatus  domain.ConditionStatus
		beforeReason  string
		afterStatus   domain.ConditionStatus
		afterReason   string
		wantGateEvent bool
		wantCode      int32
		writeFails    bool
	}{
		{
			name:         "When Pending becomes Succeeded it should trigger fleet reconciliation",
			beforeStatus: domain.ConditionStatusFalse, beforeReason: domain.EnrollmentHooksReasonPending,
			afterStatus: domain.ConditionStatusTrue, afterReason: domain.EnrollmentHooksReasonSucceeded,
			wantGateEvent: true,
		},
		{
			name:         "When Pending becomes Continued it should trigger fleet reconciliation",
			beforeStatus: domain.ConditionStatusFalse, beforeReason: domain.EnrollmentHooksReasonPending,
			afterStatus: domain.ConditionStatusTrue, afterReason: domain.EnrollmentHooksReasonContinued,
			wantGateEvent: true,
		},
		{
			name:         "When Pending becomes Failed it should keep fleet reconciliation gated",
			beforeStatus: domain.ConditionStatusFalse, beforeReason: domain.EnrollmentHooksReasonPending,
			afterStatus: domain.ConditionStatusFalse, afterReason: domain.EnrollmentHooksReasonFailed,
		},
		{
			name:         "When NotifyPending is unchanged it should not trigger fleet reconciliation",
			beforeStatus: domain.ConditionStatusFalse, beforeReason: domain.EnrollmentHooksReasonNotifyPending,
			afterStatus: domain.ConditionStatusFalse, afterReason: domain.EnrollmentHooksReasonNotifyPending,
		},
		{
			name:         "When Pending is unchanged it should not trigger fleet reconciliation",
			beforeStatus: domain.ConditionStatusFalse, beforeReason: domain.EnrollmentHooksReasonPending,
			afterStatus: domain.ConditionStatusFalse, afterReason: domain.EnrollmentHooksReasonPending,
		},
		{
			name:         "When Failed is unchanged it should not trigger fleet reconciliation",
			beforeStatus: domain.ConditionStatusFalse, beforeReason: domain.EnrollmentHooksReasonFailed,
			afterStatus: domain.ConditionStatusFalse, afterReason: domain.EnrollmentHooksReasonFailed,
		},
		{
			name:         "When Succeeded is unchanged it should not trigger fleet reconciliation",
			beforeStatus: domain.ConditionStatusTrue, beforeReason: domain.EnrollmentHooksReasonSucceeded,
			afterStatus: domain.ConditionStatusTrue, afterReason: domain.EnrollmentHooksReasonSucceeded,
		},
		{
			name:         "When Continued is unchanged it should not trigger fleet reconciliation",
			beforeStatus: domain.ConditionStatusTrue, beforeReason: domain.EnrollmentHooksReasonContinued,
			afterStatus: domain.ConditionStatusTrue, afterReason: domain.EnrollmentHooksReasonContinued,
		},
		{
			name:         "When ManualOverride is unchanged it should not trigger fleet reconciliation",
			beforeStatus: domain.ConditionStatusTrue, beforeReason: domain.EnrollmentHooksReasonManualOverride,
			afterStatus: domain.ConditionStatusTrue, afterReason: domain.EnrollmentHooksReasonManualOverride,
		},
		{
			name:         "When an agent tries to clear a failed gate it should reject the update without emitting a gate event",
			beforeStatus: domain.ConditionStatusFalse, beforeReason: domain.EnrollmentHooksReasonFailed,
			afterStatus: domain.ConditionStatusTrue, afterReason: domain.EnrollmentHooksReasonSucceeded,
			wantCode: http.StatusBadRequest,
		},
		{
			name:         "When an agent tries to skip notification it should reject the update without emitting a gate event",
			beforeStatus: domain.ConditionStatusFalse, beforeReason: domain.EnrollmentHooksReasonNotifyPending,
			afterStatus: domain.ConditionStatusTrue, afterReason: domain.EnrollmentHooksReasonSucceeded,
			wantCode: http.StatusBadRequest,
		},
		{
			name:         "When saving hook completion fails it should not emit a gate event",
			beforeStatus: domain.ConditionStatusFalse, beforeReason: domain.EnrollmentHooksReasonPending,
			afterStatus: domain.ConditionStatusTrue, afterReason: domain.EnrollmentHooksReasonSucceeded,
			wantCode: http.StatusInternalServerError, writeFails: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)
			h, st, ev, orgID, name := newEnrollmentHookTestDevice(t, tt.beforeStatus, tt.beforeReason)
			conditionPath := enrollmentHooksConditionPatchPath(t, h, orgID, name)
			if tt.writeFails {
				st.device.mutateErr = errors.New("write failed")
			}
			var statusValue any = tt.afterStatus
			var reasonValue any = tt.afterReason
			var agentVersion any = "v2"
			patch := domain.PatchRequest{
				{Op: "replace", Path: conditionPath + "/status", Value: &statusValue},
				{Op: "replace", Path: conditionPath + "/reason", Value: &reasonValue},
				{Op: "replace", Path: "/status/systemInfo/agentVersion", Value: &agentVersion},
			}
			_, status := h.PatchDeviceStatus(context.Background(), orgID, name, patch, false)
			wantCode := tt.wantCode
			if wantCode == 0 {
				wantCode = http.StatusOK
			}
			require.Equal(wantCode, status.Code)

			gateEvents := 0
			for _, event := range ev.created {
				if event.Reason != domain.EventReasonResourceUpdated || event.Details == nil {
					continue
				}
				details, err := event.Details.AsResourceUpdatedDetails()
				require.NoError(err)
				if lo.Contains(details.UpdatedFields, domain.UpdatedFieldEnrollmentHooksCondition) {
					gateEvents++
				}
			}
			if tt.wantGateEvent {
				require.Equal(1, gateEvents)
			} else {
				require.Zero(gateEvents)
			}
		})
	}
}

func TestSetDeviceServiceConditions_EnrollmentHookEvents(t *testing.T) {
	ctx := context.Background()

	t.Run("When EnrollmentHooks transitions to Succeeded it should emit EnrollmentHookSucceeded event", func(t *testing.T) {
		require := require.New(t)
		h, _, ev, orgId, deviceName := newEnrollmentHookTestDevice(t,
			domain.ConditionStatusFalse, domain.EnrollmentHooksReasonPending)

		succeededCondition := domain.Condition{
			Type:   domain.ConditionTypeDeviceEnrollmentHooks,
			Status: domain.ConditionStatusTrue,
			Reason: domain.EnrollmentHooksReasonSucceeded,
		}

		status := h.SetDeviceServiceConditions(ctx, orgId, deviceName, []domain.Condition{succeededCondition})
		require.Equal(int32(http.StatusOK), status.Code)

		require.Len(ev.created, 2)
		require.Equal(domain.EventReasonResourceUpdated, ev.created[0].Reason)
		require.Equal(domain.EventReasonEnrollmentHookSucceeded, ev.created[1].Reason)
	})

	t.Run("When EnrollmentHooks transitions to Failed it should emit EnrollmentHookFailed event", func(t *testing.T) {
		require := require.New(t)
		h, _, ev, orgId, deviceName := newEnrollmentHookTestDevice(t,
			domain.ConditionStatusFalse, domain.EnrollmentHooksReasonPending)

		failedCondition := domain.Condition{
			Type:    domain.ConditionTypeDeviceEnrollmentHooks,
			Status:  domain.ConditionStatusFalse,
			Reason:  domain.EnrollmentHooksReasonFailed,
			Message: "hook exit code 1",
		}

		status := h.SetDeviceServiceConditions(ctx, orgId, deviceName, []domain.Condition{failedCondition})
		require.Equal(int32(http.StatusOK), status.Code)

		require.Len(ev.created, 1)
		require.Equal(domain.EventReasonEnrollmentHookFailed, ev.created[0].Reason)
	})

	t.Run("When EnrollmentHooks transitions to ManualOverride it should emit ManualOverride event", func(t *testing.T) {
		require := require.New(t)
		h, _, ev, orgId, deviceName := newEnrollmentHookTestDevice(t,
			domain.ConditionStatusFalse, domain.EnrollmentHooksReasonFailed)

		overrideCondition := domain.Condition{
			Type:   domain.ConditionTypeDeviceEnrollmentHooks,
			Status: domain.ConditionStatusTrue,
			Reason: domain.EnrollmentHooksReasonManualOverride,
		}

		status := h.SetDeviceServiceConditions(ctx, orgId, deviceName, []domain.Condition{overrideCondition})
		require.Equal(int32(http.StatusOK), status.Code)

		require.Len(ev.created, 2)
		require.Equal(domain.EventReasonResourceUpdated, ev.created[0].Reason)
		require.Equal(domain.EventReasonEnrollmentHookManualOverride, ev.created[1].Reason)
	})

	t.Run("When condition does not change it should not emit any events", func(t *testing.T) {
		require := require.New(t)
		h, _, ev, orgId, deviceName := newEnrollmentHookTestDevice(t,
			domain.ConditionStatusFalse, domain.EnrollmentHooksReasonFailed)

		// Set the same condition again - no change.
		sameCondition := domain.Condition{
			Type:   domain.ConditionTypeDeviceEnrollmentHooks,
			Status: domain.ConditionStatusFalse,
			Reason: domain.EnrollmentHooksReasonFailed,
		}

		status := h.SetDeviceServiceConditions(ctx, orgId, deviceName, []domain.Condition{sameCondition})
		require.Equal(int32(http.StatusOK), status.Code)
		require.Empty(ev.created)
	})
}
