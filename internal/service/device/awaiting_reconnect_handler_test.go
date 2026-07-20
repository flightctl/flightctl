package device

import (
	"context"
	"testing"
	"time"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/kvstore"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

type fakeKVStore struct {
	values      map[string][]byte
	deletedKeys []string
}

func (k *fakeKVStore) Get(ctx context.Context, key string) ([]byte, error) {
	if k.values == nil {
		return nil, nil
	}
	return k.values[key], nil
}

func (k *fakeKVStore) DeleteKeysForTemplateVersion(ctx context.Context, key string) error {
	k.deletedKeys = append(k.deletedKeys, key)
	delete(k.values, key)
	return nil
}

func (k *fakeKVStore) Close()                                              {}
func (k *fakeKVStore) SetNX(ctx context.Context, key string, value []byte) (bool, error) {
	return false, nil
}
func (k *fakeKVStore) SetIfGreater(ctx context.Context, key string, newVal int64) (bool, error) {
	return false, nil
}
func (k *fakeKVStore) GetOrSetNX(ctx context.Context, key string, value []byte) ([]byte, error) {
	return nil, nil
}
func (k *fakeKVStore) DeleteAllKeys(ctx context.Context) error { return nil }
func (k *fakeKVStore) PrintAllKeys(ctx context.Context)        {}
func (k *fakeKVStore) StreamAdd(ctx context.Context, key string, value []byte) (string, error) {
	return "", nil
}
func (k *fakeKVStore) StreamRange(ctx context.Context, key string, start, stop string) ([]kvstore.StreamEntry, error) {
	return nil, nil
}
func (k *fakeKVStore) StreamRead(ctx context.Context, key string, lastID string, block time.Duration, count int64) ([]kvstore.StreamEntry, error) {
	return nil, nil
}
func (k *fakeKVStore) SetExpire(ctx context.Context, key string, expiration time.Duration) error {
	return nil
}
func (k *fakeKVStore) Delete(ctx context.Context, key string) error { return nil }

func TestProcessAwaitingReconnectIfNeeded(t *testing.T) {
	orgId := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	deviceName := "dev-1"
	key := (&kvstore.AwaitingReconnectionKey{OrgID: orgId, DeviceName: deviceName}).ComposeKey()

	t.Run("When the KV gate is not set it should skip processing", func(t *testing.T) {
		st := newFakeStore()
		kv := &fakeKVStore{values: map[string][]byte{}}
		ev := &fakeEvents{}
		h := NewDeviceServiceHandler(st.device, st.catalog, st.fleet, ev, kv, "agent.example.com", logrus.New()).(*DeviceServiceHandler)

		processed := h.processAwaitingReconnectIfNeeded(context.Background(), orgId, deviceName, lo.ToPtr("1"))
		require.False(t, processed)
		require.Empty(t, kv.deletedKeys)
		require.Empty(t, ev.created)
	})

	t.Run("When awaiting reconnect applies as online it should persist the outcome and clear the KV key", func(t *testing.T) {
		st := newFakeStore()
		st.device.devices[deviceName] = &domain.Device{
			Metadata: domain.ObjectMeta{
				Name: lo.ToPtr(deviceName),
				Annotations: lo.ToPtr(map[string]string{
					domain.DeviceAnnotationAwaitingReconnect: "true",
					domain.DeviceAnnotationRenderedVersion:   "5",
				}),
			},
		}
		kv := &fakeKVStore{values: map[string][]byte{key: []byte("true")}}
		ev := &fakeEvents{}
		h := NewDeviceServiceHandler(st.device, st.catalog, st.fleet, ev, kv, "agent.example.com", logrus.New()).(*DeviceServiceHandler)

		processed := h.processAwaitingReconnectIfNeeded(context.Background(), orgId, deviceName, lo.ToPtr("3"))
		require.True(t, processed)
		updated := st.device.devices[deviceName]
		_, hasAwaiting := (*updated.Metadata.Annotations)[domain.DeviceAnnotationAwaitingReconnect]
		require.False(t, hasAwaiting)
		require.Equal(t, domain.DeviceSummaryStatusOnline, updated.Status.Summary.Status)
		require.Equal(t, []string{key}, kv.deletedKeys)
		require.Empty(t, ev.created)
	})

	t.Run("When awaiting reconnect applies as conflict paused it should emit an event", func(t *testing.T) {
		st := newFakeStore()
		st.device.devices[deviceName] = &domain.Device{
			Metadata: domain.ObjectMeta{
				Name: lo.ToPtr(deviceName),
				Annotations: lo.ToPtr(map[string]string{
					domain.DeviceAnnotationAwaitingReconnect: "true",
					domain.DeviceAnnotationRenderedVersion:   "3",
				}),
			},
		}
		kv := &fakeKVStore{values: map[string][]byte{key: []byte("true")}}
		ev := &fakeEvents{}
		h := NewDeviceServiceHandler(st.device, st.catalog, st.fleet, ev, kv, "agent.example.com", logrus.New()).(*DeviceServiceHandler)

		processed := h.processAwaitingReconnectIfNeeded(context.Background(), orgId, deviceName, lo.ToPtr("5"))
		require.True(t, processed)
		updated := st.device.devices[deviceName]
		require.Equal(t, "true", (*updated.Metadata.Annotations)[domain.DeviceAnnotationConflictPaused])
		require.Equal(t, domain.DeviceSummaryStatusConflictPaused, updated.Status.Summary.Status)
		require.Len(t, ev.created, 1)
		require.Equal(t, domain.EventReasonDeviceConflictPaused, ev.created[0].Reason)
	})

	t.Run("When the device has no awaiting reconnect annotation it should clear KV without applying", func(t *testing.T) {
		st := newFakeStore()
		st.device.devices[deviceName] = &domain.Device{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr(deviceName)},
		}
		kv := &fakeKVStore{values: map[string][]byte{key: []byte("true")}}
		ev := &fakeEvents{}
		h := NewDeviceServiceHandler(st.device, st.catalog, st.fleet, ev, kv, "agent.example.com", logrus.New()).(*DeviceServiceHandler)

		processed := h.processAwaitingReconnectIfNeeded(context.Background(), orgId, deviceName, lo.ToPtr("1"))
		require.True(t, processed)
		require.Equal(t, []string{key}, kv.deletedKeys)
		require.Empty(t, ev.created)
	})
}
