package service

import (
	"context"
	"fmt"
	"sync/atomic"
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

type resourceEvent struct {
	ResourceKind                 api.ResourceKind
	ResourceName                 string
	Prefix                       string
	ReasonSuccess, ReasonFailure api.EventReason
	OutcomeSuccess               string
	OutcomeFailure               outcomeFailureFunc
	Status                       api.Status
	UpdateDetails                *api.ResourceUpdatedDetails
}

type outcomeFailureFunc func() string

func (h *ServiceHandler) CreateEvent(ctx context.Context, event *api.Event) {
	if event == nil {
		return
	}

	orgId := store.NullOrgId

	err := h.store.Event().Create(ctx, orgId, event)
	if err != nil {
		h.log.Errorf("failed emitting <%s> resource updated %s event for %s %s/%s: %v", *event.Metadata.Name, event.Reason, event.InvolvedObject.Kind, orgId, event.InvolvedObject.Name, err)
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

func getBaseEvent(ctx context.Context, resourceEvent resourceEvent, log logrus.FieldLogger) *api.Event {
	var operationSucceeded bool
	if resourceEvent.Status.Code >= 200 && resourceEvent.Status.Code < 299 {
		operationSucceeded = true
	} else if resourceEvent.Status.Code >= 500 && resourceEvent.Status.Code < 599 {
		operationSucceeded = false
	} else {
		// If it's not one of the above cases, it's 4XX, which we don't emit events for
		return nil
	}

	var requestIDstr string
	if reqID := ctx.Value(middleware.RequestIDKey); reqID == nil {
		// If the requestID is nil or not set, fallback to a UUID
		requestIDstr = uuid.New().String()
	} else {
		requestIDstr = reqID.(string)
	}

	var actorStr string
	if actor := ctx.Value(consts.EventActorCtxKey); actor != nil {
		actorStr = actor.(string)
	}

	var componentStr string
	if component := ctx.Value(consts.EventSourceComponentCtxKey); component != nil {
		componentStr = component.(string)
	}

	event := api.Event{
		Metadata: api.ObjectMeta{
			Name: lo.ToPtr(fmt.Sprintf("%s-%s-%s-%s", resourceEvent.Prefix, resourceEvent.ResourceKind, resourceEvent.ResourceName, requestIDstr)),
		},
		Type: api.Normal,
		InvolvedObject: api.ObjectReference{
			Kind: string(resourceEvent.ResourceKind),
			Name: resourceEvent.ResourceName,
		},
		Source: api.EventSource{
			Component: componentStr,
		},
		Actor: actorStr,
	}

	if operationSucceeded {
		event.Reason = resourceEvent.ReasonSuccess
		event.Message = resourceEvent.OutcomeSuccess
	} else {
		event.Reason = resourceEvent.ReasonFailure
		if resourceEvent.OutcomeFailure != nil {
			event.Message = resourceEvent.OutcomeFailure()
		} else {
			event.Message = "generic failure"
		}
		event.Type = api.Warning
	}

	if resourceEvent.UpdateDetails != nil {
		details := api.EventDetails{}
		if err := details.FromResourceUpdatedDetails(*resourceEvent.UpdateDetails); err != nil {
			log.WithError(err).WithField("event", event).Error("Failed to serialize event details")
			// Ignore the error and create an event, even without details
		} else {
			event.Details = &details
		}
	}

	return &event
}

func GetResourceCreatedOrUpdatedEvent(ctx context.Context, created bool, resourceKind api.ResourceKind, resourceName string, status api.Status, updateDesc *api.ResourceUpdatedDetails, log logrus.FieldLogger) *api.Event {
	if created {
		createFailure := func() string { return fmt.Sprintf("create failed: %s", status.Message) }
		return getBaseEvent(ctx,
			resourceEvent{
				ResourceKind:   resourceKind,
				ResourceName:   resourceName,
				Prefix:         "resource-create",
				ReasonSuccess:  api.EventReasonResourceCreated,
				ReasonFailure:  api.EventReasonResourceCreationFailed,
				OutcomeSuccess: "created successfully",
				OutcomeFailure: createFailure,
				Status:         status,
			}, log)
	}

	updateFailure := func() string { return fmt.Sprintf("update failed: %s", status.Message) }
	return getBaseEvent(ctx,
		resourceEvent{
			ResourceKind:   resourceKind,
			ResourceName:   resourceName,
			Prefix:         "resource-update",
			ReasonSuccess:  api.EventReasonResourceUpdated,
			ReasonFailure:  api.EventReasonResourceUpdateFailed,
			OutcomeSuccess: "updated successfully",
			OutcomeFailure: updateFailure,
			Status:         status,
			UpdateDetails:  updateDesc,
		}, log)
}

func GetResourceDeletedEvent(ctx context.Context, resourceKind api.ResourceKind, resourceName string, status api.Status, log logrus.FieldLogger) *api.Event {
	deleteFailure := func() string { return fmt.Sprintf("delete failed: %s", status.Message) }
	return getBaseEvent(ctx,
		resourceEvent{
			ResourceKind:   resourceKind,
			ResourceName:   resourceName,
			Prefix:         "resource-delete",
			ReasonSuccess:  api.EventReasonResourceDeleted,
			ReasonFailure:  api.EventReasonResourceDeletionFailed,
			OutcomeSuccess: "deleted successfully",
			OutcomeFailure: deleteFailure,
			Status:         status,
		}, log)
}

func GetResourceDecommissionedEvent(ctx context.Context, resourceKind api.ResourceKind, resourceName string, status api.Status, updateDetails *api.ResourceUpdatedDetails, log logrus.FieldLogger) *api.Event {
	decommissionFailure := func() string { return fmt.Sprintf("decommission failed: %s", status.Message) }
	return getBaseEvent(ctx,
		resourceEvent{
			ResourceKind:   resourceKind,
			ResourceName:   resourceName,
			Prefix:         "resource-decommission",
			ReasonSuccess:  api.EventReasonDeviceDecommissioned,
			ReasonFailure:  api.EventReasonDeviceDecommissionFailed,
			Status:         status,
			OutcomeSuccess: "decommissioned successfully",
			OutcomeFailure: decommissionFailure,
			UpdateDetails:  updateDetails,
		}, log)
}

func createPrefixGenerator(basePrefix string) func() string {
	var counter int64
	return func() string {
		count := atomic.AddInt64(&counter, 1)
		return fmt.Sprintf("%s_%d", basePrefix, count)
	}
}

var generateUpdateDetailsPrefix = createPrefixGenerator("from-update-details")

func GetResourceEventFromUpdateDetails(ctx context.Context, resourceKind api.ResourceKind, resourceName string, reasonSuccess api.EventReason, updateDetails string, log logrus.FieldLogger) *api.Event {
	return getBaseEvent(ctx,
		resourceEvent{
			ResourceKind:   resourceKind,
			ResourceName:   resourceName,
			Prefix:         generateUpdateDetailsPrefix(),
			ReasonSuccess:  reasonSuccess,
			Status:         api.StatusOK(),
			OutcomeSuccess: updateDetails,
			OutcomeFailure: nil,
		}, log)
}
