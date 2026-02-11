package tasks

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/imagebuilder_api/domain"
	imagebuilderapi "github.com/flightctl/flightctl/internal/imagebuilder_api/service"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
)

// CheckAndMarkTimeoutsForOrg checks for timed-out image builds and exports in a specific organization
// This method is public to allow testing with mocked services
// Returns the number of resources moved to fail
func (c *Consumer) CheckAndMarkTimeoutsForOrg(ctx context.Context, orgID uuid.UUID, timeoutDuration time.Duration, log logrus.FieldLogger) (int, error) {
	log.Debug("Starting timeout check for organization")
	defer log.Debug("Timeout check for organization completed")

	// Calculate threshold timestamp
	threshold := time.Now().UTC().Add(-timeoutDuration)
	thresholdStr := threshold.Format(time.RFC3339)

	failedCount := 0

	// Build field selector to find resources that:
	// 1. Are not in terminal states (Completed, Failed, Canceled, Pending)
	// 2. Have lastSeen older than threshold
	fieldSelectorStr := fmt.Sprintf("status.conditions.ready.reason notin (Completed, Failed, Canceled, Pending),status.lastSeen<%s", thresholdStr)

	// Check ImageBuilds
	imageBuilds, status := c.imageBuilderService.ImageBuild().List(ctx, orgID, domain.ListImageBuildsParams{
		FieldSelector: &fieldSelectorStr,
	})
	if !imagebuilderapi.IsStatusOK(status) {
		log.WithError(fmt.Errorf("status: %v", status)).Error("Failed to list imagebuilds for timeout check")
	} else if imageBuilds != nil {
		for _, imageBuild := range imageBuilds.Items {
			// Check if this is a Canceling state - should be marked as Canceled, not Failed
			if c.isImageBuildCanceling(&imageBuild) {
				updated, err := c.markImageBuildAsCanceledTimeout(ctx, orgID, &imageBuild, log)
				if err != nil {
					log.WithError(err).WithField("imageBuild", lo.FromPtr(imageBuild.Metadata.Name)).Error("Failed to mark imageBuild as canceled")
					failedCount++
					continue
				}
				if updated {
					failedCount++
				}
			} else {
				updated, err := c.markImageBuildAsTimedOut(ctx, orgID, &imageBuild, timeoutDuration, log)
				if err != nil {
					log.WithError(err).WithField("imageBuild", lo.FromPtr(imageBuild.Metadata.Name)).Error("Failed to mark imageBuild as timed out")
					failedCount++
					continue
				}
				if updated {
					failedCount++
				}
			}
		}
	}

	// Check ImageExports
	imageExports, status := c.imageBuilderService.ImageExport().List(ctx, orgID, domain.ListImageExportsParams{
		FieldSelector: &fieldSelectorStr,
	})
	if !imagebuilderapi.IsStatusOK(status) {
		log.WithError(fmt.Errorf("status: %v", status)).Error("Failed to list imageexports for timeout check")
	} else if imageExports != nil {
		for _, imageExport := range imageExports.Items {
			// Check if this is a Canceling state - should be marked as Canceled, not Failed
			if c.isImageExportCanceling(&imageExport) {
				updated, err := c.markImageExportAsCanceledTimeout(ctx, orgID, &imageExport, log)
				if err != nil {
					log.WithError(err).WithField("imageExport", lo.FromPtr(imageExport.Metadata.Name)).Error("Failed to mark imageExport as canceled")
					failedCount++
					continue
				}
				if updated {
					failedCount++
				}
			} else {
				updated, err := c.markImageExportAsTimedOut(ctx, orgID, &imageExport, timeoutDuration, log)
				if err != nil {
					log.WithError(err).WithField("imageExport", lo.FromPtr(imageExport.Metadata.Name)).Error("Failed to mark imageExport as timed out")
					failedCount++
					continue
				}
				if updated {
					failedCount++
				}
			}
		}
	}

	return failedCount, nil
}

// markImageBuildAsTimedOut initiates graceful cancellation of an ImageBuild due to timeout
// Uses the service's CancelWithReason to set status to Canceling with a timeout message
// and write to Redis cancellation stream. The worker will then transition to Canceled
// with the preserved timeout message.
// Returns (updated, error) where updated indicates whether a DB row was actually updated
func (c *Consumer) markImageBuildAsTimedOut(ctx context.Context, orgID uuid.UUID, imageBuild *domain.ImageBuild, timeoutDuration time.Duration, log logrus.FieldLogger) (bool, error) {
	name := lo.FromPtr(imageBuild.Metadata.Name)
	if name == "" {
		return false, fmt.Errorf("imageBuild name is empty")
	}

	reason := fmt.Sprintf("Operation timed out: last seen more than %v ago", timeoutDuration)
	_, err := c.imageBuilderService.ImageBuild().CancelWithReason(ctx, orgID, name, reason)

	if err != nil {
		// ErrNotCancelable = already in terminal state or Canceling - skip silently
		// ErrResourceNotFound = not found - skip silently
		// ErrNoRowsUpdated = conflict (resource version mismatch) - skip silently
		if errors.Is(err, imagebuilderapi.ErrNotCancelable) ||
			errors.Is(err, flterrors.ErrResourceNotFound) ||
			errors.Is(err, flterrors.ErrNoRowsUpdated) {
			log.WithField("imageBuild", name).WithError(err).Debug("ImageBuild could not be canceled, skipping")
			return false, nil
		}
		return false, fmt.Errorf("failed to cancel ImageBuild: %w", err)
	}

	log.WithField("imageBuild", name).Info("Initiated graceful cancellation due to timeout")
	return true, nil
}

// markImageExportAsTimedOut initiates graceful cancellation of an ImageExport due to timeout
// Uses the service's CancelWithReason to set status to Canceling with a timeout message
// and write to Redis cancellation stream. The worker will then transition to Canceled
// with the preserved timeout message.
// Returns (updated, error) where updated indicates whether a DB row was actually updated
func (c *Consumer) markImageExportAsTimedOut(ctx context.Context, orgID uuid.UUID, imageExport *domain.ImageExport, timeoutDuration time.Duration, log logrus.FieldLogger) (bool, error) {
	name := lo.FromPtr(imageExport.Metadata.Name)
	if name == "" {
		return false, fmt.Errorf("imageExport name is empty")
	}

	reason := fmt.Sprintf("Operation timed out: last seen more than %v ago", timeoutDuration)
	_, err := c.imageBuilderService.ImageExport().CancelWithReason(ctx, orgID, name, reason)

	if err != nil {
		// ErrNotCancelable = already in terminal state or Canceling - skip silently
		// ErrResourceNotFound = not found - skip silently
		// ErrNoRowsUpdated = conflict (resource version mismatch) - skip silently
		if errors.Is(err, imagebuilderapi.ErrNotCancelable) ||
			errors.Is(err, flterrors.ErrResourceNotFound) ||
			errors.Is(err, flterrors.ErrNoRowsUpdated) {
			log.WithField("imageExport", name).WithError(err).Debug("ImageExport could not be canceled, skipping")
			return false, nil
		}
		return false, fmt.Errorf("failed to cancel ImageExport: %w", err)
	}

	log.WithField("imageExport", name).Info("Initiated graceful cancellation due to timeout")
	return true, nil
}

// markImageBuildAsCanceledTimeout marks an ImageBuild as Canceled because it was being canceled when it timed out
// Preserves the message from the Canceling condition (which may contain timeout info)
// Note: isImageBuildCanceling is defined in requeue.go
func (c *Consumer) markImageBuildAsCanceledTimeout(ctx context.Context, orgID uuid.UUID, imageBuild *domain.ImageBuild, log logrus.FieldLogger) (bool, error) {
	name := lo.FromPtr(imageBuild.Metadata.Name)
	if name == "" {
		return false, fmt.Errorf("imageBuild name is empty")
	}

	// Preserve the message from Canceling condition (may contain timeout info)
	message := "Build was canceled"
	if imageBuild.Status != nil && imageBuild.Status.Conditions != nil {
		readyCondition := domain.FindImageBuildStatusCondition(*imageBuild.Status.Conditions, domain.ImageBuildConditionTypeReady)
		if readyCondition != nil && readyCondition.Message != "" {
			message = readyCondition.Message
		}
	}

	// Update status to Canceled
	now := time.Now().UTC()
	canceledCondition := domain.ImageBuildCondition{
		Type:               domain.ImageBuildConditionTypeReady,
		Status:             domain.ConditionStatusFalse,
		Reason:             string(domain.ImageBuildConditionReasonCanceled),
		Message:            message,
		LastTransitionTime: now,
	}

	domain.SetImageBuildStatusCondition(imageBuild.Status.Conditions, canceledCondition)

	_, err := c.imageBuilderService.ImageBuild().UpdateStatus(ctx, orgID, imageBuild)
	if err != nil {
		if errors.Is(err, flterrors.ErrNoRowsUpdated) {
			log.WithField("imageBuild", name).Debug("ImageBuild was updated by another process, skipping canceled mark")
			return false, nil
		}
		return false, fmt.Errorf("failed to update ImageBuild status: %w", err)
	}

	log.WithField("imageBuild", name).WithField("message", message).Info("Marked ImageBuild as canceled (was canceling when timed out)")
	return true, nil
}

// markImageExportAsCanceledTimeout marks an ImageExport as Canceled because it was being canceled when it timed out
// Preserves the message from the Canceling condition (which may contain timeout info)
// Note: isImageExportCanceling is defined in requeue.go
func (c *Consumer) markImageExportAsCanceledTimeout(ctx context.Context, orgID uuid.UUID, imageExport *domain.ImageExport, log logrus.FieldLogger) (bool, error) {
	name := lo.FromPtr(imageExport.Metadata.Name)
	if name == "" {
		return false, fmt.Errorf("imageExport name is empty")
	}

	// Preserve the message from Canceling condition (may contain timeout info)
	message := "Export was canceled"
	if imageExport.Status != nil && imageExport.Status.Conditions != nil {
		readyCondition := domain.FindImageExportStatusCondition(*imageExport.Status.Conditions, domain.ImageExportConditionTypeReady)
		if readyCondition != nil && readyCondition.Message != "" {
			message = readyCondition.Message
		}
	}

	// Update status to Canceled
	now := time.Now().UTC()
	canceledCondition := domain.ImageExportCondition{
		Type:               domain.ImageExportConditionTypeReady,
		Status:             domain.ConditionStatusFalse,
		Reason:             string(domain.ImageExportConditionReasonCanceled),
		Message:            message,
		LastTransitionTime: now,
	}

	domain.SetImageExportStatusCondition(imageExport.Status.Conditions, canceledCondition)

	_, err := c.imageBuilderService.ImageExport().UpdateStatus(ctx, orgID, imageExport)
	if err != nil {
		if errors.Is(err, flterrors.ErrNoRowsUpdated) {
			log.WithField("imageExport", name).Debug("ImageExport was updated by another process, skipping canceled mark")
			return false, nil
		}
		return false, fmt.Errorf("failed to update ImageExport status: %w", err)
	}

	log.WithField("imageExport", name).WithField("message", message).Info("Marked ImageExport as canceled (was canceling when timed out)")
	return true, nil
}
