package event

import (
	"context"
	"time"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/service/common"
	"github.com/flightctl/flightctl/internal/service/events"
	"github.com/flightctl/flightctl/internal/store"
	eventstore "github.com/flightctl/flightctl/internal/store/event"
	"github.com/flightctl/flightctl/internal/store/selector"
	"github.com/google/uuid"
	"github.com/samber/lo"
)

// ServiceHandler implements Service. Needs both the isolated event store (for
// ListEvents/DeleteEventsOlderThan) and events.Service (for CreateEvent, which is a pure
// one-line forward to events.Service.CreateEvent in the original code) — no `log`, since
// internal/service/event.go never references it directly (it lives behind events.Service).
type ServiceHandler struct {
	store  eventstore.Store
	events events.Service
}

// NewServiceHandler creates a new event ServiceHandler instance.
func NewServiceHandler(store eventstore.Store, events events.Service) *ServiceHandler {
	return &ServiceHandler{store: store, events: events}
}

var _ Service = (*ServiceHandler)(nil)

func (h *ServiceHandler) CreateEvent(ctx context.Context, orgId uuid.UUID, event *domain.Event) {
	h.events.CreateEvent(ctx, orgId, event)
}

func (h *ServiceHandler) ListEvents(ctx context.Context, orgId uuid.UUID, params domain.ListEventsParams) (*domain.EventList, domain.Status) {
	listParams, status := common.PrepareListParams(params.Continue, nil, params.FieldSelector, params.Limit)
	if status != domain.StatusOK() {
		return nil, status
	}

	// default is to sort created_at with desc
	listParams.SortColumns = []store.SortColumn{store.SortByCreatedAt, store.SortByName}
	listParams.SortOrder = lo.ToPtr(store.SortDesc)
	if params.Order != nil {
		listParams.SortOrder = lo.ToPtr(map[domain.ListEventsParamsOrder]store.SortOrder{domain.Asc: store.SortAsc, domain.Desc: store.SortDesc}[*params.Order])
	}

	result, err := h.store.List(ctx, orgId, *listParams)
	if err == nil {
		return result, domain.StatusOK()
	}

	var se *selector.SelectorError

	switch {
	case selector.AsSelectorError(err, &se):
		return nil, domain.StatusBadRequest(se.Error())
	default:
		return nil, domain.StatusInternalServerError(err.Error())
	}
}

func (h *ServiceHandler) DeleteEventsOlderThan(ctx context.Context, cutoffTime time.Time) (int64, domain.Status) {
	numDeleted, err := h.store.DeleteOlderThan(ctx, cutoffTime)
	return numDeleted, common.StoreErrorToApiStatus(err, false, domain.EventKind, nil)
}
