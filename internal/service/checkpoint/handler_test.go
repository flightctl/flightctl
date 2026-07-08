package checkpoint

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/stretchr/testify/require"
)

// fakeCheckpointStore is a small in-memory implementation of internal/store/checkpoint.Store.
type fakeCheckpointStore struct {
	values map[string][]byte
	dbTime time.Time
	err    error
	dbErr  error
	getErr error
	setErr error
}

func newFakeCheckpointStore() *fakeCheckpointStore {
	return &fakeCheckpointStore{values: map[string][]byte{}}
}

func key(consumer, k string) string { return consumer + "/" + k }

func (f *fakeCheckpointStore) InitialMigration(ctx context.Context) error { return f.err }

func (f *fakeCheckpointStore) Set(ctx context.Context, consumer string, k string, value []byte) error {
	if f.setErr != nil {
		return f.setErr
	}
	f.values[key(consumer, k)] = value
	return nil
}

func (f *fakeCheckpointStore) Get(ctx context.Context, consumer string, k string) ([]byte, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	v, ok := f.values[key(consumer, k)]
	if !ok {
		return nil, flterrors.ErrResourceNotFound
	}
	return v, nil
}

func (f *fakeCheckpointStore) GetDatabaseTime(ctx context.Context) (time.Time, error) {
	if f.dbErr != nil {
		return time.Time{}, f.dbErr
	}
	return f.dbTime, nil
}

func TestGetCheckpoint(t *testing.T) {
	t.Run("When the key exists it should return its value with StatusOK", func(t *testing.T) {
		fakeStore := newFakeCheckpointStore()
		h := NewServiceHandler(fakeStore)
		require.NoError(t, fakeStore.Set(context.Background(), "consumer1", "key1", []byte("value1")))

		value, status := h.GetCheckpoint(context.Background(), "consumer1", "key1")
		require.Equal(t, int32(200), status.Code)
		require.Equal(t, []byte("value1"), value)
	})

	t.Run("When the key does not exist it should return a not-found status", func(t *testing.T) {
		fakeStore := newFakeCheckpointStore()
		h := NewServiceHandler(fakeStore)

		_, status := h.GetCheckpoint(context.Background(), "consumer1", "missing")
		require.Equal(t, int32(404), status.Code)
	})

	t.Run("When the store returns an unmapped error it should return an internal-error status", func(t *testing.T) {
		fakeStore := newFakeCheckpointStore()
		fakeStore.getErr = errors.New("db down")
		h := NewServiceHandler(fakeStore)

		_, status := h.GetCheckpoint(context.Background(), "consumer1", "key1")
		require.Equal(t, int32(500), status.Code)
	})
}

func TestSetCheckpoint(t *testing.T) {
	t.Run("When the store succeeds it should return StatusOK", func(t *testing.T) {
		fakeStore := newFakeCheckpointStore()
		h := NewServiceHandler(fakeStore)

		status := h.SetCheckpoint(context.Background(), "consumer1", "key1", []byte("value1"))
		require.Equal(t, int32(200), status.Code)
		require.Equal(t, []byte("value1"), fakeStore.values[key("consumer1", "key1")])
	})

	t.Run("When the store fails it should return an internal-error status", func(t *testing.T) {
		fakeStore := newFakeCheckpointStore()
		fakeStore.setErr = errors.New("db down")
		h := NewServiceHandler(fakeStore)

		status := h.SetCheckpoint(context.Background(), "consumer1", "key1", []byte("value1"))
		require.Equal(t, int32(500), status.Code)
	})
}

func TestGetDatabaseTime(t *testing.T) {
	t.Run("When the store succeeds it should return the time with StatusOK", func(t *testing.T) {
		fakeStore := newFakeCheckpointStore()
		fakeStore.dbTime = time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
		h := NewServiceHandler(fakeStore)

		dbTime, status := h.GetDatabaseTime(context.Background())
		require.Equal(t, int32(200), status.Code)
		require.Equal(t, fakeStore.dbTime, dbTime)
	})

	t.Run("When the store fails it should return an internal-error status", func(t *testing.T) {
		fakeStore := newFakeCheckpointStore()
		fakeStore.dbErr = errors.New("db down")
		h := NewServiceHandler(fakeStore)

		_, status := h.GetDatabaseTime(context.Background())
		require.Equal(t, int32(500), status.Code)
	})
}
