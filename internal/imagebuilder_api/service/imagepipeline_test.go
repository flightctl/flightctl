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

	svc := NewImagePipelineService(dummyStore.ImagePipeline(), imageBuildSvc, imageExportSvc, dummyStore.ImageBuild(), dummyStore.ImageExport(), log.InitLogs())
	return svc, dummyStore, imageBuildSvc, imageExportSvc
}

func newValidImagePipelineRequest(buildName string, exportNames ...string) api.ImagePipelineRequest {
	imageBuild := newValidImageBuild(buildName)
	var imageExports []api.ImageExport
	for _, exportName := range exportNames {
		imageExport := newValidImageExport(exportName)
		imageExports = append(imageExports, imageExport)
	}

	return api.ImagePipelineRequest{
		ImageBuild:   imageBuild,
		ImageExports: lo.Ternary(len(imageExports) > 0, &imageExports, nil),
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
	require.NotNil(result.ImageExports)
	require.Len(*result.ImageExports, 1)
	require.Equal("test-export", lo.FromPtr((*result.ImageExports)[0].Metadata.Name))

	// Verify the ImageExport source was set to reference the ImageBuild
	sourceType, err := (*result.ImageExports)[0].Spec.Source.Discriminator()
	require.NoError(err)
	require.Equal(string(api.ImageExportSourceTypeImageBuild), sourceType)

	source, err := (*result.ImageExports)[0].Spec.Source.AsImageBuildRefSource()
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
		ImageBuild: imageBuild,
	}

	result, status := svc.Create(ctx, orgId, req)

	require.Equal(int32(http.StatusCreated), statusCode(status))
	require.NotNil(result)
	require.Equal("only-build", lo.FromPtr(result.ImageBuild.Metadata.Name))
	require.Nil(result.ImageExports)

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
	imageExports := []api.ImageExport{imageExport}

	req := api.ImagePipelineRequest{
		ImageBuild:   imageBuild,
		ImageExports: &imageExports,
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
	imageExports := []api.ImageExport{imageExport}

	req := api.ImagePipelineRequest{
		ImageBuild:   imageBuild,
		ImageExports: &imageExports,
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
		ImageBuild:   imageBuild,
		ImageExports: &[]api.ImageExport{imageExport},
	}

	result, status := svc.Create(ctx, orgId, req)

	require.Equal(int32(http.StatusCreated), statusCode(status))
	require.NotNil(result.ImageExports)
	require.Len(*result.ImageExports, 1)

	// Verify the source was overridden to imageBuild type
	sourceType, err := (*result.ImageExports)[0].Spec.Source.Discriminator()
	require.NoError(err)
	require.Equal(string(api.ImageExportSourceTypeImageBuild), sourceType)

	source, err := (*result.ImageExports)[0].Spec.Source.AsImageBuildRefSource()
	require.NoError(err)
	require.Equal("my-build", source.ImageBuildRef)
}

func TestGetImagePipeline(t *testing.T) {
	require := require.New(t)
	svc, store, _, _ := newTestImagePipelineService()
	ctx := context.Background()
	orgId := uuid.New()

	// Create an ImageBuild
	imageBuild := newValidImageBuild("get-build")
	_, err := store.imageBuildStore.Create(ctx, orgId, &imageBuild)
	require.NoError(err)

	// Create ImageExports that reference the ImageBuild
	export1 := newValidImageExport("export-1")
	source1 := api.ImageExportSource{}
	_ = source1.FromImageBuildRefSource(api.ImageBuildRefSource{
		Type:          api.ImageBuildRefSourceTypeImageBuild,
		ImageBuildRef: "get-build",
	})
	export1.Spec.Source = source1
	_, err = store.imageExportStore.Create(ctx, orgId, &export1)
	require.NoError(err)

	export2 := newValidImageExport("export-2")
	source2 := api.ImageExportSource{}
	_ = source2.FromImageBuildRefSource(api.ImageBuildRefSource{
		Type:          api.ImageBuildRefSourceTypeImageBuild,
		ImageBuildRef: "get-build",
	})
	export2.Spec.Source = source2
	_, err = store.imageExportStore.Create(ctx, orgId, &export2)
	require.NoError(err)

	// Get the ImagePipeline
	result, status := svc.Get(ctx, orgId, "get-build")

	require.Equal(int32(http.StatusOK), statusCode(status))
	require.NotNil(result)
	require.Equal("get-build", lo.FromPtr(result.ImageBuild.Metadata.Name))
	require.NotNil(result.ImageExports)
	require.Len(*result.ImageExports, 2)

	exportNames := []string{
		lo.FromPtr((*result.ImageExports)[0].Metadata.Name),
		lo.FromPtr((*result.ImageExports)[1].Metadata.Name),
	}
	require.Contains(exportNames, "export-1")
	require.Contains(exportNames, "export-2")
}

func TestGetImagePipelineNotFound(t *testing.T) {
	require := require.New(t)
	svc, _, _, _ := newTestImagePipelineService()
	ctx := context.Background()
	orgId := uuid.New()

	_, status := svc.Get(ctx, orgId, "nonexistent-build")

	require.Equal(int32(http.StatusNotFound), statusCode(status))
}

func TestGetImagePipelineNoExports(t *testing.T) {
	require := require.New(t)
	svc, store, _, _ := newTestImagePipelineService()
	ctx := context.Background()
	orgId := uuid.New()

	// Create an ImageBuild without any exports
	imageBuild := newValidImageBuild("no-exports-build")
	_, err := store.imageBuildStore.Create(ctx, orgId, &imageBuild)
	require.NoError(err)

	result, status := svc.Get(ctx, orgId, "no-exports-build")

	require.Equal(int32(http.StatusOK), statusCode(status))
	require.NotNil(result)
	require.Equal("no-exports-build", lo.FromPtr(result.ImageBuild.Metadata.Name))
	require.Nil(result.ImageExports)
}

func TestListImagePipelines(t *testing.T) {
	require := require.New(t)
	svc, store, _, _ := newTestImagePipelineService()
	ctx := context.Background()
	orgId := uuid.New()

	// Create multiple ImageBuilds
	build1 := newValidImageBuild("build-1")
	_, err := store.imageBuildStore.Create(ctx, orgId, &build1)
	require.NoError(err)

	build2 := newValidImageBuild("build-2")
	_, err = store.imageBuildStore.Create(ctx, orgId, &build2)
	require.NoError(err)

	// Create exports for build-1
	export1 := newValidImageExport("export-1")
	source1 := api.ImageExportSource{}
	_ = source1.FromImageBuildRefSource(api.ImageBuildRefSource{
		Type:          api.ImageBuildRefSourceTypeImageBuild,
		ImageBuildRef: "build-1",
	})
	export1.Spec.Source = source1
	_, err = store.imageExportStore.Create(ctx, orgId, &export1)
	require.NoError(err)

	// List ImagePipelines
	result, status := svc.List(ctx, orgId, api.ListImagePipelinesParams{})

	require.Equal(int32(http.StatusOK), statusCode(status))
	require.NotNil(result)
	require.GreaterOrEqual(len(result.Items), 2)

	// Find build-1 and build-2 in results
	var foundBuild1, foundBuild2 bool
	for i := range result.Items {
		buildName := lo.FromPtr(result.Items[i].ImageBuild.Metadata.Name)
		if buildName == "build-1" {
			foundBuild1 = true
			require.NotNil(result.Items[i].ImageExports)
			require.Len(*result.Items[i].ImageExports, 1)
			require.Equal("export-1", lo.FromPtr((*result.Items[i].ImageExports)[0].Metadata.Name))
		}
		if buildName == "build-2" {
			foundBuild2 = true
			require.Nil(result.Items[i].ImageExports)
		}
	}
	require.True(foundBuild1)
	require.True(foundBuild2)
}

func TestListImagePipelinesEmpty(t *testing.T) {
	require := require.New(t)
	svc, _, _, _ := newTestImagePipelineService()
	ctx := context.Background()
	orgId := uuid.New()

	result, status := svc.List(ctx, orgId, api.ListImagePipelinesParams{})

	require.Equal(int32(http.StatusOK), statusCode(status))
	require.NotNil(result)
	require.Empty(result.Items)
}

func TestDeleteImagePipeline(t *testing.T) {
	require := require.New(t)
	svc, store, _, _ := newTestImagePipelineService()
	ctx := context.Background()
	orgId := uuid.New()

	// Create ImagePipeline with ImageBuild and ImageExports
	req := newValidImagePipelineRequest("delete-build", "delete-export-1", "delete-export-2")
	_, status := svc.Create(ctx, orgId, req)
	require.Equal(int32(http.StatusCreated), statusCode(status))

	// Verify they exist
	_, status = svc.Get(ctx, orgId, "delete-build")
	require.Equal(int32(http.StatusOK), statusCode(status))

	// Delete the ImagePipeline
	result, status := svc.Delete(ctx, orgId, "delete-build")
	require.Equal(int32(http.StatusOK), statusCode(status))
	require.NotNil(result)
	require.Equal("delete-build", lo.FromPtr(result.ImageBuild.Metadata.Name))
	require.NotNil(result.ImageExports)
	require.Len(*result.ImageExports, 2)

	// Verify ImageBuild is deleted
	_, err := store.imageBuildStore.Get(ctx, orgId, "delete-build")
	require.Error(err)

	// Verify ImageExports are deleted
	_, err = store.imageExportStore.Get(ctx, orgId, "delete-export-1")
	require.Error(err)
	_, err = store.imageExportStore.Get(ctx, orgId, "delete-export-2")
	require.Error(err)
}

func TestDeleteImagePipelineNotFound(t *testing.T) {
	require := require.New(t)
	svc, _, _, _ := newTestImagePipelineService()
	ctx := context.Background()
	orgId := uuid.New()

	// Try to delete non-existent ImagePipeline
	_, status := svc.Delete(ctx, orgId, "non-existent")
	require.Equal(int32(http.StatusNotFound), statusCode(status))
}

func TestDeleteImagePipelineNoExports(t *testing.T) {
	require := require.New(t)
	svc, store, _, _ := newTestImagePipelineService()
	ctx := context.Background()
	orgId := uuid.New()

	// Create ImagePipeline with only ImageBuild (no exports)
	req := newValidImagePipelineRequest("delete-build-only")
	_, status := svc.Create(ctx, orgId, req)
	require.Equal(int32(http.StatusCreated), statusCode(status))

	// Delete the ImagePipeline
	result, status := svc.Delete(ctx, orgId, "delete-build-only")
	require.Equal(int32(http.StatusOK), statusCode(status))
	require.NotNil(result)
	require.Equal("delete-build-only", lo.FromPtr(result.ImageBuild.Metadata.Name))
	require.Nil(result.ImageExports)

	// Verify ImageBuild is deleted
	_, err := store.imageBuildStore.Get(ctx, orgId, "delete-build-only")
	require.Error(err)
}
