package tasks

import (
	"context"
	"errors"
	"testing"
	"time"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/testutils"
	flightlog "github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/reqid"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
)

func TestRepoTester_SetAccessCondition(t *testing.T) {
	require := require.New(t)

	reqid.OverridePrefix(repotesterName)
	requestID := reqid.NextRequestID()
	ctx := context.Background()
	ctx = context.WithValue(ctx, middleware.RequestIDKey, requestID)
	ctx = context.WithValue(ctx, consts.EventActorCtxKey, repotesterName)
	log := flightlog.InitLogs()
	serviceHandler := testutils.NewTestServiceHandler(log)
	r := NewRepoTester(log, serviceHandler)

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
	repo, err := serviceHandler.Store.Repository().Create(ctx, store.NullOrgId, &repository, nil)
	require.NoError(err)

	err = r.SetAccessCondition(ctx, repo, nil)
	require.NoError(err)
	events, _ := serviceHandler.Store.Event().List(ctx, store.NullOrgId, store.ListParams{})
	require.NotEmpty(events.Items)
	require.Len(events.Items, 1)
	event := events.Items[0]
	require.Equal(event.Actor, repotesterName)
	require.Equal(event.InvolvedObject.Kind, api.RepositoryKind)
	require.Equal(event.InvolvedObject.Name, *repo.Metadata.Name)
	require.Equal(event.Message, "Repository is accessible")
	require.Equal(event.Reason, api.EventReasonRepositoryAccessible)
	require.Equal(event.Type, api.Normal)

	err = r.SetAccessCondition(ctx, repo, nil)
	require.NoError(err)
	events, _ = serviceHandler.Store.Event().List(ctx, store.NullOrgId, store.ListParams{})
	require.NotEmpty(events.Items)
	require.Len(events.Items, 1)
	event = events.Items[0]
	require.Equal(event.Actor, repotesterName)
	require.Equal(event.InvolvedObject.Kind, api.RepositoryKind)
	require.Equal(event.InvolvedObject.Name, *repo.Metadata.Name)
	require.Equal(event.Message, "Repository is accessible")
	require.Equal(event.Reason, api.EventReasonRepositoryAccessible)
	require.Equal(event.Type, api.Normal)

	err = r.SetAccessCondition(ctx, repo, errors.New("InternalServerError"))
	require.NoError(err)
	events, _ = serviceHandler.Store.Event().List(ctx, store.NullOrgId, store.ListParams{})
	require.NotEmpty(events.Items)
	require.Len(events.Items, 2)
	event = events.Items[1]
	require.Equal(event.Actor, repotesterName)
	require.Equal(event.InvolvedObject.Kind, api.RepositoryKind)
	require.Equal(event.InvolvedObject.Name, *repo.Metadata.Name)
	require.Contains(event.Message, "Repository is inaccessible")
	require.Equal(event.Reason, api.EventReasonRepositoryInaccessible)
	require.Equal(event.Type, api.Warning)
}

func createRepository(ctx context.Context, repostore store.Repository, orgId uuid.UUID, name string, labels *map[string]string) error {
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

	callback := store.RepositoryStoreCallback(func(context.Context, uuid.UUID, *api.Repository, *api.Repository) {})
	_, err = repostore.Create(ctx, orgId, &resource, callback)
	return err
}

func TestRepoTester_Run(t *testing.T) {
	require := require.New(t)

	reqid.OverridePrefix(repotesterName)
	requestID := reqid.NextRequestID()
	ctx := context.Background()
	ctx = context.WithValue(ctx, middleware.RequestIDKey, requestID)
	ctx = context.WithValue(ctx, consts.EventActorCtxKey, repotesterName)
	log := flightlog.InitLogs()
	serviceHandler := testutils.NewTestServiceHandler(log)

	err := createRepository(ctx, serviceHandler.Store.Repository(), store.NullOrgId, "nil-to-ok", &map[string]string{"status": "OK"})
	require.NoError(err)

	r := NewRepoTester(log, serviceHandler)
	r.TestRepositories(ctx)

	repo, err := serviceHandler.Store.Repository().Get(ctx, store.NullOrgId, "nil-to-ok")
	require.NoError(err)
	require.NotNil(repo.Status.Conditions)
	require.Len(repo.Status.Conditions, 1)
	require.Equal(repo.Status.Conditions[0].Type, api.ConditionTypeRepositoryAccessible)
	require.Equal(repo.Status.Conditions[0].Status, api.ConditionStatusTrue)
	require.NotEqual(repo.Status.Conditions[0].LastTransitionTime, time.Time{})
}
