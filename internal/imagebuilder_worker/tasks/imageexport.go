package tasks

import (
	"context"
	"fmt"
	"time"

	v1beta1 "github.com/flightctl/flightctl/api/v1beta1"
	api "github.com/flightctl/flightctl/api/v1beta1/imagebuilder"
	imagebuilderstore "github.com/flightctl/flightctl/internal/imagebuilder_api/store"
	"github.com/flightctl/flightctl/internal/kvstore"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
)

// processImageExport processes an imageExport job by loading the ImageExport resource
// and converting/pushing the image to the target format
func processImageExport(
	ctx context.Context,
	store imagebuilderstore.Store,
	kvStore kvstore.KVStore,
	job Job,
	log logrus.FieldLogger,
) error {
	log = log.WithField("job", job.Name).WithField("orgId", job.OrgID)
	log.Info("Processing imageExport job")

	// Parse org ID
	orgID, err := uuid.Parse(job.OrgID)
	if err != nil {
		return fmt.Errorf("invalid org ID %q: %w", job.OrgID, err)
	}

	// Load the ImageExport resource from the database
	imageExport, err := store.ImageExport().Get(ctx, orgID, job.Name)
	if err != nil {
		return fmt.Errorf("failed to load ImageExport %q: %w", job.Name, err)
	}

	log.WithField("spec", imageExport.Spec).Debug("Loaded ImageExport resource")

	// Initialize status if nil
	if imageExport.Status == nil {
		imageExport.Status = &api.ImageExportStatus{}
	}

	// Check if already completed or failed - skip if so
	// Note: Completed has Status=True, Failed has Status=False, so we check Reason regardless of Status
	if imageExport.Status.Conditions != nil {
		for _, cond := range *imageExport.Status.Conditions {
			if cond.Type == api.ImageExportConditionTypeReady {
				if cond.Reason == string(api.ImageExportConditionReasonCompleted) || cond.Reason == string(api.ImageExportConditionReasonFailed) {
					log.Infof("ImageExport %q already in terminal state %q, skipping", job.Name, cond.Reason)
					return nil
				}
			}
		}
	}

	// Update status to Converting
	now := time.Now().UTC()
	setImageExportCondition(imageExport, api.ImageExportConditionTypeReady, v1beta1.ConditionStatusFalse, api.ImageExportConditionReasonConverting, "Export conversion in progress", now)
	imageExport.Status.LastSeen = lo.ToPtr(now)

	_, err = store.ImageExport().UpdateStatus(ctx, orgID, imageExport)
	if err != nil {
		return fmt.Errorf("failed to update ImageExport status to Converting: %w", err)
	}

	log.Info("Updated ImageExport status to Converting")

	// TODO(E6): Execute the actual export using bootc-image-builder
	// For now, this is a placeholder that will be implemented in a future epic
	// The export handler will:
	// 1. Load the source image from the ImageBuild
	// 2. Run bootc-image-builder with the appropriate format options (vmdk, qcow2, iso)
	// 3. Stream logs to Redis pub/sub
	// 4. Push the result to the target registry
	// 5. Update the ImageExport status with the result

	// Placeholder: Execute the export
	if err := executeExport(ctx, store, kvStore, imageExport, orgID, log); err != nil {
		// Update status to Failed
		failedTime := time.Now().UTC()
		setImageExportCondition(imageExport, api.ImageExportConditionTypeReady, v1beta1.ConditionStatusFalse, api.ImageExportConditionReasonFailed, err.Error(), failedTime)

		if _, updateErr := store.ImageExport().UpdateStatus(ctx, orgID, imageExport); updateErr != nil {
			log.WithError(updateErr).Error("failed to update ImageExport status to Failed")
		}
		return err
	}

	return nil
}

// setImageExportCondition sets or updates a condition on the ImageExport status
func setImageExportCondition(imageExport *api.ImageExport, conditionType api.ImageExportConditionType, status v1beta1.ConditionStatus, reason api.ImageExportConditionReason, message string, transitionTime time.Time) {
	if imageExport.Status == nil {
		imageExport.Status = &api.ImageExportStatus{}
	}

	if imageExport.Status.Conditions == nil {
		imageExport.Status.Conditions = &[]api.ImageExportCondition{}
	}

	// Find existing condition or add new one
	conditions := *imageExport.Status.Conditions
	found := false
	for i := range conditions {
		if conditions[i].Type == conditionType {
			conditions[i].Status = status
			conditions[i].Reason = string(reason)
			conditions[i].Message = message
			conditions[i].LastTransitionTime = transitionTime
			found = true
			break
		}
	}

	if !found {
		conditions = append(conditions, api.ImageExportCondition{
			Type:               conditionType,
			Status:             status,
			Reason:             string(reason),
			Message:            message,
			LastTransitionTime: transitionTime,
		})
	}

	imageExport.Status.Conditions = &conditions
}

// executeExport is a placeholder for the actual export execution logic
// This will be implemented in a future epic with bootc-image-builder integration
func executeExport(
	ctx context.Context,
	store imagebuilderstore.Store,
	kvStore kvstore.KVStore,
	imageExport *api.ImageExport,
	orgID uuid.UUID,
	log logrus.FieldLogger,
) error {
	log.Info("Export execution placeholder - actual export will be implemented in future epic")

	// Check context for cancellation
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	// TODO: Implement actual export execution here
	// 1. Set up bootc-image-builder with format-specific options
	// 2. Stream export logs to Redis pub/sub for real-time viewing
	// 3. Handle export completion and push results

	// For now, mark as Completed (placeholder)
	now := time.Now().UTC()
	setImageExportCondition(imageExport, api.ImageExportConditionTypeReady, v1beta1.ConditionStatusTrue, api.ImageExportConditionReasonCompleted, "Export completed successfully (placeholder)", now)

	_, err := store.ImageExport().UpdateStatus(ctx, orgID, imageExport)
	if err != nil {
		return fmt.Errorf("failed to update ImageExport status to Completed: %w", err)
	}

	log.Info("ImageExport marked as Completed (placeholder)")
	return nil
}
