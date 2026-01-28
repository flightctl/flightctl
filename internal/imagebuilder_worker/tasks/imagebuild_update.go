package tasks

import (
	"context"
	"fmt"

	coredomain "github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/imagebuilder_api/domain"
	imagebuilderapi "github.com/flightctl/flightctl/internal/imagebuilder_api/service"
	"github.com/flightctl/flightctl/internal/service/common"
	"github.com/flightctl/flightctl/internal/worker_client"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
)

// HandleImageBuildUpdate handles ImageBuild ResourceUpdated events
// If the ImageBuild is completed, it requeues related ImageExports that haven't started yet
// This method is public to allow testing with mocked services
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

	// Check if ImageBuild is completed
	if imageBuild.Status == nil || imageBuild.Status.Conditions == nil {
		log.Debug("ImageBuild has no status or conditions, skipping requeue of ImageExports")
		return nil
	}

	readyCondition := domain.FindImageBuildStatusCondition(*imageBuild.Status.Conditions, domain.ImageBuildConditionTypeReady)
	if readyCondition == nil ||
		readyCondition.Status != domain.ConditionStatusTrue ||
		readyCondition.Reason != string(domain.ImageBuildConditionReasonCompleted) {
		log.Debug("ImageBuild is not completed, skipping requeue of ImageExports")
		return nil
	}

	log.Info("ImageBuild is completed, checking for related ImageExports to requeue")

	// Find ImageExports that reference this ImageBuild
	// Use field selector to filter at database level by imageBuildRef
	fieldSelectorStr := fmt.Sprintf("spec.source.imageBuildRef = %s", imageBuildName)
	imageExports, status := c.imageBuilderService.ImageExport().List(ctx, orgID, domain.ListImageExportsParams{
		FieldSelector: &fieldSelectorStr,
	})
	if !imagebuilderapi.IsStatusOK(status) {
		return fmt.Errorf("failed to list ImageExports for ImageBuild %q: %v", imageBuildName, status)
	}
	if imageExports == nil || len(imageExports.Items) == 0 {
		log.Debug("No ImageExports found for this ImageBuild")
		return nil
	}

	// Filter for ImageExports that need requeueing:
	// - Have Pending reason, OR
	// - Don't have a Ready condition at all (not started yet)
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
			log.WithField("imageExport", exportName).Warn("Failed to create requeue event")
			continue
		}

		// Enqueue the event
		if err := c.enqueueEvent(ctx, orgID, requeueEvent, log); err != nil {
			log.WithError(err).WithField("imageExport", exportName).Error("Failed to requeue ImageExport")
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

	return nil
}
