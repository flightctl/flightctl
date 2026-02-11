package service

import (
	"context"
	"time"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/selector"
	"github.com/google/uuid"
	"github.com/samber/lo"
)

func (h *ServiceHandler) CreateEvent(ctx context.Context, orgId uuid.UUID, event *domain.Event) {
	h.eventHandler.CreateEvent(ctx, orgId, event)
}

func (h *ServiceHandler) ListEvents(ctx context.Context, orgId uuid.UUID, params domain.ListEventsParams) (*domain.EventList, domain.Status) {
	listParams, status := prepareListParams(params.Continue, nil, params.FieldSelector, params.Limit)
	if status != domain.StatusOK() {
		return nil, status
	}

	// default is to sort created_at with desc
	listParams.SortColumns = []store.SortColumn{store.SortByCreatedAt, store.SortByName}
	listParams.SortOrder = lo.ToPtr(store.SortDesc)
	if params.Order != nil {
		listParams.SortOrder = lo.ToPtr(map[domain.ListEventsParamsOrder]store.SortOrder{domain.Asc: store.SortAsc, domain.Desc: store.SortDesc}[*params.Order])
	}

	result, err := h.store.Event().List(ctx, orgId, *listParams)
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
	numDeleted, err := h.store.Event().DeleteOlderThan(ctx, cutoffTime)
	return numDeleted, StoreErrorToApiStatus(err, false, domain.EventKind, nil)
}
