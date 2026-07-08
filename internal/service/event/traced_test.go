package event

import (
	"context"
	"testing"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestWrapWithTracing(t *testing.T) {
	t.Run("When inner is nil it should return nil", func(t *testing.T) {
		require.Nil(t, WrapWithTracing(nil))
	})

	t.Run("When inner is non-nil it should delegate calls and return the result unchanged", func(t *testing.T) {
		fakeStore := &fakeEventStore{}
		fakeEvents := &fakeEventsService{}
		handler := NewServiceHandler(fakeStore, fakeEvents)
		traced := WrapWithTracing(handler)
		require.NotNil(t, traced)

		orgId := uuid.New()
		event := domain.GetBaseEvent(context.Background(), domain.DeviceKind, "dev1", domain.EventReasonResourceCreated, "created", nil)
		traced.CreateEvent(context.Background(), orgId, event)
		require.Len(t, fakeEvents.created, 1)

		result, status := traced.ListEvents(context.Background(), orgId, domain.ListEventsParams{})
		expected, expectedStatus := handler.ListEvents(context.Background(), orgId, domain.ListEventsParams{})
		require.Equal(t, expectedStatus, status)
		require.Equal(t, expected, result)
	})
}
