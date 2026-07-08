package templateversion

import (
	"context"
	"fmt"
	"testing"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/kvstore"
	"github.com/flightctl/flightctl/internal/service/events"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

const (
	statusSuccessCode    = int32(200)
	statusCreatedCode    = int32(201)
	statusBadRequestCode = int32(400)
	statusNotFoundCode   = int32(404)
)

type tvKey struct {
	fleet string
	name  string
}

// fakeTemplateVersionStore is a small in-memory implementation of
// internal/store/templateversion.Store.
type fakeTemplateVersionStore struct {
	items map[tvKey]*domain.TemplateVersion
}

func newFakeTemplateVersionStore() *fakeTemplateVersionStore {
	return &fakeTemplateVersionStore{items: map[tvKey]*domain.TemplateVersion{}}
}

func (f *fakeTemplateVersionStore) InitialMigration(ctx context.Context) error { return nil }

func (f *fakeTemplateVersionStore) Create(ctx context.Context, orgId uuid.UUID, tv *domain.TemplateVersion, eventCallback store.EventCallback) (*domain.TemplateVersion, error) {
	_, fleet, _ := util.GetResourceOwner(tv.Metadata.Owner)
	key := tvKey{fleet: fleet, name: lo.FromPtr(tv.Metadata.Name)}
	if _, exists := f.items[key]; exists {
		return nil, flterrors.ErrDuplicateName
	}
	f.items[key] = tv
	if eventCallback != nil {
		eventCallback(ctx, domain.TemplateVersionKind, orgId, key.name, nil, tv, true, nil)
	}
	return tv, nil
}

func (f *fakeTemplateVersionStore) Get(ctx context.Context, orgId uuid.UUID, fleet string, name string) (*domain.TemplateVersion, error) {
	tv, ok := f.items[tvKey{fleet: fleet, name: name}]
	if !ok {
		return nil, flterrors.ErrResourceNotFound
	}
	return tv, nil
}

func (f *fakeTemplateVersionStore) List(ctx context.Context, orgId uuid.UUID, listParams store.ListParams) (*domain.TemplateVersionList, error) {
	var items []domain.TemplateVersion
	for _, tv := range f.items {
		items = append(items, *tv)
	}
	return &domain.TemplateVersionList{Items: items}, nil
}

func (f *fakeTemplateVersionStore) Delete(ctx context.Context, orgId uuid.UUID, fleet string, name string, eventCallback store.EventCallback) (bool, error) {
	key := tvKey{fleet: fleet, name: name}
	if _, exists := f.items[key]; !exists {
		return false, nil
	}
	delete(f.items, key)
	if eventCallback != nil {
		eventCallback(ctx, domain.TemplateVersionKind, orgId, name, nil, nil, false, nil)
	}
	return true, nil
}

func (f *fakeTemplateVersionStore) GetLatest(ctx context.Context, orgId uuid.UUID, fleet string) (*domain.TemplateVersion, error) {
	var latest *domain.TemplateVersion
	for key, tv := range f.items {
		if key.fleet != fleet {
			continue
		}
		latest = tv
	}
	if latest == nil {
		return nil, flterrors.ErrResourceNotFound
	}
	return latest, nil
}

func (f *fakeTemplateVersionStore) UpdateStatus(ctx context.Context, orgId uuid.UUID, resource *domain.TemplateVersion, valid *bool) error {
	_, fleet, _ := util.GetResourceOwner(resource.Metadata.Owner)
	key := tvKey{fleet: fleet, name: lo.FromPtr(resource.Metadata.Name)}
	existing, ok := f.items[key]
	if !ok {
		return flterrors.ErrResourceNotFound
	}
	existing.Status = resource.Status
	return nil
}

// fakeKVStore embeds kvstore.KVStore (nil) and overrides only DeleteKeysForTemplateVersion,
// the sole method DeleteTemplateVersion calls.
type fakeKVStore struct {
	kvstore.KVStore
	deletedKeys []string
	deleteErr   error
}

func (f *fakeKVStore) DeleteKeysForTemplateVersion(ctx context.Context, key string) error {
	f.deletedKeys = append(f.deletedKeys, key)
	return f.deleteErr
}

// fakeEventsService is a recording fake for events.Service.
type fakeEventsService struct {
	events.Service
	rolloutStartedCalls []string
	updated             []string
	deleted             []string
}

func (f *fakeEventsService) EmitFleetRolloutStartedEvent(ctx context.Context, orgId uuid.UUID, templateVersionName string, fleetName string, immediateRollout bool) {
	f.rolloutStartedCalls = append(f.rolloutStartedCalls, fmt.Sprintf("%s/%s", fleetName, templateVersionName))
}

func (f *fakeEventsService) HandleTemplateVersionUpdatedEvents(ctx context.Context, resourceKind domain.ResourceKind, orgId uuid.UUID, name string, oldResource, newResource interface{}, created bool, err error) {
	f.updated = append(f.updated, name)
}

func (f *fakeEventsService) HandleGenericResourceDeletedEvents(ctx context.Context, resourceKind domain.ResourceKind, orgId uuid.UUID, name string, oldResource, newResource interface{}, created bool, err error) {
	f.deleted = append(f.deleted, name)
}

func newTestHandler() (*ServiceHandler, *fakeTemplateVersionStore, *fakeKVStore, *fakeEventsService) {
	tvStore := newFakeTemplateVersionStore()
	kv := &fakeKVStore{}
	ev := &fakeEventsService{}
	logger := logrus.New()
	return NewServiceHandler(tvStore, kv, ev, logger), tvStore, kv, ev
}

func testTemplateVersion(fleet, name string) domain.TemplateVersion {
	return domain.TemplateVersion{
		ApiVersion: "v1beta1",
		Kind:       "TemplateVersion",
		Metadata: domain.ObjectMeta{
			Name:  lo.ToPtr(name),
			Owner: util.SetResourceOwner(domain.FleetKind, fleet),
		},
		Spec: domain.TemplateVersionSpec{
			Fleet: fleet,
		},
	}
}

func TestCreateTemplateVersion(t *testing.T) {
	t.Run("When the resource is valid it should create it and emit a fleet rollout started event", func(t *testing.T) {
		h, fakeStore, _, fakeEvents := newTestHandler()
		tv := testTemplateVersion("myfleet", "v1")

		result, status := h.CreateTemplateVersion(context.Background(), uuid.New(), tv, true)
		require.Equal(t, statusCreatedCode, status.Code)
		require.NotNil(t, result)
		require.Contains(t, fakeStore.items, tvKey{fleet: "myfleet", name: "v1"})
		require.Equal(t, []string{"myfleet/v1"}, fakeEvents.rolloutStartedCalls)
	})

	t.Run("When metadata.owner and spec.fleet disagree it should return bad request", func(t *testing.T) {
		h, _, _, _ := newTestHandler()
		tv := testTemplateVersion("myfleet", "v1")
		tv.Spec.Fleet = "otherfleet"

		_, status := h.CreateTemplateVersion(context.Background(), uuid.New(), tv, false)
		require.Equal(t, statusBadRequestCode, status.Code)
	})
}

func TestListTemplateVersions(t *testing.T) {
	h, fakeStore, _, _ := newTestHandler()
	tv := testTemplateVersion("myfleet", "v1")
	fakeStore.items[tvKey{fleet: "myfleet", name: "v1"}] = &tv

	result, status := h.ListTemplateVersions(context.Background(), uuid.New(), "myfleet", domain.ListTemplateVersionsParams{})
	require.Equal(t, statusSuccessCode, status.Code)
	require.Len(t, result.Items, 1)
}

func TestListTemplateVersionsInvalidSelector(t *testing.T) {
	h, _, _, _ := newTestHandler()
	badSelector := "this is not=a valid selector!!"
	_, status := h.ListTemplateVersions(context.Background(), uuid.New(), "myfleet", domain.ListTemplateVersionsParams{FieldSelector: &badSelector})
	require.Equal(t, statusBadRequestCode, status.Code)
}

func TestGetTemplateVersion(t *testing.T) {
	h, fakeStore, _, _ := newTestHandler()
	tv := testTemplateVersion("myfleet", "v1")
	fakeStore.items[tvKey{fleet: "myfleet", name: "v1"}] = &tv

	result, status := h.GetTemplateVersion(context.Background(), uuid.New(), "myfleet", "v1")
	require.Equal(t, statusSuccessCode, status.Code)
	require.Equal(t, "v1", lo.FromPtr(result.Metadata.Name))

	_, status = h.GetTemplateVersion(context.Background(), uuid.New(), "myfleet", "missing")
	require.Equal(t, statusNotFoundCode, status.Code)
}

func TestGetLatestTemplateVersion(t *testing.T) {
	h, fakeStore, _, _ := newTestHandler()
	tv := testTemplateVersion("myfleet", "v1")
	fakeStore.items[tvKey{fleet: "myfleet", name: "v1"}] = &tv

	result, status := h.GetLatestTemplateVersion(context.Background(), uuid.New(), "myfleet")
	require.Equal(t, statusSuccessCode, status.Code)
	require.Equal(t, "v1", lo.FromPtr(result.Metadata.Name))

	_, status = h.GetLatestTemplateVersion(context.Background(), uuid.New(), "unknownfleet")
	require.Equal(t, statusNotFoundCode, status.Code)
}

func TestDeleteTemplateVersion(t *testing.T) {
	t.Run("When deleting an existing template version it should remove it and fire a deleted callback", func(t *testing.T) {
		h, fakeStore, fakeKV, fakeEvents := newTestHandler()
		tv := testTemplateVersion("myfleet", "v1")
		fakeStore.items[tvKey{fleet: "myfleet", name: "v1"}] = &tv

		status := h.DeleteTemplateVersion(context.Background(), uuid.New(), "myfleet", "v1")
		require.Equal(t, statusSuccessCode, status.Code)
		require.NotContains(t, fakeStore.items, tvKey{fleet: "myfleet", name: "v1"})
		require.Len(t, fakeKV.deletedKeys, 1)
		require.Len(t, fakeEvents.deleted, 1)
	})

	t.Run("When the KV store delete fails it should still delete from the store", func(t *testing.T) {
		h, fakeStore, fakeKV, _ := newTestHandler()
		fakeKV.deleteErr = fmt.Errorf("kv unavailable")
		tv := testTemplateVersion("myfleet", "v1")
		fakeStore.items[tvKey{fleet: "myfleet", name: "v1"}] = &tv

		status := h.DeleteTemplateVersion(context.Background(), uuid.New(), "myfleet", "v1")
		require.Equal(t, statusSuccessCode, status.Code)
		require.NotContains(t, fakeStore.items, tvKey{fleet: "myfleet", name: "v1"})
	})
}
