package service

import (
	"context"
	"fmt"
	"strings"
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
	ReasonSuccess, ReasonFailure api.EventReason
	OutcomeSuccess               string
	OutcomeFailure               outcomeFailureFunc
	Status                       api.Status
	UpdateDetails                *api.ResourceUpdatedDetails
	CustomDetails                *api.EventDetails
}

type eventConfig struct {
	ReasonSuccess   api.EventReason
	ReasonFailure   api.EventReason
	SuccessMessage  string
	FailureTemplate string
	UpdateDetails   *api.ResourceUpdatedDetails
}

type outcomeFailureFunc func() string

// Helper functions for standardized event message formatting

// formatResourceActionMessage creates a standardized message for resource actions
func formatResourceActionMessage(resourceKind api.ResourceKind, action string) string {
	return fmt.Sprintf("%s was %s successfully.", resourceKind, action)
}

// formatResourceActionFailedTemplate creates a template for failed resource actions
func formatResourceActionFailedTemplate(resourceKind api.ResourceKind, action string) string {
	return fmt.Sprintf("%s %s failed: %%s.", resourceKind, action)
}

// formatDeviceMultipleOwnersMessage creates a standardized message for multiple owners detected
func formatDeviceMultipleOwnersMessage(matchingFleets []string) string {
	return fmt.Sprintf("Device matches multiple fleets: %s.", strings.Join(matchingFleets, ", "))
}

// formatDeviceMultipleOwnersResolvedMessage creates a standardized message for multiple owners resolved
func formatDeviceMultipleOwnersResolvedMessage(resolutionType api.DeviceMultipleOwnersResolvedDetailsResolutionType, assignedOwner *string) string {
	switch resolutionType {
	case api.SingleMatch:
		return fmt.Sprintf("Device multiple owners conflict was resolved: single fleet match, assigned to fleet '%s'.", lo.FromPtr(assignedOwner))
	case api.NoMatch:
		return "Device multiple owners conflict was resolved: no fleet matches, owner was removed."
	case api.FleetDeleted:
		return "Device multiple owners conflict was resolved: fleet was deleted."
	default:
		return "Device multiple owners conflict was resolved."
	}
}

// formatInternalTaskFailedMessage creates a standardized message for internal task failures
func formatInternalTaskFailedMessage(resourceKind api.ResourceKind, taskType, errorMessage string) string {
	return fmt.Sprintf("%s internal task failed: %s - %s.", resourceKind, taskType, errorMessage)
}

// formatFleetSelectorProcessingMessage creates a standardized message for fleet selector processing

func (h *ServiceHandler) CreateEvent(ctx context.Context, event *api.Event) {
	if event == nil {
		return
	}

	orgId := getOrgIdFromContext(ctx)

	err := h.store.Event().Create(ctx, orgId, event)
	if err != nil {
		h.log.Errorf("failed emitting <%s> resource updated %s event for %s %s/%s: %v", *event.Metadata.Name, event.Reason, event.InvolvedObject.Kind, orgId, event.InvolvedObject.Name, err)
	}
}

func (h *ServiceHandler) ListEvents(ctx context.Context, params api.ListEventsParams) (*api.EventList, api.Status) {
	orgId := getOrgIdFromContext(ctx)

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

	var actorStr string
	if actor := ctx.Value(consts.EventActorCtxKey); actor != nil {
		actorStr = actor.(string)
	}

	var componentStr string
	if component := ctx.Value(consts.EventSourceComponentCtxKey); component != nil {
		componentStr = component.(string)
	}

	// Generate a UUID for the event name to ensure k8s compliance
	eventName := uuid.New().String()

	event := api.Event{
		Metadata: api.ObjectMeta{
			Name: lo.ToPtr(eventName),
		},
		InvolvedObject: api.ObjectReference{
			Kind: string(resourceEvent.ResourceKind),
			Name: resourceEvent.ResourceName,
		},
		Source: api.EventSource{
			Component: componentStr,
		},
		Actor: actorStr,
	}

	// Add request ID to the event for correlation
	if reqID := ctx.Value(middleware.RequestIDKey); reqID != nil {
		event.Metadata.Annotations = &map[string]string{api.EventAnnotationRequestID: reqID.(string)}
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
	}

	event.Type = getEventType(event.Reason)

	// Handle custom details first, then fall back to UpdateDetails
	if resourceEvent.CustomDetails != nil {
		event.Details = resourceEvent.CustomDetails
	} else if resourceEvent.UpdateDetails != nil {
		details := api.EventDetails{}
		if err := details.FromResourceUpdatedDetails(*resourceEvent.UpdateDetails); err != nil {
			log.WithError(err).WithField("event", event).Error("Failed to serialize event details")
			return nil
		}
		event.Details = &details
	}

	return &event
}

func buildResourceEvent(ctx context.Context, resourceKind api.ResourceKind, resourceName string, status api.Status, config eventConfig, log logrus.FieldLogger) *api.Event {
	failureFunc := func() string { return fmt.Sprintf(config.FailureTemplate, status.Message) }
	return getBaseEvent(ctx,
		resourceEvent{
			ResourceKind:   resourceKind,
			ResourceName:   resourceName,
			ReasonSuccess:  config.ReasonSuccess,
			ReasonFailure:  config.ReasonFailure,
			OutcomeSuccess: config.SuccessMessage,
			OutcomeFailure: failureFunc,
			Status:         status,
			UpdateDetails:  config.UpdateDetails,
		}, log)
}

func GetResourceCreatedOrUpdatedEvent(ctx context.Context, created bool, resourceKind api.ResourceKind, resourceName string, status api.Status, updateDesc *api.ResourceUpdatedDetails, log logrus.FieldLogger) *api.Event {
	if created {
		return buildResourceEvent(ctx, resourceKind, resourceName, status, eventConfig{
			ReasonSuccess:   api.EventReasonResourceCreated,
			ReasonFailure:   api.EventReasonResourceCreationFailed,
			SuccessMessage:  formatResourceActionMessage(resourceKind, "created"),
			FailureTemplate: formatResourceActionFailedTemplate(resourceKind, "creation"),
		}, log)
	}

	return buildResourceEvent(ctx, resourceKind, resourceName, status, eventConfig{
		ReasonSuccess:   api.EventReasonResourceUpdated,
		ReasonFailure:   api.EventReasonResourceUpdateFailed,
		SuccessMessage:  formatResourceActionMessage(resourceKind, "updated"),
		FailureTemplate: formatResourceActionFailedTemplate(resourceKind, "update"),
		UpdateDetails:   updateDesc,
	}, log)
}

func GetResourceDeletedEvent(ctx context.Context, resourceKind api.ResourceKind, resourceName string, status api.Status, log logrus.FieldLogger) *api.Event {
	return buildResourceEvent(ctx, resourceKind, resourceName, status, eventConfig{
		ReasonSuccess:   api.EventReasonResourceDeleted,
		ReasonFailure:   api.EventReasonResourceDeletionFailed,
		SuccessMessage:  formatResourceActionMessage(resourceKind, "deleted"),
		FailureTemplate: formatResourceActionFailedTemplate(resourceKind, "deletion"),
	}, log)
}

func GetResourceApprovedEvent(ctx context.Context, resourceKind api.ResourceKind, resourceName string, status api.Status, log logrus.FieldLogger) *api.Event {
	return buildResourceEvent(ctx, resourceKind, resourceName, status, eventConfig{
		ReasonSuccess:   api.EventReasonEnrollmentRequestApproved,
		ReasonFailure:   api.EventReasonEnrollmentRequestApprovalFailed,
		SuccessMessage:  formatResourceActionMessage(resourceKind, "approved"),
		FailureTemplate: formatResourceActionFailedTemplate(resourceKind, "approval"),
	}, log)
}

func GetResourceDecommissionedEvent(ctx context.Context, resourceKind api.ResourceKind, resourceName string, status api.Status, updateDetails *api.ResourceUpdatedDetails, log logrus.FieldLogger) *api.Event {
	return buildResourceEvent(ctx, resourceKind, resourceName, status, eventConfig{
		ReasonSuccess:   api.EventReasonDeviceDecommissioned,
		ReasonFailure:   api.EventReasonDeviceDecommissionFailed,
		SuccessMessage:  formatResourceActionMessage(resourceKind, "decommissioned"),
		FailureTemplate: formatResourceActionFailedTemplate(resourceKind, "decommission"),
		UpdateDetails:   updateDetails,
	}, log)
}

// getEventType determines the event type based on the event reason
func getEventType(reason api.EventReason) api.EventType {
	warningReasons := []api.EventReason{
		api.EventReasonResourceCreationFailed,
		api.EventReasonResourceUpdateFailed,
		api.EventReasonResourceDeletionFailed,
		api.EventReasonDeviceDecommissionFailed,
		api.EventReasonEnrollmentRequestApprovalFailed,
		api.EventReasonDeviceApplicationDegraded,
		api.EventReasonDeviceApplicationError,
		api.EventReasonDeviceCPUCritical,
		api.EventReasonDeviceCPUWarning,
		api.EventReasonDeviceMemoryCritical,
		api.EventReasonDeviceMemoryWarning,
		api.EventReasonDeviceDiskCritical,
		api.EventReasonDeviceDiskWarning,
		api.EventReasonDeviceDisconnected,
		api.EventReasonDeviceSpecInvalid,
		api.EventReasonDeviceMultipleOwnersDetected,
		api.EventReasonInternalTaskFailed,
	}

	if lo.Contains(warningReasons, reason) {
		return api.Warning
	}

	return api.Normal
}

func GetResourceEventFromUpdateDetails(ctx context.Context, resourceKind api.ResourceKind, resourceName string, reasonSuccess api.EventReason, updateDetails string, log logrus.FieldLogger) *api.Event {
	return getBaseEvent(ctx,
		resourceEvent{
			ResourceKind:   resourceKind,
			ResourceName:   resourceName,
			ReasonSuccess:  reasonSuccess,
			Status:         api.StatusOK(),
			OutcomeSuccess: updateDetails,
			OutcomeFailure: nil,
		}, log)
}

// GetDeviceMultipleOwnersDetectedEvent creates an event for multiple fleet owners detected
func GetDeviceMultipleOwnersDetectedEvent(ctx context.Context, deviceName string, matchingFleets []string, log logrus.FieldLogger) *api.Event {
	message := formatDeviceMultipleOwnersMessage(matchingFleets)

	details := api.EventDetails{}
	detailsStruct := api.DeviceMultipleOwnersDetectedDetails{
		MatchingFleets: matchingFleets,
	}
	if err := details.FromDeviceMultipleOwnersDetectedDetails(detailsStruct); err != nil {
		log.WithError(err).Error("Failed to serialize device multiple owners detected event details")
		return nil
	}

	return getBaseEvent(ctx, resourceEvent{
		ResourceKind:   api.DeviceKind,
		ResourceName:   deviceName,
		ReasonFailure:  api.EventReasonDeviceMultipleOwnersDetected,
		OutcomeFailure: func() string { return message },
		Status:         api.StatusInternalServerError("Multiple fleet owners detected"),
		CustomDetails:  &details,
	}, log)
}

// GetDeviceMultipleOwnersResolvedEvent creates an event for multiple fleet owners resolved
func GetDeviceMultipleOwnersResolvedEvent(ctx context.Context, deviceName string, resolutionType api.DeviceMultipleOwnersResolvedDetailsResolutionType, assignedOwner *string, previousMatchingFleets []string, log logrus.FieldLogger) *api.Event {
	message := formatDeviceMultipleOwnersResolvedMessage(resolutionType, assignedOwner)

	details := api.EventDetails{}
	detailsStruct := api.DeviceMultipleOwnersResolvedDetails{
		ResolutionType:         resolutionType,
		AssignedOwner:          assignedOwner,
		PreviousMatchingFleets: &previousMatchingFleets,
	}
	if err := details.FromDeviceMultipleOwnersResolvedDetails(detailsStruct); err != nil {
		log.WithError(err).Error("Failed to serialize device multiple owners resolved event details")
		return nil
	}

	return getBaseEvent(ctx, resourceEvent{
		ResourceKind:   api.DeviceKind,
		ResourceName:   deviceName,
		ReasonSuccess:  api.EventReasonDeviceMultipleOwnersResolved,
		OutcomeSuccess: message,
		Status:         api.StatusOK(),
		CustomDetails:  &details,
	}, log)
}

// GetDeviceSpecValidEvent creates an event for device spec becoming valid
func GetDeviceSpecValidEvent(ctx context.Context, deviceName string, log logrus.FieldLogger) *api.Event {
	message := "Device specification is valid."

	return getBaseEvent(ctx, resourceEvent{
		ResourceKind:   api.DeviceKind,
		ResourceName:   deviceName,
		ReasonSuccess:  api.EventReasonDeviceSpecValid,
		OutcomeSuccess: message,
		Status:         api.StatusOK(),
	}, log)
}

// GetDeviceSpecInvalidEvent creates an event for device spec becoming invalid
func GetDeviceSpecInvalidEvent(ctx context.Context, deviceName string, message string, log logrus.FieldLogger) *api.Event {
	msg := fmt.Sprintf("Device specification is invalid: %s.", message)

	return getBaseEvent(ctx, resourceEvent{
		ResourceKind:   api.DeviceKind,
		ResourceName:   deviceName,
		ReasonFailure:  api.EventReasonDeviceSpecInvalid,
		OutcomeFailure: func() string { return msg },
		Status:         api.StatusInternalServerError("Invalid device specification"),
	}, log)
}

// GetInternalTaskFailedEvent creates an event for internal task failures
func GetInternalTaskFailedEvent(ctx context.Context, resourceKind api.ResourceKind, resourceName string, taskType string, errorMessage string, retryCount *int, taskParameters map[string]string, log logrus.FieldLogger) *api.Event {
	message := formatInternalTaskFailedMessage(resourceKind, taskType, errorMessage)

	details := api.EventDetails{}
	detailsStruct := api.InternalTaskFailedDetails{
		TaskType:       taskType,
		ErrorMessage:   errorMessage,
		RetryCount:     retryCount,
		TaskParameters: &taskParameters,
	}
	if err := details.FromInternalTaskFailedDetails(detailsStruct); err != nil {
		log.WithError(err).Error("Failed to serialize internal task failed event details")
		return nil
	}

	return getBaseEvent(ctx, resourceEvent{
		ResourceKind:   resourceKind,
		ResourceName:   resourceName,
		ReasonFailure:  api.EventReasonInternalTaskFailed,
		OutcomeFailure: func() string { return message },
		Status:         api.StatusInternalServerError("Internal task failed"),
		CustomDetails:  &details,
	}, log)
}
