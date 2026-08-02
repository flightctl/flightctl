package device

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

// decodeLifecycleOverrides unmarshals a raw DeviceAnnotationApplicationLifecycle annotation
// value for assertions that need to check individual fields (e.g. ignoring the exact,
// non-deterministic desiredStateVersion stamp).
func decodeLifecycleOverrides(t *testing.T, raw string) map[string]domain.ApplicationLifecycleOverride {
	t.Helper()
	overrides := map[string]domain.ApplicationLifecycleOverride{}
	require.NoError(t, json.Unmarshal([]byte(raw), &overrides))
	return overrides
}

// newLifecycleTestDevice creates a device with a single container application named appName,
// registers it in a fresh DeviceServiceHandler backed by the fake stores, and returns the
// handler along with the org and device name to use in calls.
func newLifecycleTestDevice(t *testing.T, appName string) (h Service, st *fakeStore, ev *fakeEvents, orgId uuid.UUID, deviceName string) {
	t.Helper()
	require := require.New(t)

	containerApp := domain.ContainerApplication{
		AppType: domain.AppTypeContainer,
		Name:    lo.ToPtr(appName),
		Image:   "quay.io/test/app:v1",
	}
	var app domain.ApplicationProviderSpec
	require.NoError(app.FromContainerApplication(containerApp))

	deviceName = "device-1"
	device := domain.Device{
		Metadata: domain.ObjectMeta{
			Name: lo.ToPtr(deviceName),
		},
		Spec: &domain.DeviceSpec{
			Applications: &[]domain.ApplicationProviderSpec{app},
		},
	}

	st = newFakeStore()
	ev = &fakeEvents{}
	h = NewDeviceServiceHandler(st.device, st.fleet, ev, nil, "agent.example.com", logrus.New())
	orgId = uuid.New()
	_, err := st.device.Create(context.Background(), orgId, &device)
	require.NoError(err)

	return h, st, ev, orgId, deviceName
}

func TestStopStartRestartDeviceApplication(t *testing.T) {
	ctx := context.Background()

	t.Run("StopDeviceApplication sets desiredState=stopped without touching the declarative spec", func(t *testing.T) {
		require := require.New(t)
		h, _, _, orgId, deviceName := newLifecycleTestDevice(t, "app-1")

		dev, status := h.StopDeviceApplication(ctx, orgId, deviceName, "app-1")
		require.Equal(int32(http.StatusOK), status.Code)
		require.NotNil(dev.Metadata.Annotations)
		overrides := decodeLifecycleOverrides(t, (*dev.Metadata.Annotations)[domain.DeviceAnnotationApplicationLifecycle])
		require.NotNil(overrides["app-1"].DesiredState)
		require.Equal(domain.ApplicationDesiredStateStopped, *overrides["app-1"].DesiredState)
		require.NotNil(overrides["app-1"].DesiredStateVersion)

		// The declarative spec itself is untouched; the override lives only in the annotation.
		require.Equal(domain.ApplicationDesiredStateRunning, (*dev.Spec.Applications)[0].GetDesiredState())
	})

	t.Run("StartDeviceApplication sets desiredState=running", func(t *testing.T) {
		require := require.New(t)
		h, _, _, orgId, deviceName := newLifecycleTestDevice(t, "app-1")

		_, status := h.StopDeviceApplication(ctx, orgId, deviceName, "app-1")
		require.Equal(int32(http.StatusOK), status.Code)

		dev, status := h.StartDeviceApplication(ctx, orgId, deviceName, "app-1")
		require.Equal(int32(http.StatusOK), status.Code)
		overrides := decodeLifecycleOverrides(t, (*dev.Metadata.Annotations)[domain.DeviceAnnotationApplicationLifecycle])
		require.NotNil(overrides["app-1"].DesiredState)
		require.Equal(domain.ApplicationDesiredStateRunning, *overrides["app-1"].DesiredState)
		require.NotNil(overrides["app-1"].DesiredStateVersion)
	})

	t.Run("RestartDeviceApplication increments restartGeneration starting from 0", func(t *testing.T) {
		require := require.New(t)
		h, _, _, orgId, deviceName := newLifecycleTestDevice(t, "app-1")

		dev, status := h.RestartDeviceApplication(ctx, orgId, deviceName, "app-1")
		require.Equal(int32(http.StatusOK), status.Code)
		require.Equal(`{"app-1":{"restartGeneration":1}}`, (*dev.Metadata.Annotations)[domain.DeviceAnnotationApplicationLifecycle])

		dev, status = h.RestartDeviceApplication(ctx, orgId, deviceName, "app-1")
		require.Equal(int32(http.StatusOK), status.Code)
		require.Equal(`{"app-1":{"restartGeneration":2}}`, (*dev.Metadata.Annotations)[domain.DeviceAnnotationApplicationLifecycle])
	})

	t.Run("Stop then restart preserves both fields in the annotation", func(t *testing.T) {
		require := require.New(t)
		h, _, _, orgId, deviceName := newLifecycleTestDevice(t, "app-1")

		_, status := h.StopDeviceApplication(ctx, orgId, deviceName, "app-1")
		require.Equal(int32(http.StatusOK), status.Code)

		dev, status := h.RestartDeviceApplication(ctx, orgId, deviceName, "app-1")
		require.Equal(int32(http.StatusOK), status.Code)
		overrides := decodeLifecycleOverrides(t, (*dev.Metadata.Annotations)[domain.DeviceAnnotationApplicationLifecycle])
		require.NotNil(overrides["app-1"].DesiredState)
		require.Equal(domain.ApplicationDesiredStateStopped, *overrides["app-1"].DesiredState)
		require.NotNil(overrides["app-1"].RestartGeneration)
		require.Equal(1, *overrides["app-1"].RestartGeneration)
	})

	t.Run("Restart then start preserves restartGeneration and sets desiredState=running", func(t *testing.T) {
		require := require.New(t)
		h, _, _, orgId, deviceName := newLifecycleTestDevice(t, "app-1")

		_, status := h.RestartDeviceApplication(ctx, orgId, deviceName, "app-1")
		require.Equal(int32(http.StatusOK), status.Code)

		dev, status := h.StartDeviceApplication(ctx, orgId, deviceName, "app-1")
		require.Equal(int32(http.StatusOK), status.Code)
		overrides := decodeLifecycleOverrides(t, (*dev.Metadata.Annotations)[domain.DeviceAnnotationApplicationLifecycle])
		require.NotNil(overrides["app-1"].DesiredState)
		require.Equal(domain.ApplicationDesiredStateRunning, *overrides["app-1"].DesiredState)
		require.NotNil(overrides["app-1"].RestartGeneration)
		require.Equal(1, *overrides["app-1"].RestartGeneration, "start must not drop a previously-stored restartGeneration")
	})

	t.Run("Restart after start continues incrementing restartGeneration rather than resetting it", func(t *testing.T) {
		require := require.New(t)
		h, _, _, orgId, deviceName := newLifecycleTestDevice(t, "app-1")

		dev, status := h.RestartDeviceApplication(ctx, orgId, deviceName, "app-1")
		require.Equal(int32(http.StatusOK), status.Code)
		overrides := decodeLifecycleOverrides(t, (*dev.Metadata.Annotations)[domain.DeviceAnnotationApplicationLifecycle])
		require.Equal(1, *overrides["app-1"].RestartGeneration)

		_, status = h.StartDeviceApplication(ctx, orgId, deviceName, "app-1")
		require.Equal(int32(http.StatusOK), status.Code)

		dev, status = h.RestartDeviceApplication(ctx, orgId, deviceName, "app-1")
		require.Equal(int32(http.StatusOK), status.Code)
		overrides = decodeLifecycleOverrides(t, (*dev.Metadata.Annotations)[domain.DeviceAnnotationApplicationLifecycle])
		require.Equal(2, *overrides["app-1"].RestartGeneration, "start no longer clears the annotation, so restartGeneration keeps incrementing")
	})

	t.Run("Lifecycle calls for an unknown application return not found", func(t *testing.T) {
		require := require.New(t)
		h, _, _, orgId, deviceName := newLifecycleTestDevice(t, "app-1")

		_, status := h.StopDeviceApplication(ctx, orgId, deviceName, "does-not-exist")
		require.Equal(int32(http.StatusNotFound), status.Code)

		_, status = h.RestartDeviceApplication(ctx, orgId, deviceName, "does-not-exist")
		require.Equal(int32(http.StatusNotFound), status.Code)
	})

	t.Run("Lifecycle calls for an unknown device return not found", func(t *testing.T) {
		require := require.New(t)
		h, _, _, orgId, _ := newLifecycleTestDevice(t, "app-1")

		_, status := h.StopDeviceApplication(ctx, orgId, "does-not-exist", "app-1")
		require.Equal(int32(http.StatusNotFound), status.Code)
	})

	t.Run("Each lifecycle action emits an ApplicationLifecycleChanged event", func(t *testing.T) {
		require := require.New(t)
		h, _, ev, orgId, deviceName := newLifecycleTestDevice(t, "app-1")

		_, status := h.StopDeviceApplication(ctx, orgId, deviceName, "app-1")
		require.Equal(int32(http.StatusOK), status.Code)

		require.Len(ev.created, 1)
		require.Equal(domain.EventReasonApplicationLifecycleChanged, ev.created[0].Reason)
	})
}
