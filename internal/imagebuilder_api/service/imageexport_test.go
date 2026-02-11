package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	api "github.com/flightctl/flightctl/api/imagebuilder/v1alpha1"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/google/uuid"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
)

func newTestImageExportService() (ImageExportService, *DummyImageExportStore, *DummyImageBuildStore) {
	imageExportStore := NewDummyImageExportStore()
	imageBuildStore := NewDummyImageBuildStore()
	repositoryStore := NewDummyRepositoryStore()
	svc := NewImageExportService(imageExportStore, imageBuildStore, repositoryStore, nil, nil, nil, config.NewDefaultImageBuilderServiceConfig(), log.InitLogs())
	return svc, imageExportStore, imageBuildStore
}

func newValidImageExport(name string) api.ImageExport {
	source := api.ImageExportSource{}
	_ = source.FromImageBuildRefSource(api.ImageBuildRefSource{
		Type:          api.ImageBuildRefSourceTypeImageBuild,
		ImageBuildRef: "test-image-build",
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

func newImageExportWithImageBuildSource(name, imageBuildRef string) api.ImageExport {
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
			Format: api.ExportFormatTypeVMDK,
		},
	}
}

func setupRepositoriesForImageExport(repoStore *DummyRepositoryStore, ctx context.Context, orgId uuid.UUID, includeSource bool) {
	if includeSource {
		// Create source repository (Read is fine for source)
		sourceRepo := newOciRepository("source-registry", v1beta1.Read)
		_, _ = repoStore.Create(ctx, orgId, sourceRepo, nil)
	}

	// Create destination repository (must be ReadWrite)
	destRepo := newOciRepository("output-registry", v1beta1.ReadWrite)
	_, _ = repoStore.Create(ctx, orgId, destRepo, nil)
}

// setupImageBuildForExport creates the ImageBuild that newValidImageExport references
func setupImageBuildForExport(imageBuildStore *DummyImageBuildStore, ctx context.Context, orgId uuid.UUID) {
	imageBuild := newValidImageBuild("test-image-build")
	_, _ = imageBuildStore.Create(ctx, orgId, &imageBuild)
}

func TestCreateImageExport(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	orgId := uuid.New()

	// Set up repositories
	repoStore := NewDummyRepositoryStore()
	setupRepositoriesForImageBuild(repoStore, ctx, orgId)
	imageBuildStore := NewDummyImageBuildStore()

	// Create the ImageBuild that will be referenced
	imageBuild := newValidImageBuild("test-image-build")
	_, err := imageBuildStore.Create(ctx, orgId, &imageBuild)
	require.NoError(err)

	svc := NewImageExportService(NewDummyImageExportStore(), imageBuildStore, repoStore, nil, nil, nil, config.NewDefaultImageBuilderServiceConfig(), log.InitLogs())

	imageExport := newValidImageExport("test-export")
	result, status := svc.Create(ctx, orgId, imageExport)

	require.Equal(int32(http.StatusCreated), statusCode(status))
	require.NotNil(result)
	require.Equal("test-export", lo.FromPtr(result.Metadata.Name))
}

func TestCreateImageExportDuplicate(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	orgId := uuid.New()

	// Set up repositories
	repoStore := NewDummyRepositoryStore()
	setupRepositoriesForImageBuild(repoStore, ctx, orgId)
	imageBuildStore := NewDummyImageBuildStore()

	// Create the ImageBuild that will be referenced
	imageBuild := newValidImageBuild("test-image-build")
	_, err := imageBuildStore.Create(ctx, orgId, &imageBuild)
	require.NoError(err)

	svc := NewImageExportService(NewDummyImageExportStore(), imageBuildStore, repoStore, nil, nil, nil, config.NewDefaultImageBuilderServiceConfig(), log.InitLogs())

	imageExport := newValidImageExport("duplicate-test")

	// First create should succeed
	_, status := svc.Create(ctx, orgId, imageExport)
	require.Equal(int32(http.StatusCreated), statusCode(status))

	// Second create should fail with conflict
	_, status = svc.Create(ctx, orgId, imageExport)
	require.Equal(int32(http.StatusConflict), statusCode(status))
}

// TestCreateImageExportMissingDestinationRepository removed - ImageExport no longer has destination field

func TestCreateImageExportMissingFormats(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	orgId := uuid.New()

	// Set up repositories
	repoStore := NewDummyRepositoryStore()
	setupRepositoriesForImageBuild(repoStore, ctx, orgId)
	imageBuildStore := NewDummyImageBuildStore()

	// Create the ImageBuild that will be referenced
	imageBuild := newValidImageBuild("test-image-build")
	_, err := imageBuildStore.Create(ctx, orgId, &imageBuild)
	require.NoError(err)

	svc := NewImageExportService(NewDummyImageExportStore(), imageBuildStore, repoStore, nil, nil, nil, config.NewDefaultImageBuilderServiceConfig(), log.InitLogs())

	imageExport := newValidImageExport("test")
	imageExport.Spec.Format = ""

	_, status := svc.Create(ctx, orgId, imageExport)
	require.Equal(int32(http.StatusBadRequest), statusCode(status))
}

func TestCreateImageExportWithImageBuildRef(t *testing.T) {
	require := require.New(t)
	_, _, imageBuildStore := newTestImageExportService()
	ctx := context.Background()
	orgId := uuid.New()

	// Set up repositories (destination only, source comes from ImageBuild)
	repoStore := NewDummyRepositoryStore()
	setupRepositoriesForImageExport(repoStore, ctx, orgId, false)
	svc := NewImageExportService(NewDummyImageExportStore(), imageBuildStore, repoStore, nil, nil, nil, config.NewDefaultImageBuilderServiceConfig(), log.InitLogs())

	// First create the ImageBuild that will be referenced
	imageBuild := newValidImageBuild("my-build")
	_, err := imageBuildStore.Create(ctx, orgId, &imageBuild)
	require.NoError(err)

	// Now create the ImageExport referencing it
	imageExport := newImageExportWithImageBuildSource("test-export", "my-build")
	result, status := svc.Create(ctx, orgId, imageExport)

	require.Equal(int32(http.StatusCreated), statusCode(status))
	require.NotNil(result)
}

func TestCreateImageExportWithNonexistentImageBuildRef(t *testing.T) {
	require := require.New(t)
	svc, _, _ := newTestImageExportService()
	ctx := context.Background()
	orgId := uuid.New()

	// Create ImageExport referencing non-existent ImageBuild
	imageExport := newImageExportWithImageBuildSource("test-export", "nonexistent-build")
	_, status := svc.Create(ctx, orgId, imageExport)

	require.Equal(int32(http.StatusBadRequest), statusCode(status))
	require.Contains(status.Message, "not found")
}

func TestGetImageExport(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	orgId := uuid.New()

	// Set up repositories
	repoStore := NewDummyRepositoryStore()
	setupRepositoriesForImageBuild(repoStore, ctx, orgId)
	imageBuildStore := NewDummyImageBuildStore()

	// Create the ImageBuild that will be referenced
	imageBuild := newValidImageBuild("test-image-build")
	_, err := imageBuildStore.Create(ctx, orgId, &imageBuild)
	require.NoError(err)

	svc := NewImageExportService(NewDummyImageExportStore(), imageBuildStore, repoStore, nil, nil, nil, config.NewDefaultImageBuilderServiceConfig(), log.InitLogs())

	// Create first
	imageExport := newValidImageExport("get-test")
	_, status := svc.Create(ctx, orgId, imageExport)
	require.Equal(int32(http.StatusCreated), statusCode(status))

	// Get it back
	result, status := svc.Get(ctx, orgId, "get-test")
	require.Equal(int32(http.StatusOK), statusCode(status))
	require.NotNil(result)
	require.Equal("get-test", lo.FromPtr(result.Metadata.Name))
}

func TestGetImageExportNotFound(t *testing.T) {
	require := require.New(t)
	svc, _, _ := newTestImageExportService()
	ctx := context.Background()
	orgId := uuid.New()

	_, status := svc.Get(ctx, orgId, "nonexistent")
	require.Equal(int32(http.StatusNotFound), statusCode(status))
}

func TestListImageExports(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	orgId := uuid.New()

	// Set up repositories
	repoStore := NewDummyRepositoryStore()
	setupRepositoriesForImageBuild(repoStore, ctx, orgId)
	imageBuildStore := NewDummyImageBuildStore()

	// Create the ImageBuild that will be referenced
	imageBuild := newValidImageBuild("test-image-build")
	_, err := imageBuildStore.Create(ctx, orgId, &imageBuild)
	require.NoError(err)

	svc := NewImageExportService(NewDummyImageExportStore(), imageBuildStore, repoStore, nil, nil, nil, config.NewDefaultImageBuilderServiceConfig(), log.InitLogs())

	// Create multiple
	for i := 0; i < 3; i++ {
		imageExport := newValidImageExport(string(rune('a'+i)) + "-export")
		_, status := svc.Create(ctx, orgId, imageExport)
		require.Equal(int32(http.StatusCreated), statusCode(status))
	}

	// List all
	result, status := svc.List(ctx, orgId, api.ListImageExportsParams{})
	require.Equal(int32(http.StatusOK), statusCode(status))
	require.NotNil(result)
	require.Len(result.Items, 3)
}

func TestListImageExportsWithLimit(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	orgId := uuid.New()

	// Set up repositories
	repoStore := NewDummyRepositoryStore()
	setupRepositoriesForImageBuild(repoStore, ctx, orgId)
	imageBuildStore := NewDummyImageBuildStore()

	// Create the ImageBuild that will be referenced
	imageBuild := newValidImageBuild("test-image-build")
	_, err := imageBuildStore.Create(ctx, orgId, &imageBuild)
	require.NoError(err)

	svc := NewImageExportService(NewDummyImageExportStore(), imageBuildStore, repoStore, nil, nil, nil, config.NewDefaultImageBuilderServiceConfig(), log.InitLogs())

	// Create multiple
	for i := 0; i < 5; i++ {
		imageExport := newValidImageExport(string(rune('a'+i)) + "-export")
		_, status := svc.Create(ctx, orgId, imageExport)
		require.Equal(int32(http.StatusCreated), statusCode(status))
	}

	// List with limit
	limit := int32(2)
	result, status := svc.List(ctx, orgId, api.ListImageExportsParams{Limit: &limit})
	require.Equal(int32(http.StatusOK), statusCode(status))
	require.NotNil(result)
	require.Len(result.Items, 2)
}

// Helper to create a short timeout config for delete tests
func newShortTimeoutConfig() *config.ImageBuilderServiceConfig {
	cfg := config.NewDefaultImageBuilderServiceConfig()
	cfg.DeleteCancelTimeout = util.Duration(100 * time.Millisecond) // Very short timeout for testing
	return cfg
}

// Helper to set up service with KVStore and short timeout for delete tests
func setupDeleteTestService(ctx context.Context, orgId uuid.UUID, kvStore *DummyKVStore) (ImageExportService, *DummyImageExportStore, *DummyImageBuildStore) {
	repoStore := NewDummyRepositoryStore()
	setupRepositoriesForImageBuild(repoStore, ctx, orgId)
	imageBuildStore := NewDummyImageBuildStore()
	imageExportStore := NewDummyImageExportStore()

	// Create the ImageBuild that will be referenced
	imageBuild := newValidImageBuild("test-image-build")
	_, _ = imageBuildStore.Create(ctx, orgId, &imageBuild)

	svc := NewImageExportService(imageExportStore, imageBuildStore, repoStore, nil, nil, kvStore, newShortTimeoutConfig(), log.InitLogs())
	return svc, imageExportStore, imageBuildStore
}

// Helper to create an ImageExport with a specific status condition
func createImageExportWithStatus(ctx context.Context, svc ImageExportService, imageExportStore *DummyImageExportStore, orgId uuid.UUID, name string, reason api.ImageExportConditionReason) *api.ImageExport {
	imageExport := newValidImageExport(name)
	created, _ := svc.Create(ctx, orgId, imageExport)

	if created == nil {
		return nil
	}

	// Initialize status if needed
	if created.Status == nil {
		created.Status = &api.ImageExportStatus{}
	}
	if created.Status.Conditions == nil {
		created.Status.Conditions = &[]api.ImageExportCondition{}
	}

	// Set the appropriate status based on reason
	conditionStatus := v1beta1.ConditionStatusFalse
	if reason == api.ImageExportConditionReasonCompleted {
		conditionStatus = v1beta1.ConditionStatusTrue
	}

	api.SetImageExportStatusCondition(created.Status.Conditions, api.ImageExportCondition{
		Type:               api.ImageExportConditionTypeReady,
		Status:             conditionStatus,
		Reason:             string(reason),
		Message:            "Test status",
		LastTransitionTime: time.Now().UTC(),
	})
	updated, _ := svc.UpdateStatus(ctx, orgId, created)
	return updated
}

func TestDeleteImageExport_Pending_CancelSuccess(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	orgId := uuid.New()

	kvStore := NewDummyKVStore()
	svc, _, _ := setupDeleteTestService(ctx, orgId, kvStore)

	// Create ImageExport (starts in Pending state)
	imageExport := newValidImageExport("delete-pending-success")
	_, status := svc.Create(ctx, orgId, imageExport)
	require.Equal(int32(http.StatusCreated), statusCode(status))

	// Simulate that the worker will send the canceled signal
	canceledStreamKey := fmt.Sprintf("imageexport:canceled:%s:%s", orgId.String(), "delete-pending-success")
	kvStore.SimulateCanceledSignal(canceledStreamKey)

	// Delete it - should cancel first and wait for signal
	deleted, status := svc.Delete(ctx, orgId, "delete-pending-success")
	require.Equal(int32(http.StatusOK), statusCode(status))
	require.NotNil(deleted)
	require.Equal("delete-pending-success", lo.FromPtr(deleted.Metadata.Name))

	// Verify it's gone
	_, status = svc.Get(ctx, orgId, "delete-pending-success")
	require.Equal(int32(http.StatusNotFound), statusCode(status))
}

func TestDeleteImageExport_Converting_CancelSuccess(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	orgId := uuid.New()

	kvStore := NewDummyKVStore()
	svc, imageExportStore, _ := setupDeleteTestService(ctx, orgId, kvStore)

	// Create ImageExport and set to Converting state
	created := createImageExportWithStatus(ctx, svc, imageExportStore, orgId, "delete-converting-success", api.ImageExportConditionReasonConverting)
	require.NotNil(created)

	// Simulate that the worker will send the canceled signal
	canceledStreamKey := fmt.Sprintf("imageexport:canceled:%s:%s", orgId.String(), "delete-converting-success")
	kvStore.SimulateCanceledSignal(canceledStreamKey)

	// Delete it - should cancel first and wait for signal
	deleted, status := svc.Delete(ctx, orgId, "delete-converting-success")
	require.Equal(int32(http.StatusOK), statusCode(status))
	require.NotNil(deleted)

	// Verify it's gone
	_, status = svc.Get(ctx, orgId, "delete-converting-success")
	require.Equal(int32(http.StatusNotFound), statusCode(status))
}

func TestDeleteImageExport_Pushing_CancelSuccess(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	orgId := uuid.New()

	kvStore := NewDummyKVStore()
	svc, imageExportStore, _ := setupDeleteTestService(ctx, orgId, kvStore)

	// Create ImageExport and set to Pushing state
	created := createImageExportWithStatus(ctx, svc, imageExportStore, orgId, "delete-pushing-success", api.ImageExportConditionReasonPushing)
	require.NotNil(created)

	// Simulate that the worker will send the canceled signal
	canceledStreamKey := fmt.Sprintf("imageexport:canceled:%s:%s", orgId.String(), "delete-pushing-success")
	kvStore.SimulateCanceledSignal(canceledStreamKey)

	// Delete it - should cancel first and wait for signal
	deleted, status := svc.Delete(ctx, orgId, "delete-pushing-success")
	require.Equal(int32(http.StatusOK), statusCode(status))
	require.NotNil(deleted)

	// Verify it's gone
	_, status = svc.Get(ctx, orgId, "delete-pushing-success")
	require.Equal(int32(http.StatusNotFound), statusCode(status))
}

func TestDeleteImageExport_CancelTimeout(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	orgId := uuid.New()

	kvStore := NewDummyKVStore()
	svc, _, _ := setupDeleteTestService(ctx, orgId, kvStore)

	// Create ImageExport (starts in Pending state - cancelable)
	imageExport := newValidImageExport("delete-timeout")
	_, status := svc.Create(ctx, orgId, imageExport)
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
	_, status = svc.Get(ctx, orgId, "delete-timeout")
	require.Equal(int32(http.StatusNotFound), statusCode(status))
}

func TestDeleteImageExport_Completed_NoCancelAttempt(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	orgId := uuid.New()

	kvStore := NewDummyKVStore()
	svc, imageExportStore, _ := setupDeleteTestService(ctx, orgId, kvStore)

	// Create ImageExport and set to Completed state (not cancelable)
	imageExport := newValidImageExport("delete-completed")
	created, _ := svc.Create(ctx, orgId, imageExport)
	require.NotNil(created)

	// Initialize status if needed and set to Completed state
	if created.Status == nil {
		created.Status = &api.ImageExportStatus{}
	}
	if created.Status.Conditions == nil {
		created.Status.Conditions = &[]api.ImageExportCondition{}
	}
	api.SetImageExportStatusCondition(created.Status.Conditions, api.ImageExportCondition{
		Type:               api.ImageExportConditionTypeReady,
		Status:             v1beta1.ConditionStatusTrue,
		Reason:             string(api.ImageExportConditionReasonCompleted),
		Message:            "Test completed",
		LastTransitionTime: time.Now().UTC(),
	})
	_, _ = svc.UpdateStatus(ctx, orgId, created)

	// Delete it - should NOT try to cancel (not cancelable)
	start := time.Now()
	deleted, status := svc.Delete(ctx, orgId, "delete-completed")
	elapsed := time.Since(start)

	require.Equal(int32(http.StatusOK), statusCode(status))
	require.NotNil(deleted)

	// Should be fast (no cancel wait)
	require.Less(elapsed.Milliseconds(), int64(50), "Should not wait for cancel on completed export")

	// Verify it's gone
	_, status = svc.Get(ctx, orgId, "delete-completed")
	require.Equal(int32(http.StatusNotFound), statusCode(status))
	_ = imageExportStore // suppress unused warning
}

func TestDeleteImageExport_Failed_NoCancelAttempt(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	orgId := uuid.New()

	kvStore := NewDummyKVStore()
	svc, imageExportStore, _ := setupDeleteTestService(ctx, orgId, kvStore)

	// Create ImageExport and set to Failed state (not cancelable)
	created := createImageExportWithStatus(ctx, svc, imageExportStore, orgId, "delete-failed", api.ImageExportConditionReasonFailed)
	require.NotNil(created)

	// Delete it - should NOT try to cancel (not cancelable)
	start := time.Now()
	deleted, status := svc.Delete(ctx, orgId, "delete-failed")
	elapsed := time.Since(start)

	require.Equal(int32(http.StatusOK), statusCode(status))
	require.NotNil(deleted)

	// Should be fast (no cancel wait)
	require.Less(elapsed.Milliseconds(), int64(50), "Should not wait for cancel on failed export")

	// Verify it's gone
	_, status = svc.Get(ctx, orgId, "delete-failed")
	require.Equal(int32(http.StatusNotFound), statusCode(status))
}

func TestDeleteImageExport_Canceled_NoCancelAttempt(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	orgId := uuid.New()

	kvStore := NewDummyKVStore()
	svc, imageExportStore, _ := setupDeleteTestService(ctx, orgId, kvStore)

	// Create ImageExport and set to Canceled state (not cancelable)
	created := createImageExportWithStatus(ctx, svc, imageExportStore, orgId, "delete-canceled", api.ImageExportConditionReasonCanceled)
	require.NotNil(created)

	// Delete it - should NOT try to cancel (already canceled)
	start := time.Now()
	deleted, status := svc.Delete(ctx, orgId, "delete-canceled")
	elapsed := time.Since(start)

	require.Equal(int32(http.StatusOK), statusCode(status))
	require.NotNil(deleted)

	// Should be fast (no cancel wait)
	require.Less(elapsed.Milliseconds(), int64(50), "Should not wait for cancel on already canceled export")

	// Verify it's gone
	_, status = svc.Get(ctx, orgId, "delete-canceled")
	require.Equal(int32(http.StatusNotFound), statusCode(status))
}

func TestDeleteImageExport_Canceling_NoCancelAttempt(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	orgId := uuid.New()

	kvStore := NewDummyKVStore()
	svc, imageExportStore, _ := setupDeleteTestService(ctx, orgId, kvStore)

	// Create ImageExport and set to Canceling state (not cancelable - already canceling)
	created := createImageExportWithStatus(ctx, svc, imageExportStore, orgId, "delete-canceling", api.ImageExportConditionReasonCanceling)
	require.NotNil(created)

	// Delete it - should NOT try to cancel (already canceling)
	start := time.Now()
	deleted, status := svc.Delete(ctx, orgId, "delete-canceling")
	elapsed := time.Since(start)

	require.Equal(int32(http.StatusOK), statusCode(status))
	require.NotNil(deleted)

	// Should be fast (no cancel wait)
	require.Less(elapsed.Milliseconds(), int64(50), "Should not wait for cancel on already canceling export")

	// Verify it's gone
	_, status = svc.Get(ctx, orgId, "delete-canceling")
	require.Equal(int32(http.StatusNotFound), statusCode(status))
}

func TestDeleteImageExportNotFound(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	orgId := uuid.New()

	kvStore := NewDummyKVStore()
	svc, _, _ := setupDeleteTestService(ctx, orgId, kvStore)

	// Delete is idempotent - deleting non-existent resource returns success
	deleted, status := svc.Delete(ctx, orgId, "nonexistent")
	require.Equal(int32(http.StatusOK), statusCode(status))
	require.Nil(deleted)
}

func TestUpdateImageExportStatus(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	orgId := uuid.New()

	// Set up repositories
	repoStore := NewDummyRepositoryStore()
	setupRepositoriesForImageBuild(repoStore, ctx, orgId)
	imageBuildStore := NewDummyImageBuildStore()

	// Create the ImageBuild that will be referenced
	imageBuild := newValidImageBuild("test-image-build")
	_, err := imageBuildStore.Create(ctx, orgId, &imageBuild)
	require.NoError(err)

	svc := NewImageExportService(NewDummyImageExportStore(), imageBuildStore, repoStore, nil, nil, nil, config.NewDefaultImageBuilderServiceConfig(), log.InitLogs())

	// Create first
	imageExport := newValidImageExport("status-test")
	_, status := svc.Create(ctx, orgId, imageExport)
	require.Equal(int32(http.StatusCreated), statusCode(status))

	// Update status with condition
	now := time.Now()
	imageExport.Status = &api.ImageExportStatus{
		Conditions: &[]api.ImageExportCondition{
			{
				Type:               api.ImageExportConditionTypeReady,
				Status:             v1beta1.ConditionStatusUnknown,
				Reason:             string(api.ImageExportConditionReasonConverting),
				Message:            "Converting in progress",
				LastTransitionTime: now,
			},
		},
	}
	result, err := svc.UpdateStatus(ctx, orgId, &imageExport)
	require.NoError(err)
	require.NotNil(result)
	require.NotNil(result.Status)
	require.NotNil(result.Status.Conditions)
	require.Len(*result.Status.Conditions, 1)
	require.Equal(api.ImageExportConditionTypeReady, (*result.Status.Conditions)[0].Type)
	require.Equal(string(api.ImageExportConditionReasonConverting), (*result.Status.Conditions)[0].Reason)
}

// Tests for old imageReference source type removed - only imageBuild source is supported now
// Tests for destination validation removed - destination now comes from ImageBuild

func newReadyImageExport(name string, manifestDigest string) api.ImageExport {
	imageExport := newValidImageExport(name)
	now := time.Now()
	imageExport.Status = &api.ImageExportStatus{
		ManifestDigest: lo.ToPtr(manifestDigest),
		Conditions: &[]api.ImageExportCondition{
			{
				Type:               api.ImageExportConditionTypeReady,
				Status:             v1beta1.ConditionStatusTrue,
				Reason:             string(api.ImageExportConditionReasonCompleted),
				Message:            "Export completed",
				LastTransitionTime: now,
			},
		},
	}
	return imageExport
}

func TestDownloadImageExportNotFound(t *testing.T) {
	require := require.New(t)
	svc, _, _ := newTestImageExportService()
	ctx := context.Background()
	orgId := uuid.New()

	_, err := svc.Download(ctx, orgId, "nonexistent")
	require.Error(err)
	require.True(errors.Is(err, flterrors.ErrResourceNotFound))
}

func TestDownloadImageExportNotReadyNoStatus(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	orgId := uuid.New()

	// Set up repositories
	repoStore := NewDummyRepositoryStore()
	setupRepositoriesForImageBuild(repoStore, ctx, orgId)
	imageBuildStore := NewDummyImageBuildStore()
	setupImageBuildForExport(imageBuildStore, ctx, orgId)
	imageExportStore := NewDummyImageExportStore()
	svc := NewImageExportService(imageExportStore, imageBuildStore, repoStore, nil, nil, nil, config.NewDefaultImageBuilderServiceConfig(), log.InitLogs())

	// Create ImageExport without status
	imageExport := newValidImageExport("test-export")
	_, err := imageExportStore.Create(ctx, orgId, &imageExport)
	require.NoError(err)

	_, err = svc.Download(ctx, orgId, "test-export")
	require.Error(err)
	require.True(errors.Is(err, ErrImageExportStatusNotReady))
}

func TestDownloadImageExportNotReadyNoConditions(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	orgId := uuid.New()

	// Set up repositories
	repoStore := NewDummyRepositoryStore()
	setupRepositoriesForImageBuild(repoStore, ctx, orgId)
	imageBuildStore := NewDummyImageBuildStore()
	setupImageBuildForExport(imageBuildStore, ctx, orgId)
	imageExportStore := NewDummyImageExportStore()
	svc := NewImageExportService(imageExportStore, imageBuildStore, repoStore, nil, nil, nil, config.NewDefaultImageBuilderServiceConfig(), log.InitLogs())

	// Create ImageExport with status but no conditions
	imageExport := newValidImageExport("test-export")
	imageExport.Status = &api.ImageExportStatus{
		ManifestDigest: lo.ToPtr("sha256:abc123"),
	}
	_, err := imageExportStore.Create(ctx, orgId, &imageExport)
	require.NoError(err)

	_, err = svc.Download(ctx, orgId, "test-export")
	require.Error(err)
	require.True(errors.Is(err, ErrImageExportStatusNotReady))
}

func TestDownloadImageExportNotReadyNoReadyCondition(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	orgId := uuid.New()

	// Set up repositories
	repoStore := NewDummyRepositoryStore()
	setupRepositoriesForImageBuild(repoStore, ctx, orgId)
	imageBuildStore := NewDummyImageBuildStore()
	setupImageBuildForExport(imageBuildStore, ctx, orgId)
	imageExportStore := NewDummyImageExportStore()
	svc := NewImageExportService(imageExportStore, imageBuildStore, repoStore, nil, nil, nil, config.NewDefaultImageBuilderServiceConfig(), log.InitLogs())

	// Create ImageExport with status but no Ready condition
	imageExport := newValidImageExport("test-export")
	now := time.Now()
	imageExport.Status = &api.ImageExportStatus{
		ManifestDigest: lo.ToPtr("sha256:abc123"),
		Conditions: &[]api.ImageExportCondition{
			{
				Type:               "Other",
				Status:             v1beta1.ConditionStatusTrue,
				LastTransitionTime: now,
			},
		},
	}
	_, err := imageExportStore.Create(ctx, orgId, &imageExport)
	require.NoError(err)

	_, err = svc.Download(ctx, orgId, "test-export")
	require.Error(err)
	require.True(errors.Is(err, ErrImageExportReadyConditionNotFound))
}

func TestDownloadImageExportNotReadyFalseStatus(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	orgId := uuid.New()

	// Set up repositories
	repoStore := NewDummyRepositoryStore()
	setupRepositoriesForImageBuild(repoStore, ctx, orgId)
	imageBuildStore := NewDummyImageBuildStore()
	setupImageBuildForExport(imageBuildStore, ctx, orgId)
	imageExportStore := NewDummyImageExportStore()
	svc := NewImageExportService(imageExportStore, imageBuildStore, repoStore, nil, nil, nil, config.NewDefaultImageBuilderServiceConfig(), log.InitLogs())

	// Create ImageExport with Ready condition but status is False
	imageExport := newValidImageExport("test-export")
	now := time.Now()
	imageExport.Status = &api.ImageExportStatus{
		ManifestDigest: lo.ToPtr("sha256:abc123"),
		Conditions: &[]api.ImageExportCondition{
			{
				Type:               api.ImageExportConditionTypeReady,
				Status:             v1beta1.ConditionStatusFalse,
				Reason:             string(api.ImageExportConditionReasonConverting),
				Message:            "Still converting",
				LastTransitionTime: now,
			},
		},
	}
	_, err := imageExportStore.Create(ctx, orgId, &imageExport)
	require.NoError(err)

	_, err = svc.Download(ctx, orgId, "test-export")
	require.Error(err)
	require.True(errors.Is(err, ErrImageExportNotReady))
	require.Contains(err.Error(), "status: False")
}

func TestDownloadImageExportMissingManifestDigest(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	orgId := uuid.New()

	// Set up repositories
	repoStore := NewDummyRepositoryStore()
	setupRepositoriesForImageBuild(repoStore, ctx, orgId)
	imageBuildStore := NewDummyImageBuildStore()
	setupImageBuildForExport(imageBuildStore, ctx, orgId)
	imageExportStore := NewDummyImageExportStore()
	svc := NewImageExportService(imageExportStore, imageBuildStore, repoStore, nil, nil, nil, config.NewDefaultImageBuilderServiceConfig(), log.InitLogs())

	// Create ImageExport with Ready condition but no manifest digest
	imageExport := newValidImageExport("test-export")
	now := time.Now()
	imageExport.Status = &api.ImageExportStatus{
		Conditions: &[]api.ImageExportCondition{
			{
				Type:               api.ImageExportConditionTypeReady,
				Status:             v1beta1.ConditionStatusTrue,
				Reason:             string(api.ImageExportConditionReasonCompleted),
				Message:            "Export completed",
				LastTransitionTime: now,
			},
		},
	}
	_, err := imageExportStore.Create(ctx, orgId, &imageExport)
	require.NoError(err)

	_, err = svc.Download(ctx, orgId, "test-export")
	require.Error(err)
	require.True(errors.Is(err, ErrImageExportManifestDigestNotSet))
}

func TestDownloadImageExportEmptyManifestDigest(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	orgId := uuid.New()

	// Set up repositories
	repoStore := NewDummyRepositoryStore()
	setupRepositoriesForImageBuild(repoStore, ctx, orgId)
	imageBuildStore := NewDummyImageBuildStore()
	setupImageBuildForExport(imageBuildStore, ctx, orgId)
	imageExportStore := NewDummyImageExportStore()
	svc := NewImageExportService(imageExportStore, imageBuildStore, repoStore, nil, nil, nil, config.NewDefaultImageBuilderServiceConfig(), log.InitLogs())

	// Create ImageExport with Ready condition but empty manifest digest
	imageExport := newValidImageExport("test-export")
	now := time.Now()
	imageExport.Status = &api.ImageExportStatus{
		ManifestDigest: lo.ToPtr(""),
		Conditions: &[]api.ImageExportCondition{
			{
				Type:               api.ImageExportConditionTypeReady,
				Status:             v1beta1.ConditionStatusTrue,
				Reason:             string(api.ImageExportConditionReasonCompleted),
				Message:            "Export completed",
				LastTransitionTime: now,
			},
		},
	}
	_, err := imageExportStore.Create(ctx, orgId, &imageExport)
	require.NoError(err)

	_, err = svc.Download(ctx, orgId, "test-export")
	require.Error(err)
	require.True(errors.Is(err, ErrImageExportManifestDigestNotSet))
}

func TestDownloadImageExportDestinationRepositoryNotFound(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	orgId := uuid.New()

	// Set up repositories - don't create destination repository
	repoStore := NewDummyRepositoryStore()
	sourceRepo := newOciRepository("input-registry", v1beta1.Read)
	_, _ = repoStore.Create(ctx, orgId, sourceRepo, nil)

	// Create ImageBuild with destination repository that doesn't exist
	imageBuildStore := NewDummyImageBuildStore()
	imageBuild := newValidImageBuild("test-image-build")
	imageBuild.Spec.Destination.Repository = "output-registry" // This repository doesn't exist
	_, err := imageBuildStore.Create(ctx, orgId, &imageBuild)
	require.NoError(err)

	imageExportStore := NewDummyImageExportStore()
	svc := NewImageExportService(imageExportStore, imageBuildStore, repoStore, nil, nil, nil, config.NewDefaultImageBuilderServiceConfig(), log.InitLogs())

	// Create ImageExport that references ImageBuild with non-existent destination repository
	imageExport := newReadyImageExport("test-export", "sha256:abc123")
	_, err = imageExportStore.Create(ctx, orgId, &imageExport)
	require.NoError(err)

	_, err = svc.Download(ctx, orgId, "test-export")
	require.Error(err)
	require.True(errors.Is(err, ErrRepositoryNotFound))
}

func TestDownloadImageExportInvalidManifestDigest(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	orgId := uuid.New()

	// Set up repositories
	repoStore := NewDummyRepositoryStore()
	setupRepositoriesForImageBuild(repoStore, ctx, orgId)
	imageBuildStore := NewDummyImageBuildStore()

	// Create the ImageBuild that will be referenced
	imageBuild := newValidImageBuild("test-image-build")
	_, err := imageBuildStore.Create(ctx, orgId, &imageBuild)
	require.NoError(err)

	imageExportStore := NewDummyImageExportStore()
	svc := NewImageExportService(imageExportStore, imageBuildStore, repoStore, nil, nil, nil, config.NewDefaultImageBuilderServiceConfig(), log.InitLogs())

	// Create ImageExport with invalid manifest digest
	imageExport := newReadyImageExport("test-export", "invalid-digest")
	_, err = imageExportStore.Create(ctx, orgId, &imageExport)
	require.NoError(err)

	_, err = svc.Download(ctx, orgId, "test-export")
	require.Error(err)
	require.True(errors.Is(err, ErrInvalidManifestDigest))
}

// newOciRepositoryWithRegistry creates a test OCI repository pointing to a specific registry hostname
func newOciRepositoryWithRegistry(name string, accessMode v1beta1.OciRepoSpecAccessMode, registryHostname string, scheme *v1beta1.OciRepoSpecScheme, skipVerification bool) *v1beta1.Repository {
	spec := v1beta1.RepositorySpec{}
	ociSpec := v1beta1.OciRepoSpec{
		Registry:   registryHostname,
		Type:       v1beta1.OciRepoSpecTypeOci,
		AccessMode: &accessMode,
	}
	if scheme != nil {
		ociSpec.Scheme = scheme
	}
	if skipVerification {
		ociSpec.SkipServerVerification = lo.ToPtr(true)
	}
	_ = spec.FromOciRepoSpec(ociSpec)
	return &v1beta1.Repository{
		ApiVersion: "flightctl.io/v1beta1",
		Kind:       string(v1beta1.ResourceKindRepository),
		Metadata: v1beta1.ObjectMeta{
			Name: lo.ToPtr(name),
		},
		Spec: spec,
	}
}

// TestDownloadImageExportWithRedirect tests successful download with redirect response.
// NOTE: This test requires proper TLS setup for the oras library to work with test servers.
// The oras library uses its own HTTP client which doesn't respect SkipServerVerification
// for the manifest fetch operations. This test is skipped until we can properly configure
// the oras library's HTTP client or use a real registry for integration testing.
func TestDownloadImageExportWithRedirect(t *testing.T) {
	t.Skip("Skipping integration test - requires proper TLS setup for oras library")
	require := require.New(t)
	ctx := context.Background()
	orgId := uuid.New()

	// Create a test HTTP server that mocks an OCI registry
	manifestDigest := "sha256:1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef"
	layerDigest := "sha256:abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890"
	presignedURL := "https://storage.example.com/presigned-blob-url"

	// Create manifest JSON
	manifest := ocispec.Manifest{
		MediaType: ocispec.MediaTypeImageManifest,
		Config: ocispec.Descriptor{
			MediaType: ocispec.MediaTypeImageConfig,
			Digest:    digest.Digest("sha256:config123"),
			Size:      100,
		},
		Layers: []ocispec.Descriptor{
			{
				MediaType: "application/vnd.oci.image.layer.v1.tar+gzip",
				Digest:    digest.Digest(layerDigest),
				Size:      1024,
			},
		},
	}
	manifestBytes, err := json.Marshal(manifest)
	require.NoError(err)

	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/":
			// Registry API version check
			w.WriteHeader(http.StatusOK)
		case "/v2/test-image/manifests/" + manifestDigest:
			// Manifest fetch
			w.Header().Set("Content-Type", ocispec.MediaTypeImageManifest)
			w.Header().Set("Content-Length", strconv.Itoa(len(manifestBytes)))
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(manifestBytes)
		case "/v2/test-image/blobs/" + layerDigest:
			// Blob fetch - return redirect
			w.Header().Set("Location", presignedURL)
			w.WriteHeader(http.StatusTemporaryRedirect)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer ts.Close()

	// Extract hostname from test server URL (remove "https://" prefix)
	registryHostname := ts.URL[8:]
	scheme := v1beta1.Https

	// Set up repositories pointing to test server
	repoStore := NewDummyRepositoryStore()
	destRepo := newOciRepositoryWithRegistry("output-registry", v1beta1.ReadWrite, registryHostname, &scheme, true)
	_, err = repoStore.Create(ctx, orgId, destRepo, nil)
	require.NoError(err)

	// Create ImageBuild with destination
	imageBuildStore := NewDummyImageBuildStore()
	imageBuild := newValidImageBuild("test-image-build")
	imageBuild.Spec.Destination.Repository = "output-registry"
	imageBuild.Spec.Destination.ImageName = "test-image"
	imageBuild.Spec.Destination.ImageTag = "v1.0"
	_, err = imageBuildStore.Create(ctx, orgId, &imageBuild)
	require.NoError(err)

	imageExportStore := NewDummyImageExportStore()
	svc := NewImageExportService(imageExportStore, imageBuildStore, repoStore, nil, nil, nil, config.NewDefaultImageBuilderServiceConfig(), log.InitLogs())

	// Create ImageExport
	imageExport := newReadyImageExport("test-export", manifestDigest)
	_, err = imageExportStore.Create(ctx, orgId, &imageExport)
	require.NoError(err)

	// Download
	result, err := svc.Download(ctx, orgId, "test-export")
	require.NoError(err)
	require.NotNil(result)
	require.Equal(presignedURL, result.RedirectURL)
	require.Equal(http.StatusTemporaryRedirect, result.StatusCode)
	require.Nil(result.BlobReader)
}

// TestDownloadImageExportWithBlobReader tests successful download with direct blob stream.
// NOTE: This test requires proper TLS setup for the oras library to work with test servers.
// The oras library uses its own HTTP client which doesn't respect SkipServerVerification
// for the manifest fetch operations. This test is skipped until we can properly configure
// the oras library's HTTP client or use a real registry for integration testing.
func TestDownloadImageExportWithBlobReader(t *testing.T) {
	t.Skip("Skipping integration test - requires proper TLS setup for oras library")
	require := require.New(t)
	ctx := context.Background()
	orgId := uuid.New()

	// Create a test HTTP server that mocks an OCI registry
	manifestDigest := "sha256:1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef"
	layerDigest := "sha256:abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890"
	blobContent := []byte("test blob content")

	// Create manifest JSON
	manifest := ocispec.Manifest{
		MediaType: ocispec.MediaTypeImageManifest,
		Config: ocispec.Descriptor{
			MediaType: ocispec.MediaTypeImageConfig,
			Digest:    digest.Digest("sha256:config123"),
			Size:      100,
		},
		Layers: []ocispec.Descriptor{
			{
				MediaType: "application/vnd.oci.image.layer.v1.tar+gzip",
				Digest:    digest.Digest(layerDigest),
				Size:      int64(len(blobContent)),
			},
		},
	}
	manifestBytes, err := json.Marshal(manifest)
	require.NoError(err)

	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/":
			// Registry API version check
			w.WriteHeader(http.StatusOK)
		case "/v2/test-image/manifests/" + manifestDigest:
			// Manifest fetch
			w.Header().Set("Content-Type", ocispec.MediaTypeImageManifest)
			w.Header().Set("Content-Length", strconv.Itoa(len(manifestBytes)))
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(manifestBytes)
		case "/v2/test-image/blobs/" + layerDigest:
			// Blob fetch - return blob content directly
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Header().Set("Content-Length", strconv.Itoa(len(blobContent)))
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(blobContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer ts.Close()

	// Extract hostname from test server URL (remove "https://" prefix)
	registryHostname := ts.URL[8:]
	scheme := v1beta1.Https

	// Set up repositories pointing to test server
	repoStore := NewDummyRepositoryStore()
	destRepo := newOciRepositoryWithRegistry("output-registry", v1beta1.ReadWrite, registryHostname, &scheme, true)
	_, err = repoStore.Create(ctx, orgId, destRepo, nil)
	require.NoError(err)

	// Create ImageBuild with destination
	imageBuildStore := NewDummyImageBuildStore()
	imageBuild := newValidImageBuild("test-image-build")
	imageBuild.Spec.Destination.Repository = "output-registry"
	imageBuild.Spec.Destination.ImageName = "test-image"
	imageBuild.Spec.Destination.ImageTag = "v1.0"
	_, err = imageBuildStore.Create(ctx, orgId, &imageBuild)
	require.NoError(err)

	imageExportStore := NewDummyImageExportStore()
	svc := NewImageExportService(imageExportStore, imageBuildStore, repoStore, nil, nil, nil, config.NewDefaultImageBuilderServiceConfig(), log.InitLogs())

	// Create ImageExport
	imageExport := newReadyImageExport("test-export", manifestDigest)
	_, err = imageExportStore.Create(ctx, orgId, &imageExport)
	require.NoError(err)

	// Download
	result, err := svc.Download(ctx, orgId, "test-export")
	require.NoError(err)
	require.NotNil(result)
	require.Empty(result.RedirectURL)
	require.NotNil(result.BlobReader)
	require.Equal(http.StatusOK, result.StatusCode)
	require.NotNil(result.Headers)

	// Read blob content
	if result.BlobReader != nil {
		defer result.BlobReader.Close()
		readContent, err := io.ReadAll(result.BlobReader)
		require.NoError(err)
		require.Equal(blobContent, readContent)
	}
}

// TestDownloadImageExportManifestWrongLayerCount tests validation of manifest layer count.
// NOTE: This test requires proper TLS setup for the oras library to work with test servers.
// The oras library uses its own HTTP client which doesn't respect SkipServerVerification
// for the manifest fetch operations. This test is skipped until we can properly configure
// the oras library's HTTP client or use a real registry for integration testing.
func TestDownloadImageExportManifestWrongLayerCount(t *testing.T) {
	t.Skip("Skipping integration test - requires proper TLS setup for oras library")
	require := require.New(t)
	ctx := context.Background()
	orgId := uuid.New()

	// Create a test HTTP server that mocks an OCI registry
	manifestDigest := "sha256:1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef"
	layerDigest1 := "sha256:abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890"
	layerDigest2 := "sha256:fedcba0987654321fedcba0987654321fedcba0987654321fedcba0987654321"

	// Create manifest JSON with 2 layers (should be exactly 1)
	manifest := ocispec.Manifest{
		MediaType: ocispec.MediaTypeImageManifest,
		Config: ocispec.Descriptor{
			MediaType: ocispec.MediaTypeImageConfig,
			Digest:    digest.Digest("sha256:config123"),
			Size:      100,
		},
		Layers: []ocispec.Descriptor{
			{
				MediaType: "application/vnd.oci.image.layer.v1.tar+gzip",
				Digest:    digest.Digest(layerDigest1),
				Size:      1024,
			},
			{
				MediaType: "application/vnd.oci.image.layer.v1.tar+gzip",
				Digest:    digest.Digest(layerDigest2),
				Size:      2048,
			},
		},
	}
	manifestBytes, err := json.Marshal(manifest)
	require.NoError(err)

	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/":
			// Registry API version check
			w.WriteHeader(http.StatusOK)
		case "/v2/test-image/manifests/" + manifestDigest:
			// Manifest fetch
			w.Header().Set("Content-Type", ocispec.MediaTypeImageManifest)
			w.Header().Set("Content-Length", strconv.Itoa(len(manifestBytes)))
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(manifestBytes)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer ts.Close()

	// Extract hostname from test server URL (remove "https://" prefix)
	registryHostname := ts.URL[8:]
	scheme := v1beta1.Https

	// Set up repositories pointing to test server
	repoStore := NewDummyRepositoryStore()
	destRepo := newOciRepositoryWithRegistry("output-registry", v1beta1.ReadWrite, registryHostname, &scheme, true)
	_, err = repoStore.Create(ctx, orgId, destRepo, nil)
	require.NoError(err)

	// Create ImageBuild with destination
	imageBuildStore := NewDummyImageBuildStore()
	imageBuild := newValidImageBuild("test-image-build")
	imageBuild.Spec.Destination.Repository = "output-registry"
	imageBuild.Spec.Destination.ImageName = "test-image"
	imageBuild.Spec.Destination.ImageTag = "v1.0"
	_, err = imageBuildStore.Create(ctx, orgId, &imageBuild)
	require.NoError(err)

	imageExportStore := NewDummyImageExportStore()
	svc := NewImageExportService(imageExportStore, imageBuildStore, repoStore, nil, nil, nil, config.NewDefaultImageBuilderServiceConfig(), log.InitLogs())

	// Create ImageExport
	imageExport := newReadyImageExport("test-export", manifestDigest)
	_, err = imageExportStore.Create(ctx, orgId, &imageExport)
	require.NoError(err)

	// Download should fail because manifest has 2 layers instead of 1
	result, err := svc.Download(ctx, orgId, "test-export")
	require.Error(err)
	require.Nil(result)
	require.True(errors.Is(err, ErrInvalidManifestLayerCount))
	require.Contains(err.Error(), "manifest must have exactly one layer")
	require.Contains(err.Error(), "found 2")
}

// Cancel ImageExport tests

func newTestImageExportServiceWithKVStore() (ImageExportService, *DummyImageExportStore, *DummyImageBuildStore) {
	imageExportStore := NewDummyImageExportStore()
	imageBuildStore := NewDummyImageBuildStore()
	repositoryStore := NewDummyRepositoryStore()
	kvStore := NewDummyKVStore()
	svc := NewImageExportService(imageExportStore, imageBuildStore, repositoryStore, nil, nil, kvStore, config.NewDefaultImageBuilderServiceConfig(), log.InitLogs())
	return svc, imageExportStore, imageBuildStore
}

func TestCancelImageExport_Pending(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	orgId := uuid.New()

	svc, imageExportStore, _ := newTestImageExportServiceWithKVStore()

	// Create an ImageExport
	imageExport := newValidImageExport("cancel-test")
	_, err := imageExportStore.Create(ctx, orgId, &imageExport)
	require.NoError(err)

	// Set status to Pending
	imageExport.Status = &api.ImageExportStatus{
		Conditions: &[]api.ImageExportCondition{
			{
				Type:               api.ImageExportConditionTypeReady,
				Status:             v1beta1.ConditionStatusFalse,
				Reason:             string(api.ImageExportConditionReasonPending),
				Message:            "Export is pending",
				LastTransitionTime: time.Now(),
			},
		},
	}
	_, err = imageExportStore.UpdateStatus(ctx, orgId, &imageExport)
	require.NoError(err)

	// Cancel the export
	result, err := svc.Cancel(ctx, orgId, "cancel-test")
	require.NoError(err)
	require.NotNil(result)
	require.NotNil(result.Status)
	require.NotNil(result.Status.Conditions)

	// Verify status is Canceled (Pending resources go directly to Canceled, no active processing to stop)
	readyCondition := api.FindImageExportStatusCondition(*result.Status.Conditions, api.ImageExportConditionTypeReady)
	require.NotNil(readyCondition)
	require.Equal(string(api.ImageExportConditionReasonCanceled), readyCondition.Reason)
}

func TestCancelImageExport_Converting(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	orgId := uuid.New()

	svc, imageExportStore, _ := newTestImageExportServiceWithKVStore()

	// Create an ImageExport
	imageExport := newValidImageExport("cancel-converting-test")
	_, err := imageExportStore.Create(ctx, orgId, &imageExport)
	require.NoError(err)

	// Set status to Converting
	imageExport.Status = &api.ImageExportStatus{
		Conditions: &[]api.ImageExportCondition{
			{
				Type:               api.ImageExportConditionTypeReady,
				Status:             v1beta1.ConditionStatusFalse,
				Reason:             string(api.ImageExportConditionReasonConverting),
				Message:            "Export conversion in progress",
				LastTransitionTime: time.Now(),
			},
		},
	}
	_, err = imageExportStore.UpdateStatus(ctx, orgId, &imageExport)
	require.NoError(err)

	// Cancel the export
	result, err := svc.Cancel(ctx, orgId, "cancel-converting-test")
	require.NoError(err)
	require.NotNil(result)

	// Verify status is Canceling
	readyCondition := api.FindImageExportStatusCondition(*result.Status.Conditions, api.ImageExportConditionTypeReady)
	require.NotNil(readyCondition)
	require.Equal(string(api.ImageExportConditionReasonCanceling), readyCondition.Reason)
}

func TestCancelImageExport_Pushing(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	orgId := uuid.New()

	svc, imageExportStore, _ := newTestImageExportServiceWithKVStore()

	// Create an ImageExport
	imageExport := newValidImageExport("cancel-pushing-test")
	_, err := imageExportStore.Create(ctx, orgId, &imageExport)
	require.NoError(err)

	// Set status to Pushing
	imageExport.Status = &api.ImageExportStatus{
		Conditions: &[]api.ImageExportCondition{
			{
				Type:               api.ImageExportConditionTypeReady,
				Status:             v1beta1.ConditionStatusFalse,
				Reason:             string(api.ImageExportConditionReasonPushing),
				Message:            "Pushing export artifact",
				LastTransitionTime: time.Now(),
			},
		},
	}
	_, err = imageExportStore.UpdateStatus(ctx, orgId, &imageExport)
	require.NoError(err)

	// Cancel the export
	result, err := svc.Cancel(ctx, orgId, "cancel-pushing-test")
	require.NoError(err)
	require.NotNil(result)

	// Verify status is Canceling
	readyCondition := api.FindImageExportStatusCondition(*result.Status.Conditions, api.ImageExportConditionTypeReady)
	require.NotNil(readyCondition)
	require.Equal(string(api.ImageExportConditionReasonCanceling), readyCondition.Reason)
}

func TestCancelImageExport_NotCancelable_Completed(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	orgId := uuid.New()

	svc, imageExportStore, _ := newTestImageExportServiceWithKVStore()

	// Create an ImageExport
	imageExport := newValidImageExport("cancel-completed-test")
	_, err := imageExportStore.Create(ctx, orgId, &imageExport)
	require.NoError(err)

	// Set status to Completed
	imageExport.Status = &api.ImageExportStatus{
		Conditions: &[]api.ImageExportCondition{
			{
				Type:               api.ImageExportConditionTypeReady,
				Status:             v1beta1.ConditionStatusTrue,
				Reason:             string(api.ImageExportConditionReasonCompleted),
				Message:            "Export completed",
				LastTransitionTime: time.Now(),
			},
		},
	}
	_, err = imageExportStore.UpdateStatus(ctx, orgId, &imageExport)
	require.NoError(err)

	// Attempt to cancel - should fail
	_, err = svc.Cancel(ctx, orgId, "cancel-completed-test")
	require.ErrorIs(err, ErrNotCancelable)
}

func TestCancelImageExport_NotCancelable_Failed(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	orgId := uuid.New()

	svc, imageExportStore, _ := newTestImageExportServiceWithKVStore()

	// Create an ImageExport
	imageExport := newValidImageExport("cancel-failed-test")
	_, err := imageExportStore.Create(ctx, orgId, &imageExport)
	require.NoError(err)

	// Set status to Failed
	imageExport.Status = &api.ImageExportStatus{
		Conditions: &[]api.ImageExportCondition{
			{
				Type:               api.ImageExportConditionTypeReady,
				Status:             v1beta1.ConditionStatusFalse,
				Reason:             string(api.ImageExportConditionReasonFailed),
				Message:            "Export failed",
				LastTransitionTime: time.Now(),
			},
		},
	}
	_, err = imageExportStore.UpdateStatus(ctx, orgId, &imageExport)
	require.NoError(err)

	// Attempt to cancel - should fail
	_, err = svc.Cancel(ctx, orgId, "cancel-failed-test")
	require.ErrorIs(err, ErrNotCancelable)
}

func TestCancelImageExport_NotCancelable_Canceled(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	orgId := uuid.New()

	svc, imageExportStore, _ := newTestImageExportServiceWithKVStore()

	// Create an ImageExport
	imageExport := newValidImageExport("cancel-canceled-test")
	_, err := imageExportStore.Create(ctx, orgId, &imageExport)
	require.NoError(err)

	// Set status to Canceled
	imageExport.Status = &api.ImageExportStatus{
		Conditions: &[]api.ImageExportCondition{
			{
				Type:               api.ImageExportConditionTypeReady,
				Status:             v1beta1.ConditionStatusFalse,
				Reason:             string(api.ImageExportConditionReasonCanceled),
				Message:            "Export was canceled",
				LastTransitionTime: time.Now(),
			},
		},
	}
	_, err = imageExportStore.UpdateStatus(ctx, orgId, &imageExport)
	require.NoError(err)

	// Attempt to cancel - should fail
	_, err = svc.Cancel(ctx, orgId, "cancel-canceled-test")
	require.ErrorIs(err, ErrNotCancelable)
}

func TestCancelImageExport_NotFound(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	orgId := uuid.New()

	svc, _, _ := newTestImageExportServiceWithKVStore()

	// Attempt to cancel non-existent export
	_, err := svc.Cancel(ctx, orgId, "nonexistent")
	require.Error(err)
}

func TestCancelImageExport_NoStatus(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	orgId := uuid.New()

	svc, imageExportStore, _ := newTestImageExportServiceWithKVStore()

	// Create an ImageExport with no status (treated as Pending)
	imageExport := newValidImageExport("cancel-nostatus-test")
	_, err := imageExportStore.Create(ctx, orgId, &imageExport)
	require.NoError(err)

	// Cancel the export (should work since no status = Pending)
	result, err := svc.Cancel(ctx, orgId, "cancel-nostatus-test")
	require.NoError(err)
	require.NotNil(result)

	// Verify status is Canceled (no status = Pending, which goes directly to Canceled)
	readyCondition := api.FindImageExportStatusCondition(*result.Status.Conditions, api.ImageExportConditionTypeReady)
	require.NotNil(readyCondition)
	require.Equal(string(api.ImageExportConditionReasonCanceled), readyCondition.Reason)
}
