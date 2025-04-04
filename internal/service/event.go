package service

import (
	"context"
	"fmt"
	"strconv"
	"time"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/go-openapi/swag"
	"github.com/samber/lo"
)

func (h *ServiceHandler) CreateEvent(ctx context.Context, event api.Event) {
	orgId := store.NullOrgId

	err := h.store.Event().Create(ctx, orgId, &event)
	if err != nil {
		h.log.Errorf("failed emitting resource updated event for %s %s/%s: %v", event.ResourceKind, orgId, event.ResourceName, err)
	}
}

func (h *ServiceHandler) ListEvents(ctx context.Context, params api.ListEventsParams) (*api.EventList, api.Status) {
	orgId := store.NullOrgId

	listParams := store.ListEventsParams{
		Name:          params.Name,
		CorrelationId: params.CorrelationId,
		StartTime:     params.StartTime,
		EndTime:       params.EndTime,
		Limit:         int(swag.Int32Value(params.Limit)),
	}

	if listParams.Limit == 0 {
		listParams.Limit = MaxRecordsPerListRequest
	} else if listParams.Limit > MaxRecordsPerListRequest {
		return nil, api.StatusBadRequest(fmt.Sprintf("limit cannot exceed %d", MaxRecordsPerListRequest))
	} else if listParams.Limit < 0 {
		return nil, api.StatusBadRequest("limit cannot be negative")
	}

	if params.Kind != nil {
		listParams.Kind = lo.ToPtr(string(*params.Kind))
	}

	if params.Severity != nil {
		listParams.Severity = lo.ToPtr(string(*params.Severity))
	}

	if params.Continue != nil {
		continueInt, err := strconv.ParseUint(*params.Continue, 10, 64)
		if err != nil {
			return nil, api.StatusBadRequest(fmt.Sprintf("failed to parse continue parameter: %v", err))
		}
		listParams.Continue = &continueInt
	}

	result, err := h.store.Event().List(ctx, orgId, listParams)
	return result, StoreErrorToApiStatus(err, false, api.EventKind, nil)
}

func (h *ServiceHandler) DeleteEventsOlderThan(ctx context.Context, cutoffTime time.Time) (int64, api.Status) {
	numDeleted, err := h.store.Event().DeleteOlderThan(ctx, cutoffTime)
	return numDeleted, StoreErrorToApiStatus(err, false, api.EventKind, nil)
}
