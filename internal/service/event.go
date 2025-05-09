package service

import (
	"context"
	"fmt"
	"time"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/selector"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-openapi/swag"
	"github.com/google/uuid"
	"github.com/samber/lo"
)

func (h *ServiceHandler) CreateEvent(ctx context.Context, event *api.Event) {
	if event == nil {
		return
	}

	orgId := store.NullOrgId

	err := h.store.Event().Create(ctx, orgId, event)
	if err != nil {
		h.log.Errorf("failed emitting resource updated %s event for %s %s/%s: %v", event.Reason, event.InvolvedObject.Kind, orgId, event.InvolvedObject.Name, err)
	}
}

func (h *ServiceHandler) ListEvents(ctx context.Context, params api.ListEventsParams) (*api.EventList, api.Status) {
	orgId := store.NullOrgId

	cont, err := store.ParseContinueString(params.Continue)
	if err != nil {
		return nil, api.StatusBadRequest(fmt.Sprintf("failed to parse continue parameter: %v", err))
	}

	var fieldSelector *selector.FieldSelector
	if params.FieldSelector != nil {
		if fieldSelector, err = selector.NewFieldSelector(*params.FieldSelector); err != nil {
			return nil, api.StatusBadRequest(fmt.Sprintf("failed to parse field selector: %v", err))
		}
	}

	listParams := store.ListParams{
		Limit:         int(swag.Int32Value(params.Limit)),
		Continue:      cont,
		FieldSelector: fieldSelector,
	}
	if listParams.Limit == 0 {
		listParams.Limit = MaxRecordsPerListRequest
	} else if listParams.Limit > MaxRecordsPerListRequest {
		return nil, api.StatusBadRequest(fmt.Sprintf("limit cannot exceed %d", MaxRecordsPerListRequest))
	} else if listParams.Limit < 0 {
		return nil, api.StatusBadRequest("limit cannot be negative")
	}

	result, err := h.store.Event().List(ctx, orgId, listParams)
	if err == nil {
		return result, api.StatusOK()
	}

	var se *selector.SelectorError

	switch {
	case selector.AsSelectorError(err, &se):
		return nil, api.StatusBadRequest(se.Error())
	default:
		return nil, api.StatusInternalServerError(err.Error())
	}
}

func (h *ServiceHandler) DeleteEventsOlderThan(ctx context.Context, cutoffTime time.Time) (int64, api.Status) {
	numDeleted, err := h.store.Event().DeleteOlderThan(ctx, cutoffTime)
	return numDeleted, StoreErrorToApiStatus(err, false, api.EventKind, nil)
}

type eventOutcome struct {
	Reason  api.EventReason
	Message string
}

func getBaseEvent(ctx context.Context, status api.Status, prefix string, resourceKind api.ResourceKind, resourceName string, success, failure eventOutcome) *api.Event {
	var operationSucceeded bool
	if status.Code >= 200 && status.Code < 299 {
		operationSucceeded = true
	} else if status.Code >= 500 && status.Code < 599 {
		operationSucceeded = false
	} else {
		// If it's not one of the above cases, it's 4XX, which we don't emit events for
		return nil
	}

	reqID := ctx.Value(middleware.RequestIDKey)

	// If the requestID is nil or not set, fallback to a UUID
	var requestIDstr string
	if reqID == nil {
		requestIDstr = uuid.New().String()
	} else {
		requestIDstr = reqID.(string)
	}

	actor := ctx.Value(consts.EventActorCtxKey)
	var actorStr string
	if actor != nil {
		actorStr = actor.(string)
	}

	component := ctx.Value(consts.EventSourceComponentCtxKey)
	var componentStr string
	if component != nil {
		componentStr = component.(string)
	}

	event := api.Event{
		Metadata: api.ObjectMeta{
			Name: lo.ToPtr(fmt.Sprintf("%s-%s-%s-%s", prefix, resourceKind, resourceName, requestIDstr)),
		},
		Type: api.Normal,
		InvolvedObject: api.ObjectReference{
			Kind: string(resourceKind),
			Name: resourceName,
		},
		Source: api.EventSource{
			Component: componentStr,
		},
		Actor: actorStr,
	}

	if operationSucceeded {
		event.Reason = success.Reason
		event.Message = success.Message
	} else {
		event.Reason = failure.Reason
		event.Message = failure.Message
		event.Type = api.Warning
	}

	return &event
}

func GetResourceCreatedOrUpdatedEvent(ctx context.Context, created bool, resourceKind api.ResourceKind, resourceName string, status api.Status, updateDesc *api.ResourceUpdatedDetails) *api.Event {
	var event *api.Event
	if created {
		event = getBaseEvent(ctx, status, "resource-create", resourceKind, resourceName, eventOutcome{
			Reason:  api.ResourceCreated,
			Message: fmt.Sprintf("%s %s created successfully", resourceKind, resourceName),
		}, eventOutcome{
			Reason:  api.ResourceCreationFailed,
			Message: fmt.Sprintf("%s %s creation failed: %s", resourceKind, resourceName, status.Message),
		})
	} else {
		event = getBaseEvent(ctx, status, "resource-update", resourceKind, resourceName, eventOutcome{
			Reason:  api.ResourceUpdated,
			Message: fmt.Sprintf("%s %s updated successfully", resourceKind, resourceName),
		}, eventOutcome{
			Reason:  api.ResourceUpdateFailed,
			Message: fmt.Sprintf("%s %s update failed: %s", resourceKind, resourceName, status.Message),
		})

		if event == nil {
			return nil
		}

		if updateDesc != nil {
			details := api.EventDetails{}
			err := details.FromResourceUpdatedDetails(*updateDesc)
			if err == nil {
				event.Details = &details
			}
		}
	}

	return event
}

func GetResourceDeletedEvent(ctx context.Context, resourceKind api.ResourceKind, resourceName string, status api.Status) *api.Event {
	event := getBaseEvent(ctx, status, "resource-delete", resourceKind, resourceName, eventOutcome{
		Reason:  api.ResourceDeleted,
		Message: fmt.Sprintf("%s %s deleted successfully", resourceKind, resourceName),
	}, eventOutcome{
		Reason:  api.ResourceDeletionFailed,
		Message: fmt.Sprintf("%s %s deletion failed: %s", resourceKind, resourceName, status.Message),
	})

	return event
}
