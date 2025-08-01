package common

import (
	"context"
	"fmt"
	"strings"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
)

type resourceEvent struct {
	resourceKind api.ResourceKind
	resourceName string
	reason       api.EventReason
	message      string
	details      *api.EventDetails
}

// convertUpdateDetails converts ResourceUpdatedDetails to EventDetails
func convertUpdateDetails(updates *api.ResourceUpdatedDetails) *api.EventDetails {
	if updates == nil {
		return nil
	}
	details := api.EventDetails{}
	if err := details.FromResourceUpdatedDetails(*updates); err != nil {
		// If conversion fails, return nil rather than panicking
		return nil
	}
	return &details
}

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

func getBaseEvent(ctx context.Context, resourceEvent resourceEvent) *api.Event {
	return api.GetBaseEvent(ctx, resourceEvent.resourceKind, resourceEvent.resourceName, resourceEvent.reason, resourceEvent.message, resourceEvent.details)
}

// GetResourceCreatedOrUpdatedSuccessEvent creates an event for successful resource creation or update
func GetResourceCreatedOrUpdatedSuccessEvent(ctx context.Context, created bool, resourceKind api.ResourceKind, resourceName string, updates *api.ResourceUpdatedDetails, log logrus.FieldLogger) *api.Event {
	if !created && updates != nil && len(updates.UpdatedFields) == 0 {
		return nil
	}

	details := convertUpdateDetails(updates)
	if updates != nil && details == nil {
		log.WithField("updates", updates).Error("Failed to serialize event details")
		return nil
	}

	var event *api.Event
	if created {
		event = getBaseEvent(ctx, resourceEvent{
			resourceKind: resourceKind,
			resourceName: resourceName,
			reason:       api.EventReasonResourceCreated,
			message:      formatResourceActionMessage(resourceKind, "created"),
			details:      details,
		})
	} else {
		event = getBaseEvent(ctx, resourceEvent{
			resourceKind: resourceKind,
			resourceName: resourceName,
			reason:       api.EventReasonResourceUpdated,
			message:      formatResourceActionMessage(resourceKind, "updated"),
			details:      details,
		})
	}

	return event
}

// GetDeviceEventFromUpdateDetails creates a device event from update details
func GetDeviceEventFromUpdateDetails(ctx context.Context, resourceName string, update ResourceUpdate) *api.Event {
	return getBaseEvent(ctx, resourceEvent{
		resourceKind: api.DeviceKind,
		resourceName: resourceName,
		reason:       update.Reason,
		message:      update.Details,
		details:      nil,
	})
}

// GetResourceCreatedOrUpdatedFailureEvent creates an event for failed resource creation or update
func GetResourceCreatedOrUpdatedFailureEvent(ctx context.Context, created bool, resourceKind api.ResourceKind, resourceName string, status api.Status, updatedDetails *api.ResourceUpdatedDetails) *api.Event {
	if created {
		return getBaseEvent(ctx, resourceEvent{
			resourceKind: resourceKind,
			resourceName: resourceName,
			reason:       api.EventReasonResourceCreationFailed,
			message:      fmt.Sprintf(formatResourceActionFailedTemplate(resourceKind, "creation"), status.Message),
			details:      convertUpdateDetails(updatedDetails),
		})
	}

	return getBaseEvent(ctx, resourceEvent{
		resourceKind: resourceKind,
		resourceName: resourceName,
		reason:       api.EventReasonResourceUpdateFailed,
		message:      fmt.Sprintf(formatResourceActionFailedTemplate(resourceKind, "update"), status.Message),
		details:      convertUpdateDetails(updatedDetails),
	})
}

// GetResourceDeletedFailureEvent creates an event for failed resource deletion
func GetResourceDeletedFailureEvent(ctx context.Context, resourceKind api.ResourceKind, resourceName string, status api.Status) *api.Event {
	return getBaseEvent(ctx, resourceEvent{
		resourceKind: resourceKind,
		resourceName: resourceName,
		reason:       api.EventReasonResourceDeletionFailed,
		message:      fmt.Sprintf(formatResourceActionFailedTemplate(resourceKind, "deletion"), status.Message),
		details:      nil,
	})
}

// GetResourceDeletedSuccessEvent creates an event for successful resource deletion
func GetResourceDeletedSuccessEvent(ctx context.Context, resourceKind api.ResourceKind, resourceName string) *api.Event {
	return getBaseEvent(ctx, resourceEvent{
		resourceKind: resourceKind,
		resourceName: resourceName,
		reason:       api.EventReasonResourceDeleted,
		message:      formatResourceActionMessage(resourceKind, "deleted"),
		details:      nil,
	})
}

// GetEnrollmentRequestApprovedEvent creates an event for enrollment request approval
func GetEnrollmentRequestApprovedEvent(ctx context.Context, resourceName string, log logrus.FieldLogger) *api.Event {
	return getBaseEvent(ctx, resourceEvent{
		resourceKind: api.EnrollmentRequestKind,
		resourceName: resourceName,
		reason:       api.EventReasonEnrollmentRequestApproved,
		message:      formatResourceActionMessage(api.EnrollmentRequestKind, "approved"),
		details:      nil,
	})
}

// GetEnrollmentRequestApprovalFailedEvent creates an event for failed enrollment request approval
func GetEnrollmentRequestApprovalFailedEvent(ctx context.Context, resourceName string, status api.Status, log logrus.FieldLogger) *api.Event {
	return getBaseEvent(ctx, resourceEvent{
		resourceKind: api.EnrollmentRequestKind,
		resourceName: resourceName,
		reason:       api.EventReasonEnrollmentRequestApprovalFailed,
		message:      fmt.Sprintf(formatResourceActionFailedTemplate(api.EnrollmentRequestKind, "approval"), status.Message),
		details:      nil,
	})
}

// GetDeviceDecommissionedSuccessEvent creates an event for successful device decommission
func GetDeviceDecommissionedSuccessEvent(ctx context.Context, _ bool, _ api.ResourceKind, resourceName string, update *api.ResourceUpdatedDetails, log logrus.FieldLogger) *api.Event {
	return getBaseEvent(ctx, resourceEvent{
		resourceKind: api.DeviceKind,
		resourceName: resourceName,
		reason:       api.EventReasonDeviceDecommissioned,
		message:      formatResourceActionMessage(api.DeviceKind, "decommissioned"),
		details:      convertUpdateDetails(update),
	})
}

// GetDeviceDecommissionedFailureEvent creates an event for failed device decommission
func GetDeviceDecommissionedFailureEvent(ctx context.Context, _ bool, _ api.ResourceKind, resourceName string, status api.Status) *api.Event {
	return getBaseEvent(ctx, resourceEvent{
		resourceKind: api.DeviceKind,
		resourceName: resourceName,
		reason:       api.EventReasonDeviceDecommissionFailed,
		message:      fmt.Sprintf(formatResourceActionFailedTemplate(api.DeviceKind, "decommission"), status.Message),
		details:      nil,
	})
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
		resourceKind: api.DeviceKind,
		resourceName: deviceName,
		reason:       api.EventReasonDeviceMultipleOwnersDetected,
		message:      message,
		details:      &details,
	})
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
		resourceKind: api.DeviceKind,
		resourceName: deviceName,
		reason:       api.EventReasonDeviceMultipleOwnersResolved,
		message:      message,
		details:      &details,
	})
}

// GetDeviceSpecValidEvent creates an event for device spec becoming valid
func GetDeviceSpecValidEvent(ctx context.Context, deviceName string) *api.Event {
	return getBaseEvent(ctx, resourceEvent{
		resourceKind: api.DeviceKind,
		resourceName: deviceName,
		reason:       api.EventReasonDeviceSpecValid,
		message:      "Device specification is valid.",
		details:      nil,
	})
}

// GetDeviceSpecInvalidEvent creates an event for device spec becoming invalid
func GetDeviceSpecInvalidEvent(ctx context.Context, deviceName string, message string) *api.Event {
	msg := fmt.Sprintf("Device specification is invalid: %s.", message)

	return getBaseEvent(ctx, resourceEvent{
		resourceKind: api.DeviceKind,
		resourceName: deviceName,
		reason:       api.EventReasonDeviceSpecInvalid,
		message:      msg,
		details:      nil,
	})
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
		resourceKind: resourceKind,
		resourceName: resourceName,
		reason:       api.EventReasonInternalTaskFailed,
		message:      message,
		details:      &details,
	})
}

// ResourceSync event functions
func GetResourceSyncCommitDetectedEvent(ctx context.Context, resourceName string, commitHash string) *api.Event {
	return getBaseEvent(ctx, resourceEvent{
		resourceKind: api.ResourceSyncKind,
		resourceName: resourceName,
		reason:       api.EventReasonResourceSyncCommitDetected,
		message:      fmt.Sprintf("New commit detected: %s", commitHash),
		details:      nil,
	})
}

func GetResourceSyncAccessibleEvent(ctx context.Context, resourceName string) *api.Event {
	return getBaseEvent(ctx, resourceEvent{
		resourceKind: api.ResourceSyncKind,
		resourceName: resourceName,
		reason:       api.EventReasonResourceSyncAccessible,
		message:      "Repository is accessible",
		details:      nil,
	})
}

func GetResourceSyncInaccessibleEvent(ctx context.Context, resourceName string, errorMessage string) *api.Event {
	return getBaseEvent(ctx, resourceEvent{
		resourceKind: api.ResourceSyncKind,
		resourceName: resourceName,
		reason:       api.EventReasonResourceSyncInaccessible,
		message:      fmt.Sprintf("Repository is inaccessible: %s", errorMessage),
		details:      nil,
	})
}

func GetResourceSyncParsedEvent(ctx context.Context, resourceName string) *api.Event {
	return getBaseEvent(ctx, resourceEvent{
		resourceKind: api.ResourceSyncKind,
		resourceName: resourceName,
		reason:       api.EventReasonResourceSyncParsed,
		message:      "Resources parsed successfully",
		details:      nil,
	})
}

func GetResourceSyncParsingFailedEvent(ctx context.Context, resourceName string, errorMessage string) *api.Event {
	return getBaseEvent(ctx, resourceEvent{
		resourceKind: api.ResourceSyncKind,
		resourceName: resourceName,
		reason:       api.EventReasonResourceSyncParsingFailed,
		message:      fmt.Sprintf("Resource parsing failed: %s", errorMessage),
		details:      nil,
	})
}

func GetResourceSyncSyncedEvent(ctx context.Context, resourceName string) *api.Event {
	return getBaseEvent(ctx, resourceEvent{
		resourceKind: api.ResourceSyncKind,
		resourceName: resourceName,
		reason:       api.EventReasonResourceSyncSynced,
		message:      "Resources synced successfully",
		details:      nil,
	})
}

func GetResourceSyncSyncFailedEvent(ctx context.Context, resourceName string, errorMessage string) *api.Event {
	return getBaseEvent(ctx, resourceEvent{
		resourceKind: api.ResourceSyncKind,
		resourceName: resourceName,
		reason:       api.EventReasonResourceSyncSyncFailed,
		message:      fmt.Sprintf("Resource sync failed: %s", errorMessage),
		details:      nil,
	})
}

// GetFleetRolloutNewEvent creates an event for fleet rollout creation
func GetFleetRolloutNewEvent(ctx context.Context, name string) *api.Event {
	return getBaseEvent(ctx, resourceEvent{
		resourceKind: api.FleetKind,
		resourceName: name,
		reason:       api.EventReasonFleetRolloutCreated,
		message:      "Fleet rollout created",
		details:      nil,
	})
}

// GetFleetRolloutCompletedEvent creates an event for fleet rollout completion
func GetFleetRolloutCompletedEvent(ctx context.Context, name string) *api.Event {
	return getBaseEvent(ctx, resourceEvent{
		resourceKind: api.FleetKind,
		resourceName: name,
		reason:       api.EventReasonFleetRolloutBatchCompleted,
		message:      "Fleet rollout batch completed",
		details:      nil,
	})
}

// GetFleetRolloutStartedEvent creates an event for fleet rollout start
func GetFleetRolloutStartedEvent(ctx context.Context, templateVersionName string, fleetName string, immediateRollout bool) *api.Event {
	rolloutType := "batched"
	if immediateRollout {
		rolloutType = "immediate"
	}
	details := api.FleetRolloutStartedDetails{
		IsImmediate:     api.FleetRolloutStartedDetailsIsImmediate(rolloutType),
		TemplateVersion: templateVersionName,
	}
	eventDetails := api.EventDetails{}
	if err := eventDetails.FromFleetRolloutStartedDetails(details); err != nil {
		// If serialization fails, return nil rather than panicking
		return nil
	}
	return getBaseEvent(ctx, resourceEvent{
		resourceKind: api.FleetKind,
		resourceName: fleetName,
		reason:       api.EventReasonFleetRolloutStarted,
		message:      "template created with rollout device selection",
		details:      &eventDetails,
	})
}

// GetRepositoryAccessibleEvent creates an event for repository accessibility
func GetRepositoryAccessibleEvent(ctx context.Context, name string) *api.Event {
	return getBaseEvent(ctx, resourceEvent{
		resourceKind: api.RepositoryKind,
		resourceName: name,
		reason:       api.EventReasonRepositoryAccessible,
		message:      "Repository is accessible",
		details:      nil,
	})
}

// GetRepositoryInaccessibleEvent creates an event for repository inaccessibility
func GetRepositoryInaccessibleEvent(ctx context.Context, name string, errorMessage string) *api.Event {
	return getBaseEvent(ctx, resourceEvent{
		resourceKind: api.RepositoryKind,
		resourceName: name,
		reason:       api.EventReasonRepositoryInaccessible,
		message:      fmt.Sprintf("Repository is inaccessible: %s", errorMessage),
		details:      nil,
	})
}
