package service

import (
	"context"
	"testing"

	api "github.com/flightctl/flightctl/api/v1beta1"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

func verifyRepoPatchFailed(require *require.Assertions, status api.Status) {
	require.Equal(statusBadRequestCode, status.Code)
}

func testRepositoryPatch(require *require.Assertions, patch api.PatchRequest) (*api.Repository, api.Repository, api.Status) {
	spec := api.RepositorySpec{}
	err := spec.FromGenericRepoSpec(api.GenericRepoSpec{
		Url:  "foo",
		Type: "git",
	})
	require.NoError(err)
	repository := api.Repository{
		ApiVersion: "v1",
		Kind:       "Repository",
		Metadata: api.ObjectMeta{
			Name:   lo.ToPtr("foo"),
			Labels: &map[string]string{"labelKey": "labelValue"},
		},
		Spec: spec,
	}

	ts := &TestStore{}
	wc := &DummyWorkerClient{}
	serviceHandler := ServiceHandler{
		eventHandler: NewEventHandler(ts, wc, logrus.New()),
		store:        ts,
		workerClient: wc,
		log:          logrus.New(),
	}
	ctx := context.Background()
	_, err = serviceHandler.store.Repository().Create(ctx, store.NullOrgId, &repository, nil)
	require.NoError(err)
	resp, status := serviceHandler.PatchRepository(ctx, store.NullOrgId, "foo", patch)
	require.NotEqual(statusFailedCode, status.Code)
	return resp, repository, status
}
func TestRepositoryPatchName(t *testing.T) {
	require := require.New(t)
	var value interface{} = "bar"
	pr := api.PatchRequest{
		{Op: "replace", Path: "/metadata/name", Value: &value},
	}
	_, _, status := testRepositoryPatch(require, pr)
	verifyRepoPatchFailed(require, status)

	pr = api.PatchRequest{
		{Op: "remove", Path: "/metadata/name"},
	}
	_, _, status = testRepositoryPatch(require, pr)
	verifyRepoPatchFailed(require, status)
}

func TestRepositoryPatchKind(t *testing.T) {
	require := require.New(t)
	var value interface{} = "bar"
	pr := api.PatchRequest{
		{Op: "replace", Path: "/kind", Value: &value},
	}
	_, _, status := testRepositoryPatch(require, pr)
	verifyRepoPatchFailed(require, status)

	pr = api.PatchRequest{
		{Op: "remove", Path: "/kind"},
	}
	_, _, status = testRepositoryPatch(require, pr)
	verifyRepoPatchFailed(require, status)
}

func TestRepositoryPatchAPIVersion(t *testing.T) {
	require := require.New(t)
	var value interface{} = "bar"
	pr := api.PatchRequest{
		{Op: "replace", Path: "/apiVersion", Value: &value},
	}
	_, _, status := testRepositoryPatch(require, pr)
	verifyRepoPatchFailed(require, status)

	pr = api.PatchRequest{
		{Op: "remove", Path: "/apiVersion"},
	}
	_, _, status = testRepositoryPatch(require, pr)
	verifyRepoPatchFailed(require, status)
}

func TestRepositoryPatchSpec(t *testing.T) {
	require := require.New(t)
	pr := api.PatchRequest{
		{Op: "remove", Path: "/spec"},
	}
	_, _, status := testRepositoryPatch(require, pr)
	verifyRepoPatchFailed(require, status)
}

func TestRepositoryPatchStatus(t *testing.T) {
	require := require.New(t)
	var value interface{} = "1234"
	pr := api.PatchRequest{
		{Op: "replace", Path: "/status/updatedAt", Value: &value},
	}
	_, _, status := testRepositoryPatch(require, pr)
	verifyRepoPatchFailed(require, status)

	pr = api.PatchRequest{
		{Op: "replace", Path: "/status/updatedAt"},
	}
	_, _, status = testRepositoryPatch(require, pr)
	verifyRepoPatchFailed(require, status)
}

func TestRepositoryPatchNonExistingPath(t *testing.T) {
	require := require.New(t)
	var value interface{} = "foo"
	pr := api.PatchRequest{
		{Op: "replace", Path: "/spec/os/doesnotexist", Value: &value},
	}
	_, _, status := testRepositoryPatch(require, pr)
	verifyRepoPatchFailed(require, status)

	pr = api.PatchRequest{
		{Op: "remove", Path: "/spec/os/doesnotexist"},
	}
	_, _, status = testRepositoryPatch(require, pr)
	verifyRepoPatchFailed(require, status)
}

func TestRepositoryPatchLabels(t *testing.T) {
	require := require.New(t)
	addLabels := map[string]string{"labelKey": "labelValue1"}
	var value interface{} = "labelValue1"
	pr := api.PatchRequest{
		{Op: "replace", Path: "/metadata/labels/labelKey", Value: &value},
	}

	resp, orig, status := testRepositoryPatch(require, pr)
	orig.Metadata.Labels = &addLabels
	require.Equal(statusSuccessCode, status.Code)
	require.Equal(orig, *resp)

	pr = api.PatchRequest{
		{Op: "remove", Path: "/metadata/labels/labelKey"},
	}

	resp, orig, status = testRepositoryPatch(require, pr)
	orig.Metadata.Labels = &map[string]string{}
	require.Equal(statusSuccessCode, status.Code)
	require.Equal(orig, *resp)
}

func TestRepositoryNonExistingResource(t *testing.T) {
	require := require.New(t)
	var value interface{} = "labelValue1"
	pr := api.PatchRequest{
		{Op: "replace", Path: "/metadata/labels/labelKey", Value: &value},
	}

	ts := &TestStore{}
	wc := &DummyWorkerClient{}
	serviceHandler := ServiceHandler{
		eventHandler: NewEventHandler(ts, wc, logrus.New()),
		store:        ts,
		workerClient: wc,
		log:          logrus.New(),
	}
	ctx := context.Background()
	_, err := serviceHandler.store.Repository().Create(ctx, store.NullOrgId, &api.Repository{
		Metadata: api.ObjectMeta{Name: lo.ToPtr("foo")},
	}, nil)
	require.NoError(err)
	_, status := serviceHandler.PatchRepository(ctx, store.NullOrgId, "bar", pr)
	require.Equal(statusNotFoundCode, status.Code)
	event, _ := serviceHandler.store.Event().List(context.Background(), store.NullOrgId, store.ListParams{})
	require.Len(event.Items, 0)
}

func createRepository(ctx context.Context, r store.Repository, orgId uuid.UUID, name string, labels *map[string]string) error {
	spec := api.RepositorySpec{}
	err := spec.FromGenericRepoSpec(api.GenericRepoSpec{
		Url: "myrepourl",
	})
	if err != nil {
		return err
	}
	resource := api.Repository{
		Metadata: api.ObjectMeta{
			Name:   lo.ToPtr(name),
			Labels: labels,
		},
		Spec: spec,
	}

	callback := store.EventCallback(func(context.Context, api.ResourceKind, uuid.UUID, string, interface{}, interface{}, bool, error) {})
	_, err = r.Create(ctx, orgId, &resource, callback)
	return err
}

func setAccessCondition(ctx context.Context, orgId uuid.UUID, repository *api.Repository, err error, h ServiceHandler) error {
	if repository.Status == nil {
		repository.Status = &api.RepositoryStatus{Conditions: []api.Condition{}}
	}
	if repository.Status.Conditions == nil {
		repository.Status.Conditions = []api.Condition{}
	}
	_, status := h.ReplaceRepositoryStatusByError(ctx, orgId, lo.FromPtr(repository.Metadata.Name), *repository, err)

	return ApiStatusToErr(status)
}

func TestRepoTester_SetAccessCondition(t *testing.T) {
	require := require.New(t)

	ts := &TestStore{}
	wc := &DummyWorkerClient{}
	serviceHandler := ServiceHandler{
		eventHandler: NewEventHandler(ts, wc, logrus.New()),
		store:        ts,
		workerClient: wc,
		log:          logrus.New(),
	}
	r := serviceHandler.store.Repository()
	ctx := context.Background()
	orgId := store.NullOrgId

	err := createRepository(ctx, r, orgId, "nil-to-ok", &map[string]string{"status": "OK"})
	require.NoError(err)

	err = createRepository(ctx, r, orgId, "ok-to-ok", &map[string]string{"status": "OK"})
	require.NoError(err)
	repo, err := r.Get(ctx, orgId, "ok-to-ok")
	require.NoError(err)

	err = setAccessCondition(ctx, orgId, repo, err, serviceHandler)
	require.NoError(err)
}
