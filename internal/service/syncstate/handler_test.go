package syncstate

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// fakeSyncStateStore is a small in-memory implementation of internal/store/syncstate.Store.
type fakeSyncStateStore struct {
	states                  map[string]*model.SyncState
	lastCheckedResourceKeys []string
	err                     error
}

func newFakeSyncStateStore() *fakeSyncStateStore {
	return &fakeSyncStateStore{states: map[string]*model.SyncState{}}
}

func (f *fakeSyncStateStore) InitialMigration(ctx context.Context) error { return f.err }

func (f *fakeSyncStateStore) Get(ctx context.Context, orgID uuid.UUID, resourceKey string) (*model.SyncState, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.states[resourceKey], nil
}

func (f *fakeSyncStateStore) Set(ctx context.Context, orgID uuid.UUID, state *model.SyncState) error {
	if f.err != nil {
		return f.err
	}
	f.states[state.ResourceKey] = state
	return nil
}

func (f *fakeSyncStateStore) SetLastCheckedAt(ctx context.Context, orgID uuid.UUID, resourceKey string, t time.Time) error {
	return f.err
}

func (f *fakeSyncStateStore) BulkUpsert(ctx context.Context, orgID uuid.UUID, states []model.SyncState) error {
	if f.err != nil {
		return f.err
	}
	for _, s := range states {
		state := s
		f.states[s.ResourceKey] = &state
	}
	return nil
}

func (f *fakeSyncStateStore) BulkUpdateLastCheckedAt(ctx context.Context, orgID uuid.UUID, resourceKeys []string, t time.Time) error {
	f.lastCheckedResourceKeys = resourceKeys
	return f.err
}

func TestGetSyncState(t *testing.T) {
	t.Run("When the store succeeds it should return the state with StatusOK", func(t *testing.T) {
		fakeStore := newFakeSyncStateStore()
		fakeStore.states["r1"] = &model.SyncState{ResourceKey: "r1"}
		h := NewServiceHandler(fakeStore)

		state, status := h.GetSyncState(context.Background(), uuid.New(), "r1")
		require.Equal(t, domain.StatusOK(), status)
		require.Equal(t, "r1", state.ResourceKey)
	})

	t.Run("When the store fails it should return an internal-error status", func(t *testing.T) {
		fakeStore := newFakeSyncStateStore()
		fakeStore.err = errors.New("db down")
		h := NewServiceHandler(fakeStore)

		_, status := h.GetSyncState(context.Background(), uuid.New(), "r1")
		require.Equal(t, int32(500), status.Code)
	})
}

func TestSetSyncState(t *testing.T) {
	t.Run("When the store succeeds it should return StatusOK", func(t *testing.T) {
		fakeStore := newFakeSyncStateStore()
		h := NewServiceHandler(fakeStore)

		status := h.SetSyncState(context.Background(), uuid.New(), &model.SyncState{ResourceKey: "r1"})
		require.Equal(t, domain.StatusOK(), status)
		require.Contains(t, fakeStore.states, "r1")
	})

	t.Run("When the store fails it should return an internal-error status", func(t *testing.T) {
		fakeStore := newFakeSyncStateStore()
		fakeStore.err = errors.New("db down")
		h := NewServiceHandler(fakeStore)

		status := h.SetSyncState(context.Background(), uuid.New(), &model.SyncState{ResourceKey: "r1"})
		require.Equal(t, int32(500), status.Code)
	})
}

func TestSetSyncStateLastCheckedAt(t *testing.T) {
	t.Run("When the store succeeds it should return StatusOK", func(t *testing.T) {
		fakeStore := newFakeSyncStateStore()
		h := NewServiceHandler(fakeStore)

		status := h.SetSyncStateLastCheckedAt(context.Background(), uuid.New(), "r1", time.Now())
		require.Equal(t, domain.StatusOK(), status)
	})

	t.Run("When the store fails it should return an internal-error status", func(t *testing.T) {
		fakeStore := newFakeSyncStateStore()
		fakeStore.err = errors.New("db down")
		h := NewServiceHandler(fakeStore)

		status := h.SetSyncStateLastCheckedAt(context.Background(), uuid.New(), "r1", time.Now())
		require.Equal(t, int32(500), status.Code)
	})
}

func TestBulkUpsertSyncState(t *testing.T) {
	t.Run("When the store succeeds it should return StatusOK", func(t *testing.T) {
		fakeStore := newFakeSyncStateStore()
		h := NewServiceHandler(fakeStore)

		status := h.BulkUpsertSyncState(context.Background(), uuid.New(), []model.SyncState{{ResourceKey: "r1"}, {ResourceKey: "r2"}})
		require.Equal(t, domain.StatusOK(), status)
		require.Len(t, fakeStore.states, 2)
	})

	t.Run("When the store fails it should return an internal-error status", func(t *testing.T) {
		fakeStore := newFakeSyncStateStore()
		fakeStore.err = errors.New("db down")
		h := NewServiceHandler(fakeStore)

		status := h.BulkUpsertSyncState(context.Background(), uuid.New(), []model.SyncState{{ResourceKey: "r1"}})
		require.Equal(t, int32(500), status.Code)
	})
}

func TestBulkUpdateSyncStateLastCheckedAt(t *testing.T) {
	t.Run("When the store succeeds it should return StatusOK", func(t *testing.T) {
		fakeStore := newFakeSyncStateStore()
		h := NewServiceHandler(fakeStore)

		status := h.BulkUpdateSyncStateLastCheckedAt(context.Background(), uuid.New(), []string{"r1", "r2"}, time.Now())
		require.Equal(t, domain.StatusOK(), status)
		require.Equal(t, []string{"r1", "r2"}, fakeStore.lastCheckedResourceKeys)
	})

	t.Run("When the store fails it should return an internal-error status", func(t *testing.T) {
		fakeStore := newFakeSyncStateStore()
		fakeStore.err = errors.New("db down")
		h := NewServiceHandler(fakeStore)

		status := h.BulkUpdateSyncStateLastCheckedAt(context.Background(), uuid.New(), []string{"r1"}, time.Now())
		require.Equal(t, int32(500), status.Code)
	})
}
