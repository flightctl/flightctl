package service

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	api "github.com/flightctl/flightctl/api/imagebuilder/v1beta1"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/google/uuid"
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
