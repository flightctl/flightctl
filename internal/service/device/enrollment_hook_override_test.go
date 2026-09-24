package device

import (
	"context"
	"net/http"
	"testing"

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

	t.Run("When device does not exist it should return not found", func(t *testing.T) {
		require := require.New(t)
		h, _, _, orgId, _ := newEnrollmentHookTestDevice(t,
			domain.ConditionStatusFalse, domain.EnrollmentHooksReasonFailed)

		dev, status := h.OverrideDeviceEnrollmentHook(ctx, orgId, "nonexistent")
		require.NotEqual(int32(http.StatusOK), status.Code)
		require.Nil(dev)
	})
}

func TestRejectManualOverrideViaStatusPatch(t *testing.T) {
	t.Run("When device has ManualOverride reason it should reject", func(t *testing.T) {
		require := require.New(t)
		status := domain.NewDeviceStatus()
		status.Conditions = []domain.Condition{
			{
				Type:   domain.ConditionTypeDeviceEnrollmentHooks,
				Status: domain.ConditionStatusTrue,
				Reason: domain.EnrollmentHooksReasonManualOverride,
			},
		}
		device := &domain.Device{Status: &status}

		err := rejectManualOverrideViaStatusPatch(device)
		require.Error(err)
		require.Contains(err.Error(), "ManualOverride cannot be set via status patch")
	})

	t.Run("When device has Succeeded reason it should allow", func(t *testing.T) {
		require := require.New(t)
		status := domain.NewDeviceStatus()
		status.Conditions = []domain.Condition{
			{
				Type:   domain.ConditionTypeDeviceEnrollmentHooks,
				Status: domain.ConditionStatusTrue,
				Reason: domain.EnrollmentHooksReasonSucceeded,
			},
		}
		device := &domain.Device{Status: &status}

		err := rejectManualOverrideViaStatusPatch(device)
		require.NoError(err)
	})

	t.Run("When device has Failed reason it should allow", func(t *testing.T) {
		require := require.New(t)
		status := domain.NewDeviceStatus()
		status.Conditions = []domain.Condition{
			{
				Type:   domain.ConditionTypeDeviceEnrollmentHooks,
				Status: domain.ConditionStatusFalse,
				Reason: domain.EnrollmentHooksReasonFailed,
			},
		}
		device := &domain.Device{Status: &status}

		err := rejectManualOverrideViaStatusPatch(device)
		require.NoError(err)
	})

	t.Run("When device has no EnrollmentHooks condition it should allow", func(t *testing.T) {
		require := require.New(t)
		status := domain.NewDeviceStatus()
		device := &domain.Device{Status: &status}

		err := rejectManualOverrideViaStatusPatch(device)
		require.NoError(err)
	})

	t.Run("When device is nil it should allow", func(t *testing.T) {
		require := require.New(t)
		err := rejectManualOverrideViaStatusPatch(nil)
		require.NoError(err)
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
