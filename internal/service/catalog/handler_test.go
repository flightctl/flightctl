package catalog

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/service/events"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// fakeCatalogStore is a small in-memory implementation of internal/store/catalog.Store,
// adapted from the CRUD-over-a-slice / callback-invocation behavior of
// internal/service/teststore_framework_test.go's DummyCatalog (which cannot be imported
// directly since it lives in a _test.go file in a different package).
type fakeCatalogStore struct {
	catalogs map[string]*domain.Catalog
	items    map[string]*domain.CatalogItem // key: itemKey(catalogName, itemName)
	err      error
}

func newFakeCatalogStore() *fakeCatalogStore {
	return &fakeCatalogStore{catalogs: map[string]*domain.Catalog{}, items: map[string]*domain.CatalogItem{}}
}

func itemKey(catalogName, itemName string) string {
	return catalogName + "/" + itemName
}

func (f *fakeCatalogStore) InitialMigration(ctx context.Context) error { return f.err }

func (f *fakeCatalogStore) Create(ctx context.Context, orgId uuid.UUID, catalog *domain.Catalog, callbackEvent store.EventCallback) (*domain.Catalog, error) {
	if f.err != nil {
		return nil, f.err
	}
	name := lo.FromPtr(catalog.Metadata.Name)
	if _, exists := f.catalogs[name]; exists {
		return nil, flterrors.ErrDuplicateName
	}
	f.catalogs[name] = catalog
	if callbackEvent != nil {
		callbackEvent(ctx, domain.CatalogKind, orgId, name, nil, catalog, true, nil)
	}
	return catalog, nil
}

func (f *fakeCatalogStore) Update(ctx context.Context, orgId uuid.UUID, catalog *domain.Catalog, callbackEvent store.EventCallback) (*domain.Catalog, error) {
	if f.err != nil {
		return nil, f.err
	}
	name := lo.FromPtr(catalog.Metadata.Name)
	old, exists := f.catalogs[name]
	if !exists {
		return nil, flterrors.ErrResourceNotFound
	}
	f.catalogs[name] = catalog
	if callbackEvent != nil {
		callbackEvent(ctx, domain.CatalogKind, orgId, name, old, catalog, false, nil)
	}
	return catalog, nil
}

func (f *fakeCatalogStore) CreateOrUpdate(ctx context.Context, orgId uuid.UUID, catalog *domain.Catalog, fromAPI bool, callbackEvent store.EventCallback) (*domain.Catalog, bool, error) {
	name := lo.FromPtr(catalog.Metadata.Name)
	if _, exists := f.catalogs[name]; exists {
		result, err := f.Update(ctx, orgId, catalog, callbackEvent)
		return result, false, err
	}
	result, err := f.Create(ctx, orgId, catalog, callbackEvent)
	return result, true, err
}

func (f *fakeCatalogStore) Get(ctx context.Context, orgId uuid.UUID, name string) (*domain.Catalog, error) {
	if f.err != nil {
		return nil, f.err
	}
	c, ok := f.catalogs[name]
	if !ok {
		return nil, flterrors.ErrResourceNotFound
	}
	return c, nil
}

func (f *fakeCatalogStore) List(ctx context.Context, orgId uuid.UUID, listParams store.ListParams) (*domain.CatalogList, error) {
	if f.err != nil {
		return nil, f.err
	}
	items := make([]domain.Catalog, 0, len(f.catalogs))
	for _, c := range f.catalogs {
		items = append(items, *c)
	}
	return &domain.CatalogList{Items: items}, nil
}

func (f *fakeCatalogStore) Delete(ctx context.Context, orgId uuid.UUID, name string, callback store.RemoveOwnerCallback, callbackEvent store.EventCallback) error {
	old, exists := f.catalogs[name]
	if !exists {
		return flterrors.ErrResourceNotFound
	}
	delete(f.catalogs, name)
	if callback != nil {
		_ = callback(ctx, nil, orgId, name)
	}
	if callbackEvent != nil {
		callbackEvent(ctx, domain.CatalogKind, orgId, name, old, nil, false, nil)
	}
	return nil
}

func (f *fakeCatalogStore) UpdateStatus(ctx context.Context, orgId uuid.UUID, resource *domain.Catalog, eventCallback store.EventCallback) (*domain.Catalog, error) {
	name := lo.FromPtr(resource.Metadata.Name)
	old, exists := f.catalogs[name]
	if !exists {
		return nil, flterrors.ErrResourceNotFound
	}
	f.catalogs[name] = resource
	if eventCallback != nil {
		eventCallback(ctx, domain.CatalogKind, orgId, name, old, resource, false, nil)
	}
	return resource, nil
}

func (f *fakeCatalogStore) Count(ctx context.Context, orgId uuid.UUID, listParams store.ListParams) (int64, error) {
	return int64(len(f.catalogs)), f.err
}

func (f *fakeCatalogStore) UnsetOwner(ctx context.Context, tx *gorm.DB, orgId uuid.UUID, owner string) error {
	return f.err
}

func (f *fakeCatalogStore) UnsetItemOwner(ctx context.Context, tx *gorm.DB, orgId uuid.UUID, owner string) error {
	return f.err
}

func (f *fakeCatalogStore) ListAllItems(ctx context.Context, orgId uuid.UUID, listParams store.ListParams) (*domain.CatalogItemList, error) {
	if f.err != nil {
		return nil, f.err
	}
	items := make([]domain.CatalogItem, 0, len(f.items))
	for _, it := range f.items {
		items = append(items, *it)
	}
	return &domain.CatalogItemList{Items: items}, nil
}

func (f *fakeCatalogStore) ListItems(ctx context.Context, orgId uuid.UUID, catalogName string, listParams store.ListParams) (*domain.CatalogItemList, error) {
	if _, ok := f.catalogs[catalogName]; !ok {
		return nil, flterrors.ErrParentResourceNotFound
	}
	items := make([]domain.CatalogItem, 0)
	for _, it := range f.items {
		if it.Metadata.Catalog == catalogName {
			items = append(items, *it)
		}
	}
	return &domain.CatalogItemList{Items: items}, nil
}

func (f *fakeCatalogStore) GetItem(ctx context.Context, orgId uuid.UUID, catalogName string, itemName string) (*domain.CatalogItem, error) {
	if _, ok := f.catalogs[catalogName]; !ok {
		return nil, flterrors.ErrParentResourceNotFound
	}
	it, ok := f.items[itemKey(catalogName, itemName)]
	if !ok {
		return nil, flterrors.ErrResourceNotFound
	}
	return it, nil
}

func (f *fakeCatalogStore) CreateItem(ctx context.Context, orgId uuid.UUID, catalogName string, item *domain.CatalogItem) (*domain.CatalogItem, error) {
	if _, ok := f.catalogs[catalogName]; !ok {
		return nil, flterrors.ErrParentResourceNotFound
	}
	item.Metadata.Catalog = catalogName
	f.items[itemKey(catalogName, lo.FromPtr(item.Metadata.Name))] = item
	return item, nil
}

func (f *fakeCatalogStore) UpdateItem(ctx context.Context, orgId uuid.UUID, catalogName string, item *domain.CatalogItem) (*domain.CatalogItem, error) {
	if _, ok := f.catalogs[catalogName]; !ok {
		return nil, flterrors.ErrParentResourceNotFound
	}
	key := itemKey(catalogName, lo.FromPtr(item.Metadata.Name))
	if _, ok := f.items[key]; !ok {
		return nil, flterrors.ErrResourceNotFound
	}
	item.Metadata.Catalog = catalogName
	f.items[key] = item
	return item, nil
}

func (f *fakeCatalogStore) CreateOrUpdateItem(ctx context.Context, orgId uuid.UUID, catalogName string, item *domain.CatalogItem) (*domain.CatalogItem, bool, error) {
	if _, ok := f.catalogs[catalogName]; !ok {
		return nil, false, flterrors.ErrParentResourceNotFound
	}
	key := itemKey(catalogName, lo.FromPtr(item.Metadata.Name))
	if _, ok := f.items[key]; ok {
		result, err := f.UpdateItem(ctx, orgId, catalogName, item)
		return result, false, err
	}
	result, err := f.CreateItem(ctx, orgId, catalogName, item)
	return result, true, err
}

func (f *fakeCatalogStore) DeleteItem(ctx context.Context, orgId uuid.UUID, catalogName string, itemName string) error {
	if _, ok := f.catalogs[catalogName]; !ok {
		return flterrors.ErrParentResourceNotFound
	}
	key := itemKey(catalogName, itemName)
	if _, ok := f.items[key]; !ok {
		return flterrors.ErrResourceNotFound
	}
	delete(f.items, key)
	return nil
}

// fakeEventsService is a recording fake for events.Service; embedding events.Service means
// only the 2 generic methods Catalog's own event logic calls into need overriding.
// Catalog-specific decisions now live in this package, so tests assert on the actual events
// recorded via CreateEvent rather than intercepting a resource-specific callback.
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

func newTestHandler() (*ServiceHandler, *fakeCatalogStore, *fakeEventsService) {
	fakeStore := newFakeCatalogStore()
	fakeEvents := &fakeEventsService{}
	return NewServiceHandler(fakeStore, fakeEvents, logrus.New()), fakeStore, fakeEvents
}

func createTestCatalog(name string, owner *string) domain.Catalog {
	return domain.Catalog{
		ApiVersion: "v1alpha1",
		Kind:       "Catalog",
		Metadata: domain.ObjectMeta{
			Name:  lo.ToPtr(name),
			Owner: owner,
		},
		Spec: domain.CatalogSpec{},
	}
}

func createTestCatalogItem(catalogName, itemName string, owner *string) domain.CatalogItem {
	return domain.CatalogItem{
		ApiVersion: "v1alpha1",
		Kind:       "CatalogItem",
		Metadata: domain.CatalogItemMeta{
			Name:    lo.ToPtr(itemName),
			Catalog: catalogName,
			Owner:   owner,
		},
		Spec: domain.CatalogItemSpec{
			Artifacts: []domain.CatalogItemArtifact{
				{
					Type: domain.CatalogItemArtifactTypeContainer,
					Uri:  "quay.io/example/app",
				},
			},
			Type: domain.CatalogItemTypeContainer,
			Versions: []domain.CatalogItemVersion{
				{
					Version:    "1.0.0",
					Channels:   []string{"stable"},
					References: map[string]string{"container": "v1.0.0"},
				},
			},
		},
	}
}

func TestCreateCatalog(t *testing.T) {
	t.Run("When the catalog is valid it should create it and fire an updated callback", func(t *testing.T) {
		h, fakeStore, fakeEvents := newTestHandler()
		catalog := createTestCatalog("c1", nil)

		result, status := h.CreateCatalog(context.Background(), uuid.New(), catalog)
		require.Equal(t, int32(http.StatusCreated), status.Code)
		require.NotNil(t, result)
		require.Contains(t, fakeStore.catalogs, "c1")
		require.Len(t, fakeEvents.created, 1)
		require.Equal(t, domain.EventReasonResourceCreated, fakeEvents.created[0].Reason)
	})

	t.Run("When the store errors it should return an internal-server-error status", func(t *testing.T) {
		h, fakeStore, _ := newTestHandler()
		fakeStore.err = errors.New("db down")

		_, status := h.CreateCatalog(context.Background(), uuid.New(), createTestCatalog("c3", nil))
		require.Equal(t, int32(http.StatusInternalServerError), status.Code)
	})

	t.Run("When managed metadata fields are set by the caller CreateCatalogFromUntrusted should clear them before creation", func(t *testing.T) {
		h, fakeStore, _ := newTestHandler()
		catalog := createTestCatalog("c4", nil)
		catalog.Metadata.Owner = lo.ToPtr("someone")
		catalog.Metadata.Generation = lo.ToPtr(int64(5))

		_, status := CreateCatalogFromUntrusted(context.Background(), h, uuid.New(), catalog)
		require.Equal(t, int32(http.StatusCreated), status.Code)
		require.Nil(t, fakeStore.catalogs["c4"].Metadata.Owner)
		require.Nil(t, fakeStore.catalogs["c4"].Metadata.Generation)
	})

	t.Run("When managed metadata fields are set by the caller CreateCatalog (trusted) should preserve them", func(t *testing.T) {
		h, fakeStore, _ := newTestHandler()
		catalog := createTestCatalog("c4-trusted", nil)
		catalog.Metadata.Owner = lo.ToPtr("someone")
		catalog.Metadata.Generation = lo.ToPtr(int64(5))

		_, status := h.CreateCatalog(context.Background(), uuid.New(), catalog)
		require.Equal(t, int32(http.StatusCreated), status.Code)
		require.Equal(t, "someone", lo.FromPtr(fakeStore.catalogs["c4-trusted"].Metadata.Owner))
		require.Equal(t, int64(5), lo.FromPtr(fakeStore.catalogs["c4-trusted"].Metadata.Generation))
	})
}

func TestListCatalogs(t *testing.T) {
	t.Run("When the store succeeds it should return the list with StatusOK", func(t *testing.T) {
		h, fakeStore, _ := newTestHandler()
		c := createTestCatalog("c1", nil)
		fakeStore.catalogs["c1"] = &c

		result, status := h.ListCatalogs(context.Background(), uuid.New(), domain.ListCatalogsParams{})
		require.Equal(t, domain.StatusOK(), status)
		require.Len(t, result.Items, 1)
	})

	t.Run("When the field selector is invalid it should return a bad-request status", func(t *testing.T) {
		h, _, _ := newTestHandler()
		badSelector := "%%%invalid%%%"

		_, status := h.ListCatalogs(context.Background(), uuid.New(), domain.ListCatalogsParams{FieldSelector: &badSelector})
		require.Equal(t, int32(http.StatusBadRequest), status.Code)
	})
}

func TestGetCatalog(t *testing.T) {
	t.Run("When the catalog exists it should return it with StatusOK", func(t *testing.T) {
		h, fakeStore, _ := newTestHandler()
		c := createTestCatalog("c1", nil)
		fakeStore.catalogs["c1"] = &c

		result, status := h.GetCatalog(context.Background(), uuid.New(), "c1")
		require.Equal(t, domain.StatusOK(), status)
		require.Equal(t, "c1", lo.FromPtr(result.Metadata.Name))
	})

	t.Run("When the catalog does not exist it should return a not-found status", func(t *testing.T) {
		h, _, _ := newTestHandler()

		_, status := h.GetCatalog(context.Background(), uuid.New(), "missing")
		require.Equal(t, int32(http.StatusNotFound), status.Code)
	})
}

func TestReplaceCatalog(t *testing.T) {
	t.Run("When the catalog does not exist it should create it", func(t *testing.T) {
		h, fakeStore, _ := newTestHandler()
		catalog := createTestCatalog("new-catalog", nil)

		result, status := h.ReplaceCatalog(context.Background(), uuid.New(), "new-catalog", catalog, true)
		require.Equal(t, int32(http.StatusCreated), status.Code)
		require.NotNil(t, result)
		require.Contains(t, fakeStore.catalogs, "new-catalog")
	})

	t.Run("When the name in the path does not match metadata.name it should return a bad-request status", func(t *testing.T) {
		h, _, _ := newTestHandler()
		catalog := createTestCatalog("c1", nil)

		_, status := h.ReplaceCatalog(context.Background(), uuid.New(), "different-name", catalog, true)
		require.Equal(t, int32(http.StatusBadRequest), status.Code)
	})

	t.Run("When the catalog exists it should update it and fire an updated callback", func(t *testing.T) {
		h, fakeStore, fakeEvents := newTestHandler()
		orgId := uuid.New()
		catalog := createTestCatalog("c1", nil)
		_, status := h.CreateCatalog(context.Background(), orgId, catalog)
		require.Equal(t, int32(http.StatusCreated), status.Code)

		result, status := h.ReplaceCatalog(context.Background(), orgId, "c1", catalog, true)
		require.Equal(t, int32(http.StatusOK), status.Code)
		require.NotNil(t, result)
		require.Contains(t, fakeStore.catalogs, "c1")
		// Only the create produces a ResourceCreated event; replacing with identical
		// metadata (no generation/labels/owner change) emits nothing further.
		require.Len(t, fakeEvents.created, 1)
		require.Equal(t, domain.EventReasonResourceCreated, fakeEvents.created[0].Reason)
	})

	t.Run("When managed metadata fields are set by the caller ReplaceCatalogFromUntrusted should clear them before replacing", func(t *testing.T) {
		h, fakeStore, _ := newTestHandler()
		orgId := uuid.New()
		catalog := createTestCatalog("replace-untrusted", nil)
		catalog.Metadata.Owner = lo.ToPtr("someone")
		catalog.Metadata.Generation = lo.ToPtr(int64(5))

		_, status := ReplaceCatalogFromUntrusted(context.Background(), h, orgId, "replace-untrusted", catalog, true)
		require.Equal(t, int32(http.StatusCreated), status.Code)
		require.Nil(t, fakeStore.catalogs["replace-untrusted"].Metadata.Owner)
		require.Nil(t, fakeStore.catalogs["replace-untrusted"].Metadata.Generation)
	})

	t.Run("When managed metadata fields are set by the caller ReplaceCatalog (trusted) should preserve them", func(t *testing.T) {
		h, fakeStore, _ := newTestHandler()
		orgId := uuid.New()
		catalog := createTestCatalog("replace-trusted", nil)
		catalog.Metadata.Owner = lo.ToPtr("someone")
		catalog.Metadata.Generation = lo.ToPtr(int64(5))

		_, status := h.ReplaceCatalog(context.Background(), orgId, "replace-trusted", catalog, true)
		require.Equal(t, int32(http.StatusCreated), status.Code)
		require.Equal(t, "someone", lo.FromPtr(fakeStore.catalogs["replace-trusted"].Metadata.Owner))
		require.Equal(t, int64(5), lo.FromPtr(fakeStore.catalogs["replace-trusted"].Metadata.Generation))
	})
}

func TestReplaceCatalogOwnership(t *testing.T) {
	owner := "ResourceSync/my-resourcesync"

	t.Run("When replacing an owned catalog with a changed spec it should return conflict", func(t *testing.T) {
		h, fakeStore, _ := newTestHandler()
		orgId := uuid.New()
		existing := createTestCatalog("owned-catalog", &owner)
		fakeStore.catalogs["owned-catalog"] = &existing

		updated := createTestCatalog("owned-catalog", nil)
		updated.Spec.DisplayName = lo.ToPtr("Changed Name")

		_, status := h.ReplaceCatalog(context.Background(), orgId, "owned-catalog", updated, true)
		require.Equal(t, int32(http.StatusConflict), status.Code)
		require.Equal(t, flterrors.ErrUpdatingResourceWithOwnerNotAllowed.Error(), status.Message)
	})

	t.Run("When enforceOwnership is false it should allow updating an owned catalog", func(t *testing.T) {
		h, fakeStore, _ := newTestHandler()
		orgId := uuid.New()
		existing := createTestCatalog("owned-catalog", &owner)
		fakeStore.catalogs["owned-catalog"] = &existing

		updated := createTestCatalog("owned-catalog", nil)
		updated.Spec.DisplayName = lo.ToPtr("Changed Name")

		result, status := h.ReplaceCatalog(context.Background(), orgId, "owned-catalog", updated, false)
		require.Equal(t, int32(http.StatusOK), status.Code)
		require.NotNil(t, result)
		require.Equal(t, "Changed Name", lo.FromPtr(fakeStore.catalogs["owned-catalog"].Spec.DisplayName))
	})

	t.Run("When replacing an unowned catalog with a changed spec it should allow the update", func(t *testing.T) {
		h, fakeStore, _ := newTestHandler()
		orgId := uuid.New()
		existing := createTestCatalog("unowned-catalog", nil)
		fakeStore.catalogs["unowned-catalog"] = &existing

		updated := createTestCatalog("unowned-catalog", nil)
		updated.Spec.DisplayName = lo.ToPtr("Changed Name")

		result, status := h.ReplaceCatalog(context.Background(), orgId, "unowned-catalog", updated, true)
		require.Equal(t, int32(http.StatusOK), status.Code)
		require.NotNil(t, result)
		require.Equal(t, "Changed Name", lo.FromPtr(fakeStore.catalogs["unowned-catalog"].Spec.DisplayName))
	})
}

func TestPatchCatalogOwnership(t *testing.T) {
	owner := "ResourceSync/my-resourcesync"

	t.Run("When patching an owned catalog spec it should return conflict", func(t *testing.T) {
		h, fakeStore, _ := newTestHandler()
		existing := createTestCatalog("owned-catalog", &owner)
		fakeStore.catalogs["owned-catalog"] = &existing

		var valueIface interface{} = "Changed Name"
		patch := domain.PatchRequest{{Op: "replace", Path: "/spec/displayName", Value: &valueIface}}

		_, status := h.PatchCatalog(context.Background(), uuid.New(), "owned-catalog", patch, true)
		require.Equal(t, int32(http.StatusConflict), status.Code)
		require.Equal(t, flterrors.ErrUpdatingResourceWithOwnerNotAllowed.Error(), status.Message)
	})

	t.Run("When enforceOwnership is false it should allow patching an owned catalog", func(t *testing.T) {
		h, fakeStore, _ := newTestHandler()
		existing := createTestCatalog("owned-catalog", &owner)
		fakeStore.catalogs["owned-catalog"] = &existing

		var valueIface interface{} = "Changed Name"
		patch := domain.PatchRequest{{Op: "replace", Path: "/spec/displayName", Value: &valueIface}}

		result, status := h.PatchCatalog(context.Background(), uuid.New(), "owned-catalog", patch, false)
		require.Equal(t, int32(http.StatusOK), status.Code)
		require.NotNil(t, result)
		require.Equal(t, "Changed Name", lo.FromPtr(fakeStore.catalogs["owned-catalog"].Spec.DisplayName))
	})

	t.Run("When patching an owned catalog labels it should allow the update", func(t *testing.T) {
		h, fakeStore, _ := newTestHandler()
		existing := createTestCatalog("owned-catalog", &owner)
		fakeStore.catalogs["owned-catalog"] = &existing

		var valueIface interface{} = map[string]string{"env": "prod"}
		patch := domain.PatchRequest{{Op: "replace", Path: "/metadata/labels", Value: &valueIface}}

		result, status := h.PatchCatalog(context.Background(), uuid.New(), "owned-catalog", patch, true)
		require.Equal(t, int32(http.StatusOK), status.Code)
		require.NotNil(t, result)
	})
}

func TestDeleteCatalog(t *testing.T) {
	owner := "ResourceSync/my-resourcesync"

	tests := []struct {
		name                 string
		catalogName          string
		catalogOwner         *string
		createCatalog        bool
		enforceOwnership     bool
		expectedStatusCode   int32
		expectedError        error
		expectCatalogDeleted bool
	}{
		{
			name:                 "delete catalog without owner succeeds",
			catalogName:          "test-catalog",
			catalogOwner:         nil,
			createCatalog:        true,
			enforceOwnership:     true,
			expectedStatusCode:   int32(http.StatusOK),
			expectCatalogDeleted: true,
		},
		{
			name:                 "delete non-existent catalog returns OK (idempotent)",
			catalogName:          "nonexistent-catalog",
			createCatalog:        false,
			enforceOwnership:     true,
			expectedStatusCode:   int32(http.StatusOK),
			expectCatalogDeleted: true,
		},
		{
			name:                 "delete catalog with owner fails with conflict",
			catalogName:          "owned-catalog",
			catalogOwner:         &owner,
			createCatalog:        true,
			enforceOwnership:     true,
			expectedStatusCode:   int32(http.StatusConflict),
			expectedError:        flterrors.ErrDeletingResourceWithOwnerNotAllowed,
			expectCatalogDeleted: false,
		},
		{
			name:                 "delete owned catalog succeeds when enforceOwnership is false",
			catalogName:          "resourcesync-owned-catalog",
			catalogOwner:         &owner,
			createCatalog:        true,
			enforceOwnership:     false,
			expectedStatusCode:   int32(http.StatusOK),
			expectCatalogDeleted: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, fakeStore, fakeEvents := newTestHandler()
			ctx := context.Background()
			testOrgId := uuid.New()

			if tt.createCatalog {
				catalog := createTestCatalog(tt.catalogName, tt.catalogOwner)
				fakeStore.catalogs[tt.catalogName] = &catalog
			}

			status := h.DeleteCatalog(ctx, testOrgId, tt.catalogName, tt.enforceOwnership)
			require.Equal(t, tt.expectedStatusCode, status.Code)

			if tt.expectedError != nil {
				require.Equal(t, tt.expectedError.Error(), status.Message)
			}

			_, ok := fakeStore.catalogs[tt.catalogName]
			require.Equal(t, !tt.expectCatalogDeleted, ok)

			// Verify the deletion callback wiring survived extraction: a successful delete
			// of a pre-existing catalog must invoke events.HandleGenericResourceDeletedEvents.
			if tt.createCatalog && tt.expectCatalogDeleted {
				require.Len(t, fakeEvents.deleted, 1)
				require.Equal(t, tt.catalogName, fakeEvents.deleted[0].name)
			} else {
				require.Empty(t, fakeEvents.deleted)
			}
		})
	}
}

func TestPatchCatalog(t *testing.T) {
	t.Run("When the catalog does not exist it should return a not-found status", func(t *testing.T) {
		h, _, _ := newTestHandler()
		var value interface{} = "value"
		patch := domain.PatchRequest{{Op: "replace", Path: "/metadata/labels/k", Value: &value}}

		_, status := h.PatchCatalog(context.Background(), uuid.New(), "missing", patch, true)
		require.Equal(t, int32(http.StatusNotFound), status.Code)
	})

	t.Run("When the patch is valid it should apply it and fire an updated callback", func(t *testing.T) {
		h, fakeStore, fakeEvents := newTestHandler()
		catalog := createTestCatalog("c1", nil)
		fakeStore.catalogs["c1"] = &catalog

		var value interface{} = map[string]string{"env": "prod"}
		patch := domain.PatchRequest{{Op: "replace", Path: "/metadata/labels", Value: &value}}

		result, status := h.PatchCatalog(context.Background(), uuid.New(), "c1", patch, true)
		require.Equal(t, int32(http.StatusOK), status.Code)
		require.NotNil(t, result)
		require.Len(t, fakeEvents.created, 1)
		require.Equal(t, domain.EventReasonResourceUpdated, fakeEvents.created[0].Reason)
	})
}

func TestGetCatalogStatus(t *testing.T) {
	h, fakeStore, _ := newTestHandler()
	catalog := createTestCatalog("c1", nil)
	fakeStore.catalogs["c1"] = &catalog

	result, status := h.GetCatalogStatus(context.Background(), uuid.New(), "c1")
	require.Equal(t, domain.StatusOK(), status)
	require.Equal(t, "c1", lo.FromPtr(result.Metadata.Name))
}

func TestReplaceCatalogStatus(t *testing.T) {
	t.Run("When the catalog exists it should update its status", func(t *testing.T) {
		h, fakeStore, fakeEvents := newTestHandler()
		catalog := createTestCatalog("c1", nil)
		fakeStore.catalogs["c1"] = &catalog

		result, status := h.ReplaceCatalogStatus(context.Background(), uuid.New(), "c1", catalog)
		require.Equal(t, int32(http.StatusOK), status.Code)
		require.NotNil(t, result)
		// Replacing the status with an otherwise-identical catalog doesn't touch
		// generation/labels/owner, so no event is emitted.
		require.Empty(t, fakeEvents.created)
	})

	t.Run("When the name in the path does not match metadata.name it should return a bad-request status", func(t *testing.T) {
		h, _, _ := newTestHandler()
		catalog := createTestCatalog("c1", nil)

		_, status := h.ReplaceCatalogStatus(context.Background(), uuid.New(), "different-name", catalog)
		require.Equal(t, int32(http.StatusBadRequest), status.Code)
	})
}

func TestPatchCatalogStatus(t *testing.T) {
	t.Run("When the catalog does not exist it should return a not-found status", func(t *testing.T) {
		h, _, _ := newTestHandler()
		var value interface{} = "value"
		patch := domain.PatchRequest{{Op: "replace", Path: "/status/conditions", Value: &value}}

		_, status := h.PatchCatalogStatus(context.Background(), uuid.New(), "missing", patch)
		require.Equal(t, int32(http.StatusNotFound), status.Code)
	})
}

func TestListAllCatalogItems(t *testing.T) {
	t.Run("When the store succeeds it should return the list with StatusOK", func(t *testing.T) {
		h, fakeStore, _ := newTestHandler()
		catalog := createTestCatalog("c1", nil)
		fakeStore.catalogs["c1"] = &catalog
		item := createTestCatalogItem("c1", "i1", nil)
		fakeStore.items[itemKey("c1", "i1")] = &item

		result, status := h.ListAllCatalogItems(context.Background(), uuid.New(), domain.ListAllCatalogItemsParams{})
		require.Equal(t, domain.StatusOK(), status)
		require.Len(t, result.Items, 1)
	})

	t.Run("When the field selector is invalid it should return a bad-request status", func(t *testing.T) {
		h, _, _ := newTestHandler()
		badSelector := "%%%invalid%%%"

		_, status := h.ListAllCatalogItems(context.Background(), uuid.New(), domain.ListAllCatalogItemsParams{FieldSelector: &badSelector})
		require.Equal(t, int32(http.StatusBadRequest), status.Code)
	})
}

func TestListCatalogItems(t *testing.T) {
	t.Run("When the catalog exists it should return its items with StatusOK", func(t *testing.T) {
		h, fakeStore, _ := newTestHandler()
		catalog := createTestCatalog("c1", nil)
		fakeStore.catalogs["c1"] = &catalog
		item := createTestCatalogItem("c1", "i1", nil)
		fakeStore.items[itemKey("c1", "i1")] = &item

		result, status := h.ListCatalogItems(context.Background(), uuid.New(), "c1", domain.ListCatalogItemsParams{})
		require.Equal(t, domain.StatusOK(), status)
		require.Len(t, result.Items, 1)
	})

	t.Run("When the parent catalog does not exist it should return a not-found status", func(t *testing.T) {
		h, _, _ := newTestHandler()

		_, status := h.ListCatalogItems(context.Background(), uuid.New(), "missing", domain.ListCatalogItemsParams{})
		require.Equal(t, int32(http.StatusNotFound), status.Code)
	})
}

func TestGetCatalogItem(t *testing.T) {
	t.Run("When the item exists it should return it with StatusOK", func(t *testing.T) {
		h, fakeStore, _ := newTestHandler()
		catalog := createTestCatalog("c1", nil)
		fakeStore.catalogs["c1"] = &catalog
		item := createTestCatalogItem("c1", "i1", nil)
		fakeStore.items[itemKey("c1", "i1")] = &item

		result, status := h.GetCatalogItem(context.Background(), uuid.New(), "c1", "i1")
		require.Equal(t, domain.StatusOK(), status)
		require.Equal(t, "i1", lo.FromPtr(result.Metadata.Name))
	})

	t.Run("When the parent catalog does not exist it should return a not-found status", func(t *testing.T) {
		h, _, _ := newTestHandler()

		_, status := h.GetCatalogItem(context.Background(), uuid.New(), "missing", "i1")
		require.Equal(t, int32(http.StatusNotFound), status.Code)
	})

	t.Run("When the item does not exist it should return a not-found status", func(t *testing.T) {
		h, fakeStore, _ := newTestHandler()
		catalog := createTestCatalog("c1", nil)
		fakeStore.catalogs["c1"] = &catalog

		_, status := h.GetCatalogItem(context.Background(), uuid.New(), "c1", "missing-item")
		require.Equal(t, int32(http.StatusNotFound), status.Code)
	})
}

func TestCreateCatalogItem(t *testing.T) {
	t.Run("When the item is valid it should create it", func(t *testing.T) {
		h, fakeStore, _ := newTestHandler()
		catalog := createTestCatalog("c1", nil)
		fakeStore.catalogs["c1"] = &catalog
		item := createTestCatalogItem("c1", "i1", nil)

		result, status := h.CreateCatalogItem(context.Background(), uuid.New(), "c1", item)
		require.Equal(t, int32(http.StatusCreated), status.Code)
		require.NotNil(t, result)
		require.Contains(t, fakeStore.items, itemKey("c1", "i1"))
	})

	t.Run("When the parent catalog does not exist it should return a not-found status", func(t *testing.T) {
		h, _, _ := newTestHandler()
		item := createTestCatalogItem("missing", "i1", nil)

		_, status := h.CreateCatalogItem(context.Background(), uuid.New(), "missing", item)
		require.Equal(t, int32(http.StatusNotFound), status.Code)
	})

	t.Run("When managed metadata fields are set by the caller CreateCatalogItemFromUntrusted should clear them before creation", func(t *testing.T) {
		h, fakeStore, _ := newTestHandler()
		catalog := createTestCatalog("c1", nil)
		fakeStore.catalogs["c1"] = &catalog
		item := createTestCatalogItem("c1", "i2", nil)
		item.Metadata.Owner = lo.ToPtr("someone")
		item.Metadata.Generation = lo.ToPtr(int64(5))

		_, status := CreateCatalogItemFromUntrusted(context.Background(), h, uuid.New(), "c1", item)
		require.Equal(t, int32(http.StatusCreated), status.Code)
		require.Nil(t, fakeStore.items[itemKey("c1", "i2")].Metadata.Owner)
		require.Nil(t, fakeStore.items[itemKey("c1", "i2")].Metadata.Generation)
	})

	t.Run("When managed metadata fields are set by the caller CreateCatalogItem (trusted) should preserve them", func(t *testing.T) {
		h, fakeStore, _ := newTestHandler()
		catalog := createTestCatalog("c1", nil)
		fakeStore.catalogs["c1"] = &catalog
		item := createTestCatalogItem("c1", "i3", nil)
		item.Metadata.Owner = lo.ToPtr("someone")
		item.Metadata.Generation = lo.ToPtr(int64(5))

		_, status := h.CreateCatalogItem(context.Background(), uuid.New(), "c1", item)
		require.Equal(t, int32(http.StatusCreated), status.Code)
		require.Equal(t, "someone", lo.FromPtr(fakeStore.items[itemKey("c1", "i3")].Metadata.Owner))
		require.Equal(t, int64(5), lo.FromPtr(fakeStore.items[itemKey("c1", "i3")].Metadata.Generation))
	})
}

func TestReplaceCatalogItem(t *testing.T) {
	// Creating a new item via Replace should always succeed (no existing owner to check).
	t.Run("When the item does not exist it should create it", func(t *testing.T) {
		h, fakeStore, _ := newTestHandler()
		catalogName := "test-catalog"
		itemName := "new-item"

		catalog := createTestCatalog(catalogName, nil)
		fakeStore.catalogs[catalogName] = &catalog

		item := createTestCatalogItem(catalogName, itemName, nil)
		result, status := h.ReplaceCatalogItem(context.Background(), uuid.New(), catalogName, itemName, item, true)
		require.Equal(t, int32(http.StatusCreated), status.Code)
		require.NotNil(t, result)
	})

	t.Run("When the name in the path does not match metadata.name it should return a bad-request status", func(t *testing.T) {
		h, fakeStore, _ := newTestHandler()
		catalog := createTestCatalog("c1", nil)
		fakeStore.catalogs["c1"] = &catalog
		item := createTestCatalogItem("c1", "i1", nil)

		_, status := h.ReplaceCatalogItem(context.Background(), uuid.New(), "c1", "different-item", item, true)
		require.Equal(t, int32(http.StatusBadRequest), status.Code)
	})

	t.Run("When managed metadata fields are set by the caller ReplaceCatalogItemFromUntrusted should clear them before replacing", func(t *testing.T) {
		h, fakeStore, _ := newTestHandler()
		orgId := uuid.New()
		catalog := createTestCatalog("c1", nil)
		fakeStore.catalogs["c1"] = &catalog
		item := createTestCatalogItem("c1", "replace-untrusted", nil)
		item.Metadata.Owner = lo.ToPtr("someone")
		item.Metadata.Generation = lo.ToPtr(int64(5))

		_, status := ReplaceCatalogItemFromUntrusted(context.Background(), h, orgId, "c1", "replace-untrusted", item, true)
		require.Equal(t, int32(http.StatusCreated), status.Code)
		require.Nil(t, fakeStore.items[itemKey("c1", "replace-untrusted")].Metadata.Owner)
		require.Nil(t, fakeStore.items[itemKey("c1", "replace-untrusted")].Metadata.Generation)
	})

	t.Run("When managed metadata fields are set by the caller ReplaceCatalogItem (trusted) should preserve them", func(t *testing.T) {
		h, fakeStore, _ := newTestHandler()
		orgId := uuid.New()
		catalog := createTestCatalog("c1", nil)
		fakeStore.catalogs["c1"] = &catalog
		item := createTestCatalogItem("c1", "replace-trusted", nil)
		item.Metadata.Owner = lo.ToPtr("someone")
		item.Metadata.Generation = lo.ToPtr(int64(5))

		_, status := h.ReplaceCatalogItem(context.Background(), orgId, "c1", "replace-trusted", item, true)
		require.Equal(t, int32(http.StatusCreated), status.Code)
		require.Equal(t, "someone", lo.FromPtr(fakeStore.items[itemKey("c1", "replace-trusted")].Metadata.Owner))
		require.Equal(t, int64(5), lo.FromPtr(fakeStore.items[itemKey("c1", "replace-trusted")].Metadata.Generation))
	})
}

func TestPatchCatalogItem(t *testing.T) {
	t.Run("When the parent catalog does not exist it should return a not-found status", func(t *testing.T) {
		h, _, _ := newTestHandler()
		var value interface{} = "value"
		patch := domain.PatchRequest{{Op: "replace", Path: "/metadata/labels/k", Value: &value}}

		_, status := h.PatchCatalogItem(context.Background(), uuid.New(), "missing", "i1", patch, true)
		require.Equal(t, int32(http.StatusNotFound), status.Code)
	})

	t.Run("When the patch is valid it should apply it", func(t *testing.T) {
		h, fakeStore, _ := newTestHandler()
		catalog := createTestCatalog("c1", nil)
		fakeStore.catalogs["c1"] = &catalog
		item := createTestCatalogItem("c1", "i1", nil)
		fakeStore.items[itemKey("c1", "i1")] = &item

		var value interface{} = map[string]string{"env": "prod"}
		patch := domain.PatchRequest{{Op: "replace", Path: "/metadata/labels", Value: &value}}

		result, status := h.PatchCatalogItem(context.Background(), uuid.New(), "c1", "i1", patch, true)
		require.Equal(t, int32(http.StatusOK), status.Code)
		require.NotNil(t, result)
	})
}

func TestDeleteCatalogItem(t *testing.T) {
	owner := "ResourceSync/my-resourcesync"

	tests := []struct {
		name               string
		catalogName        string
		itemName           string
		itemOwner          *string
		createItem         bool
		enforceOwnership   bool
		expectedStatusCode int32
		expectedError      error
		expectItemDeleted  bool
	}{
		{
			name:               "delete item without owner succeeds",
			catalogName:        "test-catalog",
			itemName:           "test-item",
			itemOwner:          nil,
			createItem:         true,
			enforceOwnership:   true,
			expectedStatusCode: int32(http.StatusOK),
			expectItemDeleted:  true,
		},
		{
			name:               "delete non-existent item returns OK (idempotent)",
			catalogName:        "test-catalog",
			itemName:           "nonexistent-item",
			createItem:         false,
			enforceOwnership:   true,
			expectedStatusCode: int32(http.StatusOK),
			expectItemDeleted:  true,
		},
		{
			name:               "delete item with owner fails with conflict",
			catalogName:        "test-catalog",
			itemName:           "owned-item",
			itemOwner:          &owner,
			createItem:         true,
			enforceOwnership:   true,
			expectedStatusCode: int32(http.StatusConflict),
			expectedError:      flterrors.ErrDeletingResourceWithOwnerNotAllowed,
			expectItemDeleted:  false,
		},
		{
			name:               "delete owned item succeeds when enforceOwnership is false",
			catalogName:        "test-catalog",
			itemName:           "rs-owned-item",
			itemOwner:          &owner,
			createItem:         true,
			enforceOwnership:   false,
			expectedStatusCode: int32(http.StatusOK),
			expectItemDeleted:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, fakeStore, _ := newTestHandler()
			ctx := context.Background()
			testOrgId := uuid.New()

			catalog := createTestCatalog(tt.catalogName, nil)
			fakeStore.catalogs[tt.catalogName] = &catalog

			if tt.createItem {
				item := createTestCatalogItem(tt.catalogName, tt.itemName, tt.itemOwner)
				fakeStore.items[itemKey(tt.catalogName, tt.itemName)] = &item
			}

			status := h.DeleteCatalogItem(ctx, testOrgId, tt.catalogName, tt.itemName, tt.enforceOwnership)
			require.Equal(t, tt.expectedStatusCode, status.Code)

			if tt.expectedError != nil {
				require.Equal(t, tt.expectedError.Error(), status.Message)
			}

			_, ok := fakeStore.items[itemKey(tt.catalogName, tt.itemName)]
			require.Equal(t, !tt.expectItemDeleted, ok)
		})
	}
}
