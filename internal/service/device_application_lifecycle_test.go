package service

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/rendered"
	"github.com/flightctl/flightctl/pkg/queues"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ─── rendered.Bus test wiring ──────────────────────────────────────────────
//
// modifyDeviceApplicationLifecycle unconditionally notifies rendered.Bus (a
// package-level singleton) on success. Initialize it once, for the whole test
// binary, with no-op fakes so unit tests never depend on Redis.

var initRenderedBusOnce sync.Once

func ensureRenderedBusInitialized(t *testing.T) {
	t.Helper()
	initRenderedBusOnce.Do(func() {
		err := rendered.Bus.Initialize(context.Background(), &MockKVStore{}, noopQueuesProvider{}, time.Second, logrus.New())
		require.NoError(t, err)
	})
}

type noopQueuesProvider struct{}

func (noopQueuesProvider) NewQueueConsumer(_ context.Context, _ string) (queues.QueueConsumer, error) {
	return nil, nil
}
func (noopQueuesProvider) NewQueueProducer(_ context.Context, _ string) (queues.QueueProducer, error) {
	return nil, nil
}
func (noopQueuesProvider) NewPubSubPublisher(_ context.Context, _ string) (queues.PubSubPublisher, error) {
	return noopPubSubPublisher{}, nil
}
func (noopQueuesProvider) NewPubSubSubscriber(_ context.Context, _ string) (queues.PubSubSubscriber, error) {
	return noopPubSubSubscriber{}, nil
}
func (noopQueuesProvider) ProcessTimedOutMessages(_ context.Context, _ string, _ time.Duration, _ func(entryID string, body []byte) error) (int, error) {
	return 0, nil
}
func (noopQueuesProvider) RetryFailedMessages(_ context.Context, _ string, _ queues.RetryConfig, _ func(entryID string, body []byte, retryCount int) error) (int, error) {
	return 0, nil
}
func (noopQueuesProvider) Stop()                               {}
func (noopQueuesProvider) Wait()                               {}
func (noopQueuesProvider) CheckHealth(_ context.Context) error { return nil }
func (noopQueuesProvider) GetLatestProcessedTimestamp(_ context.Context) (time.Time, error) {
	return time.Time{}, nil
}
func (noopQueuesProvider) AdvanceCheckpointAndCleanup(_ context.Context) error { return nil }
func (noopQueuesProvider) SetCheckpointTimestamp(_ context.Context, _ time.Time) error {
	return nil
}

type noopPubSubPublisher struct{}

func (noopPubSubPublisher) Publish(_ context.Context, _ []byte) error { return nil }
func (noopPubSubPublisher) Close()                                    {}

type noopPubSubSubscriber struct{}

func (noopPubSubSubscriber) Subscribe(_ context.Context, _ queues.PubSubHandler) (queues.Subscription, error) {
	return noopSubscription{}, nil
}
func (noopPubSubSubscriber) Close() {}

type noopSubscription struct{}

func (noopSubscription) Close() {}

var _ queues.Provider = noopQueuesProvider{}

// ─── test helpers ───────────────────────────────────────────────────────────

func newLifecycleTestHandler(t *testing.T) (*ServiceHandler, *TestStore, uuid.UUID) {
	t.Helper()
	ensureRenderedBusInitialized(t)
	ts := &TestStore{}
	wc := &DummyWorkerClient{}
	h := &ServiceHandler{
		eventHandler: NewEventHandler(ts, wc, logrus.New()),
		store:        ts,
		workerClient: wc,
		log:          logrus.New(),
	}
	return h, ts, uuid.New()
}

func newDeviceWithApp(name, appName string) *domain.Device {
	device := prepareDevice(uuid.New(), name)
	app := domain.ContainerApplication{
		AppType: domain.AppTypeContainer,
		Name:    lo.ToPtr(appName),
		Image:   "quay.io/test/app:v1",
	}
	var appSpec domain.ApplicationProviderSpec
	_ = appSpec.FromContainerApplication(app)
	device.Spec.Applications = &[]domain.ApplicationProviderSpec{appSpec}
	return device
}

func createLifecycleDevice(t *testing.T, h *ServiceHandler, orgId uuid.UUID, device *domain.Device) {
	t.Helper()
	_, err := h.store.Device().Create(context.Background(), orgId, device, nil)
	require.NoError(t, err)
}

// ─── GetDeviceApplicationLifecycle ─────────────────────────────────────────

func TestGetDeviceApplicationLifecycle(t *testing.T) {
	t.Run("When device does not exist it should return not found", func(t *testing.T) {
		h, _, orgId := newLifecycleTestHandler(t)
		_, status := h.GetDeviceApplicationLifecycle(context.Background(), orgId, "missing", "app-1")
		assert.Equal(t, int32(http.StatusNotFound), status.Code)
	})

	t.Run("When device has no lifecycle annotation it should return not found for the application", func(t *testing.T) {
		h, _, orgId := newLifecycleTestHandler(t)
		device := newDeviceWithApp("device-1", "app-1")
		createLifecycleDevice(t, h, orgId, device)

		_, status := h.GetDeviceApplicationLifecycle(context.Background(), orgId, "device-1", "app-1")
		assert.Equal(t, int32(http.StatusNotFound), status.Code)
	})

	t.Run("When device has a lifecycle entry for the application it should return it", func(t *testing.T) {
		h, _, orgId := newLifecycleTestHandler(t)
		device := newDeviceWithApp("device-1", "app-1")
		device.Metadata.Annotations = &map[string]string{
			domain.DeviceAnnotationApplicationLifecycle: `{"app-1":{"desiredState":"stopped"}}`,
		}
		createLifecycleDevice(t, h, orgId, device)

		lifecycle, status := h.GetDeviceApplicationLifecycle(context.Background(), orgId, "device-1", "app-1")
		require.Equal(t, int32(http.StatusOK), status.Code)
		require.NotNil(t, lifecycle)
		require.NotNil(t, lifecycle.DesiredState)
		assert.Equal(t, domain.ApplicationDesiredStateStopped, *lifecycle.DesiredState)
	})

	t.Run("When the lifecycle annotation is invalid JSON it should return an internal error", func(t *testing.T) {
		h, _, orgId := newLifecycleTestHandler(t)
		device := newDeviceWithApp("device-1", "app-1")
		device.Metadata.Annotations = &map[string]string{
			domain.DeviceAnnotationApplicationLifecycle: `not-json`,
		}
		createLifecycleDevice(t, h, orgId, device)

		_, status := h.GetDeviceApplicationLifecycle(context.Background(), orgId, "device-1", "app-1")
		assert.Equal(t, int32(http.StatusInternalServerError), status.Code)
	})
}

// ─── SetDeviceApplicationDesiredState ───────────────────────────────────────

func TestSetDeviceApplicationDesiredState(t *testing.T) {
	t.Run("When the application does not exist on the device it should return not found", func(t *testing.T) {
		h, _, orgId := newLifecycleTestHandler(t)
		device := newDeviceWithApp("device-1", "app-1")
		createLifecycleDevice(t, h, orgId, device)

		_, status := h.SetDeviceApplicationDesiredState(context.Background(), orgId, "device-1", "does-not-exist", domain.ApplicationDesiredStateStopped)
		assert.Equal(t, int32(http.StatusNotFound), status.Code)
	})

	t.Run("When the application exists it should persist the desired state and bump the rendered version", func(t *testing.T) {
		h, ts, orgId := newLifecycleTestHandler(t)
		device := newDeviceWithApp("device-1", "app-1")
		createLifecycleDevice(t, h, orgId, device)

		lifecycle, status := h.SetDeviceApplicationDesiredState(context.Background(), orgId, "device-1", "app-1", domain.ApplicationDesiredStateStopped)
		require.Equal(t, int32(http.StatusOK), status.Code)
		require.NotNil(t, lifecycle)
		require.NotNil(t, lifecycle.DesiredState)
		assert.Equal(t, domain.ApplicationDesiredStateStopped, *lifecycle.DesiredState)

		stored, err := ts.Device().Get(context.Background(), orgId, "device-1")
		require.NoError(t, err)
		require.NotNil(t, stored.Metadata.Annotations)
		annotations := *stored.Metadata.Annotations
		assert.Contains(t, annotations[domain.DeviceAnnotationApplicationLifecycle], `"app-1"`)
		assert.NotEmpty(t, annotations[domain.DeviceAnnotationRenderedVersion])
	})

	t.Run("When a restart generation is already set it should update the desired state without disturbing it", func(t *testing.T) {
		h, _, orgId := newLifecycleTestHandler(t)
		device := newDeviceWithApp("device-1", "app-1")
		device.Metadata.Annotations = &map[string]string{
			domain.DeviceAnnotationApplicationLifecycle: `{"app-1":{"restartGeneration":4}}`,
		}
		createLifecycleDevice(t, h, orgId, device)

		lifecycle, status := h.SetDeviceApplicationDesiredState(context.Background(), orgId, "device-1", "app-1", domain.ApplicationDesiredStateStopped)
		require.Equal(t, int32(http.StatusOK), status.Code)
		require.NotNil(t, lifecycle.DesiredState)
		assert.Equal(t, domain.ApplicationDesiredStateStopped, *lifecycle.DesiredState)
		require.NotNil(t, lifecycle.RestartGeneration)
		assert.Equal(t, 4, *lifecycle.RestartGeneration)
	})

	t.Run("When the device is awaiting reconnect it should return a conflict", func(t *testing.T) {
		h, _, orgId := newLifecycleTestHandler(t)
		device := newDeviceWithApp("device-1", "app-1")
		device.Metadata.Annotations = &map[string]string{
			domain.DeviceAnnotationAwaitingReconnect: "true",
		}
		createLifecycleDevice(t, h, orgId, device)

		_, status := h.SetDeviceApplicationDesiredState(context.Background(), orgId, "device-1", "app-1", domain.ApplicationDesiredStateStopped)
		assert.Equal(t, int32(http.StatusConflict), status.Code)
	})

	t.Run("When the device is conflict-paused it should return a conflict", func(t *testing.T) {
		h, _, orgId := newLifecycleTestHandler(t)
		device := newDeviceWithApp("device-1", "app-1")
		device.Metadata.Annotations = &map[string]string{
			domain.DeviceAnnotationConflictPaused: "true",
		}
		createLifecycleDevice(t, h, orgId, device)

		_, status := h.SetDeviceApplicationDesiredState(context.Background(), orgId, "device-1", "app-1", domain.ApplicationDesiredStateStopped)
		assert.Equal(t, int32(http.StatusConflict), status.Code)
	})

	t.Run("When the device is decommissioned it should return a conflict", func(t *testing.T) {
		h, _, orgId := newLifecycleTestHandler(t)
		device := newDeviceWithApp("device-1", "app-1")
		device.Spec.Decommissioning = &domain.DeviceDecommission{}
		createLifecycleDevice(t, h, orgId, device)

		_, status := h.SetDeviceApplicationDesiredState(context.Background(), orgId, "device-1", "app-1", domain.ApplicationDesiredStateStopped)
		assert.Equal(t, int32(http.StatusConflict), status.Code)
	})
}

// ─── RestartDeviceApplication ────────────────────────────────────────────────

func TestRestartDeviceApplication(t *testing.T) {
	t.Run("When the application has never been restarted it should start the generation at 1", func(t *testing.T) {
		h, _, orgId := newLifecycleTestHandler(t)
		device := newDeviceWithApp("device-1", "app-1")
		createLifecycleDevice(t, h, orgId, device)

		lifecycle, status := h.RestartDeviceApplication(context.Background(), orgId, "device-1", "app-1")
		require.Equal(t, int32(http.StatusOK), status.Code)
		require.NotNil(t, lifecycle.RestartGeneration)
		assert.Equal(t, 1, *lifecycle.RestartGeneration)
	})

	t.Run("When called again it should increment the existing generation", func(t *testing.T) {
		h, _, orgId := newLifecycleTestHandler(t)
		device := newDeviceWithApp("device-1", "app-1")
		createLifecycleDevice(t, h, orgId, device)

		_, status := h.RestartDeviceApplication(context.Background(), orgId, "device-1", "app-1")
		require.Equal(t, int32(http.StatusOK), status.Code)

		lifecycle, status := h.RestartDeviceApplication(context.Background(), orgId, "device-1", "app-1")
		require.Equal(t, int32(http.StatusOK), status.Code)
		require.NotNil(t, lifecycle.RestartGeneration)
		assert.Equal(t, 2, *lifecycle.RestartGeneration)
	})

	t.Run("When the application does not exist on the device it should return not found", func(t *testing.T) {
		h, _, orgId := newLifecycleTestHandler(t)
		device := newDeviceWithApp("device-1", "app-1")
		createLifecycleDevice(t, h, orgId, device)

		_, status := h.RestartDeviceApplication(context.Background(), orgId, "device-1", "does-not-exist")
		assert.Equal(t, int32(http.StatusNotFound), status.Code)
	})

	t.Run("When a desired state is already set it should increment the generation without disturbing it", func(t *testing.T) {
		h, _, orgId := newLifecycleTestHandler(t)
		device := newDeviceWithApp("device-1", "app-1")
		device.Metadata.Annotations = &map[string]string{
			domain.DeviceAnnotationApplicationLifecycle: `{"app-1":{"desiredState":"stopped"}}`,
		}
		createLifecycleDevice(t, h, orgId, device)

		lifecycle, status := h.RestartDeviceApplication(context.Background(), orgId, "device-1", "app-1")
		require.Equal(t, int32(http.StatusOK), status.Code)
		require.NotNil(t, lifecycle.RestartGeneration)
		assert.Equal(t, 1, *lifecycle.RestartGeneration)
		require.NotNil(t, lifecycle.DesiredState)
		assert.Equal(t, domain.ApplicationDesiredStateStopped, *lifecycle.DesiredState)
	})

	t.Run("When the device is awaiting reconnect it should return a conflict", func(t *testing.T) {
		h, _, orgId := newLifecycleTestHandler(t)
		device := newDeviceWithApp("device-1", "app-1")
		device.Metadata.Annotations = &map[string]string{
			domain.DeviceAnnotationAwaitingReconnect: "true",
		}
		createLifecycleDevice(t, h, orgId, device)

		_, status := h.RestartDeviceApplication(context.Background(), orgId, "device-1", "app-1")
		assert.Equal(t, int32(http.StatusConflict), status.Code)
	})

	t.Run("When the device is conflict-paused it should return a conflict", func(t *testing.T) {
		h, _, orgId := newLifecycleTestHandler(t)
		device := newDeviceWithApp("device-1", "app-1")
		device.Metadata.Annotations = &map[string]string{
			domain.DeviceAnnotationConflictPaused: "true",
		}
		createLifecycleDevice(t, h, orgId, device)

		_, status := h.RestartDeviceApplication(context.Background(), orgId, "device-1", "app-1")
		assert.Equal(t, int32(http.StatusConflict), status.Code)
	})

	t.Run("When the device is decommissioned it should return a conflict", func(t *testing.T) {
		h, _, orgId := newLifecycleTestHandler(t)
		device := newDeviceWithApp("device-1", "app-1")
		device.Spec.Decommissioning = &domain.DeviceDecommission{}
		createLifecycleDevice(t, h, orgId, device)

		_, status := h.RestartDeviceApplication(context.Background(), orgId, "device-1", "app-1")
		assert.Equal(t, int32(http.StatusConflict), status.Code)
	})
}

// ─── SetDeviceApplicationDesiredState(Running) clears the override ─────────
//
// There is no standalone delete endpoint: setting the desired state to "running" is
// equivalent to clearing the lifecycle override entirely, since "running" is already the
// implicit state whenever no override is present.

func TestSetDeviceApplicationDesiredStateRunningClearsOverride(t *testing.T) {
	t.Run("When it is the last lifecycle entry it should remove the annotation entirely", func(t *testing.T) {
		h, ts, orgId := newLifecycleTestHandler(t)
		device := newDeviceWithApp("device-1", "app-1")
		device.Metadata.Annotations = &map[string]string{
			domain.DeviceAnnotationApplicationLifecycle: `{"app-1":{"desiredState":"stopped"}}`,
		}
		createLifecycleDevice(t, h, orgId, device)

		_, status := h.SetDeviceApplicationDesiredState(context.Background(), orgId, "device-1", "app-1", domain.ApplicationDesiredStateRunning)
		require.Equal(t, int32(http.StatusOK), status.Code)

		stored, err := ts.Device().Get(context.Background(), orgId, "device-1")
		require.NoError(t, err)
		annotations := lo.FromPtr(stored.Metadata.Annotations)
		_, exists := annotations[domain.DeviceAnnotationApplicationLifecycle]
		assert.False(t, exists, "lifecycle annotation should be removed once empty")
	})

	t.Run("When other lifecycle entries remain it should only remove the specified application", func(t *testing.T) {
		h, ts, orgId := newLifecycleTestHandler(t)
		device := newDeviceWithApp("device-1", "app-1")
		device.Metadata.Annotations = &map[string]string{
			domain.DeviceAnnotationApplicationLifecycle: `{"app-1":{"desiredState":"stopped"},"app-2":{"desiredState":"stopped"}}`,
		}
		createLifecycleDevice(t, h, orgId, device)

		_, status := h.SetDeviceApplicationDesiredState(context.Background(), orgId, "device-1", "app-1", domain.ApplicationDesiredStateRunning)
		require.Equal(t, int32(http.StatusOK), status.Code)

		stored, err := ts.Device().Get(context.Background(), orgId, "device-1")
		require.NoError(t, err)
		annotations := lo.FromPtr(stored.Metadata.Annotations)
		raw, exists := annotations[domain.DeviceAnnotationApplicationLifecycle]
		require.True(t, exists)
		assert.Contains(t, raw, "app-2")
		assert.NotContains(t, raw, "app-1")
	})

	t.Run("When there is nothing to clear it should still succeed", func(t *testing.T) {
		h, _, orgId := newLifecycleTestHandler(t)
		device := newDeviceWithApp("device-1", "app-1")
		createLifecycleDevice(t, h, orgId, device)

		_, status := h.SetDeviceApplicationDesiredState(context.Background(), orgId, "device-1", "app-1", domain.ApplicationDesiredStateRunning)
		assert.Equal(t, int32(http.StatusOK), status.Code)
	})

	t.Run("When the entry has a restart generation it should be cleared along with the desired state", func(t *testing.T) {
		h, ts, orgId := newLifecycleTestHandler(t)
		device := newDeviceWithApp("device-1", "app-1")
		device.Metadata.Annotations = &map[string]string{
			domain.DeviceAnnotationApplicationLifecycle: `{"app-1":{"desiredState":"stopped","restartGeneration":2}}`,
		}
		createLifecycleDevice(t, h, orgId, device)

		_, status := h.SetDeviceApplicationDesiredState(context.Background(), orgId, "device-1", "app-1", domain.ApplicationDesiredStateRunning)
		require.Equal(t, int32(http.StatusOK), status.Code)

		stored, err := ts.Device().Get(context.Background(), orgId, "device-1")
		require.NoError(t, err)
		annotations := lo.FromPtr(stored.Metadata.Annotations)
		_, exists := annotations[domain.DeviceAnnotationApplicationLifecycle]
		assert.False(t, exists, "lifecycle annotation should be removed once empty")
	})

	t.Run("When the application does not exist on the device it should return not found", func(t *testing.T) {
		h, _, orgId := newLifecycleTestHandler(t)
		device := newDeviceWithApp("device-1", "app-1")
		createLifecycleDevice(t, h, orgId, device)

		_, status := h.SetDeviceApplicationDesiredState(context.Background(), orgId, "device-1", "does-not-exist", domain.ApplicationDesiredStateRunning)
		assert.Equal(t, int32(http.StatusNotFound), status.Code)
	})

	t.Run("When the device is awaiting reconnect it should return a conflict", func(t *testing.T) {
		h, _, orgId := newLifecycleTestHandler(t)
		device := newDeviceWithApp("device-1", "app-1")
		device.Metadata.Annotations = &map[string]string{
			domain.DeviceAnnotationAwaitingReconnect: "true",
		}
		createLifecycleDevice(t, h, orgId, device)

		_, status := h.SetDeviceApplicationDesiredState(context.Background(), orgId, "device-1", "app-1", domain.ApplicationDesiredStateRunning)
		assert.Equal(t, int32(http.StatusConflict), status.Code)
	})

	t.Run("When the device is conflict-paused it should return a conflict", func(t *testing.T) {
		h, _, orgId := newLifecycleTestHandler(t)
		device := newDeviceWithApp("device-1", "app-1")
		device.Metadata.Annotations = &map[string]string{
			domain.DeviceAnnotationConflictPaused: "true",
		}
		createLifecycleDevice(t, h, orgId, device)

		_, status := h.SetDeviceApplicationDesiredState(context.Background(), orgId, "device-1", "app-1", domain.ApplicationDesiredStateRunning)
		assert.Equal(t, int32(http.StatusConflict), status.Code)
	})

	t.Run("When the device is decommissioned it should return a conflict", func(t *testing.T) {
		h, _, orgId := newLifecycleTestHandler(t)
		device := newDeviceWithApp("device-1", "app-1")
		device.Spec.Decommissioning = &domain.DeviceDecommission{}
		createLifecycleDevice(t, h, orgId, device)

		_, status := h.SetDeviceApplicationDesiredState(context.Background(), orgId, "device-1", "app-1", domain.ApplicationDesiredStateRunning)
		assert.Equal(t, int32(http.StatusConflict), status.Code)
	})
}

// ─── pure helpers ────────────────────────────────────────────────────────────

func TestDecodeApplicationLifecycleMap(t *testing.T) {
	t.Run("When value is empty it should return an empty map without error", func(t *testing.T) {
		m, err := decodeApplicationLifecycleMap("")
		require.NoError(t, err)
		assert.Empty(t, m)
	})

	t.Run("When value is valid JSON it should decode it", func(t *testing.T) {
		m, err := decodeApplicationLifecycleMap(`{"app-1":{"desiredState":"running"}}`)
		require.NoError(t, err)
		require.Contains(t, m, "app-1")
		require.NotNil(t, m["app-1"].DesiredState)
		assert.Equal(t, domain.ApplicationDesiredStateRunning, *m["app-1"].DesiredState)
	})

	t.Run("When value is invalid JSON it should return an error", func(t *testing.T) {
		_, err := decodeApplicationLifecycleMap(`not-json`)
		require.Error(t, err)
	})
}

func TestDeviceHasApplication(t *testing.T) {
	t.Run("When spec is nil it should return false", func(t *testing.T) {
		assert.False(t, deviceHasApplication(&domain.Device{}, "app-1"))
	})

	t.Run("When applications is nil it should return false", func(t *testing.T) {
		assert.False(t, deviceHasApplication(&domain.Device{Spec: &domain.DeviceSpec{}}, "app-1"))
	})

	t.Run("When the application is present it should return true", func(t *testing.T) {
		device := newDeviceWithApp("device-1", "app-1")
		assert.True(t, deviceHasApplication(device, "app-1"))
	})

	t.Run("When the application is absent it should return false", func(t *testing.T) {
		device := newDeviceWithApp("device-1", "app-1")
		assert.False(t, deviceHasApplication(device, "app-2"))
	})
}
