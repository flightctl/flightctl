package tasks

import (
	"context"
	"errors"
	"fmt"
	"time"

	v1beta1 "github.com/flightctl/flightctl/api/core/v1beta1"
	apiimagebuilder "github.com/flightctl/flightctl/api/imagebuilder/v1beta1"
	"github.com/flightctl/flightctl/internal/flterrors"
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
	// 1. Are not in terminal states (Completed, Failed, Pending)
	// 2. Have lastSeen older than threshold
	fieldSelectorStr := fmt.Sprintf("status.conditions.ready.reason notin (Completed, Failed, Pending),status.lastSeen<%s", thresholdStr)

	// Check ImageBuilds
	imageBuilds, status := c.imageBuilderService.ImageBuild().List(ctx, orgID, apiimagebuilder.ListImageBuildsParams{
		FieldSelector: &fieldSelectorStr,
	})
	if !imagebuilderapi.IsStatusOK(status) {
		log.WithError(fmt.Errorf("status: %v", status)).Error("Failed to list imagebuilds for timeout check")
	} else if imageBuilds != nil {
		for _, imageBuild := range imageBuilds.Items {
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

	// Check ImageExports
	imageExports, status := c.imageBuilderService.ImageExport().List(ctx, orgID, apiimagebuilder.ListImageExportsParams{
		FieldSelector: &fieldSelectorStr,
	})
	if !imagebuilderapi.IsStatusOK(status) {
		log.WithError(fmt.Errorf("status: %v", status)).Error("Failed to list imageexports for timeout check")
	} else if imageExports != nil {
		for _, imageExport := range imageExports.Items {
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

	return failedCount, nil
}

// markImageBuildAsTimedOut marks an ImageBuild as Failed due to timeout
// Returns (updated, error) where updated indicates whether a DB row was actually updated
func (c *Consumer) markImageBuildAsTimedOut(ctx context.Context, orgID uuid.UUID, imageBuild *apiimagebuilder.ImageBuild, timeoutDuration time.Duration, log logrus.FieldLogger) (bool, error) {
	name := lo.FromPtr(imageBuild.Metadata.Name)
	if name == "" {
		return false, fmt.Errorf("imageBuild name is empty")
	}

	// Update status to Failed
	now := time.Now().UTC()
	failedCondition := apiimagebuilder.ImageBuildCondition{
		Type:               apiimagebuilder.ImageBuildConditionTypeReady,
		Status:             v1beta1.ConditionStatusFalse,
		Reason:             string(apiimagebuilder.ImageBuildConditionReasonFailed),
		Message:            fmt.Sprintf("Operation timed out: last seen more than %v ago", timeoutDuration),
		LastTransitionTime: now,
	}

	// Set the Failed condition using the helper function
	// Status and Conditions are guaranteed to exist due to field selector filtering
	apiimagebuilder.SetImageBuildStatusCondition(imageBuild.Status.Conditions, failedCondition)

	// Update status - if resource changed, optimistic locking will cause this to fail, which is fine
	_, err := c.imageBuilderService.ImageBuild().UpdateStatus(ctx, orgID, imageBuild)
	if err != nil {
		// If update failed due to resource version mismatch, that's fine - resource was updated by another process
		if errors.Is(err, flterrors.ErrNoRowsUpdated) {
			log.WithField("imageBuild", name).Debug("ImageBuild was updated by another process, skipping timeout mark")
			return false, nil
		}
		return false, fmt.Errorf("failed to update ImageBuild status: %w", err)
	}

	log.WithField("imageBuild", name).Info("Marked ImageBuild as timed out")
	return true, nil
}

// markImageExportAsTimedOut marks an ImageExport as Failed due to timeout
// Returns (updated, error) where updated indicates whether a DB row was actually updated
func (c *Consumer) markImageExportAsTimedOut(ctx context.Context, orgID uuid.UUID, imageExport *apiimagebuilder.ImageExport, timeoutDuration time.Duration, log logrus.FieldLogger) (bool, error) {
	name := lo.FromPtr(imageExport.Metadata.Name)
	if name == "" {
		return false, fmt.Errorf("imageExport name is empty")
	}

	// Update status to Failed
	now := time.Now().UTC()
	failedCondition := apiimagebuilder.ImageExportCondition{
		Type:               apiimagebuilder.ImageExportConditionTypeReady,
		Status:             v1beta1.ConditionStatusFalse,
		Reason:             string(apiimagebuilder.ImageExportConditionReasonFailed),
		Message:            fmt.Sprintf("Operation timed out: last seen more than %v ago", timeoutDuration),
		LastTransitionTime: now,
	}

	// Set the Failed condition using the helper function
	// Status and Conditions are guaranteed to exist due to field selector filtering
	apiimagebuilder.SetImageExportStatusCondition(imageExport.Status.Conditions, failedCondition)

	// Update status - if resource changed, optimistic locking will cause this to fail, which is fine
	_, err := c.imageBuilderService.ImageExport().UpdateStatus(ctx, orgID, imageExport)
	if err != nil {
		// If update failed due to resource version mismatch, that's fine - resource was updated by another process
		if errors.Is(err, flterrors.ErrNoRowsUpdated) {
			log.WithField("imageExport", name).Debug("ImageExport was updated by another process, skipping timeout mark")
			return false, nil
		}
		return false, fmt.Errorf("failed to update ImageExport status: %w", err)
	}

	log.WithField("imageExport", name).Info("Marked ImageExport as timed out")
	return true, nil
}
