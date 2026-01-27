package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	apiimagebuilder "github.com/flightctl/flightctl/api/imagebuilder/v1beta1"
	"github.com/flightctl/flightctl/internal/flterrors"
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
	imageBuilds, status := c.imageBuilderService.ImageBuild().List(ctx, orgID, apiimagebuilder.ListImageBuildsParams{
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
	imageExports, status := c.imageBuilderService.ImageExport().List(ctx, orgID, apiimagebuilder.ListImageExportsParams{
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
func (c *Consumer) shouldRequeueImageBuild(imageBuild *apiimagebuilder.ImageBuild) bool {
	// Find Ready condition - field selector guarantees it exists
	readyCondition := apiimagebuilder.FindImageBuildStatusCondition(*imageBuild.Status.Conditions, apiimagebuilder.ImageBuildConditionTypeReady)
	if readyCondition == nil {
		// Should not reach here due to field selector, but defensive return
		return false
	}
	// Only requeue if Pending
	return readyCondition.Reason == string(apiimagebuilder.ImageBuildConditionReasonPending)
}

// isImageBuildCanceling checks if an ImageBuild is in Canceling state
func (c *Consumer) isImageBuildCanceling(imageBuild *apiimagebuilder.ImageBuild) bool {
	if imageBuild.Status == nil || imageBuild.Status.Conditions == nil {
		return false
	}
	readyCondition := apiimagebuilder.FindImageBuildStatusCondition(*imageBuild.Status.Conditions, apiimagebuilder.ImageBuildConditionTypeReady)
	if readyCondition == nil {
		return false
	}
	return readyCondition.Reason == string(apiimagebuilder.ImageBuildConditionReasonCanceling)
}

// shouldRequeueImageExport checks if an ImageExport should be requeued
// Returns true only if the resource is in Pending state (hasn't started processing)
// Returns false if it has started processing (Converting, Pushing) or is in a terminal state
// Note: Field selector guarantees status.conditions.ready.reason exists
func (c *Consumer) shouldRequeueImageExport(imageExport *apiimagebuilder.ImageExport) bool {
	// Find Ready condition - field selector guarantees it exists
	readyCondition := apiimagebuilder.FindImageExportStatusCondition(*imageExport.Status.Conditions, apiimagebuilder.ImageExportConditionTypeReady)
	if readyCondition == nil {
		// Should not reach here due to field selector, but defensive return
		return false
	}
	// Only requeue if Pending
	return readyCondition.Reason == string(apiimagebuilder.ImageExportConditionReasonPending)
}

// isImageExportCanceling checks if an ImageExport is in Canceling state
func (c *Consumer) isImageExportCanceling(imageExport *apiimagebuilder.ImageExport) bool {
	if imageExport.Status == nil || imageExport.Status.Conditions == nil {
		return false
	}
	readyCondition := apiimagebuilder.FindImageExportStatusCondition(*imageExport.Status.Conditions, apiimagebuilder.ImageExportConditionTypeReady)
	if readyCondition == nil {
		return false
	}
	return readyCondition.Reason == string(apiimagebuilder.ImageExportConditionReasonCanceling)
}

// requeueImageBuild requeues an ImageBuild by creating and enqueueing a ResourceCreated event
func (c *Consumer) requeueImageBuild(ctx context.Context, orgID uuid.UUID, imageBuild *apiimagebuilder.ImageBuild, log logrus.FieldLogger) error {
	name := lo.FromPtr(imageBuild.Metadata.Name)
	if name == "" {
		return fmt.Errorf("imageBuild name is empty")
	}

	// Create ResourceCreated event
	event := common.GetResourceCreatedOrUpdatedSuccessEvent(
		ctx,
		true,
		api.ResourceKind(string(apiimagebuilder.ResourceKindImageBuild)),
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
func (c *Consumer) requeueImageExport(ctx context.Context, orgID uuid.UUID, imageExport *apiimagebuilder.ImageExport, log logrus.FieldLogger) error {
	name := lo.FromPtr(imageExport.Metadata.Name)
	if name == "" {
		return fmt.Errorf("imageExport name is empty")
	}

	// Create ResourceCreated event
	event := common.GetResourceCreatedOrUpdatedSuccessEvent(
		ctx,
		true,
		api.ResourceKind(string(apiimagebuilder.ResourceKindImageExport)),
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
func (c *Consumer) enqueueRequeueEvent(ctx context.Context, orgID uuid.UUID, event *api.Event, log logrus.FieldLogger) error {
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
func (c *Consumer) markImageBuildAsFailed(ctx context.Context, orgID uuid.UUID, imageBuild *apiimagebuilder.ImageBuild, log logrus.FieldLogger) error {
	name := lo.FromPtr(imageBuild.Metadata.Name)
	if name == "" {
		return fmt.Errorf("imageBuild name is empty")
	}

	// Update status to Failed
	now := time.Now().UTC()
	failedCondition := apiimagebuilder.ImageBuildCondition{
		Type:               apiimagebuilder.ImageBuildConditionTypeReady,
		Status:             api.ConditionStatusFalse,
		Reason:             string(apiimagebuilder.ImageBuildConditionReasonFailed),
		Message:            "Operation was in progress on startup and could not be resumed",
		LastTransitionTime: now,
	}

	// Status and Conditions are guaranteed to exist due to field selector filtering
	apiimagebuilder.SetImageBuildStatusCondition(imageBuild.Status.Conditions, failedCondition)

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
func (c *Consumer) markImageBuildAsCanceled(ctx context.Context, orgID uuid.UUID, imageBuild *apiimagebuilder.ImageBuild, log logrus.FieldLogger) error {
	name := lo.FromPtr(imageBuild.Metadata.Name)
	if name == "" {
		return fmt.Errorf("imageBuild name is empty")
	}

	// Preserve the message from Canceling condition (may contain timeout info)
	message := "Build was canceled"
	if imageBuild.Status != nil && imageBuild.Status.Conditions != nil {
		readyCondition := apiimagebuilder.FindImageBuildStatusCondition(*imageBuild.Status.Conditions, apiimagebuilder.ImageBuildConditionTypeReady)
		if readyCondition != nil && readyCondition.Message != "" {
			message = readyCondition.Message
		}
	}

	// Check if this was a timeout or user cancellation
	isTimeout := strings.Contains(message, "timed out")
	now := time.Now().UTC()

	var condition apiimagebuilder.ImageBuildCondition
	if isTimeout {
		// Timeout - set Failed status
		condition = apiimagebuilder.ImageBuildCondition{
			Type:               apiimagebuilder.ImageBuildConditionTypeReady,
			Status:             api.ConditionStatusFalse,
			Reason:             string(apiimagebuilder.ImageBuildConditionReasonFailed),
			Message:            message,
			LastTransitionTime: now,
		}
	} else {
		// User cancellation - set Canceled status
		condition = apiimagebuilder.ImageBuildCondition{
			Type:               apiimagebuilder.ImageBuildConditionTypeReady,
			Status:             api.ConditionStatusFalse,
			Reason:             string(apiimagebuilder.ImageBuildConditionReasonCanceled),
			Message:            message,
			LastTransitionTime: now,
		}
	}

	// Status and Conditions are guaranteed to exist due to field selector filtering
	apiimagebuilder.SetImageBuildStatusCondition(imageBuild.Status.Conditions, condition)

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

	if isTimeout {
		log.WithField("imageBuild", name).WithField("message", message).Info("Marked ImageBuild as failed (timed out on startup)")
	} else {
		log.WithField("imageBuild", name).WithField("message", message).Info("Marked ImageBuild as canceled (was being canceled on startup)")
	}
	return nil
}

// markImageExportAsFailed marks an ImageExport as Failed because it was in a non-terminal state on startup
func (c *Consumer) markImageExportAsFailed(ctx context.Context, orgID uuid.UUID, imageExport *apiimagebuilder.ImageExport, log logrus.FieldLogger) error {
	name := lo.FromPtr(imageExport.Metadata.Name)
	if name == "" {
		return fmt.Errorf("imageExport name is empty")
	}

	// Update status to Failed
	now := time.Now().UTC()
	failedCondition := apiimagebuilder.ImageExportCondition{
		Type:               apiimagebuilder.ImageExportConditionTypeReady,
		Status:             api.ConditionStatusFalse,
		Reason:             string(apiimagebuilder.ImageExportConditionReasonFailed),
		Message:            "Operation was in progress on startup and could not be resumed",
		LastTransitionTime: now,
	}

	// Status and Conditions are guaranteed to exist due to field selector filtering
	apiimagebuilder.SetImageExportStatusCondition(imageExport.Status.Conditions, failedCondition)

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
func (c *Consumer) markImageExportAsCanceled(ctx context.Context, orgID uuid.UUID, imageExport *apiimagebuilder.ImageExport, log logrus.FieldLogger) error {
	name := lo.FromPtr(imageExport.Metadata.Name)
	if name == "" {
		return fmt.Errorf("imageExport name is empty")
	}

	// Preserve the message from Canceling condition (may contain timeout info)
	message := "Export was canceled"
	if imageExport.Status != nil && imageExport.Status.Conditions != nil {
		readyCondition := apiimagebuilder.FindImageExportStatusCondition(*imageExport.Status.Conditions, apiimagebuilder.ImageExportConditionTypeReady)
		if readyCondition != nil && readyCondition.Message != "" {
			message = readyCondition.Message
		}
	}

	// Check if this was a timeout or user cancellation
	isTimeout := strings.Contains(message, "timed out")
	now := time.Now().UTC()

	var condition apiimagebuilder.ImageExportCondition
	if isTimeout {
		// Timeout - set Failed status
		condition = apiimagebuilder.ImageExportCondition{
			Type:               apiimagebuilder.ImageExportConditionTypeReady,
			Status:             api.ConditionStatusFalse,
			Reason:             string(apiimagebuilder.ImageExportConditionReasonFailed),
			Message:            message,
			LastTransitionTime: now,
		}
	} else {
		// User cancellation - set Canceled status
		condition = apiimagebuilder.ImageExportCondition{
			Type:               apiimagebuilder.ImageExportConditionTypeReady,
			Status:             api.ConditionStatusFalse,
			Reason:             string(apiimagebuilder.ImageExportConditionReasonCanceled),
			Message:            message,
			LastTransitionTime: now,
		}
	}

	// Status and Conditions are guaranteed to exist due to field selector filtering
	apiimagebuilder.SetImageExportStatusCondition(imageExport.Status.Conditions, condition)

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

	if isTimeout {
		log.WithField("imageExport", name).WithField("message", message).Info("Marked ImageExport as failed (timed out on startup)")
	} else {
		log.WithField("imageExport", name).WithField("message", message).Info("Marked ImageExport as canceled (was being canceled on startup)")
	}
	return nil
}
