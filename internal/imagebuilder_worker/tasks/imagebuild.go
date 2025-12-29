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

// processImageBuild processes an imageBuild job by loading the ImageBuild resource
// and routing to the appropriate build handler
func processImageBuild(
	ctx context.Context,
	store imagebuilderstore.Store,
	kvStore kvstore.KVStore,
	job Job,
	log logrus.FieldLogger,
) error {
	log = log.WithField("job", job.Name).WithField("orgId", job.OrgID)
	log.Info("Processing imageBuild job")

	// Parse org ID
	orgID, err := uuid.Parse(job.OrgID)
	if err != nil {
		return fmt.Errorf("invalid org ID %q: %w", job.OrgID, err)
	}

	// Load the ImageBuild resource from the database
	imageBuild, err := store.ImageBuild().Get(ctx, orgID, job.Name)
	if err != nil {
		return fmt.Errorf("failed to load ImageBuild %q: %w", job.Name, err)
	}

	log.WithField("spec", imageBuild.Spec).Debug("Loaded ImageBuild resource")

	// Initialize status if nil
	if imageBuild.Status == nil {
		imageBuild.Status = &api.ImageBuildStatus{}
	}

	// Check if already completed or failed - skip if so
	if imageBuild.Status.Conditions != nil {
		for _, cond := range *imageBuild.Status.Conditions {
			if cond.Type == api.ImageBuildConditionTypeReady {
				isCompleted := cond.Reason == string(api.ImageBuildConditionReasonCompleted) && cond.Status == v1beta1.ConditionStatusTrue
				isFailed := cond.Reason == string(api.ImageBuildConditionReasonFailed) && cond.Status == v1beta1.ConditionStatusFalse
				if isCompleted || isFailed {
					log.Infof("ImageBuild %q already in terminal state %q, skipping", job.Name, cond.Reason)
					return nil
				}
			}
		}
	}

	// Update status to Building
	now := time.Now().UTC()
	setImageBuildCondition(imageBuild, api.ImageBuildConditionTypeReady, v1beta1.ConditionStatusFalse, api.ImageBuildConditionReasonBuilding, "Build is in progress", now)
	imageBuild.Status.LastSeen = lo.ToPtr(now)

	_, err = store.ImageBuild().UpdateStatus(ctx, orgID, imageBuild)
	if err != nil {
		return fmt.Errorf("failed to update ImageBuild status to Building: %w", err)
	}

	log.Info("Updated ImageBuild status to Building")

	// TODO(E5): Execute the actual build using bootc-image-builder
	// For now, this is a placeholder that will be implemented in E5
	// The build handler will:
	// 1. Pull the source image
	// 2. Run bootc-image-builder with the appropriate options
	// 3. Stream logs to Redis pub/sub
	// 4. Push the result to the target registry
	// 5. Update the ImageBuild status with the result

	// Placeholder: Execute the build
	if err := executeBuild(ctx, store, kvStore, imageBuild, orgID, log); err != nil {
		// Update status to Failed
		failedTime := time.Now().UTC()
		setImageBuildCondition(imageBuild, api.ImageBuildConditionTypeReady, v1beta1.ConditionStatusFalse, api.ImageBuildConditionReasonFailed, err.Error(), failedTime)

		if _, updateErr := store.ImageBuild().UpdateStatus(ctx, orgID, imageBuild); updateErr != nil {
			log.WithError(updateErr).Error("failed to update ImageBuild status to Failed")
		}
		return err
	}

	return nil
}

// setImageBuildCondition sets or updates a condition on the ImageBuild status
func setImageBuildCondition(imageBuild *api.ImageBuild, conditionType api.ImageBuildConditionType, status v1beta1.ConditionStatus, reason api.ImageBuildConditionReason, message string, transitionTime time.Time) {
	if imageBuild.Status == nil {
		imageBuild.Status = &api.ImageBuildStatus{}
	}

	if imageBuild.Status.Conditions == nil {
		imageBuild.Status.Conditions = &[]api.ImageBuildCondition{}
	}

	// Find existing condition or add new one
	conditions := *imageBuild.Status.Conditions
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
		conditions = append(conditions, api.ImageBuildCondition{
			Type:               conditionType,
			Status:             status,
			Reason:             string(reason),
			Message:            message,
			LastTransitionTime: transitionTime,
		})
	}

	imageBuild.Status.Conditions = &conditions
}

// executeBuild is a placeholder for the actual build execution logic
// This will be implemented in E5 with podman/bootc-image-builder integration
func executeBuild(
	ctx context.Context,
	store imagebuilderstore.Store,
	kvStore kvstore.KVStore,
	imageBuild *api.ImageBuild,
	orgID uuid.UUID,
	log logrus.FieldLogger,
) error {
	log.Info("Build execution placeholder - actual build will be implemented in E5")

	// For now, just mark as succeeded after a brief delay to simulate work
	// This placeholder allows testing the queue consumer infrastructure

	// Check context for cancellation
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	// TODO(E5): Implement actual build execution here
	// 1. Set up podman with bootc-image-builder
	// 2. Stream build logs to Redis pub/sub for real-time viewing
	// 3. Handle build completion and push results

	// For now, mark as Completed (placeholder)
	now := time.Now().UTC()
	setImageBuildCondition(imageBuild, api.ImageBuildConditionTypeReady, v1beta1.ConditionStatusTrue, api.ImageBuildConditionReasonCompleted, "Build completed successfully (placeholder)", now)

	_, err := store.ImageBuild().UpdateStatus(ctx, orgID, imageBuild)
	if err != nil {
		return fmt.Errorf("failed to update ImageBuild status to Completed: %w", err)
	}

	log.Info("ImageBuild marked as Completed (placeholder)")
	return nil
}
