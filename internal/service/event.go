package service

import (
	"bytes"
	"context"
	"fmt"
	"time"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/selector"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
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

	listParams, status := prepareListParams(params.Continue, nil, params.FieldSelector, params.Limit)
	if status != api.StatusOK() {
		return nil, status
	}

	// default is to sort created_at with desc
	listParams.SortColumns = []store.SortColumn{store.SortByCreatedAt, store.SortByName}
	listParams.SortOrder = lo.ToPtr(store.SortDesc)
	if params.Order != nil {
		listParams.SortOrder = lo.ToPtr(map[api.ListEventsParamsOrder]store.SortOrder{api.Asc: store.SortAsc, api.Desc: store.SortDesc}[*params.Order])
	}

	result, err := h.store.Event().List(ctx, orgId, *listParams)
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

type eventOutcomeFunc func() (api.EventReason, string)

const eventMessageCapacity = 128

func getBaseEvent(ctx context.Context, status api.Status, prefix string, resourceKind api.ResourceKind, resourceName string, success, failure eventOutcomeFunc) *api.Event {
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

	if operationSucceeded && success != nil {
		event.Reason, event.Message = success()
	} else if failure != nil {
		event.Reason, event.Message = failure()
		event.Type = api.Warning
	}

	return &event
}

func GetResourceCreatedOrUpdatedEvent(ctx context.Context, created bool, resourceKind api.ResourceKind, resourceName string, status api.Status, updateDesc *api.ResourceUpdatedDetails, log logrus.FieldLogger) *api.Event {
	if created {
		return getResourceEvent(ctx,
			ResourceEvent{
				ResourceKind:  resourceKind,
				ResourceName:  resourceName,
				Prefix:        "resource-create",
				ActionSuccess: "created",
				ActionFailure: "create",
				ReasonSuccess: api.ResourceCreated,
				ReasonFailure: api.ResourceCreationFailed,
				Status:        status,
			}, log)
	}

	return getResourceEvent(ctx,
		ResourceEvent{
			ResourceKind:  resourceKind,
			ResourceName:  resourceName,
			Prefix:        "resource-update",
			ActionSuccess: "updated",
			ActionFailure: "update",
			ReasonSuccess: api.ResourceUpdated,
			ReasonFailure: api.ResourceUpdateFailed,
			Status:        status,
			UpdateDetails: updateDesc,
		}, log)
}

func addDetails(event *api.Event, updateDesc *api.ResourceUpdatedDetails, log logrus.FieldLogger) {
	if updateDesc != nil {
		details := api.EventDetails{}
		if err := details.FromResourceUpdatedDetails(*updateDesc); err != nil {
			log.WithError(err).WithField("event", *event).Error("Failed to serialize event details")
			// Ignore the error and create event, even without description
		} else {
			event.Details = &details
		}
	}
}

func GetResourceDeletedEvent(ctx context.Context, resourceKind api.ResourceKind, resourceName string, status api.Status, log logrus.FieldLogger) *api.Event {
	return getResourceEvent(ctx,
		ResourceEvent{
			ResourceKind:  resourceKind,
			ResourceName:  resourceName,
			Prefix:        "resource-delete",
			ActionSuccess: "deleted",
			ActionFailure: "delete",
			ReasonSuccess: api.ResourceDeleted,
			ReasonFailure: api.ResourceDeletionFailed,
			Status:        status,
		}, log)
}

func createEventOutcome(log logrus.FieldLogger, reason api.EventReason, format string, args ...interface{}) eventOutcomeFunc {
	return func() (api.EventReason, string) {
		var buffer bytes.Buffer
		buffer.Grow(eventMessageCapacity)
		if _, err := fmt.Fprintf(&buffer, format, args...); err != nil {
			log.WithError(err).Error("Error formatting message")
			return reason, fmt.Sprintf("Error formatting message: %v", err)
		}
		return reason, buffer.String()
	}
}

type ResourceEvent struct {
	ResourceKind                 api.ResourceKind
	ResourceName                 string
	Prefix                       string
	ActionSuccess, ActionFailure string
	ReasonSuccess, ReasonFailure api.EventReason
	Status                       api.Status
	UpdateDetails                *api.ResourceUpdatedDetails
}

type GetResourceEventFunc func(ctx context.Context, resourceEvent ResourceEvent, log logrus.FieldLogger) *api.Event

func getResourceEvent(ctx context.Context, resourceEvent ResourceEvent, log logrus.FieldLogger) *api.Event {
	var success = createEventOutcome(log, resourceEvent.ReasonSuccess, "%s %s %s successfully", resourceEvent.ResourceKind, resourceEvent.ResourceName, resourceEvent.ActionSuccess)
	var failure = createEventOutcome(log, resourceEvent.ReasonFailure, "%s %s %s failed: %s", resourceEvent.ResourceKind, resourceEvent.ResourceName, resourceEvent.ActionFailure, resourceEvent.Status.Message)
	event := getBaseEvent(ctx, resourceEvent.Status, resourceEvent.Prefix, resourceEvent.ResourceKind, resourceEvent.ResourceName, success, failure)
	addDetails(event, resourceEvent.UpdateDetails, log)
	return event
}

func GetResourceEventFromResourceUpdate(ctx context.Context, resourceEvent ResourceEvent, log logrus.FieldLogger) *api.Event {
	var success = createEventOutcome(log, resourceEvent.ReasonSuccess, "%s %s", resourceEvent.ResourceKind, resourceEvent.ResourceName)
	event := getBaseEvent(ctx, resourceEvent.Status, "status", resourceEvent.ResourceKind, resourceEvent.ResourceName, success, nil)
	addDetails(event, resourceEvent.UpdateDetails, log)
	return event
}
