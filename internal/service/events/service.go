package events

import (
	"context"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/google/uuid"
)

// Service is the generic events-emission core. It intentionally holds only the logic that
// makes no per-resource decisions: writing an event record, and emitting the generic
// "resource deleted" event (which needs nothing resource-specific beyond a kind and name).
// Every resource-specific event decision (what changed, which condition flipped, which
// message applies) lives in that resource's own internal/service/{resource} package, which
// calls back into this interface only for these generic parts. This keeps Service from
// becoming a hub every resource package must depend on for domain-specific logic.
type Service interface {
	CreateEvent(ctx context.Context, orgId uuid.UUID, event *domain.Event)
	HandleGenericResourceDeletedEvents(ctx context.Context, resourceKind domain.ResourceKind, orgId uuid.UUID, name string, oldResource, newResource interface{}, created bool, err error)
}
