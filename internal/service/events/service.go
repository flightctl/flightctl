package events

import (
	"context"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/google/uuid"
)

// Service is the focused events-emission interface, extracted from the monolithic
// EventHandler (internal/service/event_handler.go). It exposes exactly the 15 EXPORTED
// methods EventHandler has today — NOT all 22 (7 are private per-resource emission helpers
// called only internally by these 15; they are copied as unexported methods on the concrete
// handler in handler.go, not part of this interface).
type Service interface {
	CreateEvent(ctx context.Context, orgId uuid.UUID, event *domain.Event)
	HandleGenericResourceDeletedEvents(ctx context.Context, resourceKind domain.ResourceKind, orgId uuid.UUID, name string, oldResource, newResource interface{}, created bool, err error)
	HandleDeviceUpdatedEvents(ctx context.Context, resourceKind domain.ResourceKind, orgId uuid.UUID, name string, oldResource, newResource interface{}, created bool, err error)
	HandleDeviceDecommissionEvents(ctx context.Context, resourceKind domain.ResourceKind, orgId uuid.UUID, name string, oldResource, newResource interface{}, created bool, err error)
	EmitFleetRolloutStartedEvent(ctx context.Context, orgId uuid.UUID, templateVersionName string, fleetName string, immediateRollout bool)
	HandleFleetUpdatedEvents(ctx context.Context, resourceKind domain.ResourceKind, orgId uuid.UUID, name string, oldResource, newResource interface{}, created bool, err error)
	HandleRepositoryUpdatedEvents(ctx context.Context, resourceKind domain.ResourceKind, orgId uuid.UUID, name string, oldResource, newResource interface{}, created bool, err error)
	HandleAuthProviderUpdatedEvents(ctx context.Context, resourceKind domain.ResourceKind, orgId uuid.UUID, name string, oldResource, newResource interface{}, created bool, err error)
	HandleAuthProviderDeletedEvents(ctx context.Context, resourceKind domain.ResourceKind, orgId uuid.UUID, name string, oldResource, newResource interface{}, created bool, err error)
	HandleEnrollmentRequestUpdatedEvents(ctx context.Context, resourceKind domain.ResourceKind, orgId uuid.UUID, name string, oldResource, newResource interface{}, created bool, err error)
	HandleEnrollmentRequestApprovedEvents(ctx context.Context, resourceKind domain.ResourceKind, orgId uuid.UUID, name string, oldResource, newResource interface{}, created bool, err error)
	HandleResourceSyncUpdatedEvents(ctx context.Context, resourceKind domain.ResourceKind, orgId uuid.UUID, name string, oldResource, newResource interface{}, created bool, err error)
	HandleCertificateSigningRequestUpdatedEvents(ctx context.Context, resourceKind domain.ResourceKind, orgId uuid.UUID, name string, oldResource, newResource interface{}, created bool, err error)
	HandleTemplateVersionUpdatedEvents(ctx context.Context, resourceKind domain.ResourceKind, orgId uuid.UUID, name string, oldResource, newResource interface{}, created bool, err error)
	HandleCatalogUpdatedEvents(ctx context.Context, resourceKind domain.ResourceKind, orgId uuid.UUID, name string, oldResource, newResource interface{}, created bool, err error)
}
