package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/service"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/internal/worker_client"
	"github.com/flightctl/flightctl/pkg/queues"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
)

// QueueMaintenanceTask handles queue maintenance operations including:
// - Processing timed out messages
// - Retrying failed messages
// - Checkpoint advancement based on in-flight task completion tracking
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
	retryCount, err := t.retryFailedMessages(ctx, log)
	if err != nil {
		log.WithError(err).Error("Failed to retry failed messages")
	} else if retryCount > 0 {
		log.WithField("retryCount", retryCount).Info("Retried failed messages")
	} else {
		log.Debug("No failed messages to retry")
	}

	// Advance checkpoint based on in-flight task tracking and handle Redis failure recovery
	if err := t.advanceCheckpoint(ctx, log); err != nil {
		log.WithError(err).Error("Failed to advance checkpoint")
	}

	// Sync the Redis checkpoint to the database for persistence and cross-service visibility
	if err := t.syncCheckpointToDatabase(ctx, log); err != nil {
		// Log the error but don't fail the entire operation since Redis checkpoint is primary
		log.WithError(err).Warn("Failed to sync checkpoint to database")
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

// retryFailedMessages retries failed messages and handles permanently failed messages
func (t *QueueMaintenanceTask) retryFailedMessages(ctx context.Context, log logrus.FieldLogger) (int, error) {
	return t.queuesProvider.RetryFailedMessages(ctx, consts.TaskQueue, queues.DefaultRetryConfig(), func(entryID string, body []byte, retryCount int) error {
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
}

// advanceCheckpoint advances the global checkpoint based on in-flight task completion tracking
func (t *QueueMaintenanceTask) advanceCheckpoint(ctx context.Context, log logrus.FieldLogger) error {
	// Use atomic operation to scan in-flight tasks, advance checkpoint, and cleanup completed tasks
	// This handles the timestamp scanning, comparison and prevents races between multiple instances
	err := t.queuesProvider.AdvanceCheckpointAndCleanup(ctx)
	if err != nil {
		if errors.Is(err, queues.ErrCheckpointMissing) {
			log.Warn("Redis checkpoint missing, initiating recovery process")
			if recoveryErr := t.recoverFromMissingCheckpoint(ctx, log); recoveryErr != nil {
				return fmt.Errorf("failed to recover from missing checkpoint: %w", recoveryErr)
			}
			// After recovery, try to advance checkpoint again
			if retryErr := t.queuesProvider.AdvanceCheckpointAndCleanup(ctx); retryErr != nil {
				return fmt.Errorf("failed to advance checkpoint after recovery: %w", retryErr)
			}
			log.Info("Successfully recovered from missing checkpoint and advanced checkpoint")
		} else {
			return fmt.Errorf("failed to advance checkpoint atomically: %w", err)
		}
	}

	return nil
}

// syncCheckpointToDatabase reads the current checkpoint from Redis and stores it in the database
func (t *QueueMaintenanceTask) syncCheckpointToDatabase(ctx context.Context, log logrus.FieldLogger) error {
	// Get the current checkpoint timestamp from Redis
	checkpointTime, err := t.queuesProvider.GetLatestProcessedTimestamp(ctx)
	if err != nil {
		return fmt.Errorf("failed to get checkpoint from Redis: %w", err)
	}
	// If checkpoint is zero, nothing to sync
	if checkpointTime.IsZero() {
		log.Debug("No checkpoint to sync - Redis checkpoint is zero")
		return nil
	}
	// Convert timestamp to bytes for storage
	checkpointBytes := []byte(checkpointTime.Format(time.RFC3339Nano))
	// Store in database using task queue consumer and global checkpoint key
	status := t.serviceHandler.SetCheckpoint(ctx, consts.CheckpointConsumerTaskQueue, consts.CheckpointKeyGlobal, checkpointBytes)
	if status.Code >= 400 {
		return fmt.Errorf("failed to set checkpoint in database: %s", status.Message)
	}
	log.WithField("checkpoint", checkpointTime.Format(time.RFC3339Nano)).Debug("Successfully synced checkpoint to database")
	return nil
}

// recoverFromMissingCheckpoint handles recovery when Redis checkpoint is missing
func (t *QueueMaintenanceTask) recoverFromMissingCheckpoint(ctx context.Context, log logrus.FieldLogger) error {
	log.Info("Starting checkpoint recovery process")

	// Try to get the last known checkpoint from the database
	dbCheckpointBytes, status := t.serviceHandler.GetCheckpoint(ctx, consts.CheckpointConsumerTaskQueue, consts.CheckpointKeyGlobal)

	var lastCheckpointTime time.Time
	if status.Code >= 400 {
		if status.Code == 404 {
			log.Info("No database checkpoint found (fresh system), initializing Redis checkpoint to zero")
			// lastCheckpointTime remains zero value
		} else {
			return fmt.Errorf("failed to get checkpoint from database: %s", status.Message)
		}
	} else {
		// Success case - parse the database checkpoint
		if parsedTime, err := time.Parse(time.RFC3339Nano, string(dbCheckpointBytes)); err == nil {
			lastCheckpointTime = parsedTime
			log.WithField("lastCheckpoint", lastCheckpointTime.Format(time.RFC3339Nano)).Info("Found last checkpoint in database")
		} else {
			log.WithError(err).Warn("Failed to parse database checkpoint, treating as fresh system")
			lastCheckpointTime = time.Time{}
		}
	}

	// If we have a valid checkpoint, republish events since that time
	if !lastCheckpointTime.IsZero() {
		log.WithField("since", lastCheckpointTime.Format(time.RFC3339Nano)).Info("Republishing events since last checkpoint")
		if err := t.republishEventsSince(ctx, lastCheckpointTime, log); err != nil {
			return fmt.Errorf("failed to republish events: %w", err)
		}
	}

	// Set Redis checkpoint to zero to initialize the system
	if err := t.queuesProvider.SetCheckpointTimestamp(ctx, time.Time{}); err != nil {
		return fmt.Errorf("failed to initialize Redis checkpoint: %w", err)
	}

	log.Info("Checkpoint recovery completed successfully")
	return nil
}

// republishEventsSince republishes all events from the database since the given timestamp
func (t *QueueMaintenanceTask) republishEventsSince(ctx context.Context, since time.Time, log logrus.FieldLogger) error {
	log.WithField("since", since.Format(time.RFC3339Nano)).Info("Starting event republishing")

	// Create publisher on-demand for recovery operations
	publisher, err := t.queuesProvider.NewPublisher(ctx, consts.TaskQueue)
	if err != nil {
		return fmt.Errorf("failed to create publisher for event republishing: %w", err)
	}
	defer publisher.Close() // Ensure publisher is closed after use

	// First, get all organizations
	orgList, status := t.serviceHandler.ListOrganizations(ctx)
	if status.Code >= 400 {
		return fmt.Errorf("failed to list organizations: %s", status.Message)
	}

	var totalEventCount int

	// Iterate through each organization
	for _, org := range orgList.Items {
		orgID, err := uuid.Parse(*org.Metadata.Name)
		if err != nil {
			log.WithError(err).WithField("orgName", *org.Metadata.Name).Warn("Failed to parse organization ID, skipping")
			continue
		}

		// Create organization context
		orgCtx := util.WithOrganizationID(ctx, orgID)
		orgLog := log.WithField("orgId", orgID.String())

		orgLog.Debug("Processing events for organization")

		// Create field selector to get events since the given timestamp
		// Using the same format as alert_exporter: metadata.creationTimestamp>=timestamp
		fieldSelector := fmt.Sprintf("metadata.creationTimestamp>=%s", since.Format(time.RFC3339Nano))

		// Create list parameters with field selector and ascending order (oldest first)
		params := api.ListEventsParams{
			FieldSelector: &fieldSelector,
			Order:         lo.ToPtr(api.Asc),     // Process oldest events first
			Limit:         lo.ToPtr(int32(1000)), // Process in batches of 1000
		}

		var orgEventCount int
		var continueToken *string

		for {
			// Set continuation token for pagination
			if continueToken != nil {
				params.Continue = continueToken
			}

			// List events from database for this organization
			eventList, status := t.serviceHandler.ListEvents(orgCtx, params)
			if status.Code >= 400 {
				orgLog.WithError(fmt.Errorf("status: %s", status.Message)).Warn("Failed to list events for organization, continuing with next")
				break
			}

			orgLog.WithField("eventCount", len(eventList.Items)).Debug("Retrieved events from database")
			if len(eventList.Items) == 0 {
				break // No more events for this organization
			}

			// Republish each event to the queue
			for _, event := range eventList.Items {
				eventName := lo.FromPtrOr(event.Metadata.Name, "<unnamed>")
				orgLog.WithField("eventName", eventName).Debug("Attempting to republish event")
				if err := t.republishSingleEvent(orgCtx, &event, orgID, publisher, orgLog); err != nil {
					orgLog.WithError(err).WithField("eventName", eventName).Error("Failed to republish event, continuing with next")
					continue
				}
				orgLog.WithField("eventName", eventName).Debug("Successfully republished event")
				orgEventCount++
			}

			// Check if there are more events to process
			if eventList.Metadata.Continue == nil || *eventList.Metadata.Continue == "" {
				break
			}
			continueToken = eventList.Metadata.Continue
		}

		if orgEventCount > 0 {
			orgLog.WithField("eventCount", orgEventCount).Info("Completed event republishing for organization")
		} else {
			orgLog.Debug("No events to republish for organization")
		}
		totalEventCount += orgEventCount
	}

	log.WithField("totalEventCount", totalEventCount).WithField("organizationCount", len(orgList.Items)).Info("Completed event republishing for all organizations")
	return nil
}

// republishSingleEvent republishes a single event to the queue using the provided publisher
func (t *QueueMaintenanceTask) republishSingleEvent(ctx context.Context, event *api.Event, orgID uuid.UUID, publisher queues.Publisher, log logrus.FieldLogger) error {
	// Create the event wrapper with the correct orgId
	eventWithOrgId := worker_client.EventWithOrgId{
		OrgId: orgID,
		Event: *event,
	}

	// Marshal the event
	eventBytes, err := json.Marshal(eventWithOrgId)
	if err != nil {
		return fmt.Errorf("failed to marshal event: %w", err)
	}

	// Use the event's creation timestamp
	var timestamp int64
	if event.Metadata.CreationTimestamp != nil {
		timestamp = event.Metadata.CreationTimestamp.UnixMicro()
	} else {
		timestamp = time.Now().UnixMicro()
	}

	// Publish to the queue using the provided publisher
	if err := publisher.Publish(ctx, eventBytes, timestamp); err != nil {
		log.WithError(err).WithField("eventName", lo.FromPtrOr(event.Metadata.Name, "<unnamed>")).Error("Failed to publish event to queue")
		return fmt.Errorf("failed to publish event to queue: %w", err)
	}

	eventName := lo.FromPtrOr(event.Metadata.Name, "<unnamed>")
	log.WithField("eventName", eventName).
		WithField("timestamp", timestamp).
		Debug("Successfully republished event")

	return nil
}
