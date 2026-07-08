package event

import (
	"context"
	"time"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/google/uuid"
)

// Service is the focused Event service interface, extracted from the monolithic
// internal/service.Service (internal/service/event.go).
type Service interface {
	CreateEvent(ctx context.Context, orgId uuid.UUID, event *domain.Event)
	ListEvents(ctx context.Context, orgId uuid.UUID, params domain.ListEventsParams) (*domain.EventList, domain.Status)
	DeleteEventsOlderThan(ctx context.Context, cutoffTime time.Time) (int64, domain.Status)
}
