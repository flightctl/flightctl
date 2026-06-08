package tasks

import (
	"context"

	"github.com/flightctl/flightctl/internal/imagebuilder_api/domain"
	"github.com/flightctl/flightctl/internal/worker_client"
	"github.com/sirupsen/logrus"
)

// handleImageExportUpdated handles ImageExport ResourceUpdated events.
// When the export has completed, it re-evaluates any promotions waiting for that build.
func (c *Consumer) handleImageExportUpdated(ctx context.Context, eventWithOrgId worker_client.EventWithOrgId, log logrus.FieldLogger) error {
	exportName := eventWithOrgId.Event.InvolvedObject.Name
	orgID := eventWithOrgId.OrgId

	log = log.WithField("imageExport", exportName).WithField("orgId", orgID)
	log.Debug("Handling ImageExport updated event")

	imageExport, err := c.store.ImageExport().Get(ctx, orgID, exportName)
	if err != nil {
		log.WithError(err).Errorf("failed to get ImageExport org=%s name=%s", orgID, exportName)
		return err
	}

	// Only act when the export has actually completed successfully.
	if imageExport.Status == nil || imageExport.Status.Conditions == nil {
		log.Debugf("ImageExport %s has no status conditions; skipping", exportName)
		return nil
	}
	readyCondition := domain.FindImageExportStatusCondition(*imageExport.Status.Conditions, domain.ImageExportConditionTypeReady)
	if readyCondition == nil ||
		readyCondition.Status != domain.ConditionStatusTrue ||
		readyCondition.Reason != string(domain.ImageExportConditionReasonCompleted) {
		log.Debugf("ImageExport %s is not completed (reason=%s); skipping promotion evaluation", exportName, func() string {
			if readyCondition != nil {
				return readyCondition.Reason
			}
			return "none"
		}())
		return nil
	}

	sourceType, err := imageExport.Spec.Source.Discriminator()
	if err != nil {
		log.WithError(err).Warn("failed to get ImageExport source type; skipping promotion evaluation")
		return nil
	}
	if sourceType != string(domain.ImageExportSourceTypeImageBuild) {
		log.Debugf("ImageExport %s does not have an ImageBuild source; skipping", exportName)
		return nil
	}

	src, err := imageExport.Spec.Source.AsImageBuildRefSource()
	if err != nil {
		log.WithError(err).Warn("failed to parse ImageExport source")
		return nil
	}

	log.WithField("imageBuildRef", src.ImageBuildRef).Debug("Enqueueing promotions for evaluation after ImageExport completion")
	return c.enqueuePromotionsForBuild(ctx, orgID, src.ImageBuildRef, log)
}
