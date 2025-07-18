package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/service/common"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/selector"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
)

type resourceEvent struct {
	resourceKind                 api.ResourceKind
	resourceName                 string
	reasonSuccess, reasonFailure api.EventReason
	outcomeSuccess               string
	outcomeFailure               outcomeFailureFunc
	status                       api.Status
	updateDetails                *api.ResourceUpdatedDetails
	customDetails                *api.EventDetails
}

type eventConfig struct {
	reasonSuccess   api.EventReason
	reasonFailure   api.EventReason
	successMessage  string
	failureTemplate string
	updateDetails   *api.ResourceUpdatedDetails
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
	if resourceEvent.status.Code >= 200 && resourceEvent.status.Code < 299 {
		operationSucceeded = true
	} else if resourceEvent.status.Code >= 500 && resourceEvent.status.Code < 599 {
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
			Kind: string(resourceEvent.resourceKind),
			Name: resourceEvent.resourceName,
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
		event.Reason = resourceEvent.reasonSuccess
		event.Message = resourceEvent.outcomeSuccess
	} else {
		event.Reason = resourceEvent.reasonFailure
		if resourceEvent.outcomeFailure != nil {
			event.Message = resourceEvent.outcomeFailure()
		} else {
			event.Message = "generic failure"
		}
	}

	event.Type = getEventType(event.Reason)

	// Handle custom details first, then fall back to UpdateDetails
	if resourceEvent.customDetails != nil {
		event.Details = resourceEvent.customDetails
	} else if resourceEvent.updateDetails != nil {
		details := api.EventDetails{}
		if err := details.FromResourceUpdatedDetails(*resourceEvent.updateDetails); err != nil {
			log.WithError(err).WithField("event", event).Error("Failed to serialize event details")
			return nil
		}
		event.Details = &details
	}

	return &event
}

func buildResourceEvent(ctx context.Context, resourceKind api.ResourceKind, resourceName string, status api.Status, config eventConfig, log logrus.FieldLogger) *api.Event {
	failureFunc := func() string { return fmt.Sprintf(config.failureTemplate, status.Message) }
	return getBaseEvent(ctx,
		resourceEvent{
			resourceKind:   resourceKind,
			resourceName:   resourceName,
			reasonSuccess:  config.reasonSuccess,
			reasonFailure:  config.reasonFailure,
			outcomeSuccess: config.successMessage,
			outcomeFailure: failureFunc,
			status:         status,
			updateDetails:  config.updateDetails,
		}, log)
}

func GetResourceCreatedOrUpdatedSuccessEvent(ctx context.Context, created bool, resourceKind api.ResourceKind, resourceName string, updates *api.ResourceUpdatedDetails, log logrus.FieldLogger) *api.Event {
	var event *api.Event
	if created {
		event = buildResourceEvent(ctx, resourceKind, resourceName, api.StatusOK(), eventConfig{
			reasonSuccess:  api.EventReasonResourceCreated,
			successMessage: formatResourceActionMessage(resourceKind, "created"),
		}, log)
	} else {
		event = buildResourceEvent(ctx, resourceKind, resourceName, api.StatusOK(), eventConfig{
			reasonSuccess:  api.EventReasonResourceUpdated,
			successMessage: formatResourceActionMessage(resourceKind, "updated"),
		}, log)
	}
	if updates != nil {
		details := api.EventDetails{}
		if err := details.FromResourceUpdatedDetails(*updates); err != nil {
			log.WithError(err).WithField("event", event).Error("Failed to serialize event details")
			return nil
		}
		event.Details = &details
	}
	return event
}

func GetDeviceEventFromUpdateDetails(ctx context.Context, resourceName string, update common.ResourceUpdate) *api.Event {
	return buildResourceEvent(ctx, api.DeviceKind, resourceName, api.StatusOK(), eventConfig{
		reasonSuccess:  update.Reason,
		successMessage: update.Details,
	}, nil)
}

func GetResourceCreatedOrUpdatedFailureEvent(ctx context.Context, created bool, resourceKind api.ResourceKind, resourceName string, status api.Status, updatedDetails *api.ResourceUpdatedDetails) *api.Event {
	if created {
		return buildResourceEvent(ctx, resourceKind, resourceName, status, eventConfig{
			reasonFailure:   api.EventReasonResourceCreationFailed,
			failureTemplate: formatResourceActionFailedTemplate(resourceKind, "creation"),
			updateDetails:   updatedDetails,
		}, nil)
	}

	return buildResourceEvent(ctx, resourceKind, resourceName, status, eventConfig{
		reasonFailure:   api.EventReasonResourceUpdateFailed,
		failureTemplate: formatResourceActionFailedTemplate(resourceKind, "update"),
		updateDetails:   updatedDetails,
	}, nil)
}

func GetResourceDeletedFailureEvent(ctx context.Context, resourceKind api.ResourceKind, resourceName string, status api.Status) *api.Event {
	return buildResourceEvent(ctx, resourceKind, resourceName, status, eventConfig{
		reasonFailure:   api.EventReasonResourceDeletionFailed,
		failureTemplate: formatResourceActionFailedTemplate(resourceKind, "deletion"),
	}, nil)
}

func GetResourceDeletedSuccessEvent(ctx context.Context, resourceKind api.ResourceKind, resourceName string) *api.Event {
	return buildResourceEvent(ctx, resourceKind, resourceName, api.StatusOK(), eventConfig{
		reasonSuccess:  api.EventReasonResourceDeleted,
		successMessage: formatResourceActionMessage(resourceKind, "deleted"),
	}, nil)
}

func GetResourceApprovedEvent(ctx context.Context, resourceKind api.ResourceKind, resourceName string, status api.Status, log logrus.FieldLogger) *api.Event {
	return buildResourceEvent(ctx, resourceKind, resourceName, status, eventConfig{
		reasonSuccess:   api.EventReasonEnrollmentRequestApproved,
		reasonFailure:   api.EventReasonEnrollmentRequestApprovalFailed,
		successMessage:  formatResourceActionMessage(resourceKind, "approved"),
		failureTemplate: formatResourceActionFailedTemplate(resourceKind, "approval"),
	}, log)
}

func GetDeviceDecommissionedSuccessEvent(ctx context.Context, _ bool, _ api.ResourceKind, resourceName string, update *api.ResourceUpdatedDetails, log logrus.FieldLogger) *api.Event {
	return buildResourceEvent(ctx, api.DeviceKind, resourceName, api.StatusOK(), eventConfig{
		reasonSuccess:  api.EventReasonDeviceDecommissioned,
		successMessage: formatResourceActionMessage(api.DeviceKind, "decommissioned"),
		updateDetails:  update,
	}, log)
}

func GetDeviceDecommissionedFailureEvent(ctx context.Context, _ bool, _ api.ResourceKind, resourceName string, status api.Status) *api.Event {
	return buildResourceEvent(ctx, api.DeviceKind, resourceName, status, eventConfig{
		reasonFailure:   api.EventReasonDeviceDecommissionFailed,
		failureTemplate: formatResourceActionFailedTemplate(api.DeviceKind, "decommission"),
	}, nil)
}

var warningReasons = map[api.EventReason]struct{}{
	api.EventReasonResourceCreationFailed:          {},
	api.EventReasonResourceUpdateFailed:            {},
	api.EventReasonResourceDeletionFailed:          {},
	api.EventReasonDeviceDecommissionFailed:        {},
	api.EventReasonEnrollmentRequestApprovalFailed: {},
	api.EventReasonDeviceApplicationDegraded:       {},
	api.EventReasonDeviceApplicationError:          {},
	api.EventReasonDeviceCPUCritical:               {},
	api.EventReasonDeviceCPUWarning:                {},
	api.EventReasonDeviceMemoryCritical:            {},
	api.EventReasonDeviceMemoryWarning:             {},
	api.EventReasonDeviceDiskCritical:              {},
	api.EventReasonDeviceDiskWarning:               {},
	api.EventReasonDeviceDisconnected:              {},
	api.EventReasonDeviceSpecInvalid:               {},
	api.EventReasonDeviceMultipleOwnersDetected:    {},
	api.EventReasonInternalTaskFailed:              {},
}

// getEventType determines the event type based on the event reason
func getEventType(reason api.EventReason) api.EventType {
	if _, contains := warningReasons[reason]; contains {
		return api.Warning
	}
	return api.Normal
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
		resourceKind:   api.DeviceKind,
		resourceName:   deviceName,
		reasonFailure:  api.EventReasonDeviceMultipleOwnersDetected,
		outcomeFailure: func() string { return message },
		status:         api.StatusInternalServerError("Multiple fleet owners detected"),
		customDetails:  &details,
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
		resourceKind:   api.DeviceKind,
		resourceName:   deviceName,
		reasonSuccess:  api.EventReasonDeviceMultipleOwnersResolved,
		outcomeSuccess: message,
		status:         api.StatusOK(),
		customDetails:  &details,
	}, log)
}

// GetDeviceSpecValidEvent creates an event for device spec becoming valid
func GetDeviceSpecValidEvent(ctx context.Context, deviceName string) *api.Event {
	message := "Device specification is valid."

	return getBaseEvent(ctx, resourceEvent{
		resourceKind:   api.DeviceKind,
		resourceName:   deviceName,
		reasonSuccess:  api.EventReasonDeviceSpecValid,
		outcomeSuccess: message,
		status:         api.StatusOK(),
	}, nil)
}

// GetDeviceSpecInvalidEvent creates an event for device spec becoming invalid
func GetDeviceSpecInvalidEvent(ctx context.Context, deviceName string, message string) *api.Event {
	msg := fmt.Sprintf("Device specification is invalid: %s.", message)

	return getBaseEvent(ctx, resourceEvent{
		resourceKind:   api.DeviceKind,
		resourceName:   deviceName,
		reasonFailure:  api.EventReasonDeviceSpecInvalid,
		outcomeFailure: func() string { return msg },
		status:         api.StatusInternalServerError("Invalid device specification"),
	}, nil)
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
		resourceKind:   resourceKind,
		resourceName:   resourceName,
		reasonFailure:  api.EventReasonInternalTaskFailed,
		outcomeFailure: func() string { return message },
		status:         api.StatusInternalServerError("Internal task failed"),
		customDetails:  &details,
	}, log)
}

// Callbacks

func (h *ServiceHandler) eventCallbackDevice(ctx context.Context, _ api.ResourceKind, orgId uuid.UUID, name string, oldDevice, newDevice *api.Device, created bool, updatedDetails *api.ResourceUpdatedDetails, err error) {
	if err != nil {
		status := StoreErrorToApiStatus(err, created, api.DeviceKind, &name)
		h.CreateEvent(ctx, GetResourceCreatedOrUpdatedFailureEvent(ctx, created, api.DeviceKind, name, status, updatedDetails))
		return
	}

	resourceUpdates := common.CollectStatusChanges(ctx, oldDevice, newDevice, orgId, h.store)
	for _, resourceUpdate := range resourceUpdates {
		h.CreateEvent(ctx, GetDeviceEventFromUpdateDetails(ctx, name, resourceUpdate))
	}
	if created || len(resourceUpdates) > 0 {
		h.CreateEvent(ctx, GetResourceCreatedOrUpdatedSuccessEvent(ctx, created, api.DeviceKind, name, nil, h.log))
	}
}

func (h *ServiceHandler) eventCallbackDeviceDelete(ctx context.Context, _ api.ResourceKind, _ uuid.UUID, name string, _, _ *api.Device, created bool, _ *api.ResourceUpdatedDetails, err error) {
	if err != nil {
		status := StoreErrorToApiStatus(err, created, api.DeviceKind, &name)
		h.CreateEvent(ctx, GetResourceDeletedFailureEvent(ctx, api.DeviceKind, name, status))
	} else {
		h.CreateEvent(ctx, GetResourceDeletedSuccessEvent(ctx, api.DeviceKind, name))
	}
}

func (h *ServiceHandler) eventCallbackDeviceDecommission(ctx context.Context, _ api.ResourceKind, _ uuid.UUID, name string, _, _ *api.Device, created bool, updateDesc *api.ResourceUpdatedDetails, err error) {
	if err != nil {
		status := StoreErrorToApiStatus(err, created, api.DeviceKind, &name)
		h.CreateEvent(ctx, GetDeviceDecommissionedFailureEvent(ctx, created, api.DeviceKind, name, status))
	} else {
		h.CreateEvent(ctx, GetDeviceDecommissionedSuccessEvent(ctx, created, api.DeviceKind, name, updateDesc, nil))
	}
}

func (h *ServiceHandler) eventCallback(ctx context.Context, resourceKind api.ResourceKind, _ uuid.UUID, name string, created bool, updatedDetails *api.ResourceUpdatedDetails, err error) {
	if err != nil {
		status := StoreErrorToApiStatus(err, created, string(resourceKind), &name)
		h.CreateEvent(ctx, GetResourceCreatedOrUpdatedFailureEvent(ctx, created, resourceKind, name, status, updatedDetails))
	} else {
		h.CreateEvent(ctx, GetResourceCreatedOrUpdatedSuccessEvent(ctx, created, resourceKind, name, updatedDetails, nil))
	}
}

func (h *ServiceHandler) eventDeleteCallback(ctx context.Context, resourceKind api.ResourceKind, _ uuid.UUID, name string, created bool, _ *api.ResourceUpdatedDetails, err error) {
	if err != nil {
		status := StoreErrorToApiStatus(err, created, string(resourceKind), &name)
		h.CreateEvent(ctx, GetResourceDeletedFailureEvent(ctx, resourceKind, name, status))
	} else {
		h.CreateEvent(ctx, GetResourceDeletedSuccessEvent(ctx, resourceKind, name))
	}
}
