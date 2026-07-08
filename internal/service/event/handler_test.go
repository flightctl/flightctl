package event

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/service/events"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// fakeEventStore is a small in-memory implementation of internal/store/event.Store.
type fakeEventStore struct {
	events        []domain.Event
	listErr       error
	deleteErr     error
	deletedCount  int64
	lastListOrgID uuid.UUID
	lastListParam store.ListParams
}

func (f *fakeEventStore) InitialMigration(ctx context.Context) error { return nil }

func (f *fakeEventStore) Create(ctx context.Context, orgId uuid.UUID, event *domain.Event) error {
	f.events = append(f.events, *event)
	return nil
}

func (f *fakeEventStore) List(ctx context.Context, orgId uuid.UUID, listParams store.ListParams) (*domain.EventList, error) {
	f.lastListOrgID = orgId
	f.lastListParam = listParams
	if f.listErr != nil {
		return nil, f.listErr
	}
	return &domain.EventList{Items: f.events}, nil
}

func (f *fakeEventStore) DeleteOlderThan(ctx context.Context, cutoffTime time.Time) (int64, error) {
	if f.deleteErr != nil {
		return 0, f.deleteErr
	}
	return f.deletedCount, nil
}

// fakeEventsService is a recording fake for events.Service. Only CreateEvent is exercised by
// this package's ServiceHandler; all other methods panic if called (they should never be, per
// event.go's original CreateEvent-only forwarding).
type fakeEventsService struct {
	events.Service
	created []recordedCreate
}

type recordedCreate struct {
	orgId uuid.UUID
	event *domain.Event
}

func (f *fakeEventsService) CreateEvent(ctx context.Context, orgId uuid.UUID, event *domain.Event) {
	f.created = append(f.created, recordedCreate{orgId: orgId, event: event})
}

func TestCreateEvent(t *testing.T) {
	fakeStore := &fakeEventStore{}
	fakeEvents := &fakeEventsService{}
	h := NewServiceHandler(fakeStore, fakeEvents)

	orgId := uuid.New()
	event := domain.GetBaseEvent(context.Background(), domain.DeviceKind, "dev1", domain.EventReasonResourceCreated, "created", nil)
	h.CreateEvent(context.Background(), orgId, event)

	require.Len(t, fakeEvents.created, 1)
	require.Equal(t, orgId, fakeEvents.created[0].orgId)
	require.Same(t, event, fakeEvents.created[0].event)
	// CreateEvent must forward to events.Service, not touch the store directly.
	require.Empty(t, fakeStore.events)
}

func TestListEvents(t *testing.T) {
	t.Run("When no order is specified it should default to created_at desc", func(t *testing.T) {
		fakeStore := &fakeEventStore{}
		h := NewServiceHandler(fakeStore, &fakeEventsService{})
		orgId := uuid.New()

		_, status := h.ListEvents(context.Background(), orgId, domain.ListEventsParams{})

		require.Equal(t, domain.StatusOK(), status)
		require.Equal(t, orgId, fakeStore.lastListOrgID)
		require.NotNil(t, fakeStore.lastListParam.SortOrder)
		require.Equal(t, store.SortDesc, *fakeStore.lastListParam.SortOrder)
	})

	t.Run("When Order=Asc is specified it should sort ascending", func(t *testing.T) {
		fakeStore := &fakeEventStore{}
		h := NewServiceHandler(fakeStore, &fakeEventsService{})
		order := domain.Asc

		_, status := h.ListEvents(context.Background(), uuid.New(), domain.ListEventsParams{Order: &order})

		require.Equal(t, domain.StatusOK(), status)
		require.Equal(t, store.SortAsc, *fakeStore.lastListParam.SortOrder)
	})

	t.Run("When the field selector is invalid it should return a bad-request status", func(t *testing.T) {
		fakeStore := &fakeEventStore{}
		h := NewServiceHandler(fakeStore, &fakeEventsService{})
		badSelector := "%%%invalid%%%"

		_, status := h.ListEvents(context.Background(), uuid.New(), domain.ListEventsParams{FieldSelector: &badSelector})

		require.Equal(t, int32(400), status.Code)
	})

	t.Run("When the store returns an unmapped error it should return an internal-error status", func(t *testing.T) {
		fakeStore := &fakeEventStore{listErr: errors.New("db down")}
		h := NewServiceHandler(fakeStore, &fakeEventsService{})

		result, status := h.ListEvents(context.Background(), uuid.New(), domain.ListEventsParams{})

		require.Nil(t, result)
		require.Equal(t, int32(500), status.Code)
	})
}

func TestDeleteEventsOlderThan(t *testing.T) {
	t.Run("When the store succeeds it should return the deleted count with StatusOK", func(t *testing.T) {
		fakeStore := &fakeEventStore{deletedCount: 5}
		h := NewServiceHandler(fakeStore, &fakeEventsService{})

		count, status := h.DeleteEventsOlderThan(context.Background(), time.Now())

		require.Equal(t, domain.StatusOK(), status)
		require.Equal(t, int64(5), count)
	})

	t.Run("When the store fails it should return an internal-error status", func(t *testing.T) {
		fakeStore := &fakeEventStore{deleteErr: errors.New("db down")}
		h := NewServiceHandler(fakeStore, &fakeEventsService{})

		_, status := h.DeleteEventsOlderThan(context.Background(), time.Now())

		require.Equal(t, int32(500), status.Code)
	})
}
