package event

import (
	"context"
	"time"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/google/uuid"
)

type Service interface {
	CreateEvent(ctx context.Context, orgId uuid.UUID, event *domain.Event)
	ListEvents(ctx context.Context, orgId uuid.UUID, params domain.ListEventsParams) (*domain.EventList, domain.Status)
	DeleteEventsOlderThan(ctx context.Context, cutoffTime time.Time) (int64, domain.Status)
}
