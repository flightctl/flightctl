package resourcesync

import (
	"context"
	"testing"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/service/events"
	"github.com/flightctl/flightctl/internal/store"
	catalogstore "github.com/flightctl/flightctl/internal/store/catalog"
	fleetstore "github.com/flightctl/flightctl/internal/store/fleet"
	resourcesyncstore "github.com/flightctl/flightctl/internal/store/resourcesync"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

const (
	statusSuccessCode    = int32(200)
	statusCreatedCode    = int32(201)
	statusBadRequestCode = int32(400)
	statusNotFoundCode   = int32(404)
)

// fakeResourceSyncStore is a small in-memory implementation of internal/store/resourcesync.Store.
type fakeResourceSyncStore struct {
	items map[string]*domain.ResourceSync
}

func newFakeResourceSyncStore() *fakeResourceSyncStore {
	return &fakeResourceSyncStore{items: map[string]*domain.ResourceSync{}}
}

func (f *fakeResourceSyncStore) InitialMigration(ctx context.Context) error { return nil }

func (f *fakeResourceSyncStore) Create(ctx context.Context, orgId uuid.UUID, rs *domain.ResourceSync, callbackEvent store.EventCallback) (*domain.ResourceSync, error) {
	name := lo.FromPtr(rs.Metadata.Name)
	if _, exists := f.items[name]; exists {
		return nil, flterrors.ErrDuplicateName
	}
	f.items[name] = rs
	if callbackEvent != nil {
		callbackEvent(ctx, domain.ResourceSyncKind, orgId, name, nil, rs, true, nil)
	}
	return rs, nil
}

func (f *fakeResourceSyncStore) Update(ctx context.Context, orgId uuid.UUID, rs *domain.ResourceSync, callbackEvent store.EventCallback) (*domain.ResourceSync, error) {
	name := lo.FromPtr(rs.Metadata.Name)
	old, exists := f.items[name]
	if !exists {
		return nil, flterrors.ErrResourceNotFound
	}
	f.items[name] = rs
	if callbackEvent != nil {
		callbackEvent(ctx, domain.ResourceSyncKind, orgId, name, old, rs, false, nil)
	}
	return rs, nil
}

func (f *fakeResourceSyncStore) CreateOrUpdate(ctx context.Context, orgId uuid.UUID, rs *domain.ResourceSync, callbackEvent store.EventCallback) (*domain.ResourceSync, bool, error) {
	name := lo.FromPtr(rs.Metadata.Name)
	if _, exists := f.items[name]; exists {
		result, err := f.Update(ctx, orgId, rs, callbackEvent)
		return result, false, err
	}
	result, err := f.Create(ctx, orgId, rs, callbackEvent)
	return result, true, err
}

func (f *fakeResourceSyncStore) Get(ctx context.Context, orgId uuid.UUID, name string) (*domain.ResourceSync, error) {
	rs, ok := f.items[name]
	if !ok {
		return nil, flterrors.ErrResourceNotFound
	}
	return rs, nil
}

func (f *fakeResourceSyncStore) List(ctx context.Context, orgId uuid.UUID, listParams store.ListParams) (*domain.ResourceSyncList, error) {
	var items []domain.ResourceSync
	for _, rs := range f.items {
		items = append(items, *rs)
	}
	return &domain.ResourceSyncList{Items: items}, nil
}

func (f *fakeResourceSyncStore) Delete(ctx context.Context, orgId uuid.UUID, name string, callback store.RemoveOwnerCallback, callbackEvent store.EventCallback) error {
	if _, exists := f.items[name]; !exists {
		return nil
	}
	delete(f.items, name)
	if callback != nil {
		owner := "ResourceSync/" + name
		if err := callback(ctx, nil, orgId, owner); err != nil {
			return err
		}
	}
	if callbackEvent != nil {
		callbackEvent(ctx, domain.ResourceSyncKind, orgId, name, nil, nil, false, nil)
	}
	return nil
}

func (f *fakeResourceSyncStore) UpdateStatus(ctx context.Context, orgId uuid.UUID, resource *domain.ResourceSync, eventCallback store.EventCallback) (*domain.ResourceSync, error) {
	name := lo.FromPtr(resource.Metadata.Name)
	existing, ok := f.items[name]
	if !ok {
		return nil, flterrors.ErrResourceNotFound
	}
	existing.Status = resource.Status
	return existing, nil
}

func (f *fakeResourceSyncStore) Count(ctx context.Context, orgId uuid.UUID, listParams store.ListParams) (int64, error) {
	return int64(len(f.items)), nil
}

func (f *fakeResourceSyncStore) CountByOrgAndStatus(ctx context.Context, orgId *uuid.UUID, status *string) ([]resourcesyncstore.CountByResourceSyncOrgAndStatusResult, error) {
	return nil, nil
}

// fakeCatalogStore embeds catalogstore.Store (nil) and overrides only the 2 methods
// DeleteResourceSync's ownership-cleanup callback actually calls.
type fakeCatalogStore struct {
	catalogstore.Store
	unsetItemOwnerCalls []string
	unsetOwnerCalls     []string
}

func (f *fakeCatalogStore) UnsetItemOwner(ctx context.Context, tx *gorm.DB, orgId uuid.UUID, owner string) error {
	f.unsetItemOwnerCalls = append(f.unsetItemOwnerCalls, owner)
	return nil
}

func (f *fakeCatalogStore) UnsetOwner(ctx context.Context, tx *gorm.DB, orgId uuid.UUID, owner string) error {
	f.unsetOwnerCalls = append(f.unsetOwnerCalls, owner)
	return nil
}

// fakeFleetStore embeds fleetstore.Store (nil) and overrides only UnsetOwner.
type fakeFleetStore struct {
	fleetstore.Store
	unsetOwnerCalls []string
}

func (f *fakeFleetStore) UnsetOwner(ctx context.Context, tx *gorm.DB, orgId uuid.UUID, owner string) error {
	f.unsetOwnerCalls = append(f.unsetOwnerCalls, owner)
	return nil
}

// fakeEventsService is a recording fake for events.Service. ResourceSync's own event
// decision logic (in handler.go's callbackResourceSyncUpdated) now calls CreateEvent
// directly, so tests assert on the actual emitted events rather than intercepting a
// resource-specific callback.
type fakeEventsService struct {
	events.Service
	created []*domain.Event
	deleted []recordedCallback
}

type recordedCallback struct {
	orgId   uuid.UUID
	name    string
	created bool
	err     error
}

func (f *fakeEventsService) CreateEvent(ctx context.Context, orgId uuid.UUID, event *domain.Event) {
	if event == nil {
		return
	}
	f.created = append(f.created, event)
}

func (f *fakeEventsService) HandleGenericResourceDeletedEvents(ctx context.Context, resourceKind domain.ResourceKind, orgId uuid.UUID, name string, oldResource, newResource interface{}, created bool, err error) {
	f.deleted = append(f.deleted, recordedCallback{orgId: orgId, name: name, created: created, err: err})
}

func newTestHandler() (*ServiceHandler, *fakeResourceSyncStore, *fakeCatalogStore, *fakeFleetStore, *fakeEventsService) {
	rsStore := newFakeResourceSyncStore()
	catStore := &fakeCatalogStore{}
	flStore := &fakeFleetStore{}
	evStore := &fakeEventsService{}
	return NewServiceHandler(rsStore, catStore, flStore, evStore, logrus.New()), rsStore, catStore, flStore, evStore
}

func testResourceSync(name string) domain.ResourceSync {
	return domain.ResourceSync{
		ApiVersion: "v1beta1",
		Kind:       "ResourceSync",
		Metadata: domain.ObjectMeta{
			Name:   lo.ToPtr(name),
			Labels: &map[string]string{"labelKey": "labelValue"},
		},
		Spec: domain.ResourceSyncSpec{
			Repository:     "repo",
			TargetRevision: "main",
			Path:           "/foo",
		},
	}
}

func TestCreateResourceSync(t *testing.T) {
	t.Run("When the resource is valid it should create it and fire an updated callback", func(t *testing.T) {
		h, fakeStore, _, _, fakeEvents := newTestHandler()
		rs := testResourceSync("foo")

		result, status := h.CreateResourceSync(context.Background(), uuid.New(), rs)
		require.Equal(t, statusCreatedCode, status.Code)
		require.NotNil(t, result)
		require.Contains(t, fakeStore.items, "foo")
		require.Len(t, fakeEvents.created, 1)
		require.Equal(t, domain.EventReasonResourceCreated, fakeEvents.created[0].Reason)
	})

	t.Run("When names are very long it should still create successfully", func(t *testing.T) {
		h, _, _, _, _ := newTestHandler()
		longName := "01234567890123456789012345678901234567890123456789012345678901234567890123456789"
		rs := domain.ResourceSync{
			ApiVersion: "v1beta1",
			Kind:       "ResourceSync",
			Metadata:   domain.ObjectMeta{Name: lo.ToPtr(longName), Labels: &map[string]string{"labelKey": "labelValue"}},
			Spec: domain.ResourceSyncSpec{
				Repository:     longName,
				TargetRevision: "main",
				Path:           "/foo",
			},
		}
		_, status := h.CreateResourceSync(context.Background(), uuid.New(), rs)
		require.Equal(t, statusCreatedCode, status.Code)
	})

	t.Run("When managed metadata fields are set by the caller CreateResourceSyncFromUntrusted should clear them before creation", func(t *testing.T) {
		h, fakeStore, _, _, _ := newTestHandler()
		rs := testResourceSync("untrusted-rs")
		rs.Metadata.Owner = lo.ToPtr("someone")
		rs.Metadata.Generation = lo.ToPtr(int64(5))

		_, status := CreateResourceSyncFromUntrusted(context.Background(), h, uuid.New(), rs)
		require.Equal(t, statusCreatedCode, status.Code)
		require.Nil(t, fakeStore.items["untrusted-rs"].Metadata.Owner)
		require.Nil(t, fakeStore.items["untrusted-rs"].Metadata.Generation)
	})

	t.Run("When managed metadata fields are set by the caller CreateResourceSync (trusted) should preserve them", func(t *testing.T) {
		h, fakeStore, _, _, _ := newTestHandler()
		rs := testResourceSync("trusted-rs")
		rs.Metadata.Owner = lo.ToPtr("someone")
		rs.Metadata.Generation = lo.ToPtr(int64(5))

		_, status := h.CreateResourceSync(context.Background(), uuid.New(), rs)
		require.Equal(t, statusCreatedCode, status.Code)
		require.Equal(t, "someone", lo.FromPtr(fakeStore.items["trusted-rs"].Metadata.Owner))
		require.Equal(t, int64(5), lo.FromPtr(fakeStore.items["trusted-rs"].Metadata.Generation))
	})
}

func TestListResourceSyncs(t *testing.T) {
	h, fakeStore, _, _, _ := newTestHandler()
	fakeStore.items["foo"] = lo.ToPtr(testResourceSync("foo"))

	result, status := h.ListResourceSyncs(context.Background(), uuid.New(), domain.ListResourceSyncsParams{})
	require.Equal(t, domain.StatusOK(), status)
	require.Len(t, result.Items, 1)
}

func TestGetResourceSync(t *testing.T) {
	h, fakeStore, _, _, _ := newTestHandler()
	fakeStore.items["foo"] = lo.ToPtr(testResourceSync("foo"))

	result, status := h.GetResourceSync(context.Background(), uuid.New(), "foo")
	require.Equal(t, statusSuccessCode, status.Code)
	require.Equal(t, "foo", lo.FromPtr(result.Metadata.Name))

	_, status = h.GetResourceSync(context.Background(), uuid.New(), "missing")
	require.Equal(t, statusNotFoundCode, status.Code)
}

func TestReplaceResourceSync(t *testing.T) {
	t.Run("When it allows updating the repository field", func(t *testing.T) {
		h, _, _, _, _ := newTestHandler()
		ctx := context.Background()
		orgId := uuid.New()

		rs := testResourceSync("catalog-mixed-sync")
		created, status := h.CreateResourceSync(ctx, orgId, rs)
		require.Equal(t, statusCreatedCode, status.Code)

		created.Status = &domain.ResourceSyncStatus{Conditions: []domain.Condition{}}
		_, status = h.ReplaceResourceSyncStatus(ctx, orgId, "catalog-mixed-sync", *created)
		require.Equal(t, statusSuccessCode, status.Code)

		rs.Spec.Repository = "updated-repo"
		replaced, status := h.ReplaceResourceSync(ctx, orgId, "catalog-mixed-sync", rs)
		require.Equal(t, statusSuccessCode, status.Code)
		require.Equal(t, "updated-repo", replaced.Spec.Repository)
	})

	t.Run("When managed metadata fields are set by the caller ReplaceResourceSyncFromUntrusted should clear them before replacing", func(t *testing.T) {
		h, fakeStore, _, _, _ := newTestHandler()
		orgId := uuid.New()
		rs := testResourceSync("replace-untrusted")
		rs.Metadata.Owner = lo.ToPtr("someone")
		rs.Metadata.Generation = lo.ToPtr(int64(5))

		_, status := ReplaceResourceSyncFromUntrusted(context.Background(), h, orgId, "replace-untrusted", rs)
		require.Equal(t, statusCreatedCode, status.Code)
		require.Nil(t, fakeStore.items["replace-untrusted"].Metadata.Owner)
		require.Nil(t, fakeStore.items["replace-untrusted"].Metadata.Generation)
	})

	t.Run("When managed metadata fields are set by the caller ReplaceResourceSync (trusted) should preserve them", func(t *testing.T) {
		h, fakeStore, _, _, _ := newTestHandler()
		orgId := uuid.New()
		rs := testResourceSync("replace-trusted")
		rs.Metadata.Owner = lo.ToPtr("someone")
		rs.Metadata.Generation = lo.ToPtr(int64(5))

		_, status := h.ReplaceResourceSync(context.Background(), orgId, "replace-trusted", rs)
		require.Equal(t, statusCreatedCode, status.Code)
		require.Equal(t, "someone", lo.FromPtr(fakeStore.items["replace-trusted"].Metadata.Owner))
		require.Equal(t, int64(5), lo.FromPtr(fakeStore.items["replace-trusted"].Metadata.Generation))
	})
}

func TestPatchResourceSync(t *testing.T) {
	setup := func() (*ServiceHandler, uuid.UUID) {
		h, _, _, _, _ := newTestHandler()
		ctx := context.Background()
		orgId := uuid.New()
		rs := testResourceSync("foo")
		_, status := h.CreateResourceSync(ctx, orgId, rs)
		require.Equal(t, statusCreatedCode, status.Code)
		return h, orgId
	}

	t.Run("When the patch attempts to change metadata.name it should fail", func(t *testing.T) {
		h, orgId := setup()
		var value interface{} = "bar"
		patch := domain.PatchRequest{{Op: "replace", Path: "/metadata/name", Value: &value}}
		_, status := h.PatchResourceSync(context.Background(), orgId, "foo", patch)
		require.Equal(t, statusBadRequestCode, status.Code)
	})

	t.Run("When the patch attempts to change kind it should fail", func(t *testing.T) {
		h, orgId := setup()
		var value interface{} = "bar"
		patch := domain.PatchRequest{{Op: "replace", Path: "/kind", Value: &value}}
		_, status := h.PatchResourceSync(context.Background(), orgId, "foo", patch)
		require.Equal(t, statusBadRequestCode, status.Code)
	})

	t.Run("When the patch attempts to remove spec it should fail", func(t *testing.T) {
		h, orgId := setup()
		patch := domain.PatchRequest{{Op: "remove", Path: "/spec"}}
		_, status := h.PatchResourceSync(context.Background(), orgId, "foo", patch)
		require.Equal(t, statusBadRequestCode, status.Code)
	})

	t.Run("When the patch targets a nonexistent path it should fail", func(t *testing.T) {
		h, orgId := setup()
		var value interface{} = "foo"
		patch := domain.PatchRequest{{Op: "replace", Path: "/spec/os/doesnotexist", Value: &value}}
		_, status := h.PatchResourceSync(context.Background(), orgId, "foo", patch)
		require.Equal(t, statusBadRequestCode, status.Code)
	})

	t.Run("When the patch replaces spec.repository it should succeed", func(t *testing.T) {
		h, orgId := setup()
		var value interface{} = "bar"
		patch := domain.PatchRequest{{Op: "replace", Path: "/spec/repository", Value: &value}}
		result, status := h.PatchResourceSync(context.Background(), orgId, "foo", patch)
		require.Equal(t, statusSuccessCode, status.Code)
		require.Equal(t, "bar", result.Spec.Repository)
	})

	t.Run("When the patch replaces metadata.labels it should succeed", func(t *testing.T) {
		h, orgId := setup()
		var value interface{} = map[string]string{"labelKey": "labelValue1"}
		patch := domain.PatchRequest{{Op: "replace", Path: "/metadata/labels", Value: &value}}
		result, status := h.PatchResourceSync(context.Background(), orgId, "foo", patch)
		require.Equal(t, statusSuccessCode, status.Code)
		require.Equal(t, map[string]string{"labelKey": "labelValue1"}, *result.Metadata.Labels)
	})

	t.Run("When the resource does not exist it should return a not-found status", func(t *testing.T) {
		h, orgId := setup()
		var value interface{} = "labelValue1"
		patch := domain.PatchRequest{{Op: "replace", Path: "/metadata/labels/labelKey", Value: &value}}
		_, status := h.PatchResourceSync(context.Background(), orgId, "bar", patch)
		require.Equal(t, statusNotFoundCode, status.Code)
	})
}

func TestDeleteResourceSync(t *testing.T) {
	t.Run("When deleting a resource sync it should unset ownership on Catalog and Fleet and fire a deleted callback", func(t *testing.T) {
		h, fakeStore, fakeCatalog, fakeFleet, fakeEvents := newTestHandler()
		orgId := uuid.New()
		rs := testResourceSync("foo")
		fakeStore.items["foo"] = &rs

		status := h.DeleteResourceSync(context.Background(), orgId, "foo")
		require.Equal(t, statusSuccessCode, status.Code)

		require.Len(t, fakeCatalog.unsetItemOwnerCalls, 1)
		require.Equal(t, "ResourceSync/foo", fakeCatalog.unsetItemOwnerCalls[0])
		require.Len(t, fakeCatalog.unsetOwnerCalls, 1)
		require.Equal(t, "ResourceSync/foo", fakeCatalog.unsetOwnerCalls[0])
		require.Len(t, fakeFleet.unsetOwnerCalls, 1)
		require.Equal(t, "ResourceSync/foo", fakeFleet.unsetOwnerCalls[0])
		require.Len(t, fakeEvents.deleted, 1)
	})
}

func TestReplaceResourceSyncStatus(t *testing.T) {
	h, fakeStore, _, _, _ := newTestHandler()
	orgId := uuid.New()
	rs := testResourceSync("foo")
	fakeStore.items["foo"] = &rs

	newRs := rs
	newRs.Status = &domain.ResourceSyncStatus{Conditions: []domain.Condition{{Type: domain.ConditionTypeResourceSyncSynced, Status: domain.ConditionStatusTrue}}}

	result, status := h.ReplaceResourceSyncStatus(context.Background(), orgId, "foo", newRs)
	require.Equal(t, statusSuccessCode, status.Code)
	require.NotNil(t, result.Status)
}
