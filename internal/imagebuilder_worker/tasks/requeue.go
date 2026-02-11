package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	coredomain "github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/imagebuilder_api/domain"
	imagebuilderapi "github.com/flightctl/flightctl/internal/imagebuilder_api/service"
	"github.com/flightctl/flightctl/internal/service/common"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/worker_client"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
)

// RunRequeueOnStartup runs the requeue task once on startup
// This method is public to allow testing with mocked services
func (c *Consumer) RunRequeueOnStartup(ctx context.Context) {
	c.log.Debug("Running requeue task on startup")
	c.executeRequeue(ctx)
	c.log.Debug("Startup requeue task completed")
}

// executeRequeue performs the requeue check and requeues tasks as needed
func (c *Consumer) executeRequeue(ctx context.Context) {
	log := c.log.WithField("task", "requeue")
	log.Debug("Starting periodic requeue task")

	// List all organizations
	orgs, err := c.mainStore.Organization().List(ctx, store.ListParams{})
	if err != nil {
		log.WithError(err).Error("Failed to list organizations")
		return
	}

	totalRequeued := 0
	totalFailed := 0
	for _, org := range orgs {
		requeued, failed, err := c.requeueForOrg(ctx, org.ID, log)
		if err != nil {
			log.WithError(err).WithField("orgId", org.ID).Error("Failed to requeue tasks for organization")
			continue
		}
		totalRequeued += requeued
		totalFailed += failed
	}

	if totalRequeued > 0 || totalFailed > 0 {
		log.WithField("requeued", totalRequeued).WithField("movedToFail", totalFailed).Info("Periodic requeue task completed")
	} else {
		log.Debug("Periodic requeue task completed - no tasks requeued or moved to fail")
	}
}

// requeueForOrg checks and requeues imagebuild and imageexport tasks for a specific organization
// Only requeues resources that are in Pending state (haven't started processing)
// For resources that have started processing but aren't Completed/Failed, marks them as Failed
// Returns the number of resources requeued and the number moved to fail
func (c *Consumer) requeueForOrg(ctx context.Context, orgID uuid.UUID, log logrus.FieldLogger) (int, int, error) {
	requeuedCount := 0
	failedCount := 0

	// Get all ImageBuilds that are not in terminal states
	// Use field selector to exclude Completed, Failed, and Canceled statuses
	fieldSelectorStr := "status.conditions.ready.reason notin (Completed, Failed, Canceled)"
	imageBuilds, status := c.imageBuilderService.ImageBuild().List(ctx, orgID, domain.ListImageBuildsParams{
		FieldSelector: &fieldSelectorStr,
	})
	if !imagebuilderapi.IsStatusOK(status) {
		return requeuedCount, failedCount, fmt.Errorf("failed to list imagebuilds: %v", status)
	}
	if imageBuilds == nil {
		return requeuedCount, failedCount, fmt.Errorf("imageBuilds list is nil")
	}

	for _, imageBuild := range imageBuilds.Items {
		if c.shouldRequeueImageBuild(&imageBuild) {
			// Only requeue if Pending (hasn't started processing)
			if err := c.requeueImageBuild(ctx, orgID, &imageBuild, log); err != nil {
				log.WithError(err).WithField("imageBuild", lo.FromPtr(imageBuild.Metadata.Name)).Error("Failed to requeue imageBuild")
				continue
			}
			requeuedCount++
		} else if c.isImageBuildCanceling(&imageBuild) {
			// Was being canceled when worker stopped - mark as Canceled
			if err := c.markImageBuildAsCanceled(ctx, orgID, &imageBuild, log); err != nil {
				log.WithError(err).WithField("imageBuild", lo.FromPtr(imageBuild.Metadata.Name)).Error("Failed to mark imageBuild as canceled")
				continue
			}
			failedCount++
		} else {
			// Has started processing but isn't Completed/Failed/Canceled - mark as Failed
			if err := c.markImageBuildAsFailed(ctx, orgID, &imageBuild, log); err != nil {
				log.WithError(err).WithField("imageBuild", lo.FromPtr(imageBuild.Metadata.Name)).Error("Failed to mark imageBuild as failed")
				continue
			}
			failedCount++
		}
	}

	// Get all ImageExports that are not completed or failed
	imageExports, status := c.imageBuilderService.ImageExport().List(ctx, orgID, domain.ListImageExportsParams{
		FieldSelector: &fieldSelectorStr,
	})
	if !imagebuilderapi.IsStatusOK(status) {
		return requeuedCount, failedCount, fmt.Errorf("failed to list imageexports: %v", status)
	}
	if imageExports == nil {
		return requeuedCount, failedCount, fmt.Errorf("imageExports list is nil")
	}

	for _, imageExport := range imageExports.Items {
		if c.shouldRequeueImageExport(&imageExport) {
			// Only requeue if Pending (hasn't started processing)
			if err := c.requeueImageExport(ctx, orgID, &imageExport, log); err != nil {
				log.WithError(err).WithField("imageExport", lo.FromPtr(imageExport.Metadata.Name)).Error("Failed to requeue imageExport")
				continue
			}
			requeuedCount++
		} else if c.isImageExportCanceling(&imageExport) {
			// Was being canceled when worker stopped - mark as Canceled
			if err := c.markImageExportAsCanceled(ctx, orgID, &imageExport, log); err != nil {
				log.WithError(err).WithField("imageExport", lo.FromPtr(imageExport.Metadata.Name)).Error("Failed to mark imageExport as canceled")
				continue
			}
			failedCount++
		} else {
			// Has started processing but isn't Completed/Failed/Canceled - mark as Failed
			if err := c.markImageExportAsFailed(ctx, orgID, &imageExport, log); err != nil {
				log.WithError(err).WithField("imageExport", lo.FromPtr(imageExport.Metadata.Name)).Error("Failed to mark imageExport as failed")
				continue
			}
			failedCount++
		}
	}

	return requeuedCount, failedCount, nil
}

// shouldRequeueImageBuild checks if an ImageBuild should be requeued
// Returns true only if the resource is in Pending state (hasn't started processing)
// Returns false if it has started processing (Building, Pushing) or is in a terminal state
// Note: Field selector guarantees status.conditions.ready.reason exists
func (c *Consumer) shouldRequeueImageBuild(imageBuild *domain.ImageBuild) bool {
	// Find Ready condition - field selector guarantees it exists
	readyCondition := domain.FindImageBuildStatusCondition(*imageBuild.Status.Conditions, domain.ImageBuildConditionTypeReady)
	if readyCondition == nil {
		// Should not reach here due to field selector, but defensive return
		return false
	}
	// Only requeue if Pending
	return readyCondition.Reason == string(domain.ImageBuildConditionReasonPending)
}

// isImageBuildCanceling checks if an ImageBuild is in Canceling state
func (c *Consumer) isImageBuildCanceling(imageBuild *domain.ImageBuild) bool {
	if imageBuild.Status == nil || imageBuild.Status.Conditions == nil {
		return false
	}
	readyCondition := domain.FindImageBuildStatusCondition(*imageBuild.Status.Conditions, domain.ImageBuildConditionTypeReady)
	if readyCondition == nil {
		return false
	}
	return readyCondition.Reason == string(domain.ImageBuildConditionReasonCanceling)
}

// shouldRequeueImageExport checks if an ImageExport should be requeued
// Returns true only if the resource is in Pending state (hasn't started processing)
// Returns false if it has started processing (Converting, Pushing) or is in a terminal state
// Note: Field selector guarantees status.conditions.ready.reason exists
func (c *Consumer) shouldRequeueImageExport(imageExport *domain.ImageExport) bool {
	// Find Ready condition - field selector guarantees it exists
	readyCondition := domain.FindImageExportStatusCondition(*imageExport.Status.Conditions, domain.ImageExportConditionTypeReady)
	if readyCondition == nil {
		// Should not reach here due to field selector, but defensive return
		return false
	}
	// Only requeue if Pending
	return readyCondition.Reason == string(domain.ImageExportConditionReasonPending)
}

// isImageExportCanceling checks if an ImageExport is in Canceling state
func (c *Consumer) isImageExportCanceling(imageExport *domain.ImageExport) bool {
	if imageExport.Status == nil || imageExport.Status.Conditions == nil {
		return false
	}
	readyCondition := domain.FindImageExportStatusCondition(*imageExport.Status.Conditions, domain.ImageExportConditionTypeReady)
	if readyCondition == nil {
		return false
	}
	return readyCondition.Reason == string(domain.ImageExportConditionReasonCanceling)
}

// requeueImageBuild requeues an ImageBuild by creating and enqueueing a ResourceCreated event
func (c *Consumer) requeueImageBuild(ctx context.Context, orgID uuid.UUID, imageBuild *domain.ImageBuild, log logrus.FieldLogger) error {
	name := lo.FromPtr(imageBuild.Metadata.Name)
	if name == "" {
		return fmt.Errorf("imageBuild name is empty")
	}

	// Create ResourceCreated event
	event := common.GetResourceCreatedOrUpdatedSuccessEvent(
		ctx,
		true,
		coredomain.ResourceKind(string(domain.ResourceKindImageBuild)),
		name,
		nil,
		log,
		nil,
	)
	if event == nil {
		return fmt.Errorf("failed to create event")
	}

	// Enqueue the event
	return c.enqueueRequeueEvent(ctx, orgID, event, log)
}

// requeueImageExport requeues an ImageExport by creating and enqueueing a ResourceCreated event
func (c *Consumer) requeueImageExport(ctx context.Context, orgID uuid.UUID, imageExport *domain.ImageExport, log logrus.FieldLogger) error {
	name := lo.FromPtr(imageExport.Metadata.Name)
	if name == "" {
		return fmt.Errorf("imageExport name is empty")
	}

	// Create ResourceCreated event
	event := common.GetResourceCreatedOrUpdatedSuccessEvent(
		ctx,
		true,
		coredomain.ResourceKind(string(domain.ResourceKindImageExport)),
		name,
		nil,
		log,
		nil,
	)
	if event == nil {
		return fmt.Errorf("failed to create event")
	}

	// Enqueue the event
	return c.enqueueRequeueEvent(ctx, orgID, event, log)
}

// enqueueRequeueEvent enqueues an event to the imagebuild queue
func (c *Consumer) enqueueRequeueEvent(ctx context.Context, orgID uuid.UUID, event *coredomain.Event, log logrus.FieldLogger) error {
	// Create EventWithOrgId structure for the queue
	eventWithOrgId := worker_client.EventWithOrgId{
		OrgId: orgID,
		Event: *event,
	}

	payload, err := json.Marshal(eventWithOrgId)
	if err != nil {
		return fmt.Errorf("failed to marshal event: %w", err)
	}

	// Use creation timestamp if available, otherwise use current time
	var timestamp int64
	if event.Metadata.CreationTimestamp != nil {
		timestamp = event.Metadata.CreationTimestamp.UnixMicro()
	} else {
		timestamp = time.Now().UnixMicro()
	}

	if err := c.queueProducer.Enqueue(ctx, payload, timestamp); err != nil {
		return fmt.Errorf("failed to enqueue event: %w", err)
	}

	log.WithField("orgId", orgID).
		WithField("kind", event.InvolvedObject.Kind).
		WithField("name", event.InvolvedObject.Name).
		Debug("Requeued task")
	return nil
}

// markImageBuildAsFailed marks an ImageBuild as Failed because it was in a non-terminal state on startup
func (c *Consumer) markImageBuildAsFailed(ctx context.Context, orgID uuid.UUID, imageBuild *domain.ImageBuild, log logrus.FieldLogger) error {
	name := lo.FromPtr(imageBuild.Metadata.Name)
	if name == "" {
		return fmt.Errorf("imageBuild name is empty")
	}

	// Update status to Failed
	now := time.Now().UTC()
	failedCondition := domain.ImageBuildCondition{
		Type:               domain.ImageBuildConditionTypeReady,
		Status:             domain.ConditionStatusFalse,
		Reason:             string(domain.ImageBuildConditionReasonFailed),
		Message:            "Operation was in progress on startup and could not be resumed",
		LastTransitionTime: now,
	}

	// Status and Conditions are guaranteed to exist due to field selector filtering
	domain.SetImageBuildStatusCondition(imageBuild.Status.Conditions, failedCondition)

	// Update status - if resource changed, optimistic locking will cause this to fail, which is fine
	_, err := c.imageBuilderService.ImageBuild().UpdateStatus(ctx, orgID, imageBuild)
	if err != nil {
		// If update failed due to resource version mismatch, that's fine - resource was updated by another process
		if errors.Is(err, flterrors.ErrNoRowsUpdated) {
			log.WithField("imageBuild", name).Debug("ImageBuild was updated by another process, skipping failed mark")
			return nil
		}
		return fmt.Errorf("failed to update ImageBuild status: %w", err)
	}

	log.WithField("imageBuild", name).Info("Marked ImageBuild as failed (was in progress on startup)")
	return nil
}

// markImageBuildAsCanceled marks an ImageBuild as Canceled or Failed (for timeouts)
// because it was being canceled when the worker stopped.
// - For user cancellation: status becomes Canceled
// - For timeout: status becomes Failed (with the timeout message preserved)
func (c *Consumer) markImageBuildAsCanceled(ctx context.Context, orgID uuid.UUID, imageBuild *domain.ImageBuild, log logrus.FieldLogger) error {
	name := lo.FromPtr(imageBuild.Metadata.Name)
	if name == "" {
		return fmt.Errorf("imageBuild name is empty")
	}

	// Preserve the message from Canceling condition (may contain timeout info)
	message := "Build was canceled"
	if imageBuild.Status != nil && imageBuild.Status.Conditions != nil {
		readyCondition := domain.FindImageBuildStatusCondition(*imageBuild.Status.Conditions, domain.ImageBuildConditionTypeReady)
		if readyCondition != nil && readyCondition.Message != "" {
			message = readyCondition.Message
		}
	}

	// Check if this was a timeout or user cancellation
	isTimeout := strings.Contains(message, "timed out")
	now := time.Now().UTC()

	var condition domain.ImageBuildCondition
	if isTimeout {
		// Timeout - set Failed status
		condition = domain.ImageBuildCondition{
			Type:               domain.ImageBuildConditionTypeReady,
			Status:             domain.ConditionStatusFalse,
			Reason:             string(domain.ImageBuildConditionReasonFailed),
			Message:            message,
			LastTransitionTime: now,
		}
	} else {
		// User cancellation - set Canceled status
		condition = domain.ImageBuildCondition{
			Type:               domain.ImageBuildConditionTypeReady,
			Status:             domain.ConditionStatusFalse,
			Reason:             string(domain.ImageBuildConditionReasonCanceled),
			Message:            message,
			LastTransitionTime: now,
		}
	}

	// Status and Conditions are guaranteed to exist due to field selector filtering
	domain.SetImageBuildStatusCondition(imageBuild.Status.Conditions, condition)

	// Update status - if resource changed, optimistic locking will cause this to fail, which is fine
	_, err := c.imageBuilderService.ImageBuild().UpdateStatus(ctx, orgID, imageBuild)
	if err != nil {
		// If update failed due to resource version mismatch, that's fine - resource was updated by another process
		if errors.Is(err, flterrors.ErrNoRowsUpdated) {
			log.WithField("imageBuild", name).Debug("ImageBuild was updated by another process, skipping mark")
			return nil
		}
		return fmt.Errorf("failed to update ImageBuild status: %w", err)
	}

	// Clean up the Redis cancel signal and signal completion if kvStore is available
	if c.kvStore != nil {
		// Delete the cancel request stream
		cancelStreamKey := getImageBuildCancelStreamKey(orgID, name)
		if err := c.kvStore.Delete(ctx, cancelStreamKey); err != nil {
			log.WithError(err).Debug("Failed to delete cancellation stream key (may not exist)")
		}

		// Signal cancellation completion (for cancel-then-delete flow)
		if !isTimeout {
			canceledStreamKey := GetImageBuildCanceledStreamKey(orgID, name)
			if _, err := c.kvStore.StreamAdd(ctx, canceledStreamKey, []byte("canceled")); err != nil {
				log.WithError(err).Warn("Failed to write cancellation completion signal to Redis")
			} else {
				// Set a TTL so the key is cleaned up even if the API doesn't consume it
				if err := c.kvStore.SetExpire(ctx, canceledStreamKey, 5*time.Minute); err != nil {
					log.WithError(err).Warn("Failed to set TTL on cancellation completion signal key")
				}
			}
		}
	}

	if isTimeout {
		log.WithField("imageBuild", name).WithField("message", message).Info("Marked ImageBuild as failed (timed out on startup)")
	} else {
		log.WithField("imageBuild", name).WithField("message", message).Info("Marked ImageBuild as canceled (was being canceled on startup)")
	}
	return nil
}

// markImageExportAsFailed marks an ImageExport as Failed because it was in a non-terminal state on startup
func (c *Consumer) markImageExportAsFailed(ctx context.Context, orgID uuid.UUID, imageExport *domain.ImageExport, log logrus.FieldLogger) error {
	name := lo.FromPtr(imageExport.Metadata.Name)
	if name == "" {
		return fmt.Errorf("imageExport name is empty")
	}

	// Update status to Failed
	now := time.Now().UTC()
	failedCondition := domain.ImageExportCondition{
		Type:               domain.ImageExportConditionTypeReady,
		Status:             domain.ConditionStatusFalse,
		Reason:             string(domain.ImageExportConditionReasonFailed),
		Message:            "Operation was in progress on startup and could not be resumed",
		LastTransitionTime: now,
	}

	// Status and Conditions are guaranteed to exist due to field selector filtering
	domain.SetImageExportStatusCondition(imageExport.Status.Conditions, failedCondition)

	// Update status - if resource changed, optimistic locking will cause this to fail, which is fine
	_, err := c.imageBuilderService.ImageExport().UpdateStatus(ctx, orgID, imageExport)
	if err != nil {
		// If update failed due to resource version mismatch, that's fine - resource was updated by another process
		if errors.Is(err, flterrors.ErrNoRowsUpdated) {
			log.WithField("imageExport", name).Debug("ImageExport was updated by another process, skipping failed mark")
			return nil
		}
		return fmt.Errorf("failed to update ImageExport status: %w", err)
	}

	log.WithField("imageExport", name).Info("Marked ImageExport as failed (was in progress on startup)")
	return nil
}

// markImageExportAsCanceled marks an ImageExport as Canceled or Failed (for timeouts)
// because it was being canceled when the worker stopped.
// - For user cancellation: status becomes Canceled
// - For timeout: status becomes Failed (with the timeout message preserved)
func (c *Consumer) markImageExportAsCanceled(ctx context.Context, orgID uuid.UUID, imageExport *domain.ImageExport, log logrus.FieldLogger) error {
	name := lo.FromPtr(imageExport.Metadata.Name)
	if name == "" {
		return fmt.Errorf("imageExport name is empty")
	}

	// Preserve the message from Canceling condition (may contain timeout info)
	message := "Export was canceled"
	if imageExport.Status != nil && imageExport.Status.Conditions != nil {
		readyCondition := domain.FindImageExportStatusCondition(*imageExport.Status.Conditions, domain.ImageExportConditionTypeReady)
		if readyCondition != nil && readyCondition.Message != "" {
			message = readyCondition.Message
		}
	}

	// Check if this was a timeout or user cancellation
	isTimeout := strings.Contains(message, "timed out")
	now := time.Now().UTC()

	var condition domain.ImageExportCondition
	if isTimeout {
		// Timeout - set Failed status
		condition = domain.ImageExportCondition{
			Type:               domain.ImageExportConditionTypeReady,
			Status:             domain.ConditionStatusFalse,
			Reason:             string(domain.ImageExportConditionReasonFailed),
			Message:            message,
			LastTransitionTime: now,
		}
	} else {
		// User cancellation - set Canceled status
		condition = domain.ImageExportCondition{
			Type:               domain.ImageExportConditionTypeReady,
			Status:             domain.ConditionStatusFalse,
			Reason:             string(domain.ImageExportConditionReasonCanceled),
			Message:            message,
			LastTransitionTime: now,
		}
	}

	// Status and Conditions are guaranteed to exist due to field selector filtering
	domain.SetImageExportStatusCondition(imageExport.Status.Conditions, condition)

	// Update status - if resource changed, optimistic locking will cause this to fail, which is fine
	_, err := c.imageBuilderService.ImageExport().UpdateStatus(ctx, orgID, imageExport)
	if err != nil {
		// If update failed due to resource version mismatch, that's fine - resource was updated by another process
		if errors.Is(err, flterrors.ErrNoRowsUpdated) {
			log.WithField("imageExport", name).Debug("ImageExport was updated by another process, skipping mark")
			return nil
		}
		return fmt.Errorf("failed to update ImageExport status: %w", err)
	}

	// Clean up the Redis cancel signal and signal completion if kvStore is available
	if c.kvStore != nil {
		// Delete the cancel request stream
		cancelStreamKey := fmt.Sprintf("imageexport:cancel:%s:%s", orgID.String(), name)
		if err := c.kvStore.Delete(ctx, cancelStreamKey); err != nil {
			log.WithError(err).Debug("Failed to delete cancellation stream key (may not exist)")
		}

		// Signal cancellation completion (for cancel-then-delete flow)
		if !isTimeout {
			canceledStreamKey := GetCanceledStreamKey(orgID, name)
			if _, err := c.kvStore.StreamAdd(ctx, canceledStreamKey, []byte("canceled")); err != nil {
				log.WithError(err).Warn("Failed to write cancellation completion signal to Redis")
			} else {
				// Set a TTL so the key is cleaned up even if the API doesn't consume it
				if err := c.kvStore.SetExpire(ctx, canceledStreamKey, 5*time.Minute); err != nil {
					log.WithError(err).Warn("Failed to set TTL on cancellation completion signal key")
				}
			}
		}
	}

	if isTimeout {
		log.WithField("imageExport", name).WithField("message", message).Info("Marked ImageExport as failed (timed out on startup)")
	} else {
		log.WithField("imageExport", name).WithField("message", message).Info("Marked ImageExport as canceled (was being canceled on startup)")
	}
	return nil
}
