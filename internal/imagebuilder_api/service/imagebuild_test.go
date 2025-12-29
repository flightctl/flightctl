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

func statusCode(status v1beta1.Status) int32 {
	return status.Code
}

func newTestImageBuildService() (ImageBuildService, *DummyImageBuildStore) {
	imageBuildStore := NewDummyImageBuildStore()
	svc := NewImageBuildService(imageBuildStore, log.InitLogs())
	return svc, imageBuildStore
}

func newValidImageBuild(name string) api.ImageBuild {
	return api.ImageBuild{
		ApiVersion: api.ImageBuildAPIVersion,
		Kind:       api.ImageBuildKind,
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
				Tag:        "v1.0",
			},
		},
	}
}

func TestCreateImageBuild(t *testing.T) {
	require := require.New(t)
	svc, _ := newTestImageBuildService()
	ctx := context.Background()
	orgId := uuid.New()

	imageBuild := newValidImageBuild("test-build")
	result, status := svc.Create(ctx, orgId, imageBuild)

	require.Equal(int32(http.StatusCreated), statusCode(status))
	require.NotNil(result)
	require.Equal("test-build", lo.FromPtr(result.Metadata.Name))
}

func TestCreateImageBuildDuplicate(t *testing.T) {
	require := require.New(t)
	svc, _ := newTestImageBuildService()
	ctx := context.Background()
	orgId := uuid.New()

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
				Tag:        "v1.0",
			},
		},
	}

	_, status := svc.Create(ctx, orgId, imageBuild)
	require.Equal(int32(http.StatusBadRequest), statusCode(status))
}

func TestGetImageBuild(t *testing.T) {
	require := require.New(t)
	svc, _ := newTestImageBuildService()
	ctx := context.Background()
	orgId := uuid.New()

	// Create first
	imageBuild := newValidImageBuild("get-test")
	_, status := svc.Create(ctx, orgId, imageBuild)
	require.Equal(int32(http.StatusCreated), statusCode(status))

	// Get it back
	result, status := svc.Get(ctx, orgId, "get-test")
	require.Equal(int32(http.StatusOK), statusCode(status))
	require.NotNil(result)
	require.Equal("get-test", lo.FromPtr(result.Metadata.Name))
}

func TestGetImageBuildNotFound(t *testing.T) {
	require := require.New(t)
	svc, _ := newTestImageBuildService()
	ctx := context.Background()
	orgId := uuid.New()

	_, status := svc.Get(ctx, orgId, "nonexistent")
	require.Equal(int32(http.StatusNotFound), statusCode(status))
}

func TestListImageBuilds(t *testing.T) {
	require := require.New(t)
	svc, _ := newTestImageBuildService()
	ctx := context.Background()
	orgId := uuid.New()

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
	svc, _ := newTestImageBuildService()
	ctx := context.Background()
	orgId := uuid.New()

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

func TestDeleteImageBuild(t *testing.T) {
	require := require.New(t)
	svc, _ := newTestImageBuildService()
	ctx := context.Background()
	orgId := uuid.New()

	// Create first
	imageBuild := newValidImageBuild("delete-test")
	_, status := svc.Create(ctx, orgId, imageBuild)
	require.Equal(int32(http.StatusCreated), statusCode(status))

	// Delete it
	status = svc.Delete(ctx, orgId, "delete-test")
	require.Equal(int32(http.StatusOK), statusCode(status))

	// Verify it's gone
	_, status = svc.Get(ctx, orgId, "delete-test")
	require.Equal(int32(http.StatusNotFound), statusCode(status))
}

func TestDeleteImageBuildNotFound(t *testing.T) {
	require := require.New(t)
	svc, _ := newTestImageBuildService()
	ctx := context.Background()
	orgId := uuid.New()

	status := svc.Delete(ctx, orgId, "nonexistent")
	require.Equal(int32(http.StatusNotFound), statusCode(status))
}

func TestUpdateStatus(t *testing.T) {
	require := require.New(t)
	svc, _ := newTestImageBuildService()
	ctx := context.Background()
	orgId := uuid.New()

	// Create first
	imageBuild := newValidImageBuild("status-test")
	_, status := svc.Create(ctx, orgId, imageBuild)
	require.Equal(int32(http.StatusCreated), statusCode(status))

	// Update status
	phase := api.ImageBuildPhaseBuilding
	imageBuild.Status = &api.ImageBuildStatus{
		Phase: &phase,
	}
	result, err := svc.UpdateStatus(ctx, orgId, &imageBuild)
	require.NoError(err)
	require.NotNil(result)
	require.NotNil(result.Status)
	require.Equal(api.ImageBuildPhaseBuilding, lo.FromPtr(result.Status.Phase))
}
