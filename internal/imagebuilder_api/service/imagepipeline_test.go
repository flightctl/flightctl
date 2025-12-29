package service

import (
	"context"
	"net/http"
	"testing"

	"github.com/flightctl/flightctl/api/v1beta1"
	api "github.com/flightctl/flightctl/api/v1beta1/imagebuilder"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
)

func newTestImagePipelineService() (ImagePipelineService, *DummyStore, ImageBuildService, ImageExportService) {
	dummyStore := NewDummyStore()

	imageBuildSvc := NewImageBuildService(dummyStore.imageBuildStore, log.InitLogs())
	imageExportSvc := NewImageExportService(dummyStore.imageExportStore, dummyStore.imageBuildStore, log.InitLogs())

	svc := NewImagePipelineService(dummyStore.ImagePipeline(), imageBuildSvc, imageExportSvc, log.InitLogs())
	return svc, dummyStore, imageBuildSvc, imageExportSvc
}

func newValidImagePipelineRequest(buildName, exportName string) api.ImagePipelineRequest {
	imageBuild := newValidImageBuild(buildName)
	imageExport := newValidImageExport(exportName)

	return api.ImagePipelineRequest{
		ImageBuild:  imageBuild,
		ImageExport: &imageExport,
	}
}

func TestCreateImagePipelineBothResources(t *testing.T) {
	require := require.New(t)
	svc, store, _, _ := newTestImagePipelineService()
	ctx := context.Background()
	orgId := uuid.New()

	req := newValidImagePipelineRequest("test-build", "test-export")
	result, status := svc.Create(ctx, orgId, req)

	require.Equal(int32(http.StatusCreated), statusCode(status))
	require.NotNil(result)
	require.Equal("test-build", lo.FromPtr(result.ImageBuild.Metadata.Name))
	require.NotNil(result.ImageExport)
	require.Equal("test-export", lo.FromPtr(result.ImageExport.Metadata.Name))

	// Verify the ImageExport source was set to reference the ImageBuild
	sourceType, err := result.ImageExport.Spec.Source.Discriminator()
	require.NoError(err)
	require.Equal(string(api.ImageExportSourceTypeImageBuild), sourceType)

	source, err := result.ImageExport.Spec.Source.AsImageBuildRefSource()
	require.NoError(err)
	require.Equal("test-build", source.ImageBuildRef)

	// Verify both are in the store
	_, err = store.imageBuildStore.Get(ctx, orgId, "test-build")
	require.NoError(err)
	_, err = store.imageExportStore.Get(ctx, orgId, "test-export")
	require.NoError(err)
}

func TestCreateImagePipelineOnlyImageBuild(t *testing.T) {
	require := require.New(t)
	svc, store, _, _ := newTestImagePipelineService()
	ctx := context.Background()
	orgId := uuid.New()

	imageBuild := newValidImageBuild("only-build")
	req := api.ImagePipelineRequest{
		ImageBuild:  imageBuild,
		ImageExport: nil,
	}

	result, status := svc.Create(ctx, orgId, req)

	require.Equal(int32(http.StatusCreated), statusCode(status))
	require.NotNil(result)
	require.Equal("only-build", lo.FromPtr(result.ImageBuild.Metadata.Name))
	require.Nil(result.ImageExport)

	// Verify ImageBuild is in the store
	_, err := store.imageBuildStore.Get(ctx, orgId, "only-build")
	require.NoError(err)
}

func TestCreateImagePipelineImageBuildValidationFails(t *testing.T) {
	require := require.New(t)
	svc, store, _, _ := newTestImagePipelineService()
	ctx := context.Background()
	orgId := uuid.New()

	// Create request with invalid ImageBuild (missing source repository)
	imageBuild := api.ImageBuild{
		Metadata: v1beta1.ObjectMeta{Name: lo.ToPtr("bad-build")},
		Spec: api.ImageBuildSpec{
			Source: api.ImageBuildSource{
				// Missing Repository
				ImageName: "input-image",
				ImageTag:  "v1.0",
			},
			Destination: api.ImageBuildDestination{
				Repository: "output-registry",
				ImageName:  "output-image",
				Tag:        "v1.0",
			},
		},
	}

	req := api.ImagePipelineRequest{
		ImageBuild: imageBuild,
	}

	_, status := svc.Create(ctx, orgId, req)

	require.Equal(int32(http.StatusBadRequest), statusCode(status))

	// Verify nothing was created
	_, err := store.imageBuildStore.Get(ctx, orgId, "bad-build")
	require.Error(err)
}

func TestCreateImagePipelineImageExportValidationFails(t *testing.T) {
	require := require.New(t)
	svc, _, _, _ := newTestImagePipelineService()
	ctx := context.Background()
	orgId := uuid.New()

	imageBuild := newValidImageBuild("good-build")

	// Create ImageExport with missing destination repository
	imageExport := newValidImageExport("bad-export")
	imageExport.Spec.Destination.Repository = ""

	req := api.ImagePipelineRequest{
		ImageBuild:  imageBuild,
		ImageExport: &imageExport,
	}

	_, status := svc.Create(ctx, orgId, req)

	// Validation should fail with BadRequest
	require.Equal(int32(http.StatusBadRequest), statusCode(status))

	// Note: Transaction rollback testing requires real database.
	// Integration tests verify actual rollback behavior.
}

func TestCreateImagePipelineDuplicateImageBuild(t *testing.T) {
	require := require.New(t)
	svc, store, _, _ := newTestImagePipelineService()
	ctx := context.Background()
	orgId := uuid.New()

	// Pre-create an ImageBuild
	existingBuild := newValidImageBuild("duplicate-build")
	_, err := store.imageBuildStore.Create(ctx, orgId, &existingBuild)
	require.NoError(err)

	// Try to create with the same name
	req := newValidImagePipelineRequest("duplicate-build", "new-export")
	_, status := svc.Create(ctx, orgId, req)

	require.Equal(int32(http.StatusConflict), statusCode(status))

	// Verify ImageExport was not created
	_, err = store.imageExportStore.Get(ctx, orgId, "new-export")
	require.Error(err)
}

func TestCreateImagePipelineDuplicateImageExport(t *testing.T) {
	require := require.New(t)
	svc, store, _, _ := newTestImagePipelineService()
	ctx := context.Background()
	orgId := uuid.New()

	// Pre-create an ImageExport
	existingExport := newValidImageExport("duplicate-export")
	_, err := store.imageExportStore.Create(ctx, orgId, &existingExport)
	require.NoError(err)

	// Try to create with the same export name
	req := newValidImagePipelineRequest("new-build", "duplicate-export")
	_, status := svc.Create(ctx, orgId, req)

	require.Equal(int32(http.StatusConflict), statusCode(status))

	// Note: In a real transaction, the ImageBuild would be rolled back
	// With our dummy store, we can't fully simulate this without real DB
}

func TestCreateImagePipelineImageExportMissingFormats(t *testing.T) {
	require := require.New(t)
	svc, _, _, _ := newTestImagePipelineService()
	ctx := context.Background()
	orgId := uuid.New()

	imageBuild := newValidImageBuild("test-build")
	imageExport := newValidImageExport("test-export")
	imageExport.Spec.Format = "" // Invalid - formats required

	req := api.ImagePipelineRequest{
		ImageBuild:  imageBuild,
		ImageExport: &imageExport,
	}

	_, status := svc.Create(ctx, orgId, req)

	require.Equal(int32(http.StatusBadRequest), statusCode(status))
}

func TestCreateImagePipelineOverridesExistingSource(t *testing.T) {
	require := require.New(t)
	svc, _, _, _ := newTestImagePipelineService()
	ctx := context.Background()
	orgId := uuid.New()

	imageBuild := newValidImageBuild("my-build")

	// Create ImageExport with an existing imageReference source
	// The service should override it to reference the ImageBuild
	imageExport := newValidImageExport("my-export")

	req := api.ImagePipelineRequest{
		ImageBuild:  imageBuild,
		ImageExport: &imageExport,
	}

	result, status := svc.Create(ctx, orgId, req)

	require.Equal(int32(http.StatusCreated), statusCode(status))
	require.NotNil(result.ImageExport)

	// Verify the source was overridden to imageBuild type
	sourceType, err := result.ImageExport.Spec.Source.Discriminator()
	require.NoError(err)
	require.Equal(string(api.ImageExportSourceTypeImageBuild), sourceType)

	source, err := result.ImageExport.Spec.Source.AsImageBuildRefSource()
	require.NoError(err)
	require.Equal("my-build", source.ImageBuildRef)
}
