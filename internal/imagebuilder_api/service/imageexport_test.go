package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	api "github.com/flightctl/flightctl/api/imagebuilder/v1beta1"
	"github.com/flightctl/flightctl/internal/flterrors"
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
	svc := NewImageExportService(imageExportStore, imageBuildStore, repositoryStore, nil, nil, log.InitLogs())
	return svc, imageExportStore, imageBuildStore
}

func newValidImageExport(name string) api.ImageExport {
	source := api.ImageExportSource{}
	_ = source.FromImageReferenceSource(api.ImageReferenceSource{
		Type:       api.ImageReference,
		Repository: "source-registry",
		ImageName:  "source-image",
		ImageTag:   "v1.0",
	})

	return api.ImageExport{
		ApiVersion: api.ImageExportAPIVersion,
		Kind:       string(api.ResourceKindImageExport),
		Metadata: v1beta1.ObjectMeta{
			Name: lo.ToPtr(name),
		},
		Spec: api.ImageExportSpec{
			Source: source,
			Destination: api.ImageExportDestination{
				Repository: "output-registry",
				ImageName:  "output-image",
				Tag:        "v1.0",
			},
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
			Destination: api.ImageExportDestination{
				Repository: "output-registry",
				ImageName:  "output-image",
				Tag:        "v1.0",
			},
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

func TestCreateImageExport(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	orgId := uuid.New()

	// Set up repositories
	repoStore := NewDummyRepositoryStore()
	setupRepositoriesForImageExport(repoStore, ctx, orgId, true)
	svc := NewImageExportService(NewDummyImageExportStore(), NewDummyImageBuildStore(), repoStore, nil, nil, log.InitLogs())

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
	setupRepositoriesForImageExport(repoStore, ctx, orgId, true)
	svc := NewImageExportService(NewDummyImageExportStore(), NewDummyImageBuildStore(), repoStore, nil, nil, log.InitLogs())

	imageExport := newValidImageExport("duplicate-test")

	// First create should succeed
	_, status := svc.Create(ctx, orgId, imageExport)
	require.Equal(int32(http.StatusCreated), statusCode(status))

	// Second create should fail with conflict
	_, status = svc.Create(ctx, orgId, imageExport)
	require.Equal(int32(http.StatusConflict), statusCode(status))
}

func TestCreateImageExportMissingDestinationRepository(t *testing.T) {
	require := require.New(t)
	svc, _, _ := newTestImageExportService()
	ctx := context.Background()
	orgId := uuid.New()

	imageExport := newValidImageExport("test")
	imageExport.Spec.Destination.Repository = ""

	_, status := svc.Create(ctx, orgId, imageExport)
	require.Equal(int32(http.StatusBadRequest), statusCode(status))
}

func TestCreateImageExportMissingFormats(t *testing.T) {
	require := require.New(t)
	svc, _, _ := newTestImageExportService()
	ctx := context.Background()
	orgId := uuid.New()

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
	svc := NewImageExportService(NewDummyImageExportStore(), imageBuildStore, repoStore, nil, nil, log.InitLogs())

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
	setupRepositoriesForImageExport(repoStore, ctx, orgId, true)
	svc := NewImageExportService(NewDummyImageExportStore(), NewDummyImageBuildStore(), repoStore, nil, nil, log.InitLogs())

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
	setupRepositoriesForImageExport(repoStore, ctx, orgId, true)
	svc := NewImageExportService(NewDummyImageExportStore(), NewDummyImageBuildStore(), repoStore, nil, nil, log.InitLogs())

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
	setupRepositoriesForImageExport(repoStore, ctx, orgId, true)
	svc := NewImageExportService(NewDummyImageExportStore(), NewDummyImageBuildStore(), repoStore, nil, nil, log.InitLogs())

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

func TestDeleteImageExport(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	orgId := uuid.New()

	// Set up repositories
	repoStore := NewDummyRepositoryStore()
	setupRepositoriesForImageExport(repoStore, ctx, orgId, true)
	svc := NewImageExportService(NewDummyImageExportStore(), NewDummyImageBuildStore(), repoStore, nil, nil, log.InitLogs())

	// Create first
	imageExport := newValidImageExport("delete-test")
	_, status := svc.Create(ctx, orgId, imageExport)
	require.Equal(int32(http.StatusCreated), statusCode(status))

	// Delete it
	deleted, status := svc.Delete(ctx, orgId, "delete-test")
	require.Equal(int32(http.StatusOK), statusCode(status))
	require.NotNil(deleted)
	require.Equal("delete-test", lo.FromPtr(deleted.Metadata.Name))

	// Verify it's gone
	_, status = svc.Get(ctx, orgId, "delete-test")
	require.Equal(int32(http.StatusNotFound), statusCode(status))
}

func TestDeleteImageExportNotFound(t *testing.T) {
	require := require.New(t)
	svc, _, _ := newTestImageExportService()
	ctx := context.Background()
	orgId := uuid.New()

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
	setupRepositoriesForImageExport(repoStore, ctx, orgId, true)
	svc := NewImageExportService(NewDummyImageExportStore(), NewDummyImageBuildStore(), repoStore, nil, nil, log.InitLogs())

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

func TestCreateImageExportSourceRepositoryNotFound(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	orgId := uuid.New()

	// Set up only destination repository
	repoStore := NewDummyRepositoryStore()
	destRepo := newOciRepository("output-registry", v1beta1.ReadWrite)
	_, _ = repoStore.Create(ctx, orgId, destRepo, nil)
	svc := NewImageExportService(NewDummyImageExportStore(), NewDummyImageBuildStore(), repoStore, nil, nil, log.InitLogs())

	imageExport := newValidImageExport("test-export")
	_, status := svc.Create(ctx, orgId, imageExport)

	require.Equal(int32(http.StatusBadRequest), statusCode(status))
	require.Contains(status.Message, "spec.source.repository: Repository \"source-registry\" not found")
}

func TestCreateImageExportDestinationRepositoryNotFound(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	orgId := uuid.New()

	// Set up only source repository
	repoStore := NewDummyRepositoryStore()
	sourceRepo := newOciRepository("source-registry", v1beta1.Read)
	_, _ = repoStore.Create(ctx, orgId, sourceRepo, nil)
	svc := NewImageExportService(NewDummyImageExportStore(), NewDummyImageBuildStore(), repoStore, nil, nil, log.InitLogs())

	imageExport := newValidImageExport("test-export")
	_, status := svc.Create(ctx, orgId, imageExport)

	require.Equal(int32(http.StatusBadRequest), statusCode(status))
	require.Contains(status.Message, "spec.destination.repository: Repository \"output-registry\" not found")
}

func TestCreateImageExportSourceRepositoryNotOci(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	orgId := uuid.New()

	// Set up repositories - source is Git type (not OCI)
	repoStore := NewDummyRepositoryStore()
	spec := v1beta1.RepositorySpec{}
	_ = spec.FromGenericRepoSpec(v1beta1.GenericRepoSpec{
		Type: v1beta1.RepoSpecTypeGit,
		Url:  "https://github.com/example/repo.git",
	})
	sourceRepo := &v1beta1.Repository{
		ApiVersion: "flightctl.io/v1beta1",
		Kind:       string(v1beta1.ResourceKindRepository),
		Metadata: v1beta1.ObjectMeta{
			Name: lo.ToPtr("source-registry"),
		},
		Spec: spec,
	}
	_, _ = repoStore.Create(ctx, orgId, sourceRepo, nil)

	destRepo := newOciRepository("output-registry", v1beta1.ReadWrite)
	_, _ = repoStore.Create(ctx, orgId, destRepo, nil)
	svc := NewImageExportService(NewDummyImageExportStore(), NewDummyImageBuildStore(), repoStore, nil, nil, log.InitLogs())

	imageExport := newValidImageExport("test-export")
	_, status := svc.Create(ctx, orgId, imageExport)

	require.Equal(int32(http.StatusBadRequest), statusCode(status))
	require.Contains(status.Message, "spec.source.repository: Repository \"source-registry\" must be of type 'oci'")
}

func TestCreateImageExportDestinationRepositoryNotOci(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	orgId := uuid.New()

	// Set up repositories - destination is Git type (not OCI)
	repoStore := NewDummyRepositoryStore()
	sourceRepo := newOciRepository("source-registry", v1beta1.Read)
	_, _ = repoStore.Create(ctx, orgId, sourceRepo, nil)

	spec := v1beta1.RepositorySpec{}
	_ = spec.FromGenericRepoSpec(v1beta1.GenericRepoSpec{
		Type: v1beta1.RepoSpecTypeGit,
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
	svc := NewImageExportService(NewDummyImageExportStore(), NewDummyImageBuildStore(), repoStore, nil, nil, log.InitLogs())

	imageExport := newValidImageExport("test-export")
	_, status := svc.Create(ctx, orgId, imageExport)

	require.Equal(int32(http.StatusBadRequest), statusCode(status))
	require.Contains(status.Message, "spec.destination.repository: Repository \"output-registry\" must be of type 'oci'")
}

func TestCreateImageExportDestinationRepositoryNotReadWrite(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	orgId := uuid.New()

	// Set up repositories - destination is Read-only
	repoStore := NewDummyRepositoryStore()
	sourceRepo := newOciRepository("source-registry", v1beta1.Read)
	_, _ = repoStore.Create(ctx, orgId, sourceRepo, nil)

	destRepo := newOciRepository("output-registry", v1beta1.Read)
	_, _ = repoStore.Create(ctx, orgId, destRepo, nil)
	svc := NewImageExportService(NewDummyImageExportStore(), NewDummyImageBuildStore(), repoStore, nil, nil, log.InitLogs())

	imageExport := newValidImageExport("test-export")
	_, status := svc.Create(ctx, orgId, imageExport)

	require.Equal(int32(http.StatusBadRequest), statusCode(status))
	require.Contains(status.Message, "spec.destination.repository: Repository \"output-registry\" must have 'ReadWrite' access mode")
}

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
	setupRepositoriesForImageExport(repoStore, ctx, orgId, true)
	imageExportStore := NewDummyImageExportStore()
	svc := NewImageExportService(imageExportStore, NewDummyImageBuildStore(), repoStore, nil, nil, log.InitLogs())

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
	setupRepositoriesForImageExport(repoStore, ctx, orgId, true)
	imageExportStore := NewDummyImageExportStore()
	svc := NewImageExportService(imageExportStore, NewDummyImageBuildStore(), repoStore, nil, nil, log.InitLogs())

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
	setupRepositoriesForImageExport(repoStore, ctx, orgId, true)
	imageExportStore := NewDummyImageExportStore()
	svc := NewImageExportService(imageExportStore, NewDummyImageBuildStore(), repoStore, nil, nil, log.InitLogs())

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
	setupRepositoriesForImageExport(repoStore, ctx, orgId, true)
	imageExportStore := NewDummyImageExportStore()
	svc := NewImageExportService(imageExportStore, NewDummyImageBuildStore(), repoStore, nil, nil, log.InitLogs())

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
	setupRepositoriesForImageExport(repoStore, ctx, orgId, true)
	imageExportStore := NewDummyImageExportStore()
	svc := NewImageExportService(imageExportStore, NewDummyImageBuildStore(), repoStore, nil, nil, log.InitLogs())

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
	setupRepositoriesForImageExport(repoStore, ctx, orgId, true)
	imageExportStore := NewDummyImageExportStore()
	svc := NewImageExportService(imageExportStore, NewDummyImageBuildStore(), repoStore, nil, nil, log.InitLogs())

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
	sourceRepo := newOciRepository("source-registry", v1beta1.Read)
	_, _ = repoStore.Create(ctx, orgId, sourceRepo, nil)

	imageExportStore := NewDummyImageExportStore()
	svc := NewImageExportService(imageExportStore, NewDummyImageBuildStore(), repoStore, nil, nil, log.InitLogs())

	// Create ImageExport that references non-existent destination repository
	imageExport := newReadyImageExport("test-export", "sha256:abc123")
	_, err := imageExportStore.Create(ctx, orgId, &imageExport)
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
	setupRepositoriesForImageExport(repoStore, ctx, orgId, true)
	imageExportStore := NewDummyImageExportStore()
	svc := NewImageExportService(imageExportStore, NewDummyImageBuildStore(), repoStore, nil, nil, log.InitLogs())

	// Create ImageExport with invalid manifest digest
	imageExport := newReadyImageExport("test-export", "invalid-digest")
	_, err := imageExportStore.Create(ctx, orgId, &imageExport)
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
		Type:       v1beta1.RepoSpecTypeOci,
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
	scheme := v1beta1.OciRepoSpecSchemeHttps

	// Set up repositories pointing to test server
	repoStore := NewDummyRepositoryStore()
	destRepo := newOciRepositoryWithRegistry("output-registry", v1beta1.ReadWrite, registryHostname, &scheme, true)
	_, err = repoStore.Create(ctx, orgId, destRepo, nil)
	require.NoError(err)

	imageExportStore := NewDummyImageExportStore()
	svc := NewImageExportService(imageExportStore, NewDummyImageBuildStore(), repoStore, nil, nil, log.InitLogs())

	// Create ImageExport
	imageExport := newReadyImageExport("test-export", manifestDigest)
	imageExport.Spec.Destination.ImageName = "test-image"
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
	scheme := v1beta1.OciRepoSpecSchemeHttps

	// Set up repositories pointing to test server
	repoStore := NewDummyRepositoryStore()
	destRepo := newOciRepositoryWithRegistry("output-registry", v1beta1.ReadWrite, registryHostname, &scheme, true)
	_, err = repoStore.Create(ctx, orgId, destRepo, nil)
	require.NoError(err)

	imageExportStore := NewDummyImageExportStore()
	svc := NewImageExportService(imageExportStore, NewDummyImageBuildStore(), repoStore, nil, nil, log.InitLogs())

	// Create ImageExport
	imageExport := newReadyImageExport("test-export", manifestDigest)
	imageExport.Spec.Destination.ImageName = "test-image"
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
	scheme := v1beta1.OciRepoSpecSchemeHttps

	// Set up repositories pointing to test server
	repoStore := NewDummyRepositoryStore()
	destRepo := newOciRepositoryWithRegistry("output-registry", v1beta1.ReadWrite, registryHostname, &scheme, true)
	_, err = repoStore.Create(ctx, orgId, destRepo, nil)
	require.NoError(err)

	imageExportStore := NewDummyImageExportStore()
	svc := NewImageExportService(imageExportStore, NewDummyImageBuildStore(), repoStore, nil, nil, log.InitLogs())

	// Create ImageExport
	imageExport := newReadyImageExport("test-export", manifestDigest)
	imageExport.Spec.Destination.ImageName = "test-image"
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
