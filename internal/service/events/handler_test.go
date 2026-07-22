package events

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

// fakeEventStore is a small in-memory implementation of internal/store/event.Store.
type fakeEventStore struct {
	events    []domain.Event
	createErr error
}

func (f *fakeEventStore) InitialMigration(ctx context.Context) error { return nil }

func (f *fakeEventStore) Create(ctx context.Context, orgId uuid.UUID, event *domain.Event) error {
	if f.createErr != nil {
		return f.createErr
	}
	f.events = append(f.events, *event)
	return nil
}

func (f *fakeEventStore) List(ctx context.Context, orgId uuid.UUID, listParams store.ListParams) (*domain.EventList, error) {
	return &domain.EventList{Items: f.events}, nil
}

func (f *fakeEventStore) DeleteOlderThan(ctx context.Context, cutoffTime time.Time) (int64, error) {
	return 0, nil
}

// fakeWorkerClient records every EmitEvent call for assertions.
type fakeWorkerClient struct {
	emitted []*domain.Event
}

func (f *fakeWorkerClient) EmitEvent(ctx context.Context, orgId uuid.UUID, event *domain.Event) {
	f.emitted = append(f.emitted, event)
}

func newTestHandler() (*ServiceHandler, *fakeEventStore, *fakeWorkerClient) {
	fakeStore := &fakeEventStore{}
	fakeWorker := &fakeWorkerClient{}
	h := NewServiceHandler(fakeStore, fakeWorker, logrus.New())
	return h, fakeStore, fakeWorker
}

func compareEventReasons(t *testing.T, expected []domain.EventReason, events []domain.Event) {
	t.Helper()
	require.Len(t, events, len(expected))
	for i, event := range events {
		require.Equal(t, expected[i], event.Reason)
	}
}

func TestCreateEvent(t *testing.T) {
	t.Run("When event is nil it should no-op", func(t *testing.T) {
		h, fakeStore, fakeWorker := newTestHandler()
		h.CreateEvent(context.Background(), uuid.New(), nil)
		require.Empty(t, fakeStore.events)
		require.Empty(t, fakeWorker.emitted)
	})

	t.Run("When the store succeeds it should persist the event and notify the worker client", func(t *testing.T) {
		h, fakeStore, fakeWorker := newTestHandler()
		orgId := uuid.New()
		event := domain.GetBaseEvent(context.Background(), domain.DeviceKind, "dev1", domain.EventReasonResourceCreated, "created", nil)
		h.CreateEvent(context.Background(), orgId, event)
		require.Len(t, fakeStore.events, 1)
		require.Len(t, fakeWorker.emitted, 1)
	})

	t.Run("When the store fails it should not notify the worker client", func(t *testing.T) {
		h, fakeStore, fakeWorker := newTestHandler()
		fakeStore.createErr = errors.New("db down")
		orgId := uuid.New()
		event := domain.GetBaseEvent(context.Background(), domain.DeviceKind, "dev1", domain.EventReasonResourceCreated, "created", nil)
		h.CreateEvent(context.Background(), orgId, event)
		require.Empty(t, fakeStore.events)
		require.Empty(t, fakeWorker.emitted)
	})

	t.Run("When the workerClient is nil it should still persist the event", func(t *testing.T) {
		fakeStore := &fakeEventStore{}
		h := NewServiceHandler(fakeStore, nil, logrus.New())
		orgId := uuid.New()
		event := domain.GetBaseEvent(context.Background(), domain.DeviceKind, "dev1", domain.EventReasonResourceCreated, "created", nil)
		h.CreateEvent(context.Background(), orgId, event)
		require.Len(t, fakeStore.events, 1)
	})
}

func TestHandleGenericResourceDeletedEvents(t *testing.T) {
	t.Run("When err is nil it should emit a deletion-success event", func(t *testing.T) {
		h, fakeStore, _ := newTestHandler()
		h.HandleGenericResourceDeletedEvents(context.Background(), domain.FleetKind, uuid.New(), "f1", nil, nil, false, nil)
		compareEventReasons(t, []domain.EventReason{domain.EventReasonResourceDeleted}, fakeStore.events)
	})

	t.Run("When err is non-nil it should emit a deletion-failure event", func(t *testing.T) {
		h, fakeStore, _ := newTestHandler()
		h.HandleGenericResourceDeletedEvents(context.Background(), domain.FleetKind, uuid.New(), "f1", nil, nil, false, errors.New("boom"))
		compareEventReasons(t, []domain.EventReason{domain.EventReasonResourceDeletionFailed}, fakeStore.events)
	})
}
