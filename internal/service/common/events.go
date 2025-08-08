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
func formatResourceActionMessage(resourceKind api.ResourceKind, action string, details *string) string {
	s := fmt.Sprintf("%s was %s successfully", resourceKind, action)
	if details != nil {
		s += fmt.Sprintf(" (%s)", *details)
	}
	s += "."
	return s
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
	if !created && (updates == nil || len(updates.UpdatedFields) == 0) {
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
			message:      formatResourceActionMessage(resourceKind, "created", nil),
			details:      details,
		})
	} else {
		updatedFieldsStr := strings.Join(lo.Map(updates.UpdatedFields, func(item api.ResourceUpdatedDetailsUpdatedFields, _ int) string {
			return string(item)
		}), ", ")
		event = getBaseEvent(ctx, resourceEvent{
			resourceKind: resourceKind,
			resourceName: resourceName,
			reason:       api.EventReasonResourceUpdated,
			message:      formatResourceActionMessage(resourceKind, "updated", &updatedFieldsStr),
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
	// Ignore 4XX status codes
	if status.Code >= 400 && status.Code < 500 {
		return nil
	}

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
		message:      formatResourceActionMessage(resourceKind, "deleted", nil),
		details:      nil,
	})
}

// GetEnrollmentRequestApprovedEvent creates an event for enrollment request approval
func GetEnrollmentRequestApprovedEvent(ctx context.Context, resourceName string, log logrus.FieldLogger) *api.Event {
	return getBaseEvent(ctx, resourceEvent{
		resourceKind: api.EnrollmentRequestKind,
		resourceName: resourceName,
		reason:       api.EventReasonEnrollmentRequestApproved,
		message:      formatResourceActionMessage(api.EnrollmentRequestKind, "approved", nil),
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
		message:      formatResourceActionMessage(api.DeviceKind, "decommissioned", nil),
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
		DetailType:     api.DeviceMultipleOwnersDetected,
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
		DetailType:             api.DeviceMultipleOwnersResolved,
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

// GetFleetSpecValidEvent creates an event for fleet spec becoming valid
func GetFleetSpecValidEvent(ctx context.Context, fleetName string) *api.Event {
	return getBaseEvent(ctx, resourceEvent{
		resourceKind: api.FleetKind,
		resourceName: fleetName,
		reason:       api.EventReasonFleetValid,
		message:      "Fleet specification is valid.",
		details:      nil,
	})
}

// GetFleetSpecInvalidEvent creates an event for fleet spec becoming invalid
func GetFleetSpecInvalidEvent(ctx context.Context, fleetName string, message string) *api.Event {
	msg := fmt.Sprintf("Fleet specification is invalid: %s.", message)

	return getBaseEvent(ctx, resourceEvent{
		resourceKind: api.FleetKind,
		resourceName: fleetName,
		reason:       api.EventReasonFleetInvalid,
		message:      msg,
		details:      nil,
	})
}

// GetInternalTaskFailedEvent creates an event for internal task failures
func GetInternalTaskFailedEvent(ctx context.Context, resourceKind api.ResourceKind, resourceName string, taskType string, errorMessage string, retryCount *int, taskParameters map[string]string, log logrus.FieldLogger) *api.Event {
	message := formatInternalTaskFailedMessage(resourceKind, taskType, errorMessage)

	details := api.EventDetails{}
	detailsStruct := api.InternalTaskFailedDetails{
		DetailType:     api.InternalTaskFailed,
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
		message:      fmt.Sprintf("New commit detected: %s.", commitHash),
		details:      nil,
	})
}

func GetResourceSyncAccessibleEvent(ctx context.Context, resourceName string) *api.Event {
	return getBaseEvent(ctx, resourceEvent{
		resourceKind: api.ResourceSyncKind,
		resourceName: resourceName,
		reason:       api.EventReasonResourceSyncAccessible,
		message:      "Repository is accessible.",
		details:      nil,
	})
}

func GetResourceSyncInaccessibleEvent(ctx context.Context, resourceName string, errorMessage string) *api.Event {
	return getBaseEvent(ctx, resourceEvent{
		resourceKind: api.ResourceSyncKind,
		resourceName: resourceName,
		reason:       api.EventReasonResourceSyncInaccessible,
		message:      fmt.Sprintf("Repository is inaccessible: %s.", errorMessage),
		details:      nil,
	})
}

func GetResourceSyncParsedEvent(ctx context.Context, resourceName string) *api.Event {
	return getBaseEvent(ctx, resourceEvent{
		resourceKind: api.ResourceSyncKind,
		resourceName: resourceName,
		reason:       api.EventReasonResourceSyncParsed,
		message:      "Resources parsed successfully.",
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
		message:      "Resources synced successfully.",
		details:      nil,
	})
}

func GetResourceSyncSyncFailedEvent(ctx context.Context, resourceName string, errorMessage string) *api.Event {
	return getBaseEvent(ctx, resourceEvent{
		resourceKind: api.ResourceSyncKind,
		resourceName: resourceName,
		reason:       api.EventReasonResourceSyncSyncFailed,
		message:      fmt.Sprintf("Resource sync failed: %s.", errorMessage),
		details:      nil,
	})
}

// GetFleetRolloutNewEvent creates an event for fleet rollout creation
func GetFleetRolloutNewEvent(ctx context.Context, name string) *api.Event {
	return getBaseEvent(ctx, resourceEvent{
		resourceKind: api.FleetKind,
		resourceName: name,
		reason:       api.EventReasonFleetRolloutCreated,
		message:      "Fleet rollout created.",
		details:      nil,
	})
}

// GetFleetRolloutBatchCompletedEvent creates an event for fleet rollout completion
func GetFleetRolloutBatchCompletedEvent(ctx context.Context, name string, deployingTemplateVersion string, report *api.RolloutBatchCompletionReport) *api.Event {
	details := api.FleetRolloutBatchCompletedDetails{
		DetailType:        api.FleetRolloutBatchCompleted,
		TemplateVersion:   deployingTemplateVersion,
		Batch:             report.BatchName,
		SuccessPercentage: report.SuccessPercentage,
		Total:             report.Total,
		Successful:        report.Successful,
		Failed:            report.Failed,
		TimedOut:          report.TimedOut,
	}
	eventDetails := api.EventDetails{}
	if err := eventDetails.FromFleetRolloutBatchCompletedDetails(details); err != nil {
		// If serialization fails, return nil rather than panicking
		return nil
	}
	return getBaseEvent(ctx, resourceEvent{
		resourceKind: api.FleetKind,
		resourceName: name,
		reason:       api.EventReasonFleetRolloutBatchCompleted,
		message:      fmt.Sprintf("Fleet rollout batch %s completed with %d%% success rate.", report.BatchName, report.SuccessPercentage),
		details:      &eventDetails,
	})
}

// GetFleetRolloutStartedEvent creates an event for fleet rollout start
func GetFleetRolloutStartedEvent(ctx context.Context, templateVersionName string, fleetName string, immediateRollout bool, policyRemoved bool) *api.Event {
	rolloutType := api.Batched
	if immediateRollout {
		rolloutType = "None"
	}
	details := api.FleetRolloutStartedDetails{
		DetailType:      api.FleetRolloutStarted,
		RolloutStrategy: rolloutType,
		TemplateVersion: templateVersionName,
	}
	eventDetails := api.EventDetails{}
	if err := eventDetails.FromFleetRolloutStartedDetails(details); err != nil {
		// If serialization fails, return nil rather than panicking
		return nil
	}

	message := "Fleet rollout started."
	if policyRemoved {
		message = "Fleet rollout started due to policy removal."
	}

	return getBaseEvent(ctx, resourceEvent{
		resourceKind: api.FleetKind,
		resourceName: fleetName,
		reason:       api.EventReasonFleetRolloutStarted,
		message:      message,
		details:      &eventDetails,
	})
}

// GetFleetRolloutDeviceSelectedEvent creates an event for fleet rollout device selection
func GetFleetRolloutDeviceSelectedEvent(ctx context.Context, deviceName string, fleetName string, templateVersion string) *api.Event {
	details := api.FleetRolloutDeviceSelectedDetails{
		DetailType:      api.FleetRolloutDeviceSelected,
		FleetName:       fleetName,
		TemplateVersion: templateVersion,
	}
	eventDetails := api.EventDetails{}
	if err := eventDetails.FromFleetRolloutDeviceSelectedDetails(details); err != nil {
		// If serialization fails, return nil rather than panicking
		return nil
	}
	return getBaseEvent(ctx, resourceEvent{
		resourceKind: api.DeviceKind,
		resourceName: deviceName,
		reason:       api.EventReasonFleetRolloutDeviceSelected,
		message:      fmt.Sprintf("Device was selected for update while rolling out fleet %s with template version %s.", fleetName, templateVersion),
		details:      &eventDetails,
	})
}

// GetFleetRolloutBatchDispatchedEvent creates an event for fleet rollout batch dispatch
func GetFleetRolloutBatchDispatchedEvent(ctx context.Context, fleetName string, templateVersion string, batch string) *api.Event {
	details := api.FleetRolloutBatchDispatchedDetails{
		DetailType:      api.FleetRolloutBatchDispatched,
		TemplateVersion: templateVersion,
		Batch:           batch,
	}
	eventDetails := api.EventDetails{}
	if err := eventDetails.FromFleetRolloutBatchDispatchedDetails(details); err != nil {
		// If serialization fails, return nil rather than panicking
		return nil
	}
	return getBaseEvent(ctx, resourceEvent{
		resourceKind: api.FleetKind,
		resourceName: fleetName,
		reason:       api.EventReasonFleetRolloutBatchDispatched,
		message:      "Fleet rollout batch dispatched.",
		details:      &eventDetails,
	})
}

// GetFleetRolloutCompletedEvent creates an event for fleet rollout completion
func GetFleetRolloutCompletedEvent(ctx context.Context, name string, templateVersion string) *api.Event {
	details := api.FleetRolloutCompletedDetails{
		DetailType:      api.FleetRolloutCompleted,
		TemplateVersion: templateVersion,
	}
	eventDetails := api.EventDetails{}
	if err := eventDetails.FromFleetRolloutCompletedDetails(details); err != nil {
		// If serialization fails, return nil rather than panicking
		return nil
	}
	return getBaseEvent(ctx, resourceEvent{
		resourceKind: api.FleetKind,
		resourceName: name,
		reason:       api.EventReasonFleetRolloutCompleted,
		message:      "Fleet rollout completed.",
		details:      &eventDetails,
	})
}

// GetFleetRolloutFailedEvent creates an event for fleet rollout failure
func GetFleetRolloutFailedEvent(ctx context.Context, name string, deployingTemplateVersion string, message string) *api.Event {
	details := api.FleetRolloutFailedDetails{
		DetailType:      api.FleetRolloutFailed,
		TemplateVersion: deployingTemplateVersion,
	}
	eventDetails := api.EventDetails{}
	if err := eventDetails.FromFleetRolloutFailedDetails(details); err != nil {
		// If serialization fails, return nil rather than panicking
		return nil
	}
	return getBaseEvent(ctx, resourceEvent{
		resourceKind: api.FleetKind,
		resourceName: name,
		reason:       api.EventReasonFleetRolloutFailed,
		message:      message,
		details:      &eventDetails,
	})
}

// GetRepositoryAccessibleEvent creates an event for repository accessibility
func GetRepositoryAccessibleEvent(ctx context.Context, name string) *api.Event {
	return getBaseEvent(ctx, resourceEvent{
		resourceKind: api.RepositoryKind,
		resourceName: name,
		reason:       api.EventReasonRepositoryAccessible,
		message:      "Repository is accessible.",
		details:      nil,
	})
}

// GetRepositoryInaccessibleEvent creates an event for repository inaccessibility
func GetRepositoryInaccessibleEvent(ctx context.Context, name string, errorMessage string) *api.Event {
	return getBaseEvent(ctx, resourceEvent{
		resourceKind: api.RepositoryKind,
		resourceName: name,
		reason:       api.EventReasonRepositoryInaccessible,
		message:      fmt.Sprintf("Repository is inaccessible: %s.", errorMessage),
		details:      nil,
	})
}

// GetReferencedRepositoryUpdatedEvent creates an event for a referenced repository being updated
func GetReferencedRepositoryUpdatedEvent(ctx context.Context, kind api.ResourceKind, name, repositoryName string) *api.Event {
	details := api.ReferencedRepositoryUpdatedDetails{
		DetailType: api.ReferencedRepositoryUpdated,
		Repository: repositoryName,
	}
	eventDetails := api.EventDetails{}
	if err := eventDetails.FromReferencedRepositoryUpdatedDetails(details); err != nil {
		// If serialization fails, return nil rather than panicking
		return nil
	}
	return getBaseEvent(ctx, resourceEvent{
		resourceKind: kind,
		resourceName: name,
		reason:       api.EventReasonReferencedRepositoryUpdated,
		message:      fmt.Sprintf("Referenced repository %s updated.", repositoryName),
		details:      &eventDetails,
	})
}
