package device

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/domain"
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

func manualOverridePatch(conditionPath string) domain.PatchRequest {
	var overriddenStatus any = domain.ConditionStatusTrue
	var overriddenReason any = domain.EnrollmentHooksReasonManualOverride
	var message any = "Enrollment hook failure was manually overridden by an operator."
	return domain.PatchRequest{
		{Op: "replace", Path: conditionPath + "/status", Value: &overriddenStatus},
		{Op: "replace", Path: conditionPath + "/reason", Value: &overriddenReason},
		{Op: "replace", Path: conditionPath + "/message", Value: &message},
	}
}

func TestPatchDeviceStatus_EnrollmentHooksManualOverride(t *testing.T) {
	ctx := context.Background()

	t.Run("When operator patches Failed to ManualOverride it should succeed and emit event", func(t *testing.T) {
		require := require.New(t)
		h, _, ev, orgId, deviceName := newEnrollmentHookTestDevice(t,
			domain.ConditionStatusFalse, domain.EnrollmentHooksReasonFailed)
		conditionPath := enrollmentHooksConditionPatchPath(t, h, orgId, deviceName)

		dev, status := h.PatchDeviceStatus(ctx, orgId, deviceName, manualOverridePatch(conditionPath))
		require.Equal(int32(http.StatusOK), status.Code)
		require.NotNil(dev)

		cond := domain.FindStatusCondition(dev.Status.Conditions, domain.ConditionTypeDeviceEnrollmentHooks)
		require.NotNil(cond)
		require.Equal(domain.ConditionStatusTrue, cond.Status)
		require.Equal(domain.EnrollmentHooksReasonManualOverride, cond.Reason)

		// Gate-clear ResourceUpdated + EnrollmentHookManualOverride.
		require.GreaterOrEqual(len(ev.created), 2)
		var sawOverride bool
		for _, e := range ev.created {
			if e.Reason == domain.EventReasonEnrollmentHookManualOverride {
				sawOverride = true
			}
		}
		require.True(sawOverride)
	})

	t.Run("When operator patches ManualOverride from Pending it should reject", func(t *testing.T) {
		require := require.New(t)
		h, _, _, orgId, deviceName := newEnrollmentHookTestDevice(t,
			domain.ConditionStatusFalse, domain.EnrollmentHooksReasonPending)
		conditionPath := enrollmentHooksConditionPatchPath(t, h, orgId, deviceName)

		dev, status := h.PatchDeviceStatus(ctx, orgId, deviceName, manualOverridePatch(conditionPath))
		require.Equal(int32(http.StatusBadRequest), status.Code)
		require.Contains(status.Message, "ManualOverride is only allowed when EnrollmentHooks condition is Failed")
		require.Nil(dev)
	})

	t.Run("When agent patches ManualOverride it should reject", func(t *testing.T) {
		require := require.New(t)
		h, _, _, orgId, deviceName := newEnrollmentHookTestDevice(t,
			domain.ConditionStatusFalse, domain.EnrollmentHooksReasonFailed)
		conditionPath := enrollmentHooksConditionPatchPath(t, h, orgId, deviceName)
		agentCtx := context.WithValue(ctx, consts.AgentCtxKey, "true")

		dev, status := h.PatchDeviceStatus(agentCtx, orgId, deviceName, manualOverridePatch(conditionPath))
		require.Equal(int32(http.StatusBadRequest), status.Code)
		require.Contains(status.Message, "agent cannot modify EnrollmentHooks")
		require.Nil(dev)
	})

	t.Run("When agent patches Succeeded it should reject", func(t *testing.T) {
		require := require.New(t)
		h, _, _, orgId, deviceName := newEnrollmentHookTestDevice(t,
			domain.ConditionStatusFalse, domain.EnrollmentHooksReasonPending)
		conditionPath := enrollmentHooksConditionPatchPath(t, h, orgId, deviceName)
		agentCtx := context.WithValue(ctx, consts.AgentCtxKey, "true")
		var succeededStatus any = domain.ConditionStatusTrue
		var succeededReason any = domain.EnrollmentHooksReasonSucceeded
		patch := domain.PatchRequest{
			{Op: "replace", Path: conditionPath + "/status", Value: &succeededStatus},
			{Op: "replace", Path: conditionPath + "/reason", Value: &succeededReason},
		}

		dev, status := h.PatchDeviceStatus(agentCtx, orgId, deviceName, patch)
		require.Equal(int32(http.StatusBadRequest), status.Code)
		require.Contains(status.Message, "agent cannot modify EnrollmentHooks")
		require.Nil(dev)
	})

	t.Run("When operator patches Failed to Succeeded it should reject", func(t *testing.T) {
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

		dev, status := h.PatchDeviceStatus(ctx, orgId, deviceName, patch)
		require.Equal(int32(http.StatusBadRequest), status.Code)
		require.Contains(status.Message, "True/ManualOverride")
		require.Nil(dev)
	})

	t.Run("When a patch appends a duplicate EnrollmentHooks condition it should reject", func(t *testing.T) {
		require := require.New(t)
		h, _, _, orgId, deviceName := newEnrollmentHookTestDevice(t,
			domain.ConditionStatusFalse, domain.EnrollmentHooksReasonFailed)
		device, getStatus := h.GetDevice(ctx, orgId, deviceName)
		require.Equal(int32(http.StatusOK), getStatus.Code)
		require.NotNil(device.Status)

		dup := domain.Condition{
			Type:   domain.ConditionTypeDeviceEnrollmentHooks,
			Status: domain.ConditionStatusTrue,
			Reason: domain.EnrollmentHooksReasonSucceeded,
		}
		conditions := append(append([]domain.Condition{}, device.Status.Conditions...), dup)
		var conditionsValue any = conditions
		patch := domain.PatchRequest{
			{Op: "replace", Path: "/status/conditions", Value: &conditionsValue},
		}

		dev, status := h.PatchDeviceStatus(ctx, orgId, deviceName, patch)
		require.Equal(int32(http.StatusBadRequest), status.Code)
		require.Contains(status.Message, "at most once")
		require.Nil(dev)
	})

	t.Run("When a patch removes the condition it should reject", func(t *testing.T) {
		require := require.New(t)
		h, _, _, orgId, deviceName := newEnrollmentHookTestDevice(t,
			domain.ConditionStatusFalse, domain.EnrollmentHooksReasonFailed)
		conditionPath := enrollmentHooksConditionPatchPath(t, h, orgId, deviceName)
		patch := domain.PatchRequest{{Op: "remove", Path: conditionPath}}

		dev, status := h.PatchDeviceStatus(ctx, orgId, deviceName, patch)
		require.Equal(int32(http.StatusBadRequest), status.Code)
		require.Contains(status.Message, "EnrollmentHooks condition cannot be removed")
		require.Nil(dev)
	})
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
