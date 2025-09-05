package tasks

import (
	"context"
	"encoding/json"
	"fmt"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/service"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/internal/worker_client"
	"github.com/flightctl/flightctl/pkg/queues"
	"github.com/sirupsen/logrus"
)

// QueueMaintenanceTask handles queue maintenance operations including:
// - Processing timed out messages
// - Retrying failed messages
// - Future: Event replay in case Redis failed
type QueueMaintenanceTask struct {
	log            logrus.FieldLogger
	serviceHandler service.Service
	queuesProvider queues.Provider
}

// NewQueueMaintenanceTask creates a new queue maintenance task
func NewQueueMaintenanceTask(log logrus.FieldLogger, serviceHandler service.Service, queuesProvider queues.Provider) *QueueMaintenanceTask {
	return &QueueMaintenanceTask{
		log:            log,
		serviceHandler: serviceHandler,
		queuesProvider: queuesProvider,
	}
}

// Execute runs the queue maintenance task (system-wide, no organization context needed)
func (t *QueueMaintenanceTask) Execute(ctx context.Context) error {
	log := t.log
	log.Info("Starting queue maintenance")

	// Process timed out messages for the current organization's queue
	// Use the same timeout as event processing for consistency
	timeoutCount, err := t.processTimedOutMessages(ctx, log)
	if err != nil {
		log.WithError(err).Error("Failed to process timed out messages")
	} else if timeoutCount > 0 {
		log.WithField("timeoutCount", timeoutCount).Info("Processed timed out messages")
	} else {
		log.Debug("No timed out messages to process")
	}

	// Retry failed messages using the provider's retry config
	// We need to get the retry config from the provider to pass it here
	// For now, we'll use a reasonable default that matches the provider's config
	retryCount, err := t.queuesProvider.RetryFailedMessages(ctx, consts.TaskQueue, queues.DefaultRetryConfig(), func(entryID string, body []byte, retryCount int) error {
		log.WithField("entryID", entryID).WithField("retryCount", retryCount).Debug("Processing permanently failed message")

		// Try to extract the original event from the message body
		var originalEvent api.Event
		var eventWithOrgId worker_client.EventWithOrgId

		// Parse the body as JSON to extract the original event
		if err := json.Unmarshal(body, &eventWithOrgId); err == nil {
			// Successfully parsed the original event
			originalEvent = eventWithOrgId.Event

			// Ensure event is emitted under the correct organization
			orgCtx := util.WithOrganizationID(ctx, eventWithOrgId.OrgId)

			resourceKind := api.ResourceKind(api.SystemKind)
			resourceName := api.SystemComponentQueue
			errorMessage := fmt.Sprintf("Message processing permanently failed after %d retry attempts", retryCount)
			message := fmt.Sprintf("%s internal task failed: %s - %s.", resourceKind, originalEvent.Reason, errorMessage)
			event := api.GetBaseEvent(orgCtx,
				resourceKind,
				resourceName,
				api.EventReasonInternalTaskPermanentlyFailed,
				message,
				nil)

			details := api.EventDetails{}
			if detailsErr := details.FromInternalTaskPermanentlyFailedDetails(api.InternalTaskPermanentlyFailedDetails{
				ErrorMessage:  errorMessage,
				RetryCount:    retryCount,
				OriginalEvent: originalEvent,
			}); detailsErr == nil {
				event.Details = &details
			}

			// Emit the event
			t.serviceHandler.CreateEvent(orgCtx, event)

			log.WithField("entryID", entryID).
				WithField("resourceKind", resourceKind).
				WithField("resourceName", resourceName).
				WithField("retryCount", retryCount).
				Info("Emitted InternalTaskPermanentlyFailedEvent for permanently failed message")
		} else {
			// Failed to parse the original event, just log an error
			log.WithField("entryID", entryID).WithError(err).Error("Failed to parse original event from message body, skipping event emission for permanently failed message")
		}

		return nil
	})
	if err != nil {
		log.WithError(err).Error("Failed to retry failed messages")
	} else if retryCount > 0 {
		log.WithField("retryCount", retryCount).Info("Retried failed messages")
	} else {
		log.Debug("No failed messages to retry")
	}

	log.Info("Completed queue maintenance")
	return nil
}

// processTimedOutMessages processes timed out messages and emits appropriate events
func (t *QueueMaintenanceTask) processTimedOutMessages(ctx context.Context, log logrus.FieldLogger) (int, error) {
	return t.queuesProvider.ProcessTimedOutMessages(ctx, consts.TaskQueue, EventProcessingTimeout, func(entryID string, body []byte) error {
		log.WithField("entryID", entryID).Debug("Processing timed out message")

		// Try to extract the original event from the message body
		var originalEvent api.Event
		var eventWithOrgId worker_client.EventWithOrgId

		// Parse the body as JSON to extract the original event
		if err := json.Unmarshal(body, &eventWithOrgId); err == nil {
			// Successfully parsed the original event
			originalEvent = eventWithOrgId.Event

			// Ensure event is emitted under the correct organization
			orgCtx := util.WithOrganizationID(ctx, eventWithOrgId.OrgId)

			// Use the original event's resource information for better context
			resourceKind := originalEvent.InvolvedObject.Kind
			resourceName := originalEvent.InvolvedObject.Name

			// Emit InternalTaskFailedEvent using the original event
			EmitInternalTaskFailedEvent(orgCtx,
				fmt.Sprintf("Message processing timed out after %v", EventProcessingTimeout),
				originalEvent,
				t.serviceHandler)

			log.WithField("entryID", entryID).
				WithField("resourceKind", resourceKind).
				WithField("resourceName", resourceName).
				Info("Emitted InternalTaskFailedEvent for timed out message")
		} else {
			// Failed to parse the original event, just log an error
			log.WithField("entryID", entryID).WithError(err).Error("Failed to parse original event from message body, skipping event emission")
		}

		return nil
	})
}
