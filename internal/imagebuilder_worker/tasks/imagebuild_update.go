package tasks

import (
	"context"
	"errors"
	"fmt"

	coredomain "github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/imagebuilder_api/domain"
	imagebuilderapi "github.com/flightctl/flightctl/internal/imagebuilder_api/service"
	"github.com/flightctl/flightctl/internal/service/common"
	"github.com/flightctl/flightctl/internal/worker_client"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
)

// HandleImageBuildUpdate handles ImageBuild ResourceUpdated events.
//
//   - Completed: requeues related pending ImageExports, then evaluates waiting promotions.
//   - Failed/Canceled: fails waiting promotions.
//
// This method is public to allow testing with mocked services.
func (c *Consumer) HandleImageBuildUpdate(ctx context.Context, eventWithOrgId worker_client.EventWithOrgId, log logrus.FieldLogger) error {
	event := eventWithOrgId.Event
	orgID := eventWithOrgId.OrgId
	imageBuildName := event.InvolvedObject.Name

	log = log.WithField("imageBuild", imageBuildName).WithField("orgId", orgID)
	log.Info("Handling ImageBuild update event")

	// Load the ImageBuild resource
	imageBuild, status := c.imageBuilderService.ImageBuild().Get(ctx, orgID, imageBuildName, false)
	if imageBuild == nil || !imagebuilderapi.IsStatusOK(status) {
		return fmt.Errorf("failed to load ImageBuild %q: %v", imageBuildName, status)
	}

	if imageBuild.Status == nil || imageBuild.Status.Conditions == nil {
		log.Debug("ImageBuild has no status or conditions, nothing to do")
		return nil
	}

	readyCondition := domain.FindImageBuildStatusCondition(*imageBuild.Status.Conditions, domain.ImageBuildConditionTypeReady)
	if readyCondition == nil {
		log.Debug("ImageBuild has no Ready condition, nothing to do")
		return nil
	}

	switch readyCondition.Reason {
	case string(domain.ImageBuildConditionReasonCompleted):
		log.Info("ImageBuild is completed, checking for related ImageExports and ImagePromotions to requeue")
		// Attempt both operations and collect errors; a retry of this event will re-read current state.
		requeueErr := c.requeuePendingImageExports(ctx, orgID, imageBuildName, log)
		enqueueErr := c.enqueuePromotionsForBuild(ctx, orgID, imageBuildName, log)
		return errors.Join(requeueErr, enqueueErr)

	case string(domain.ImageBuildConditionReasonFailed):
		log.Info("ImageBuild is failed, failing related ImagePromotions")
		return c.failPromotionsForBuild(ctx, orgID, imageBuildName,
			domain.ImagePromotionConditionReasonBuildFailed,
			fmt.Sprintf("ImageBuild %s failed", imageBuildName))

	case string(domain.ImageBuildConditionReasonCanceled):
		log.Info("ImageBuild is canceled, cancelling related ImagePromotions")
		return c.failPromotionsForBuild(ctx, orgID, imageBuildName,
			domain.ImagePromotionConditionReasonBuildCanceled,
			fmt.Sprintf("ImageBuild %s was canceled", imageBuildName))

	default:
		log.Debugf("ImageBuild Ready condition reason is %q, nothing to do", readyCondition.Reason)
	}

	return nil
}

// requeuePendingImageExports finds ImageExports that are still waiting (Pending or no condition)
// for the given ImageBuild and requeues them for processing now that the build is completed.
func (c *Consumer) requeuePendingImageExports(ctx context.Context, orgID uuid.UUID, imageBuildName string, log logrus.FieldLogger) error {
	fieldSelectorStr := fmt.Sprintf("spec.source.imageBuildRef = %s", imageBuildName)
	imageExports, status := c.imageBuilderService.ImageExport().List(ctx, orgID, domain.ListImageExportsParams{
		FieldSelector: &fieldSelectorStr,
	})
	if !imagebuilderapi.IsStatusOK(status) {
		return fmt.Errorf("failed to list ImageExports for requeue: %v", status)
	}
	if imageExports == nil || len(imageExports.Items) == 0 {
		log.Debug("No ImageExports found for this ImageBuild")
		return nil
	}

	// Filter for ImageExports that need requeueing:
	// - Have Pending reason, OR
	// - Don't have a Ready condition at all (not started yet)
	var errs []error
	requeuedCount := 0
	for _, imageExport := range imageExports.Items {
		// Check if this ImageExport needs requeueing
		shouldRequeue := false
		if imageExport.Status == nil || imageExport.Status.Conditions == nil {
			// No status or conditions - needs requeueing
			shouldRequeue = true
		} else {
			// Look for Ready condition
			readyCondition := domain.FindImageExportStatusCondition(*imageExport.Status.Conditions, domain.ImageExportConditionTypeReady)
			if readyCondition == nil {
				// No Ready condition - needs requeueing
				shouldRequeue = true
			} else if readyCondition.Reason == string(domain.ImageExportConditionReasonPending) {
				// Has Pending reason - needs requeueing
				shouldRequeue = true
			}
			// Skip if already Completed, Failed, or Converting
		}

		if !shouldRequeue {
			continue
		}
		exportName := lo.FromPtr(imageExport.Metadata.Name)
		if exportName == "" {
			log.Warn("ImageExport has empty name, skipping")
			continue
		}

		requeueEvent := common.GetResourceCreatedOrUpdatedSuccessEvent(
			ctx,
			true,
			coredomain.ResourceKind(string(domain.ResourceKindImageExport)),
			exportName,
			nil,
			log,
			nil,
		)
		if requeueEvent == nil {
			errs = append(errs, fmt.Errorf("failed to create requeue event for ImageExport %q", exportName))
			continue
		}

		if err := c.enqueueEvent(ctx, orgID, requeueEvent, log); err != nil {
			errs = append(errs, fmt.Errorf("failed to enqueue ImageExport %q: %w", exportName, err))
			continue
		}

		log.WithField("imageExport", exportName).Info("Requeued ImageExport due to ImageBuild completion")
		requeuedCount++
	}

	if requeuedCount > 0 {
		log.WithField("requeuedCount", requeuedCount).Info("Requeued ImageExports due to ImageBuild completion")
	} else {
		log.Debug("No ImageExports needed requeueing")
	}

	return errors.Join(errs...)
}
