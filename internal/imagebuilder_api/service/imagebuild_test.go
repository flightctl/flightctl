package service

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	api "github.com/flightctl/flightctl/api/imagebuilder/v1alpha1"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
)

func statusCode(status v1beta1.Status) int32 {
	return status.Code
}

func newTestImageBuildService() (ImageBuildService, *DummyImageBuildStore) {
	imageBuildStore := NewDummyImageBuildStore()
	repositoryStore := NewDummyRepositoryStore()
	svc := NewImageBuildService(imageBuildStore, repositoryStore, nil, nil, nil, nil, nil, log.InitLogs())
	return svc, imageBuildStore
}

func newTestImageBuildServiceWithExports() (ImageBuildService, *DummyImageBuildStore, *DummyImageExportStore) {
	imageExportStore := NewDummyImageExportStore()
	imageBuildStore := NewDummyImageBuildStoreWithExports(imageExportStore)
	repositoryStore := NewDummyRepositoryStore()
	// Note: ImageBuildService doesn't need ImageExportService - the store handles withExports
	svc := NewImageBuildService(imageBuildStore, repositoryStore, nil, nil, nil, nil, nil, log.InitLogs())
	return svc, imageBuildStore, imageExportStore
}

func newValidImageBuild(name string) api.ImageBuild {
	return api.ImageBuild{
		ApiVersion: api.ImageBuildAPIVersion,
		Kind:       string(api.ResourceKindImageBuild),
		Metadata: v1beta1.ObjectMeta{
			Name: lo.ToPtr(name),
		},
		Spec: api.ImageBuildSpec{
			Source: api.ImageBuildSource{
				Repository: "input-registry",
				ImageName:  "input-image",
				ImageTag:   "v1.0",
			},
			Destination: api.ImageBuildDestination{
				Repository: "output-registry",
				ImageName:  "output-image",
				ImageTag:   "v1.0",
			},
		},
	}
}

func setupRepositoriesForImageBuild(repoStore *DummyRepositoryStore, ctx context.Context, orgId uuid.UUID) {
	// Create source repository (Read is fine for source)
	sourceRepo := newOciRepository("input-registry", v1beta1.Read)
	_, _ = repoStore.Create(ctx, orgId, sourceRepo, nil)

	// Create destination repository (must be ReadWrite)
	destRepo := newOciRepository("output-registry", v1beta1.ReadWrite)
	_, _ = repoStore.Create(ctx, orgId, destRepo, nil)
}

func TestCreateImageBuild(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	orgId := uuid.New()

	// Set up repositories
	repoStore := NewDummyRepositoryStore()
	setupRepositoriesForImageBuild(repoStore, ctx, orgId)
	svc := NewImageBuildService(NewDummyImageBuildStore(), repoStore, nil, nil, nil, nil, nil, log.InitLogs())

	imageBuild := newValidImageBuild("test-build")
	result, status := svc.Create(ctx, orgId, imageBuild)

	require.Equal(int32(http.StatusCreated), statusCode(status))
	require.NotNil(result)
	require.Equal("test-build", lo.FromPtr(result.Metadata.Name))
}

func TestCreateImageBuildDuplicate(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	orgId := uuid.New()

	// Set up repositories
	repoStore := NewDummyRepositoryStore()
	setupRepositoriesForImageBuild(repoStore, ctx, orgId)
	svc := NewImageBuildService(NewDummyImageBuildStore(), repoStore, nil, nil, nil, nil, nil, log.InitLogs())

	imageBuild := newValidImageBuild("duplicate-test")

	// First create should succeed
	_, status := svc.Create(ctx, orgId, imageBuild)
	require.Equal(int32(http.StatusCreated), statusCode(status))

	// Second create should fail with conflict
	_, status = svc.Create(ctx, orgId, imageBuild)
	require.Equal(int32(http.StatusConflict), statusCode(status))
}

func TestCreateImageBuildMissingInputRegistry(t *testing.T) {
	require := require.New(t)
	svc, _ := newTestImageBuildService()
	ctx := context.Background()
	orgId := uuid.New()

	imageBuild := api.ImageBuild{
		Metadata: v1beta1.ObjectMeta{Name: lo.ToPtr("test")},
		Spec: api.ImageBuildSpec{
			Source: api.ImageBuildSource{
				ImageName: "input-image",
				ImageTag:  "v1.0",
				// Missing Repository
			},
			Destination: api.ImageBuildDestination{
				Repository: "output-registry",
				ImageName:  "output-image",
				ImageTag:   "v1.0",
			},
		},
	}

	_, status := svc.Create(ctx, orgId, imageBuild)
	require.Equal(int32(http.StatusBadRequest), statusCode(status))
}

func TestGetImageBuild(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	orgId := uuid.New()

	// Set up repositories
	repoStore := NewDummyRepositoryStore()
	setupRepositoriesForImageBuild(repoStore, ctx, orgId)
	svc := NewImageBuildService(NewDummyImageBuildStore(), repoStore, nil, nil, nil, nil, nil, log.InitLogs())

	// Create first
	imageBuild := newValidImageBuild("get-test")
	_, status := svc.Create(ctx, orgId, imageBuild)
	require.Equal(int32(http.StatusCreated), statusCode(status))

	// Get it back
	result, status := svc.Get(ctx, orgId, "get-test", false)
	require.Equal(int32(http.StatusOK), statusCode(status))
	require.NotNil(result)
	require.Equal("get-test", lo.FromPtr(result.Metadata.Name))
}

func TestGetImageBuildNotFound(t *testing.T) {
	require := require.New(t)
	svc, _ := newTestImageBuildService()
	ctx := context.Background()
	orgId := uuid.New()

	_, status := svc.Get(ctx, orgId, "nonexistent", false)
	require.Equal(int32(http.StatusNotFound), statusCode(status))
}

func TestListImageBuilds(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	orgId := uuid.New()

	// Set up repositories
	repoStore := NewDummyRepositoryStore()
	setupRepositoriesForImageBuild(repoStore, ctx, orgId)
	svc := NewImageBuildService(NewDummyImageBuildStore(), repoStore, nil, nil, nil, nil, nil, log.InitLogs())

	// Create multiple
	for i := 0; i < 3; i++ {
		imageBuild := newValidImageBuild(string(rune('a'+i)) + "-build")
		_, status := svc.Create(ctx, orgId, imageBuild)
		require.Equal(int32(http.StatusCreated), statusCode(status))
	}

	// List all
	result, status := svc.List(ctx, orgId, api.ListImageBuildsParams{})
	require.Equal(int32(http.StatusOK), statusCode(status))
	require.NotNil(result)
	require.Len(result.Items, 3)
}

func TestListImageBuildsWithLimit(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	orgId := uuid.New()

	// Set up repositories
	repoStore := NewDummyRepositoryStore()
	setupRepositoriesForImageBuild(repoStore, ctx, orgId)
	svc := NewImageBuildService(NewDummyImageBuildStore(), repoStore, nil, nil, nil, nil, nil, log.InitLogs())

	// Create multiple
	for i := 0; i < 5; i++ {
		imageBuild := newValidImageBuild(string(rune('a'+i)) + "-build")
		_, status := svc.Create(ctx, orgId, imageBuild)
		require.Equal(int32(http.StatusCreated), statusCode(status))
	}

	// List with limit
	limit := int32(2)
	result, status := svc.List(ctx, orgId, api.ListImageBuildsParams{Limit: &limit})
	require.Equal(int32(http.StatusOK), statusCode(status))
	require.NotNil(result)
	require.Len(result.Items, 2)
}

// Helper to create a short timeout config for delete tests
func newShortTimeoutConfigForBuild() *config.ImageBuilderServiceConfig {
	cfg := config.NewDefaultImageBuilderServiceConfig()
	cfg.DeleteCancelTimeout = util.Duration(100 * time.Millisecond) // Very short timeout for testing
	return cfg
}

// Helper to set up ImageBuild service with KVStore and short timeout for delete tests
func setupImageBuildDeleteTestService(ctx context.Context, orgId uuid.UUID, kvStore *DummyKVStore) (ImageBuildService, ImageExportService, *DummyImageBuildStore, *DummyImageExportStore) {
	repoStore := NewDummyRepositoryStore()
	setupRepositoriesForImageBuild(repoStore, ctx, orgId)
	imageBuildStore := NewDummyImageBuildStore()
	imageExportStore := NewDummyImageExportStore()

	cfg := newShortTimeoutConfigForBuild()

	// Create ImageExportService first (ImageBuildService depends on it for delete flow)
	imageExportSvc := NewImageExportService(imageExportStore, imageBuildStore, repoStore, nil, nil, kvStore, cfg, log.InitLogs())
	// Create ImageBuildService with ImageExportService dependency
	imageBuildSvc := NewImageBuildService(imageBuildStore, repoStore, imageExportSvc, nil, nil, kvStore, cfg, log.InitLogs())

	return imageBuildSvc, imageExportSvc, imageBuildStore, imageExportStore
}

// Helper to create an ImageBuild with a specific status condition
func createImageBuildWithStatus(ctx context.Context, svc ImageBuildService, orgId uuid.UUID, name string, reason api.ImageBuildConditionReason) *api.ImageBuild {
	imageBuild := newValidImageBuild(name)
	created, _ := svc.Create(ctx, orgId, imageBuild)

	if created == nil {
		return nil
	}

	// Initialize status if needed
	if created.Status == nil {
		created.Status = &api.ImageBuildStatus{}
	}
	if created.Status.Conditions == nil {
		created.Status.Conditions = &[]api.ImageBuildCondition{}
	}

	// Set the appropriate status based on reason
	conditionStatus := v1beta1.ConditionStatusFalse
	if reason == api.ImageBuildConditionReasonCompleted {
		conditionStatus = v1beta1.ConditionStatusTrue
	}

	api.SetImageBuildStatusCondition(created.Status.Conditions, api.ImageBuildCondition{
		Type:               api.ImageBuildConditionTypeReady,
		Status:             conditionStatus,
		Reason:             string(reason),
		Message:            "Test status",
		LastTransitionTime: time.Now().UTC(),
	})
	updated, _ := svc.UpdateStatus(ctx, orgId, created)
	return updated
}

func TestDeleteImageBuild_Pending_CancelSuccess(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	orgId := uuid.New()

	kvStore := NewDummyKVStore()
	svc, _, _, _ := setupImageBuildDeleteTestService(ctx, orgId, kvStore)

	// Create ImageBuild (starts in Pending state)
	imageBuild := newValidImageBuild("delete-pending-success")
	_, status := svc.Create(ctx, orgId, imageBuild)
	require.Equal(int32(http.StatusCreated), statusCode(status))

	// Simulate that the worker will send the canceled signal
	canceledStreamKey := fmt.Sprintf("imagebuild:canceled:%s:%s", orgId.String(), "delete-pending-success")
	kvStore.SimulateCanceledSignal(canceledStreamKey)

	// Delete it - should cancel first and wait for signal
	deleted, status := svc.Delete(ctx, orgId, "delete-pending-success")
	require.Equal(int32(http.StatusOK), statusCode(status))
	require.NotNil(deleted)
	require.Equal("delete-pending-success", lo.FromPtr(deleted.Metadata.Name))

	// Verify it's gone
	_, status = svc.Get(ctx, orgId, "delete-pending-success", false)
	require.Equal(int32(http.StatusNotFound), statusCode(status))
}

func TestDeleteImageBuild_Building_CancelSuccess(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	orgId := uuid.New()

	kvStore := NewDummyKVStore()
	svc, _, _, _ := setupImageBuildDeleteTestService(ctx, orgId, kvStore)

	// Create ImageBuild and set to Building state
	created := createImageBuildWithStatus(ctx, svc, orgId, "delete-building-success", api.ImageBuildConditionReasonBuilding)
	require.NotNil(created)

	// Simulate that the worker will send the canceled signal
	canceledStreamKey := fmt.Sprintf("imagebuild:canceled:%s:%s", orgId.String(), "delete-building-success")
	kvStore.SimulateCanceledSignal(canceledStreamKey)

	// Delete it - should cancel first and wait for signal
	deleted, status := svc.Delete(ctx, orgId, "delete-building-success")
	require.Equal(int32(http.StatusOK), statusCode(status))
	require.NotNil(deleted)

	// Verify it's gone
	_, status = svc.Get(ctx, orgId, "delete-building-success", false)
	require.Equal(int32(http.StatusNotFound), statusCode(status))
}

func TestDeleteImageBuild_Pushing_CancelSuccess(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	orgId := uuid.New()

	kvStore := NewDummyKVStore()
	svc, _, _, _ := setupImageBuildDeleteTestService(ctx, orgId, kvStore)

	// Create ImageBuild and set to Pushing state
	created := createImageBuildWithStatus(ctx, svc, orgId, "delete-pushing-success", api.ImageBuildConditionReasonPushing)
	require.NotNil(created)

	// Simulate that the worker will send the canceled signal
	canceledStreamKey := fmt.Sprintf("imagebuild:canceled:%s:%s", orgId.String(), "delete-pushing-success")
	kvStore.SimulateCanceledSignal(canceledStreamKey)

	// Delete it - should cancel first and wait for signal
	deleted, status := svc.Delete(ctx, orgId, "delete-pushing-success")
	require.Equal(int32(http.StatusOK), statusCode(status))
	require.NotNil(deleted)

	// Verify it's gone
	_, status = svc.Get(ctx, orgId, "delete-pushing-success", false)
	require.Equal(int32(http.StatusNotFound), statusCode(status))
}

func TestDeleteImageBuild_CancelTimeout(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	orgId := uuid.New()

	kvStore := NewDummyKVStore()
	svc, _, _, _ := setupImageBuildDeleteTestService(ctx, orgId, kvStore)

	// Create ImageBuild (starts in Pending state - cancelable)
	imageBuild := newValidImageBuild("delete-timeout")
	_, status := svc.Create(ctx, orgId, imageBuild)
	require.Equal(int32(http.StatusCreated), statusCode(status))

	// Do NOT simulate canceled signal - this will cause timeout

	// Delete it - should cancel, timeout waiting, then delete anyway
	start := time.Now()
	deleted, status := svc.Delete(ctx, orgId, "delete-timeout")
	elapsed := time.Since(start)

	require.Equal(int32(http.StatusOK), statusCode(status))
	require.NotNil(deleted)
	require.Equal("delete-timeout", lo.FromPtr(deleted.Metadata.Name))

	// Verify timeout happened (should take at least 100ms but not too long)
	require.GreaterOrEqual(elapsed.Milliseconds(), int64(100), "Should have waited for timeout")
	require.Less(elapsed.Milliseconds(), int64(500), "Should not wait too long")

	// Verify it's gone
	_, status = svc.Get(ctx, orgId, "delete-timeout", false)
	require.Equal(int32(http.StatusNotFound), statusCode(status))
}

func TestDeleteImageBuild_Completed_NoCancelAttempt(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	orgId := uuid.New()

	kvStore := NewDummyKVStore()
	svc, _, _, _ := setupImageBuildDeleteTestService(ctx, orgId, kvStore)

	// Create ImageBuild and set to Completed state (not cancelable)
	created := createImageBuildWithStatus(ctx, svc, orgId, "delete-completed", api.ImageBuildConditionReasonCompleted)
	require.NotNil(created)

	// Delete it - should NOT try to cancel (not cancelable)
	start := time.Now()
	deleted, status := svc.Delete(ctx, orgId, "delete-completed")
	elapsed := time.Since(start)

	require.Equal(int32(http.StatusOK), statusCode(status))
	require.NotNil(deleted)

	// Should be fast (no cancel wait)
	require.Less(elapsed.Milliseconds(), int64(50), "Should not wait for cancel on completed build")

	// Verify it's gone
	_, status = svc.Get(ctx, orgId, "delete-completed", false)
	require.Equal(int32(http.StatusNotFound), statusCode(status))
}

func TestDeleteImageBuild_Failed_NoCancelAttempt(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	orgId := uuid.New()

	kvStore := NewDummyKVStore()
	svc, _, _, _ := setupImageBuildDeleteTestService(ctx, orgId, kvStore)

	// Create ImageBuild and set to Failed state (not cancelable)
	created := createImageBuildWithStatus(ctx, svc, orgId, "delete-failed", api.ImageBuildConditionReasonFailed)
	require.NotNil(created)

	// Delete it - should NOT try to cancel (not cancelable)
	start := time.Now()
	deleted, status := svc.Delete(ctx, orgId, "delete-failed")
	elapsed := time.Since(start)

	require.Equal(int32(http.StatusOK), statusCode(status))
	require.NotNil(deleted)

	// Should be fast (no cancel wait)
	require.Less(elapsed.Milliseconds(), int64(50), "Should not wait for cancel on failed build")

	// Verify it's gone
	_, status = svc.Get(ctx, orgId, "delete-failed", false)
	require.Equal(int32(http.StatusNotFound), statusCode(status))
}

func TestDeleteImageBuild_Canceled_NoCancelAttempt(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	orgId := uuid.New()

	kvStore := NewDummyKVStore()
	svc, _, _, _ := setupImageBuildDeleteTestService(ctx, orgId, kvStore)

	// Create ImageBuild and set to Canceled state (not cancelable)
	created := createImageBuildWithStatus(ctx, svc, orgId, "delete-canceled", api.ImageBuildConditionReasonCanceled)
	require.NotNil(created)

	// Delete it - should NOT try to cancel (already canceled)
	start := time.Now()
	deleted, status := svc.Delete(ctx, orgId, "delete-canceled")
	elapsed := time.Since(start)

	require.Equal(int32(http.StatusOK), statusCode(status))
	require.NotNil(deleted)

	// Should be fast (no cancel wait)
	require.Less(elapsed.Milliseconds(), int64(50), "Should not wait for cancel on already canceled build")

	// Verify it's gone
	_, status = svc.Get(ctx, orgId, "delete-canceled", false)
	require.Equal(int32(http.StatusNotFound), statusCode(status))
}

func TestDeleteImageBuild_Canceling_NoCancelAttempt(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	orgId := uuid.New()

	kvStore := NewDummyKVStore()
	svc, _, _, _ := setupImageBuildDeleteTestService(ctx, orgId, kvStore)

	// Create ImageBuild and set to Canceling state (not cancelable - already canceling)
	created := createImageBuildWithStatus(ctx, svc, orgId, "delete-canceling", api.ImageBuildConditionReasonCanceling)
	require.NotNil(created)

	// Delete it - should NOT try to cancel (already canceling)
	start := time.Now()
	deleted, status := svc.Delete(ctx, orgId, "delete-canceling")
	elapsed := time.Since(start)

	require.Equal(int32(http.StatusOK), statusCode(status))
	require.NotNil(deleted)

	// Should be fast (no cancel wait)
	require.Less(elapsed.Milliseconds(), int64(50), "Should not wait for cancel on already canceling build")

	// Verify it's gone
	_, status = svc.Get(ctx, orgId, "delete-canceling", false)
	require.Equal(int32(http.StatusNotFound), statusCode(status))
}

func TestDeleteImageBuildNotFound(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	orgId := uuid.New()

	kvStore := NewDummyKVStore()
	svc, _, _, _ := setupImageBuildDeleteTestService(ctx, orgId, kvStore)

	// Delete is idempotent - deleting non-existent resource returns success
	deleted, status := svc.Delete(ctx, orgId, "nonexistent")
	require.Equal(int32(http.StatusOK), statusCode(status))
	require.Nil(deleted)
}

func TestDeleteImageBuild_CascadeDeletesImageExports(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	orgId := uuid.New()

	kvStore := NewDummyKVStore()
	svc, imageExportSvc, _, _ := setupImageBuildDeleteTestService(ctx, orgId, kvStore)

	// Create ImageBuild
	imageBuild := newValidImageBuild("cascade-test-build")
	created, status := svc.Create(ctx, orgId, imageBuild)
	require.Equal(int32(http.StatusCreated), statusCode(status))
	require.NotNil(created)

	// Create ImageExports that reference the ImageBuild
	export1 := newValidImageExportWithBuildRef("cascade-export-1", "cascade-test-build")
	_, status = imageExportSvc.Create(ctx, orgId, export1)
	require.Equal(int32(http.StatusCreated), statusCode(status))

	export2 := newValidImageExportWithBuildRef("cascade-export-2", "cascade-test-build")
	_, status = imageExportSvc.Create(ctx, orgId, export2)
	require.Equal(int32(http.StatusCreated), statusCode(status))

	// Simulate canceled signals for all resources (ImageBuild + both ImageExports)
	kvStore.SimulateCanceledSignal(fmt.Sprintf("imagebuild:canceled:%s:%s", orgId.String(), "cascade-test-build"))
	kvStore.SimulateCanceledSignal(fmt.Sprintf("imageexport:canceled:%s:%s", orgId.String(), "cascade-export-1"))
	kvStore.SimulateCanceledSignal(fmt.Sprintf("imageexport:canceled:%s:%s", orgId.String(), "cascade-export-2"))

	// Delete the ImageBuild - should cascade delete related ImageExports
	deleted, status := svc.Delete(ctx, orgId, "cascade-test-build")
	require.Equal(int32(http.StatusOK), statusCode(status))
	require.NotNil(deleted)

	// Verify ImageBuild is gone
	_, status = svc.Get(ctx, orgId, "cascade-test-build", false)
	require.Equal(int32(http.StatusNotFound), statusCode(status))

	// Verify related ImageExports are gone
	_, status = imageExportSvc.Get(ctx, orgId, "cascade-export-1")
	require.Equal(int32(http.StatusNotFound), statusCode(status))

	_, status = imageExportSvc.Get(ctx, orgId, "cascade-export-2")
	require.Equal(int32(http.StatusNotFound), statusCode(status))
}

// Helper to create an ImageExport with a specific imageBuildRef
func newValidImageExportWithBuildRef(name, imageBuildRef string) api.ImageExport {
	source := api.ImageExportSource{}
	_ = source.FromImageBuildRefSource(api.ImageBuildRefSource{
		Type:          api.ImageBuildRefSourceTypeImageBuild,
		ImageBuildRef: imageBuildRef,
	})

	return api.ImageExport{
		ApiVersion: api.ImageExportAPIVersion,
		Kind:       string(api.ResourceKindImageExport),
		Metadata: v1beta1.ObjectMeta{
			Name: lo.ToPtr(name),
		},
		Spec: api.ImageExportSpec{
			Source: source,
			Format: api.ExportFormatTypeQCOW2,
		},
	}
}

func TestUpdateStatus(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	orgId := uuid.New()

	// Set up repositories
	repoStore := NewDummyRepositoryStore()
	setupRepositoriesForImageBuild(repoStore, ctx, orgId)
	svc := NewImageBuildService(NewDummyImageBuildStore(), repoStore, nil, nil, nil, nil, nil, log.InitLogs())

	// Create first
	imageBuild := newValidImageBuild("status-test")
	_, status := svc.Create(ctx, orgId, imageBuild)
	require.Equal(int32(http.StatusCreated), statusCode(status))

	// Update status with condition
	now := time.Now()
	imageBuild.Status = &api.ImageBuildStatus{
		Conditions: &[]api.ImageBuildCondition{
			{
				Type:               api.ImageBuildConditionTypeReady,
				Status:             v1beta1.ConditionStatusUnknown,
				Reason:             string(api.ImageBuildConditionReasonBuilding),
				Message:            "Build in progress",
				LastTransitionTime: now,
			},
		},
	}
	result, err := svc.UpdateStatus(ctx, orgId, &imageBuild)
	require.NoError(err)
	require.NotNil(result)
	require.NotNil(result.Status)
	require.NotNil(result.Status.Conditions)
	require.Len(*result.Status.Conditions, 1)
	require.Equal(api.ImageBuildConditionTypeReady, (*result.Status.Conditions)[0].Type)
	require.Equal(string(api.ImageBuildConditionReasonBuilding), (*result.Status.Conditions)[0].Reason)
}

func TestGetImageBuildWithExports(t *testing.T) {
	require := require.New(t)
	_, _, imageExportStore := newTestImageBuildServiceWithExports()
	ctx := context.Background()
	orgId := uuid.New()

	// Set up repositories
	repoStore := NewDummyRepositoryStore()
	setupRepositoriesForImageBuild(repoStore, ctx, orgId)
	svc := NewImageBuildService(NewDummyImageBuildStoreWithExports(imageExportStore), repoStore, nil, nil, nil, nil, nil, log.InitLogs())

	// Create an ImageBuild
	imageBuild := newValidImageBuild("build-with-exports")
	_, status := svc.Create(ctx, orgId, imageBuild)
	require.Equal(int32(http.StatusCreated), statusCode(status))

	// Create ImageExports that reference the ImageBuild
	imageExport1 := newImageExportWithImageBuildSource("export-1", "build-with-exports")
	_, err := imageExportStore.Create(ctx, orgId, &imageExport1)
	require.NoError(err)

	imageExport2 := newImageExportWithImageBuildSource("export-2", "build-with-exports")
	_, err = imageExportStore.Create(ctx, orgId, &imageExport2)
	require.NoError(err)

	// Get without withExports - should not include ImageExports
	result, status := svc.Get(ctx, orgId, "build-with-exports", false)
	require.Equal(int32(http.StatusOK), statusCode(status))
	require.NotNil(result)
	require.Nil(result.Imageexports)

	// Get with withExports=true - should include ImageExports
	result, status = svc.Get(ctx, orgId, "build-with-exports", true)
	require.Equal(int32(http.StatusOK), statusCode(status))
	require.NotNil(result)
	require.NotNil(result.Imageexports)
	require.Len(*result.Imageexports, 2)

	// Verify the ImageExports are correct
	exportNames := make(map[string]bool)
	for _, export := range *result.Imageexports {
		exportNames[lo.FromPtr(export.Metadata.Name)] = true
	}
	require.True(exportNames["export-1"])
	require.True(exportNames["export-2"])
}

func TestGetImageBuildWithExportsNoExports(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	orgId := uuid.New()

	// Set up repositories
	repoStore := NewDummyRepositoryStore()
	setupRepositoriesForImageBuild(repoStore, ctx, orgId)
	svc := NewImageBuildService(NewDummyImageBuildStoreWithExports(NewDummyImageExportStore()), repoStore, nil, nil, nil, nil, nil, log.InitLogs())

	// Create an ImageBuild with no ImageExports
	imageBuild := newValidImageBuild("build-no-exports")
	_, status := svc.Create(ctx, orgId, imageBuild)
	require.Equal(int32(http.StatusCreated), statusCode(status))

	// Get with withExports=true - should return nil Imageexports (not empty array)
	result, status := svc.Get(ctx, orgId, "build-no-exports", true)
	require.Equal(int32(http.StatusOK), statusCode(status))
	require.NotNil(result)
	require.Nil(result.Imageexports)
}

func TestListImageBuildsWithExports(t *testing.T) {
	require := require.New(t)
	_, _, imageExportStore := newTestImageBuildServiceWithExports()
	ctx := context.Background()
	orgId := uuid.New()

	// Set up repositories
	repoStore := NewDummyRepositoryStore()
	setupRepositoriesForImageBuild(repoStore, ctx, orgId)
	svc := NewImageBuildService(NewDummyImageBuildStoreWithExports(imageExportStore), repoStore, nil, nil, nil, nil, nil, log.InitLogs())

	// Create multiple ImageBuilds
	build1 := newValidImageBuild("build-1")
	_, status := svc.Create(ctx, orgId, build1)
	require.Equal(int32(http.StatusCreated), statusCode(status))

	build2 := newValidImageBuild("build-2")
	_, status = svc.Create(ctx, orgId, build2)
	require.Equal(int32(http.StatusCreated), statusCode(status))

	build3 := newValidImageBuild("build-3")
	_, status = svc.Create(ctx, orgId, build3)
	require.Equal(int32(http.StatusCreated), statusCode(status))

	// Create ImageExports that reference the ImageBuilds
	export1 := newImageExportWithImageBuildSource("export-1", "build-1")
	_, err := imageExportStore.Create(ctx, orgId, &export1)
	require.NoError(err)

	export2 := newImageExportWithImageBuildSource("export-2", "build-1")
	_, err = imageExportStore.Create(ctx, orgId, &export2)
	require.NoError(err)

	export3 := newImageExportWithImageBuildSource("export-3", "build-2")
	_, err = imageExportStore.Create(ctx, orgId, &export3)
	require.NoError(err)

	// List without withExports - should not include ImageExports
	result, status := svc.List(ctx, orgId, api.ListImageBuildsParams{})
	require.Equal(int32(http.StatusOK), statusCode(status))
	require.NotNil(result)
	require.Len(result.Items, 3)
	for _, item := range result.Items {
		require.Nil(item.Imageexports)
	}

	// List with withExports=true - should include ImageExports
	withExports := true
	result, status = svc.List(ctx, orgId, api.ListImageBuildsParams{WithExports: &withExports})
	require.Equal(int32(http.StatusOK), statusCode(status))
	require.NotNil(result)
	require.Len(result.Items, 3)

	// Verify ImageExports are attached correctly
	build1Found := false
	build2Found := false
	build3Found := false
	for _, item := range result.Items {
		name := lo.FromPtr(item.Metadata.Name)
		switch name {
		case "build-1":
			build1Found = true
			require.NotNil(item.Imageexports)
			require.Len(*item.Imageexports, 2)
		case "build-2":
			build2Found = true
			require.NotNil(item.Imageexports)
			require.Len(*item.Imageexports, 1)
		case "build-3":
			build3Found = true
			require.Nil(item.Imageexports) // No exports for build-3
		}
	}
	require.True(build1Found)
	require.True(build2Found)
	require.True(build3Found)
}

func TestCreateImageBuildSourceRepositoryNotFound(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	orgId := uuid.New()

	// Set up only destination repository
	repoStore := NewDummyRepositoryStore()
	destRepo := newOciRepository("output-registry", v1beta1.ReadWrite)
	_, _ = repoStore.Create(ctx, orgId, destRepo, nil)
	svc := NewImageBuildService(NewDummyImageBuildStore(), repoStore, nil, nil, nil, nil, nil, log.InitLogs())

	imageBuild := newValidImageBuild("test-build")
	_, status := svc.Create(ctx, orgId, imageBuild)

	require.Equal(int32(http.StatusBadRequest), statusCode(status))
	require.Contains(status.Message, "spec.source.repository: Repository \"input-registry\" not found")
}

func TestCreateImageBuildDestinationRepositoryNotFound(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	orgId := uuid.New()

	// Set up only source repository
	repoStore := NewDummyRepositoryStore()
	sourceRepo := newOciRepository("input-registry", v1beta1.Read)
	_, _ = repoStore.Create(ctx, orgId, sourceRepo, nil)
	svc := NewImageBuildService(NewDummyImageBuildStore(), repoStore, nil, nil, nil, nil, nil, log.InitLogs())

	imageBuild := newValidImageBuild("test-build")
	_, status := svc.Create(ctx, orgId, imageBuild)

	require.Equal(int32(http.StatusBadRequest), statusCode(status))
	require.Contains(status.Message, "spec.destination.repository: Repository \"output-registry\" not found")
}

func TestCreateImageBuildSourceRepositoryNotOci(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	orgId := uuid.New()

	// Set up repositories - source is Git type (not OCI)
	repoStore := NewDummyRepositoryStore()
	spec := v1beta1.RepositorySpec{}
	_ = spec.FromGitRepoSpec(v1beta1.GitRepoSpec{
		Type: v1beta1.GitRepoSpecTypeGit,
		Url:  "https://github.com/example/repo.git",
	})
	sourceRepo := &v1beta1.Repository{
		ApiVersion: "flightctl.io/v1beta1",
		Kind:       string(v1beta1.ResourceKindRepository),
		Metadata: v1beta1.ObjectMeta{
			Name: lo.ToPtr("input-registry"),
		},
		Spec: spec,
	}
	_, _ = repoStore.Create(ctx, orgId, sourceRepo, nil)

	destRepo := newOciRepository("output-registry", v1beta1.ReadWrite)
	_, _ = repoStore.Create(ctx, orgId, destRepo, nil)
	svc := NewImageBuildService(NewDummyImageBuildStore(), repoStore, nil, nil, nil, nil, nil, log.InitLogs())

	imageBuild := newValidImageBuild("test-build")
	_, status := svc.Create(ctx, orgId, imageBuild)

	require.Equal(int32(http.StatusBadRequest), statusCode(status))
	require.Contains(status.Message, "spec.source.repository: Repository \"input-registry\" must be of type 'oci'")
}

func TestCreateImageBuildDestinationRepositoryNotOci(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	orgId := uuid.New()

	// Set up repositories - destination is Git type (not OCI)
	repoStore := NewDummyRepositoryStore()
	sourceRepo := newOciRepository("input-registry", v1beta1.Read)
	_, _ = repoStore.Create(ctx, orgId, sourceRepo, nil)

	spec := v1beta1.RepositorySpec{}
	_ = spec.FromGitRepoSpec(v1beta1.GitRepoSpec{
		Type: v1beta1.GitRepoSpecTypeGit,
		Url:  "https://github.com/example/repo.git",
	})
	destRepo := &v1beta1.Repository{
		ApiVersion: "flightctl.io/v1beta1",
		Kind:       string(v1beta1.ResourceKindRepository),
		Metadata: v1beta1.ObjectMeta{
			Name: lo.ToPtr("output-registry"),
		},
		Spec: spec,
	}
	_, _ = repoStore.Create(ctx, orgId, destRepo, nil)
	svc := NewImageBuildService(NewDummyImageBuildStore(), repoStore, nil, nil, nil, nil, nil, log.InitLogs())

	imageBuild := newValidImageBuild("test-build")
	_, status := svc.Create(ctx, orgId, imageBuild)

	require.Equal(int32(http.StatusBadRequest), statusCode(status))
	require.Contains(status.Message, "spec.destination.repository: Repository \"output-registry\" must be of type 'oci'")
}

func TestCreateImageBuildDestinationRepositoryNotReadWrite(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	orgId := uuid.New()

	// Set up repositories - destination is Read-only
	repoStore := NewDummyRepositoryStore()
	sourceRepo := newOciRepository("input-registry", v1beta1.Read)
	_, _ = repoStore.Create(ctx, orgId, sourceRepo, nil)

	destRepo := newOciRepository("output-registry", v1beta1.Read)
	_, _ = repoStore.Create(ctx, orgId, destRepo, nil)
	svc := NewImageBuildService(NewDummyImageBuildStore(), repoStore, nil, nil, nil, nil, nil, log.InitLogs())

	imageBuild := newValidImageBuild("test-build")
	_, status := svc.Create(ctx, orgId, imageBuild)

	require.Equal(int32(http.StatusBadRequest), statusCode(status))
	require.Contains(status.Message, "spec.destination.repository: Repository \"output-registry\" must have 'ReadWrite' access mode")
}

func TestCreateImageBuildWithUserConfiguration(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	orgId := uuid.New()

	// Set up repositories
	repoStore := NewDummyRepositoryStore()
	setupRepositoriesForImageBuild(repoStore, ctx, orgId)
	svc := NewImageBuildService(NewDummyImageBuildStore(), repoStore, nil, nil, nil, nil, nil, log.InitLogs())

	imageBuild := newValidImageBuild("test-build")
	imageBuild.Spec.UserConfiguration = &api.ImageBuildUserConfiguration{
		Username:  "testuser",
		Publickey: "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABgQC7vbqajDhA/2dZ0jofdR7H3nKJvN2k3J8K9L0M1N2O3P4Q5R6S7T8U9V0W1X2Y3Z4A5B6C7D8E9F0G1H2I3J4K5L6M7N8O9P0Q1R2S3T4U5V6W7X8Y9Z0A1B2C3D4E5F6G7H8I9J0K1L2M3N4O5P6Q7R8S9T0U1V2W3X4Y5Z6A7B8C9D0E1F2G3H4I5J6K7L8M9N0O1P2Q3R4S5T6U7V8W9X0Y1Z2A3B4C5D6E7E8F9G0H1I2J3K4L5M6N7O8P9Q0R1S2T3U4V5W6X7Y8Z9A0B1C2D3E4F5G6H7I8J9K0L1M2N3O4P5Q6R7S8T9U0V1W2X3Y4Z5A6B7C8D9E0F1G2H3I4J5K6L7M8N9O0P1Q2R3S4T5U6V7W8X9Y0Z1A2B3C4D5E6F7G8H9I0J1K2L3M4N5O6P7Q8R9S0T1U2V3W4X5Y6Z7A8B9C0D1E2F3G4H5I6J7K8L9M0N1O2P3Q4R5S6T7U8V9W0X1Y2Z3A4B5C6D7E8F9G0H1I2J3K4L5M6N7O8P9Q0R1S2T3U4V5W6X7Y8Z9A0B1C2D3E4F5G6H7I8J9K0L1M2N3O4P5Q6R7S8T9U9 test@example.com",
	}

	result, status := svc.Create(ctx, orgId, imageBuild)

	require.Equal(int32(http.StatusCreated), statusCode(status))
	require.NotNil(result)
	require.NotNil(result.Spec.UserConfiguration)
	require.Equal("testuser", result.Spec.UserConfiguration.Username)
	require.Contains(result.Spec.UserConfiguration.Publickey, "ssh-rsa")
}

func TestCreateImageBuildWithUserConfigurationMissingUsername(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	orgId := uuid.New()

	// Set up repositories
	repoStore := NewDummyRepositoryStore()
	setupRepositoriesForImageBuild(repoStore, ctx, orgId)
	svc := NewImageBuildService(NewDummyImageBuildStore(), repoStore, nil, nil, nil, nil, nil, log.InitLogs())

	imageBuild := newValidImageBuild("test-build")
	imageBuild.Spec.UserConfiguration = &api.ImageBuildUserConfiguration{
		Username:  "",
		Publickey: "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABgQC7vbqajDhA/2dZ0jofdR7H3nKJvN2k3J8K9L0M1N2O3P4Q5R6S7T8U9V0W1X2Y3Z4A5B6C7D8E9F0G1H2I3J4K5L6M7N8O9P0Q1R2S3T4U5V6W7X8Y9Z0A1B2C3D4E5F6G7H8I9J0K1L2M3N4O5P6Q7R8S9T0U1V2W3X4Y5Z6A7B8C9D0E1F2G3H4I5J6K7L8M9N0O1P2Q3R4S5T6U7V8W9X0Y1Z2A3B4C5D6E7E8F9G0H1I2J3K4L5M6N7O8P9Q0R1S2T3U4V5W6X7Y8Z9A0B1C2D3E4F5G6H7I8J9K0L1M2N3O4P5Q6R7S8T9U0V1W2X3Y4Z5A6B7C8D9E0F1G2H3I4J5K6L7M8N9O0P1Q2R3S4T5U6V7W8X9Y0Z1A2B3C4D5E6F7G8H9I0J1K2L3M4N5O6P7Q8R9S0T1U2V3W4X5Y6Z7A8B9C0D1E2F3G4H5I6J7K8L9M0N1O2P3Q4R5S6T7U8V9W0X1Y2Z3A4B5C6D7E8F9G0H1I2J3K4L5M6N7O8P9Q0R1S2T3U4V5W6X7Y8Z9A0B1C2D3E4F5G6H7I8J9K0L1M2N3O4P5Q6R7S8T9U9 test@example.com",
	}

	_, status := svc.Create(ctx, orgId, imageBuild)

	require.Equal(int32(http.StatusBadRequest), statusCode(status))
	require.Contains(status.Message, "spec.userConfiguration.username")
}

func TestCreateImageBuildWithUserConfigurationMissingPublickey(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	orgId := uuid.New()

	// Set up repositories
	repoStore := NewDummyRepositoryStore()
	setupRepositoriesForImageBuild(repoStore, ctx, orgId)
	svc := NewImageBuildService(NewDummyImageBuildStore(), repoStore, nil, nil, nil, nil, nil, log.InitLogs())

	imageBuild := newValidImageBuild("test-build")
	imageBuild.Spec.UserConfiguration = &api.ImageBuildUserConfiguration{
		Username:  "testuser",
		Publickey: "",
	}

	_, status := svc.Create(ctx, orgId, imageBuild)

	require.Equal(int32(http.StatusBadRequest), statusCode(status))
	require.Contains(status.Message, "spec.userConfiguration.publickey")
}

func TestCreateImageBuildWithUserConfigurationInvalidUsername(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	orgId := uuid.New()

	// Set up repositories
	repoStore := NewDummyRepositoryStore()
	setupRepositoriesForImageBuild(repoStore, ctx, orgId)
	svc := NewImageBuildService(NewDummyImageBuildStore(), repoStore, nil, nil, nil, nil, nil, log.InitLogs())

	imageBuild := newValidImageBuild("test-build")
	imageBuild.Spec.UserConfiguration = &api.ImageBuildUserConfiguration{
		Username:  "test;user",
		Publickey: "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABgQC7vbqajDhA/2dZ0jofdR7H3nKJvN2k3J8K9L0M1N2O3P4Q5R6S7T8U9V0W1X2Y3Z4A5B6C7D8E9F0G1H2I3J4K5L6M7N8O9P0Q1R2S3T4U5V6W7X8Y9Z0A1B2C3D4E5F6G7H8I9J0K1L2M3N4O5P6Q7R8S9T0U1V2W3X4Y5Z6A7B8C9D0E1F2G3H4I5J6K7L8M9N0O1P2Q3R4S5T6U7V8W9X0Y1Z2A3B4C5D6E7E8F9G0H1I2J3K4L5M6N7O8P9Q0R1S2T3U4V5W6X7Y8Z9A0B1C2D3E4F5G6H7I8J9K0L1M2N3O4P5Q6R7S8T9U0V1W2X3Y4Z5A6B7C8D9E0F1G2H3I4J5K6L7M8N9O0P1Q2R3S4T5U6V7W8X9Y0Z1A2B3C4D5E6F7G8H9I0J1K2L3M4N5O6P7Q8R9S0T1U2V3W4X5Y6Z7A8B9C0D1E2F3G4H5I6J7K8L9M0N1O2P3Q4R5S6T7U8V9W0X1Y2Z3A4B5C6D7E8F9G0H1I2J3K4L5M6N7O8P9Q0R1S2T3U4V5W6X7Y8Z9A0B1C2D3E4F5G6H7I8J9K0L1M2N3O4P5Q6R7S8T9U9 test@example.com",
	}

	_, status := svc.Create(ctx, orgId, imageBuild)

	require.Equal(int32(http.StatusBadRequest), statusCode(status))
	require.Contains(status.Message, "spec.userConfiguration.username")
	require.Contains(status.Message, "invalid characters")
}

func TestCreateImageBuildWithUserConfigurationInvalidPublickey(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	orgId := uuid.New()

	// Set up repositories
	repoStore := NewDummyRepositoryStore()
	setupRepositoriesForImageBuild(repoStore, ctx, orgId)
	svc := NewImageBuildService(NewDummyImageBuildStore(), repoStore, nil, nil, nil, nil, nil, log.InitLogs())

	imageBuild := newValidImageBuild("test-build")
	imageBuild.Spec.UserConfiguration = &api.ImageBuildUserConfiguration{
		Username:  "testuser",
		Publickey: "invalid-key-format",
	}

	_, status := svc.Create(ctx, orgId, imageBuild)

	require.Equal(int32(http.StatusBadRequest), statusCode(status))
	require.Contains(status.Message, "spec.userConfiguration.publickey")
}

func TestCancelImageBuild_Pending(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	orgId := uuid.New()

	// Set up repositories
	repoStore := NewDummyRepositoryStore()
	setupRepositoriesForImageBuild(repoStore, ctx, orgId)
	kvStore := NewDummyKVStore()
	svc := NewImageBuildService(NewDummyImageBuildStore(), repoStore, nil, nil, nil, kvStore, nil, log.InitLogs())

	// Create an ImageBuild
	imageBuild := newValidImageBuild("cancel-test")
	_, status := svc.Create(ctx, orgId, imageBuild)
	require.Equal(int32(http.StatusCreated), statusCode(status))

	// Set status to Pending
	imageBuild.Status = &api.ImageBuildStatus{
		Conditions: &[]api.ImageBuildCondition{
			{
				Type:               api.ImageBuildConditionTypeReady,
				Status:             v1beta1.ConditionStatusFalse,
				Reason:             string(api.ImageBuildConditionReasonPending),
				Message:            "Build is pending",
				LastTransitionTime: time.Now(),
			},
		},
	}
	_, err := svc.UpdateStatus(ctx, orgId, &imageBuild)
	require.NoError(err)

	// Cancel the build
	result, err := svc.Cancel(ctx, orgId, "cancel-test")
	require.NoError(err)
	require.NotNil(result)
	require.NotNil(result.Status)
	require.NotNil(result.Status.Conditions)

	// Verify status is Canceled (Pending resources go directly to Canceled, no active processing to stop)
	readyCondition := api.FindImageBuildStatusCondition(*result.Status.Conditions, api.ImageBuildConditionTypeReady)
	require.NotNil(readyCondition)
	require.Equal(string(api.ImageBuildConditionReasonCanceled), readyCondition.Reason)
}

func TestCancelImageBuild_Building(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	orgId := uuid.New()

	// Set up repositories
	repoStore := NewDummyRepositoryStore()
	setupRepositoriesForImageBuild(repoStore, ctx, orgId)
	kvStore := NewDummyKVStore()
	svc := NewImageBuildService(NewDummyImageBuildStore(), repoStore, nil, nil, nil, kvStore, nil, log.InitLogs())

	// Create an ImageBuild
	imageBuild := newValidImageBuild("cancel-building-test")
	_, status := svc.Create(ctx, orgId, imageBuild)
	require.Equal(int32(http.StatusCreated), statusCode(status))

	// Set status to Building
	imageBuild.Status = &api.ImageBuildStatus{
		Conditions: &[]api.ImageBuildCondition{
			{
				Type:               api.ImageBuildConditionTypeReady,
				Status:             v1beta1.ConditionStatusFalse,
				Reason:             string(api.ImageBuildConditionReasonBuilding),
				Message:            "Build in progress",
				LastTransitionTime: time.Now(),
			},
		},
	}
	_, err := svc.UpdateStatus(ctx, orgId, &imageBuild)
	require.NoError(err)

	// Cancel the build
	result, err := svc.Cancel(ctx, orgId, "cancel-building-test")
	require.NoError(err)
	require.NotNil(result)

	// Verify status is Canceling
	readyCondition := api.FindImageBuildStatusCondition(*result.Status.Conditions, api.ImageBuildConditionTypeReady)
	require.NotNil(readyCondition)
	require.Equal(string(api.ImageBuildConditionReasonCanceling), readyCondition.Reason)
}

func TestCancelImageBuild_Pushing(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	orgId := uuid.New()

	// Set up repositories
	repoStore := NewDummyRepositoryStore()
	setupRepositoriesForImageBuild(repoStore, ctx, orgId)
	kvStore := NewDummyKVStore()
	svc := NewImageBuildService(NewDummyImageBuildStore(), repoStore, nil, nil, nil, kvStore, nil, log.InitLogs())

	// Create an ImageBuild
	imageBuild := newValidImageBuild("cancel-pushing-test")
	_, status := svc.Create(ctx, orgId, imageBuild)
	require.Equal(int32(http.StatusCreated), statusCode(status))

	// Set status to Pushing
	imageBuild.Status = &api.ImageBuildStatus{
		Conditions: &[]api.ImageBuildCondition{
			{
				Type:               api.ImageBuildConditionTypeReady,
				Status:             v1beta1.ConditionStatusFalse,
				Reason:             string(api.ImageBuildConditionReasonPushing),
				Message:            "Pushing image",
				LastTransitionTime: time.Now(),
			},
		},
	}
	_, err := svc.UpdateStatus(ctx, orgId, &imageBuild)
	require.NoError(err)

	// Cancel the build
	result, err := svc.Cancel(ctx, orgId, "cancel-pushing-test")
	require.NoError(err)
	require.NotNil(result)

	// Verify status is Canceling
	readyCondition := api.FindImageBuildStatusCondition(*result.Status.Conditions, api.ImageBuildConditionTypeReady)
	require.NotNil(readyCondition)
	require.Equal(string(api.ImageBuildConditionReasonCanceling), readyCondition.Reason)
}

func TestCancelImageBuild_NotCancelable_Completed(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	orgId := uuid.New()

	// Set up repositories
	repoStore := NewDummyRepositoryStore()
	setupRepositoriesForImageBuild(repoStore, ctx, orgId)
	kvStore := NewDummyKVStore()
	svc := NewImageBuildService(NewDummyImageBuildStore(), repoStore, nil, nil, nil, kvStore, nil, log.InitLogs())

	// Create an ImageBuild
	imageBuild := newValidImageBuild("cancel-completed-test")
	_, status := svc.Create(ctx, orgId, imageBuild)
	require.Equal(int32(http.StatusCreated), statusCode(status))

	// Set status to Completed
	imageBuild.Status = &api.ImageBuildStatus{
		Conditions: &[]api.ImageBuildCondition{
			{
				Type:               api.ImageBuildConditionTypeReady,
				Status:             v1beta1.ConditionStatusTrue,
				Reason:             string(api.ImageBuildConditionReasonCompleted),
				Message:            "Build completed",
				LastTransitionTime: time.Now(),
			},
		},
	}
	_, err := svc.UpdateStatus(ctx, orgId, &imageBuild)
	require.NoError(err)

	// Attempt to cancel - should fail
	_, err = svc.Cancel(ctx, orgId, "cancel-completed-test")
	require.ErrorIs(err, ErrNotCancelable)
}

func TestCancelImageBuild_NotCancelable_Failed(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	orgId := uuid.New()

	// Set up repositories
	repoStore := NewDummyRepositoryStore()
	setupRepositoriesForImageBuild(repoStore, ctx, orgId)
	kvStore := NewDummyKVStore()
	svc := NewImageBuildService(NewDummyImageBuildStore(), repoStore, nil, nil, nil, kvStore, nil, log.InitLogs())

	// Create an ImageBuild
	imageBuild := newValidImageBuild("cancel-failed-test")
	_, status := svc.Create(ctx, orgId, imageBuild)
	require.Equal(int32(http.StatusCreated), statusCode(status))

	// Set status to Failed
	imageBuild.Status = &api.ImageBuildStatus{
		Conditions: &[]api.ImageBuildCondition{
			{
				Type:               api.ImageBuildConditionTypeReady,
				Status:             v1beta1.ConditionStatusFalse,
				Reason:             string(api.ImageBuildConditionReasonFailed),
				Message:            "Build failed",
				LastTransitionTime: time.Now(),
			},
		},
	}
	_, err := svc.UpdateStatus(ctx, orgId, &imageBuild)
	require.NoError(err)

	// Attempt to cancel - should fail
	_, err = svc.Cancel(ctx, orgId, "cancel-failed-test")
	require.ErrorIs(err, ErrNotCancelable)
}

func TestCancelImageBuild_NotCancelable_Canceled(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	orgId := uuid.New()

	// Set up repositories
	repoStore := NewDummyRepositoryStore()
	setupRepositoriesForImageBuild(repoStore, ctx, orgId)
	kvStore := NewDummyKVStore()
	svc := NewImageBuildService(NewDummyImageBuildStore(), repoStore, nil, nil, nil, kvStore, nil, log.InitLogs())

	// Create an ImageBuild
	imageBuild := newValidImageBuild("cancel-canceled-test")
	_, status := svc.Create(ctx, orgId, imageBuild)
	require.Equal(int32(http.StatusCreated), statusCode(status))

	// Set status to Canceled
	imageBuild.Status = &api.ImageBuildStatus{
		Conditions: &[]api.ImageBuildCondition{
			{
				Type:               api.ImageBuildConditionTypeReady,
				Status:             v1beta1.ConditionStatusFalse,
				Reason:             string(api.ImageBuildConditionReasonCanceled),
				Message:            "Build was canceled",
				LastTransitionTime: time.Now(),
			},
		},
	}
	_, err := svc.UpdateStatus(ctx, orgId, &imageBuild)
	require.NoError(err)

	// Attempt to cancel - should fail
	_, err = svc.Cancel(ctx, orgId, "cancel-canceled-test")
	require.ErrorIs(err, ErrNotCancelable)
}

func TestCancelImageBuild_NotFound(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	orgId := uuid.New()

	// Set up repositories
	repoStore := NewDummyRepositoryStore()
	kvStore := NewDummyKVStore()
	svc := NewImageBuildService(NewDummyImageBuildStore(), repoStore, nil, nil, nil, kvStore, nil, log.InitLogs())

	// Attempt to cancel non-existent build
	_, err := svc.Cancel(ctx, orgId, "nonexistent")
	require.Error(err)
}

func TestCancelImageBuild_NoStatus(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	orgId := uuid.New()

	// Set up repositories
	repoStore := NewDummyRepositoryStore()
	setupRepositoriesForImageBuild(repoStore, ctx, orgId)
	kvStore := NewDummyKVStore()
	svc := NewImageBuildService(NewDummyImageBuildStore(), repoStore, nil, nil, nil, kvStore, nil, log.InitLogs())

	// Create an ImageBuild with no status (treated as Pending)
	imageBuild := newValidImageBuild("cancel-nostatus-test")
	_, status := svc.Create(ctx, orgId, imageBuild)
	require.Equal(int32(http.StatusCreated), statusCode(status))

	// Cancel the build (should work since no status = Pending)
	result, err := svc.Cancel(ctx, orgId, "cancel-nostatus-test")
	require.NoError(err)
	require.NotNil(result)

	// Verify status is Canceled (no status = Pending, which goes directly to Canceled)
	readyCondition := api.FindImageBuildStatusCondition(*result.Status.Conditions, api.ImageBuildConditionTypeReady)
	require.NotNil(readyCondition)
	require.Equal(string(api.ImageBuildConditionReasonCanceled), readyCondition.Reason)
}
