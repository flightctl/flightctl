package catalog

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/service/events"
	"github.com/flightctl/flightctl/internal/store"
	devicestore "github.com/flightctl/flightctl/internal/store/device"
	fleetstore "github.com/flightctl/flightctl/internal/store/fleet"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// fakeDeviceStore is a minimal in-memory implementation of devicestore.Store.
// Only ListDevicesByOsCatalogItemRef and ListDevicesByAppCatalogItemRef have
// meaningful implementations; everything else panics.
type fakeDeviceStore struct {
	osDevices  map[string]*domain.DeviceList // key: catalog/item
	appDevices map[string]*domain.DeviceList // key: catalog/item
	volDevices map[string]*domain.DeviceList // key: catalog/item
	err        error
}

func newFakeDeviceStore() *fakeDeviceStore {
	return &fakeDeviceStore{
		osDevices:  make(map[string]*domain.DeviceList),
		appDevices: make(map[string]*domain.DeviceList),
		volDevices: make(map[string]*domain.DeviceList),
	}
}

func (f *fakeDeviceStore) ListDevicesByOsCatalogItemRef(_ context.Context, _ uuid.UUID, catalog string, item string, _ store.ListParams) (*domain.DeviceList, error) {
	if f.err != nil {
		return nil, f.err
	}
	key := catalog + "/" + item
	if dl, ok := f.osDevices[key]; ok {
		return dl, nil
	}
	return &domain.DeviceList{}, nil
}

func (f *fakeDeviceStore) ListDevicesByAppCatalogItemRef(_ context.Context, _ uuid.UUID, catalog string, item string, _ store.ListParams) (*domain.DeviceList, error) {
	if f.err != nil {
		return nil, f.err
	}
	key := catalog + "/" + item
	if dl, ok := f.appDevices[key]; ok {
		return dl, nil
	}
	return &domain.DeviceList{}, nil
}

func (f *fakeDeviceStore) ListDevicesByVolumeCatalogItemRef(_ context.Context, _ uuid.UUID, catalog string, item string, _ store.ListParams) (*domain.DeviceList, error) {
	if f.err != nil {
		return nil, f.err
	}
	key := catalog + "/" + item
	if dl, ok := f.volDevices[key]; ok {
		return dl, nil
	}
	return &domain.DeviceList{}, nil
}

func (f *fakeDeviceStore) InitialMigration(context.Context) error { panic("not implemented") }
func (f *fakeDeviceStore) Create(context.Context, uuid.UUID, *domain.Device, *devicestore.DeviceRendered) (*domain.Device, error) {
	panic("not implemented")
}
func (f *fakeDeviceStore) Mutate(context.Context, uuid.UUID, string, *domain.Device, devicestore.DeviceApplyFunc, ...devicestore.MutateOption) (*domain.Device, *domain.Device, bool, error) {
	panic("not implemented")
}
func (f *fakeDeviceStore) UpdateStatus(context.Context, uuid.UUID, *domain.Device, *domain.Device) (*domain.Device, *domain.Device, error) {
	panic("not implemented")
}
func (f *fakeDeviceStore) UpdateAnnotations(context.Context, uuid.UUID, string, map[string]string, []string) error {
	panic("not implemented")
}
func (f *fakeDeviceStore) Get(context.Context, uuid.UUID, string) (*domain.Device, error) {
	panic("not implemented")
}
func (f *fakeDeviceStore) List(context.Context, uuid.UUID, devicestore.DeviceListParams) (*domain.DeviceList, error) {
	panic("not implemented")
}
func (f *fakeDeviceStore) Labels(context.Context, uuid.UUID, store.ListParams) (domain.LabelList, error) {
	panic("not implemented")
}
func (f *fakeDeviceStore) Delete(context.Context, uuid.UUID, string, store.EventCallback) (bool, error) {
	panic("not implemented")
}
func (f *fakeDeviceStore) GetRendered(context.Context, uuid.UUID, string, *string, string) (*domain.Device, error) {
	panic("not implemented")
}
func (f *fakeDeviceStore) Healthcheck(context.Context, uuid.UUID, []string) error {
	panic("not implemented")
}
func (f *fakeDeviceStore) ProcessAwaitingReconnectAnnotation(context.Context, uuid.UUID, string, *string) (bool, error) {
	panic("not implemented")
}
func (f *fakeDeviceStore) GetLastSeen(context.Context, uuid.UUID, string) (*time.Time, error) {
	panic("not implemented")
}
func (f *fakeDeviceStore) OverwriteRepositoryRefs(context.Context, uuid.UUID, string, ...string) error {
	panic("not implemented")
}
func (f *fakeDeviceStore) GetRepositoryRefs(context.Context, uuid.UUID, string) (*domain.RepositoryList, error) {
	panic("not implemented")
}
func (f *fakeDeviceStore) RemoveConflictPausedAnnotation(context.Context, uuid.UUID, store.ListParams) (int64, []string, error) {
	panic("not implemented")
}
func (f *fakeDeviceStore) SetOutOfDate(context.Context, uuid.UUID, string) error {
	panic("not implemented")
}
func (f *fakeDeviceStore) ListConnectivityChanged(context.Context, uuid.UUID, store.ListParams, time.Time) (*domain.DeviceList, error) {
	panic("not implemented")
}
func (f *fakeDeviceStore) GetWithTimestamp(context.Context, uuid.UUID, string) (*domain.Device, error) {
	panic("not implemented")
}
func (f *fakeDeviceStore) Count(context.Context, uuid.UUID, store.ListParams) (int64, error) {
	panic("not implemented")
}
func (f *fakeDeviceStore) UnmarkRolloutSelection(context.Context, uuid.UUID, string) error {
	panic("not implemented")
}
func (f *fakeDeviceStore) MarkRolloutSelection(context.Context, uuid.UUID, store.ListParams, *int) error {
	panic("not implemented")
}
func (f *fakeDeviceStore) CompletionCounts(context.Context, uuid.UUID, string, string, *time.Duration) ([]domain.DeviceCompletionCount, error) {
	panic("not implemented")
}
func (f *fakeDeviceStore) CountByLabels(context.Context, uuid.UUID, store.ListParams, []string) ([]map[string]any, error) {
	panic("not implemented")
}
func (f *fakeDeviceStore) Summary(context.Context, uuid.UUID, store.ListParams) (*domain.DevicesSummary, error) {
	panic("not implemented")
}
func (f *fakeDeviceStore) ListDevicesByServiceCondition(context.Context, uuid.UUID, string, string, store.ListParams) (*domain.DeviceList, error) {
	panic("not implemented")
}
func (f *fakeDeviceStore) SetIntegrationTestCreateOrUpdateCallback(store.IntegrationTestCallback) {
	panic("not implemented")
}
func (f *fakeDeviceStore) CountByOrgAndStatus(context.Context, *uuid.UUID, devicestore.DeviceStatusType, bool) ([]devicestore.CountByOrgAndStatusResult, error) {
	panic("not implemented")
}

var _ devicestore.Store = (*fakeDeviceStore)(nil)

// fakeFleetStore is a minimal in-memory implementation of fleetstore.Store.
// Only the ListFleetsByXxxCatalogItemRef methods have meaningful implementations;
// everything else panics.
type fakeFleetStore struct {
	osFleets  map[string]*domain.FleetList // key: catalog/item
	appFleets map[string]*domain.FleetList // key: catalog/item
	volFleets map[string]*domain.FleetList // key: catalog/item
	err       error
}

func newFakeFleetStore() *fakeFleetStore {
	return &fakeFleetStore{
		osFleets:  make(map[string]*domain.FleetList),
		appFleets: make(map[string]*domain.FleetList),
		volFleets: make(map[string]*domain.FleetList),
	}
}

func (f *fakeFleetStore) ListFleetsByOsCatalogItemRef(_ context.Context, _ uuid.UUID, catalog string, item string, _ store.ListParams) (*domain.FleetList, error) {
	if f.err != nil {
		return nil, f.err
	}
	key := catalog + "/" + item
	if fl, ok := f.osFleets[key]; ok {
		return fl, nil
	}
	return &domain.FleetList{}, nil
}

func (f *fakeFleetStore) ListFleetsByAppCatalogItemRef(_ context.Context, _ uuid.UUID, catalog string, item string, _ store.ListParams) (*domain.FleetList, error) {
	if f.err != nil {
		return nil, f.err
	}
	key := catalog + "/" + item
	if fl, ok := f.appFleets[key]; ok {
		return fl, nil
	}
	return &domain.FleetList{}, nil
}

func (f *fakeFleetStore) ListFleetsByVolumeCatalogItemRef(_ context.Context, _ uuid.UUID, catalog string, item string, _ store.ListParams) (*domain.FleetList, error) {
	if f.err != nil {
		return nil, f.err
	}
	key := catalog + "/" + item
	if fl, ok := f.volFleets[key]; ok {
		return fl, nil
	}
	return &domain.FleetList{}, nil
}

func (f *fakeFleetStore) InitialMigration(context.Context) error { panic("not implemented") }
func (f *fakeFleetStore) Create(context.Context, uuid.UUID, *domain.Fleet) (*domain.Fleet, error) {
	panic("not implemented")
}
func (f *fakeFleetStore) Mutate(context.Context, uuid.UUID, string, *domain.Fleet, fleetstore.FleetApplyFunc) (*domain.Fleet, *domain.Fleet, bool, error) {
	panic("not implemented")
}
func (f *fakeFleetStore) UpdateStatus(context.Context, uuid.UUID, *domain.Fleet) (*domain.Fleet, *domain.Fleet, error) {
	panic("not implemented")
}
func (f *fakeFleetStore) UpdateAnnotations(context.Context, uuid.UUID, string, map[string]string, []string) (*domain.Fleet, *domain.Fleet, error) {
	panic("not implemented")
}
func (f *fakeFleetStore) Get(context.Context, uuid.UUID, string, ...fleetstore.GetOption) (*domain.Fleet, error) {
	panic("not implemented")
}
func (f *fakeFleetStore) List(context.Context, uuid.UUID, store.ListParams, ...fleetstore.ListOption) (*domain.FleetList, error) {
	panic("not implemented")
}
func (f *fakeFleetStore) Delete(context.Context, uuid.UUID, string, store.EventCallback) error {
	panic("not implemented")
}
func (f *fakeFleetStore) ListRolloutDeviceSelection(context.Context, uuid.UUID) (*domain.FleetList, error) {
	panic("not implemented")
}
func (f *fakeFleetStore) ListDisruptionBudgetFleets(context.Context, uuid.UUID) (*domain.FleetList, error) {
	panic("not implemented")
}
func (f *fakeFleetStore) UnsetOwner(context.Context, *gorm.DB, uuid.UUID, string) error {
	panic("not implemented")
}
func (f *fakeFleetStore) UnsetOwnerByKind(context.Context, *gorm.DB, uuid.UUID, string) error {
	panic("not implemented")
}
func (f *fakeFleetStore) OverwriteRepositoryRefs(context.Context, uuid.UUID, string, ...string) error {
	panic("not implemented")
}
func (f *fakeFleetStore) GetRepositoryRefs(context.Context, uuid.UUID, string) (*domain.RepositoryList, error) {
	panic("not implemented")
}
func (f *fakeFleetStore) CountByRolloutStatus(context.Context, *uuid.UUID, *string) ([]fleetstore.CountByRolloutStatusResult, error) {
	panic("not implemented")
}

var _ fleetstore.Store = (*fakeFleetStore)(nil)

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

func (f *fakeCatalogStore) Create(ctx context.Context, orgId uuid.UUID, catalog *domain.Catalog) (*domain.Catalog, error) {
	if f.err != nil {
		return nil, f.err
	}
	name := lo.FromPtr(catalog.Metadata.Name)
	if _, exists := f.catalogs[name]; exists {
		return nil, flterrors.ErrDuplicateName
	}
	f.catalogs[name] = catalog
	return catalog, nil
}

func (f *fakeCatalogStore) Update(ctx context.Context, orgId uuid.UUID, catalog *domain.Catalog) (*domain.Catalog, *domain.Catalog, error) {
	if f.err != nil {
		return nil, nil, f.err
	}
	name := lo.FromPtr(catalog.Metadata.Name)
	old, exists := f.catalogs[name]
	if !exists {
		return nil, nil, flterrors.ErrResourceNotFound
	}
	// Mirrors the real generic store: fields left nil by the caller are preserved
	// from the existing resource rather than wiped on update.
	if catalog.Metadata.Owner == nil {
		catalog.Metadata.Owner = old.Metadata.Owner
	}
	f.catalogs[name] = catalog
	return catalog, old, nil
}

func (f *fakeCatalogStore) CreateOrUpdate(ctx context.Context, orgId uuid.UUID, catalog *domain.Catalog) (*domain.Catalog, *domain.Catalog, bool, error) {
	name := lo.FromPtr(catalog.Metadata.Name)
	if _, exists := f.catalogs[name]; exists {
		result, old, err := f.Update(ctx, orgId, catalog)
		return result, old, false, err
	}
	result, err := f.Create(ctx, orgId, catalog)
	return result, nil, true, err
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

func (f *fakeCatalogStore) Delete(ctx context.Context, orgId uuid.UUID, name string) error {
	_, exists := f.catalogs[name]
	if !exists {
		return flterrors.ErrResourceNotFound
	}
	prefix := name + "/"
	for key := range f.items {
		if strings.HasPrefix(key, prefix) {
			return flterrors.ErrResourceNotEmpty
		}
	}
	delete(f.catalogs, name)
	return nil
}

func (f *fakeCatalogStore) UpdateStatus(ctx context.Context, orgId uuid.UUID, resource *domain.Catalog) (*domain.Catalog, *domain.Catalog, error) {
	name := lo.FromPtr(resource.Metadata.Name)
	old, exists := f.catalogs[name]
	if !exists {
		return nil, nil, flterrors.ErrResourceNotFound
	}
	f.catalogs[name] = resource
	return resource, old, nil
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
	old, ok := f.items[key]
	if !ok {
		return nil, flterrors.ErrResourceNotFound
	}
	// Mirrors the real generic store: fields left nil by the caller are preserved
	// from the existing resource rather than wiped on update.
	if item.Metadata.Owner == nil {
		item.Metadata.Owner = old.Metadata.Owner
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
	return NewServiceHandler(fakeStore, nil, nil, fakeEvents, logrus.New()), fakeStore, fakeEvents
}

func newTestHandlerWithDeviceStore(ds devicestore.Store) (*ServiceHandler, *fakeCatalogStore, *fakeEventsService) {
	fakeStore := newFakeCatalogStore()
	fakeEvents := &fakeEventsService{}
	return NewServiceHandler(fakeStore, ds, nil, fakeEvents, logrus.New()), fakeStore, fakeEvents
}

func newTestHandlerWithStores(ds devicestore.Store, fs fleetstore.Store) (*ServiceHandler, *fakeCatalogStore, *fakeEventsService) {
	fakeStore := newFakeCatalogStore()
	fakeEvents := &fakeEventsService{}
	return NewServiceHandler(fakeStore, ds, fs, fakeEvents, logrus.New()), fakeStore, fakeEvents
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
					References: map[domain.CatalogItemArtifactType]string{"container": "v1.0.0"},
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
		require.Equal(t, owner, lo.FromPtr(fakeStore.catalogs["owned-catalog"].Metadata.Owner))
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
		require.Equal(t, owner, lo.FromPtr(fakeStore.catalogs["owned-catalog"].Metadata.Owner))
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

func TestReplaceCatalogItemOwnership(t *testing.T) {
	owner := "ResourceSync/my-resourcesync"

	t.Run("When replacing an owned item with a changed spec it should return conflict", func(t *testing.T) {
		h, fakeStore, _ := newTestHandler()
		catalog := createTestCatalog("c1", nil)
		fakeStore.catalogs["c1"] = &catalog
		existing := createTestCatalogItem("c1", "owned-item", &owner)
		fakeStore.items[itemKey("c1", "owned-item")] = &existing

		updated := createTestCatalogItem("c1", "owned-item", nil)
		updated.Spec.Artifacts[0].Uri = "quay.io/example/changed"

		_, status := h.ReplaceCatalogItem(context.Background(), uuid.New(), "c1", "owned-item", updated, true)
		require.Equal(t, int32(http.StatusConflict), status.Code)
		require.Equal(t, flterrors.ErrUpdatingResourceWithOwnerNotAllowed.Error(), status.Message)
		require.Equal(t, "quay.io/example/app", fakeStore.items[itemKey("c1", "owned-item")].Spec.Artifacts[0].Uri)
	})

	t.Run("When enforceOwnership is false it should allow updating an owned item", func(t *testing.T) {
		h, fakeStore, _ := newTestHandler()
		catalog := createTestCatalog("c1", nil)
		fakeStore.catalogs["c1"] = &catalog
		existing := createTestCatalogItem("c1", "owned-item", &owner)
		fakeStore.items[itemKey("c1", "owned-item")] = &existing

		updated := createTestCatalogItem("c1", "owned-item", nil)
		updated.Spec.Artifacts[0].Uri = "quay.io/example/changed"

		result, status := h.ReplaceCatalogItem(context.Background(), uuid.New(), "c1", "owned-item", updated, false)
		require.Equal(t, int32(http.StatusOK), status.Code)
		require.NotNil(t, result)
		require.Equal(t, "quay.io/example/changed", fakeStore.items[itemKey("c1", "owned-item")].Spec.Artifacts[0].Uri)
		require.Equal(t, owner, lo.FromPtr(fakeStore.items[itemKey("c1", "owned-item")].Metadata.Owner))
	})

	t.Run("When replacing an unowned item with a changed spec it should allow the update", func(t *testing.T) {
		h, fakeStore, _ := newTestHandler()
		catalog := createTestCatalog("c1", nil)
		fakeStore.catalogs["c1"] = &catalog
		existing := createTestCatalogItem("c1", "unowned-item", nil)
		fakeStore.items[itemKey("c1", "unowned-item")] = &existing

		updated := createTestCatalogItem("c1", "unowned-item", nil)
		updated.Spec.Artifacts[0].Uri = "quay.io/example/changed"

		result, status := h.ReplaceCatalogItem(context.Background(), uuid.New(), "c1", "unowned-item", updated, true)
		require.Equal(t, int32(http.StatusOK), status.Code)
		require.NotNil(t, result)
		require.Equal(t, "quay.io/example/changed", fakeStore.items[itemKey("c1", "unowned-item")].Spec.Artifacts[0].Uri)
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

func TestPatchCatalogItemOwnership(t *testing.T) {
	owner := "ResourceSync/my-resourcesync"

	setup := func(t *testing.T) (*ServiceHandler, *fakeCatalogStore) {
		h, fakeStore, _ := newTestHandler()
		catalog := createTestCatalog("c1", nil)
		fakeStore.catalogs["c1"] = &catalog
		item := createTestCatalogItem("c1", "owned-item", &owner)
		fakeStore.items[itemKey("c1", "owned-item")] = &item
		return h, fakeStore
	}

	t.Run("When patching an owned item spec it should return conflict", func(t *testing.T) {
		h, fakeStore := setup(t)
		var value interface{} = "quay.io/example/changed"
		patch := domain.PatchRequest{{Op: "replace", Path: "/spec/artifacts/0/uri", Value: &value}}

		_, status := h.PatchCatalogItem(context.Background(), uuid.New(), "c1", "owned-item", patch, true)
		require.Equal(t, int32(http.StatusConflict), status.Code)
		require.Equal(t, flterrors.ErrUpdatingResourceWithOwnerNotAllowed.Error(), status.Message)
		require.Equal(t, "quay.io/example/app", fakeStore.items[itemKey("c1", "owned-item")].Spec.Artifacts[0].Uri)
	})

	t.Run("When enforceOwnership is false it should allow patching an owned item", func(t *testing.T) {
		h, fakeStore := setup(t)
		var value interface{} = "quay.io/example/changed"
		patch := domain.PatchRequest{{Op: "replace", Path: "/spec/artifacts/0/uri", Value: &value}}

		result, status := h.PatchCatalogItem(context.Background(), uuid.New(), "c1", "owned-item", patch, false)
		require.Equal(t, int32(http.StatusOK), status.Code)
		require.NotNil(t, result)
		require.Equal(t, "quay.io/example/changed", fakeStore.items[itemKey("c1", "owned-item")].Spec.Artifacts[0].Uri)
		require.Equal(t, owner, lo.FromPtr(fakeStore.items[itemKey("c1", "owned-item")].Metadata.Owner))
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

func TestDeleteCatalogItemInUse(t *testing.T) {
	t.Run("When a device references the item via OS spec it should return conflict", func(t *testing.T) {
		ds := newFakeDeviceStore()
		h, fakeStore, _ := newTestHandlerWithDeviceStore(ds)
		catalog := createTestCatalog("c1", nil)
		fakeStore.catalogs["c1"] = &catalog
		item := createTestCatalogItem("c1", "item1", nil)
		fakeStore.items[itemKey("c1", "item1")] = &item

		ds.osDevices["c1/item1"] = &domain.DeviceList{
			Items: []domain.Device{makeDeviceWithOsRef("dev1", "c1", "item1", "1.0.0", nil)},
		}

		status := h.DeleteCatalogItem(context.Background(), uuid.New(), "c1", "item1", true)
		require.Equal(t, int32(http.StatusConflict), status.Code)
		require.Contains(t, status.Message, "in use by devices or fleets")
		require.Contains(t, status.Message, "1.0.0")
		_, ok := fakeStore.items[itemKey("c1", "item1")]
		require.True(t, ok)
	})

	t.Run("When a device references the item via application spec it should return conflict", func(t *testing.T) {
		ds := newFakeDeviceStore()
		h, fakeStore, _ := newTestHandlerWithDeviceStore(ds)
		catalog := createTestCatalog("c1", nil)
		fakeStore.catalogs["c1"] = &catalog
		item := createTestCatalogItem("c1", "item1", nil)
		fakeStore.items[itemKey("c1", "item1")] = &item

		apps := []domain.ApplicationProviderSpec{
			makeContainerAppSpec(t, "c1", "item1", "1.0.0", nil, lo.ToPtr("myapp")),
		}
		ds.appDevices["c1/item1"] = &domain.DeviceList{
			Items: []domain.Device{{
				Metadata: domain.ObjectMeta{Name: lo.ToPtr("dev1")},
				Spec:     &domain.DeviceSpec{Applications: &apps},
			}},
		}

		status := h.DeleteCatalogItem(context.Background(), uuid.New(), "c1", "item1", true)
		require.Equal(t, int32(http.StatusConflict), status.Code)
		require.Contains(t, status.Message, "in use by devices or fleets")
	})

	t.Run("When a device references the item via volume spec it should return conflict", func(t *testing.T) {
		ds := newFakeDeviceStore()
		h, fakeStore, _ := newTestHandlerWithDeviceStore(ds)
		catalog := createTestCatalog("c1", nil)
		fakeStore.catalogs["c1"] = &catalog
		item := createTestCatalogItem("c1", "item1", nil)
		fakeStore.items[itemKey("c1", "item1")] = &item

		apps := []domain.ApplicationProviderSpec{
			makeContainerAppWithVolumeRef(t, "c1", "item1", "1.0.0", nil, lo.ToPtr("myapp")),
		}
		ds.volDevices["c1/item1"] = &domain.DeviceList{
			Items: []domain.Device{{
				Metadata: domain.ObjectMeta{Name: lo.ToPtr("dev1")},
				Spec:     &domain.DeviceSpec{Applications: &apps},
			}},
		}

		status := h.DeleteCatalogItem(context.Background(), uuid.New(), "c1", "item1", true)
		require.Equal(t, int32(http.StatusConflict), status.Code)
		require.Contains(t, status.Message, "in use by devices or fleets")
	})

	t.Run("When no devices reference the item it should delete successfully", func(t *testing.T) {
		ds := newFakeDeviceStore()
		h, fakeStore, _ := newTestHandlerWithDeviceStore(ds)
		catalog := createTestCatalog("c1", nil)
		fakeStore.catalogs["c1"] = &catalog
		item := createTestCatalogItem("c1", "item1", nil)
		fakeStore.items[itemKey("c1", "item1")] = &item

		status := h.DeleteCatalogItem(context.Background(), uuid.New(), "c1", "item1", true)
		require.Equal(t, int32(http.StatusOK), status.Code)
		_, ok := fakeStore.items[itemKey("c1", "item1")]
		require.False(t, ok)
	})
}

func TestDeleteCatalogItemInUseByFleet(t *testing.T) {
	t.Run("When a fleet references the item via OS spec it should return conflict", func(t *testing.T) {
		fs := newFakeFleetStore()
		h, fakeStore, _ := newTestHandlerWithStores(nil, fs)
		catalog := createTestCatalog("c1", nil)
		fakeStore.catalogs["c1"] = &catalog
		item := createTestCatalogItem("c1", "item1", nil)
		fakeStore.items[itemKey("c1", "item1")] = &item

		fs.osFleets["c1/item1"] = &domain.FleetList{
			Items: []domain.Fleet{makeFleetWithOsRef("fleet1", "c1", "item1", "1.0.0", nil)},
		}

		status := h.DeleteCatalogItem(context.Background(), uuid.New(), "c1", "item1", true)
		require.Equal(t, int32(http.StatusConflict), status.Code)
		require.Contains(t, status.Message, "in use by devices or fleets")
		require.Contains(t, status.Message, "1.0.0")
		_, ok := fakeStore.items[itemKey("c1", "item1")]
		require.True(t, ok)
	})

	t.Run("When a fleet references the item via application spec it should return conflict", func(t *testing.T) {
		fs := newFakeFleetStore()
		h, fakeStore, _ := newTestHandlerWithStores(nil, fs)
		catalog := createTestCatalog("c1", nil)
		fakeStore.catalogs["c1"] = &catalog
		item := createTestCatalogItem("c1", "item1", nil)
		fakeStore.items[itemKey("c1", "item1")] = &item

		apps := []domain.ApplicationProviderSpec{
			makeContainerAppSpec(t, "c1", "item1", "1.0.0", nil, lo.ToPtr("myapp")),
		}
		fs.appFleets["c1/item1"] = &domain.FleetList{
			Items: []domain.Fleet{{
				Metadata: domain.ObjectMeta{Name: lo.ToPtr("fleet1")},
				Spec: domain.FleetSpec{
					Template: struct {
						Metadata *domain.ObjectMeta `json:"metadata,omitempty"`
						Spec     domain.DeviceSpec  `json:"spec"`
					}{
						Spec: domain.DeviceSpec{Applications: &apps},
					},
				},
			}},
		}

		status := h.DeleteCatalogItem(context.Background(), uuid.New(), "c1", "item1", true)
		require.Equal(t, int32(http.StatusConflict), status.Code)
		require.Contains(t, status.Message, "in use by devices or fleets")
	})

	t.Run("When a fleet references the item via volume spec it should return conflict", func(t *testing.T) {
		fs := newFakeFleetStore()
		h, fakeStore, _ := newTestHandlerWithStores(nil, fs)
		catalog := createTestCatalog("c1", nil)
		fakeStore.catalogs["c1"] = &catalog
		item := createTestCatalogItem("c1", "item1", nil)
		fakeStore.items[itemKey("c1", "item1")] = &item

		apps := []domain.ApplicationProviderSpec{
			makeContainerAppWithVolumeRef(t, "c1", "item1", "1.0.0", nil, lo.ToPtr("myapp")),
		}
		fs.volFleets["c1/item1"] = &domain.FleetList{
			Items: []domain.Fleet{{
				Metadata: domain.ObjectMeta{Name: lo.ToPtr("fleet1")},
				Spec: domain.FleetSpec{
					Template: struct {
						Metadata *domain.ObjectMeta `json:"metadata,omitempty"`
						Spec     domain.DeviceSpec  `json:"spec"`
					}{
						Spec: domain.DeviceSpec{Applications: &apps},
					},
				},
			}},
		}

		status := h.DeleteCatalogItem(context.Background(), uuid.New(), "c1", "item1", true)
		require.Equal(t, int32(http.StatusConflict), status.Code)
		require.Contains(t, status.Message, "in use by devices or fleets")
	})

	t.Run("When no fleets reference the item it should delete successfully", func(t *testing.T) {
		fs := newFakeFleetStore()
		h, fakeStore, _ := newTestHandlerWithStores(nil, fs)
		catalog := createTestCatalog("c1", nil)
		fakeStore.catalogs["c1"] = &catalog
		item := createTestCatalogItem("c1", "item1", nil)
		fakeStore.items[itemKey("c1", "item1")] = &item

		status := h.DeleteCatalogItem(context.Background(), uuid.New(), "c1", "item1", true)
		require.Equal(t, int32(http.StatusOK), status.Code)
		_, ok := fakeStore.items[itemKey("c1", "item1")]
		require.False(t, ok)
	})
}

func TestReplaceCatalogItemInUseByFleet(t *testing.T) {
	t.Run("When removing a version used by a fleet it should return conflict", func(t *testing.T) {
		fs := newFakeFleetStore()
		h, fakeStore, _ := newTestHandlerWithStores(nil, fs)
		catalog := createTestCatalog("c1", nil)
		fakeStore.catalogs["c1"] = &catalog
		existing := createTestCatalogItem("c1", "item1", nil)
		fakeStore.items[itemKey("c1", "item1")] = &existing

		fs.osFleets["c1/item1"] = &domain.FleetList{
			Items: []domain.Fleet{makeFleetWithOsRef("fleet1", "c1", "item1", "1.0.0", nil)},
		}

		updated := createTestCatalogItem("c1", "item1", nil)
		updated.Spec.Versions = []domain.CatalogItemVersion{
			{
				Version:    "2.0.0",
				Channels:   []string{"stable"},
				References: map[domain.CatalogItemArtifactType]string{"container": "v2.0.0"},
			},
		}

		_, status := h.ReplaceCatalogItem(context.Background(), uuid.New(), "c1", "item1", updated, true)
		require.Equal(t, int32(http.StatusConflict), status.Code)
		require.Contains(t, status.Message, "1.0.0")
		require.Contains(t, status.Message, "in use by devices or fleets")
	})

	t.Run("When adding a new version while keeping fleet-used versions unchanged it should succeed", func(t *testing.T) {
		fs := newFakeFleetStore()
		h, fakeStore, _ := newTestHandlerWithStores(nil, fs)
		catalog := createTestCatalog("c1", nil)
		fakeStore.catalogs["c1"] = &catalog
		existing := createTestCatalogItem("c1", "item1", nil)
		fakeStore.items[itemKey("c1", "item1")] = &existing

		fs.osFleets["c1/item1"] = &domain.FleetList{
			Items: []domain.Fleet{makeFleetWithOsRef("fleet1", "c1", "item1", "1.0.0", nil)},
		}

		updated := createTestCatalogItem("c1", "item1", nil)
		updated.Spec.Versions = append(updated.Spec.Versions, domain.CatalogItemVersion{
			Version:    "2.0.0",
			Channels:   []string{"stable"},
			References: map[domain.CatalogItemArtifactType]string{"container": "v2.0.0"},
		})

		_, status := h.ReplaceCatalogItem(context.Background(), uuid.New(), "c1", "item1", updated, true)
		require.Equal(t, int32(http.StatusOK), status.Code)
	})
}

func TestReplaceCatalogItemInUse(t *testing.T) {
	t.Run("When removing an in-use version it should return conflict", func(t *testing.T) {
		ds := newFakeDeviceStore()
		h, fakeStore, _ := newTestHandlerWithDeviceStore(ds)
		catalog := createTestCatalog("c1", nil)
		fakeStore.catalogs["c1"] = &catalog
		existing := createTestCatalogItem("c1", "item1", nil)
		fakeStore.items[itemKey("c1", "item1")] = &existing

		ds.osDevices["c1/item1"] = &domain.DeviceList{
			Items: []domain.Device{makeDeviceWithOsRef("dev1", "c1", "item1", "1.0.0", nil)},
		}

		updated := createTestCatalogItem("c1", "item1", nil)
		updated.Spec.Versions = []domain.CatalogItemVersion{
			{
				Version:    "2.0.0",
				Channels:   []string{"stable"},
				References: map[domain.CatalogItemArtifactType]string{"container": "v2.0.0"},
			},
		}

		_, status := h.ReplaceCatalogItem(context.Background(), uuid.New(), "c1", "item1", updated, true)
		require.Equal(t, int32(http.StatusConflict), status.Code)
		require.Contains(t, status.Message, "1.0.0")
		require.Contains(t, status.Message, "in use by devices or fleets")
	})

	t.Run("When modifying an in-use version it should return conflict", func(t *testing.T) {
		ds := newFakeDeviceStore()
		h, fakeStore, _ := newTestHandlerWithDeviceStore(ds)
		catalog := createTestCatalog("c1", nil)
		fakeStore.catalogs["c1"] = &catalog
		existing := createTestCatalogItem("c1", "item1", nil)
		fakeStore.items[itemKey("c1", "item1")] = &existing

		ds.osDevices["c1/item1"] = &domain.DeviceList{
			Items: []domain.Device{makeDeviceWithOsRef("dev1", "c1", "item1", "1.0.0", nil)},
		}

		updated := createTestCatalogItem("c1", "item1", nil)
		updated.Spec.Versions[0].References["container"] = "v1.0.0-changed"

		_, status := h.ReplaceCatalogItem(context.Background(), uuid.New(), "c1", "item1", updated, true)
		require.Equal(t, int32(http.StatusConflict), status.Code)
		require.Contains(t, status.Message, "1.0.0")
	})

	t.Run("When adding a new version while keeping in-use versions unchanged it should succeed", func(t *testing.T) {
		ds := newFakeDeviceStore()
		h, fakeStore, _ := newTestHandlerWithDeviceStore(ds)
		catalog := createTestCatalog("c1", nil)
		fakeStore.catalogs["c1"] = &catalog
		existing := createTestCatalogItem("c1", "item1", nil)
		fakeStore.items[itemKey("c1", "item1")] = &existing

		ds.osDevices["c1/item1"] = &domain.DeviceList{
			Items: []domain.Device{makeDeviceWithOsRef("dev1", "c1", "item1", "1.0.0", nil)},
		}

		updated := createTestCatalogItem("c1", "item1", nil)
		updated.Spec.Versions = append(updated.Spec.Versions, domain.CatalogItemVersion{
			Version:    "2.0.0",
			Channels:   []string{"stable"},
			References: map[domain.CatalogItemArtifactType]string{"container": "v2.0.0"},
		})

		_, status := h.ReplaceCatalogItem(context.Background(), uuid.New(), "c1", "item1", updated, true)
		require.Equal(t, int32(http.StatusOK), status.Code)
	})

	t.Run("When modifying a non-deployed version it should succeed", func(t *testing.T) {
		ds := newFakeDeviceStore()
		h, fakeStore, _ := newTestHandlerWithDeviceStore(ds)
		catalog := createTestCatalog("c1", nil)
		fakeStore.catalogs["c1"] = &catalog
		existing := createTestCatalogItem("c1", "item1", nil)
		existing.Spec.Versions = append(existing.Spec.Versions, domain.CatalogItemVersion{
			Version:    "2.0.0",
			Channels:   []string{"fast"},
			References: map[domain.CatalogItemArtifactType]string{"container": "v2.0.0"},
		})
		fakeStore.items[itemKey("c1", "item1")] = &existing

		ds.osDevices["c1/item1"] = &domain.DeviceList{
			Items: []domain.Device{makeDeviceWithOsRef("dev1", "c1", "item1", "1.0.0", nil)},
		}

		updated := createTestCatalogItem("c1", "item1", nil)
		updated.Spec.Versions = append(updated.Spec.Versions, domain.CatalogItemVersion{
			Version:    "2.0.0",
			Channels:   []string{"stable"},
			References: map[domain.CatalogItemArtifactType]string{"container": "v2.0.0-patched"},
		})

		_, status := h.ReplaceCatalogItem(context.Background(), uuid.New(), "c1", "item1", updated, true)
		require.Equal(t, int32(http.StatusOK), status.Code)
	})

	t.Run("When a deployed version is absent from both old and new specs it should not block the update", func(t *testing.T) {
		ds := newFakeDeviceStore()
		h, fakeStore, _ := newTestHandlerWithDeviceStore(ds)
		catalog := createTestCatalog("c1", nil)
		fakeStore.catalogs["c1"] = &catalog

		existing := createTestCatalogItem("c1", "item1", nil)
		existing.Spec.Versions = []domain.CatalogItemVersion{
			{
				Version:    "2.0.0",
				Channels:   []string{"stable"},
				References: map[domain.CatalogItemArtifactType]string{"container": "v2.0.0"},
			},
		}
		fakeStore.items[itemKey("c1", "item1")] = &existing

		ds.osDevices["c1/item1"] = &domain.DeviceList{
			Items: []domain.Device{makeDeviceWithOsRef("dev1", "c1", "item1", "1.0.0", nil)},
		}

		updated := createTestCatalogItem("c1", "item1", nil)
		updated.Spec.Versions = []domain.CatalogItemVersion{
			{
				Version:    "2.0.0",
				Channels:   []string{"fast"},
				References: map[domain.CatalogItemArtifactType]string{"container": "v2.0.0-patched"},
			},
		}

		_, status := h.ReplaceCatalogItem(context.Background(), uuid.New(), "c1", "item1", updated, true)
		require.Equal(t, int32(http.StatusOK), status.Code)
	})

	t.Run("When the item does not exist yet it should create it without in-use check", func(t *testing.T) {
		ds := newFakeDeviceStore()
		h, fakeStore, _ := newTestHandlerWithDeviceStore(ds)
		catalog := createTestCatalog("c1", nil)
		fakeStore.catalogs["c1"] = &catalog

		item := createTestCatalogItem("c1", "new-item", nil)
		_, status := h.ReplaceCatalogItem(context.Background(), uuid.New(), "c1", "new-item", item, true)
		require.Equal(t, int32(http.StatusCreated), status.Code)
	})
}

func TestPatchCatalogItemInUse(t *testing.T) {
	t.Run("When a patch removes an in-use version it should return conflict", func(t *testing.T) {
		ds := newFakeDeviceStore()
		h, fakeStore, _ := newTestHandlerWithDeviceStore(ds)
		catalog := createTestCatalog("c1", nil)
		fakeStore.catalogs["c1"] = &catalog
		existing := createTestCatalogItem("c1", "item1", nil)
		existing.Spec.Versions = append(existing.Spec.Versions, domain.CatalogItemVersion{
			Version:    "2.0.0",
			Channels:   []string{"stable"},
			References: map[domain.CatalogItemArtifactType]string{"container": "v2.0.0"},
		})
		fakeStore.items[itemKey("c1", "item1")] = &existing

		ds.osDevices["c1/item1"] = &domain.DeviceList{
			Items: []domain.Device{makeDeviceWithOsRef("dev1", "c1", "item1", "1.0.0", nil)},
		}

		var value interface{} = []domain.CatalogItemVersion{
			{
				Version:    "2.0.0",
				Channels:   []string{"stable"},
				References: map[domain.CatalogItemArtifactType]string{"container": "v2.0.0"},
			},
		}
		patch := domain.PatchRequest{{Op: "replace", Path: "/spec/versions", Value: &value}}

		_, status := h.PatchCatalogItem(context.Background(), uuid.New(), "c1", "item1", patch, true)
		require.Equal(t, int32(http.StatusConflict), status.Code)
		require.Contains(t, status.Message, "1.0.0")
		require.Contains(t, status.Message, "in use by devices or fleets")
	})

	t.Run("When a patch modifies an in-use version it should return conflict", func(t *testing.T) {
		ds := newFakeDeviceStore()
		h, fakeStore, _ := newTestHandlerWithDeviceStore(ds)
		catalog := createTestCatalog("c1", nil)
		fakeStore.catalogs["c1"] = &catalog
		existing := createTestCatalogItem("c1", "item1", nil)
		fakeStore.items[itemKey("c1", "item1")] = &existing

		ds.osDevices["c1/item1"] = &domain.DeviceList{
			Items: []domain.Device{makeDeviceWithOsRef("dev1", "c1", "item1", "1.0.0", nil)},
		}

		var value interface{} = "v1.0.0-changed"
		patch := domain.PatchRequest{{Op: "replace", Path: "/spec/versions/0/references/container", Value: &value}}

		_, status := h.PatchCatalogItem(context.Background(), uuid.New(), "c1", "item1", patch, true)
		require.Equal(t, int32(http.StatusConflict), status.Code)
		require.Contains(t, status.Message, "1.0.0")
	})
}

func makeFleetWithOsRef(name, catalog, item, version string, channel *string) domain.Fleet {
	return domain.Fleet{
		Metadata: domain.ObjectMeta{Name: lo.ToPtr(name)},
		Spec: domain.FleetSpec{
			Template: struct {
				Metadata *domain.ObjectMeta `json:"metadata,omitempty"`
				Spec     domain.DeviceSpec  `json:"spec"`
			}{
				Spec: domain.DeviceSpec{
					Os: &domain.DeviceOsSpec{
						CatalogItemRef: &domain.CatalogItemRefSpec{
							Catalog: catalog,
							Item:    item,
							Version: version,
							Channel: channel,
						},
					},
				},
			},
		},
	}
}

func makeContainerAppSpec(t *testing.T, catalog, item, version string, channel *string, appName *string) domain.ApplicationProviderSpec {
	t.Helper()
	container := domain.ContainerApplication{
		AppType: domain.AppTypeContainer,
		Name:    appName,
	}
	err := container.FromCatalogItemRefApplicationProviderSpec(domain.CatalogItemRefApplicationProviderSpec{
		CatalogItemRef: domain.CatalogItemRefSpec{
			Catalog: catalog,
			Item:    item,
			Version: version,
			Channel: channel,
		},
	})
	require.NoError(t, err)
	var spec domain.ApplicationProviderSpec
	err = spec.FromContainerApplication(container)
	require.NoError(t, err)
	return spec
}

func makeDeviceWithOsRef(name, catalog, item, version string, channel *string) domain.Device {
	return domain.Device{
		Metadata: domain.ObjectMeta{Name: lo.ToPtr(name)},
		Spec: &domain.DeviceSpec{
			Os: &domain.DeviceOsSpec{
				CatalogItemRef: &domain.CatalogItemRefSpec{
					Catalog: catalog,
					Item:    item,
					Version: version,
					Channel: channel,
				},
			},
		},
	}
}

func makeContainerAppWithVolumeRef(t *testing.T, catalog, item, version string, channel *string, appName *string) domain.ApplicationProviderSpec {
	t.Helper()
	vol := domain.ApplicationVolume{Name: "data-vol"}
	err := vol.FromImageVolumeProviderSpec(domain.ImageVolumeProviderSpec{
		Image: domain.ImageVolumeSource{
			CatalogItemRef: &domain.CatalogItemRefSpec{
				Catalog: catalog,
				Item:    item,
				Version: version,
				Channel: channel,
			},
		},
	})
	require.NoError(t, err)
	container := domain.ContainerApplication{
		AppType: domain.AppTypeContainer,
		Name:    appName,
		Volumes: &[]domain.ApplicationVolume{vol},
	}
	err = container.FromImageApplicationProviderSpec(domain.ImageApplicationProviderSpec{
		Image: "quay.io/example/app:latest",
	})
	require.NoError(t, err)
	var spec domain.ApplicationProviderSpec
	err = spec.FromContainerApplication(container)
	require.NoError(t, err)
	return spec
}

func TestGetCatalogItemDeployments(t *testing.T) {
	type expectedDeployedTo struct {
		resourceKind string
		resourceName string
	}

	tests := []struct {
		name              string
		catalogName       string
		itemName          string
		osDevices         []domain.Device
		appDevices        []domain.Device
		volDevices        []domain.Device
		osFleets          []domain.Fleet
		appFleets         []domain.Fleet
		volFleets         []domain.Fleet
		deviceStoreErr    error
		fleetStoreErr     error
		expectStatusCode  int32
		expectDeployments int
		expectAppNames    []string
		expectVersions    []string
		expectDeployedTo  []expectedDeployedTo
	}{
		{
			name:              "When no devices or fleets reference the catalog item it should return an empty list",
			catalogName:       "my-catalog",
			itemName:          "my-item",
			expectStatusCode:  http.StatusOK,
			expectDeployments: 0,
		},
		{
			name:        "When a device has an OS catalog item ref it should return one deployment with DeployedTo Device",
			catalogName: "my-catalog",
			itemName:    "my-item",
			osDevices: []domain.Device{
				makeDeviceWithOsRef("dev1", "my-catalog", "my-item", "1.0.0", nil),
			},
			expectStatusCode:  http.StatusOK,
			expectDeployments: 1,
			expectVersions:    []string{"1.0.0"},
			expectDeployedTo:  []expectedDeployedTo{{resourceKind: domain.DeviceKind, resourceName: "dev1"}},
		},
		{
			name:        "When a device has an app catalog item ref it should return one deployment with the app name",
			catalogName: "my-catalog",
			itemName:    "my-item",
			appDevices: []domain.Device{
				{
					Metadata: domain.ObjectMeta{Name: lo.ToPtr("dev1")},
				},
			},
			expectStatusCode:  http.StatusOK,
			expectDeployments: 1,
			expectAppNames:    []string{"web-app"},
			expectVersions:    []string{"2.0.0"},
			expectDeployedTo:  []expectedDeployedTo{{resourceKind: domain.DeviceKind, resourceName: "dev1"}},
		},
		{
			name:        "When devices have both OS and app catalog item refs it should return all deployments",
			catalogName: "my-catalog",
			itemName:    "my-item",
			osDevices: []domain.Device{
				makeDeviceWithOsRef("dev1", "my-catalog", "my-item", "1.0.0", lo.ToPtr("stable")),
			},
			appDevices: []domain.Device{
				{
					Metadata: domain.ObjectMeta{Name: lo.ToPtr("dev2")},
				},
			},
			expectStatusCode:  http.StatusOK,
			expectDeployments: 2,
			expectDeployedTo: []expectedDeployedTo{
				{resourceKind: domain.DeviceKind, resourceName: "dev1"},
				{resourceKind: domain.DeviceKind, resourceName: "dev2"},
			},
		},
		{
			name:        "When a device has a volume catalog item ref it should return one deployment with the app name",
			catalogName: "my-catalog",
			itemName:    "my-item",
			volDevices: []domain.Device{
				{
					Metadata: domain.ObjectMeta{Name: lo.ToPtr("dev1")},
				},
			},
			expectStatusCode:  http.StatusOK,
			expectDeployments: 1,
			expectAppNames:    []string{"data-app"},
			expectVersions:    []string{"3.0.0"},
			expectDeployedTo:  []expectedDeployedTo{{resourceKind: domain.DeviceKind, resourceName: "dev1"}},
		},
		{
			name:        "When devices have OS, app, and volume catalog item refs it should return all deployments",
			catalogName: "my-catalog",
			itemName:    "my-item",
			osDevices: []domain.Device{
				makeDeviceWithOsRef("dev1", "my-catalog", "my-item", "1.0.0", nil),
			},
			appDevices: []domain.Device{
				{
					Metadata: domain.ObjectMeta{Name: lo.ToPtr("dev2")},
				},
			},
			volDevices: []domain.Device{
				{
					Metadata: domain.ObjectMeta{Name: lo.ToPtr("dev3")},
				},
			},
			expectStatusCode:  http.StatusOK,
			expectDeployments: 3,
			expectDeployedTo: []expectedDeployedTo{
				{resourceKind: domain.DeviceKind, resourceName: "dev1"},
				{resourceKind: domain.DeviceKind, resourceName: "dev2"},
				{resourceKind: domain.DeviceKind, resourceName: "dev3"},
			},
		},
		{
			name:        "When a fleet has an OS catalog item ref it should return one deployment with DeployedTo Fleet",
			catalogName: "my-catalog",
			itemName:    "my-item",
			osFleets: []domain.Fleet{
				makeFleetWithOsRef("fleet1", "my-catalog", "my-item", "1.0.0", nil),
			},
			expectStatusCode:  http.StatusOK,
			expectDeployments: 1,
			expectVersions:    []string{"1.0.0"},
			expectDeployedTo:  []expectedDeployedTo{{resourceKind: domain.FleetKind, resourceName: "fleet1"}},
		},
		{
			name:        "When both devices and fleets reference the catalog item it should return all deployments",
			catalogName: "my-catalog",
			itemName:    "my-item",
			osDevices: []domain.Device{
				makeDeviceWithOsRef("dev1", "my-catalog", "my-item", "1.0.0", nil),
			},
			osFleets: []domain.Fleet{
				makeFleetWithOsRef("fleet1", "my-catalog", "my-item", "2.0.0", nil),
			},
			expectStatusCode:  http.StatusOK,
			expectDeployments: 2,
			expectDeployedTo: []expectedDeployedTo{
				{resourceKind: domain.DeviceKind, resourceName: "dev1"},
				{resourceKind: domain.FleetKind, resourceName: "fleet1"},
			},
		},
		{
			name:             "When the device store returns an error it should return an internal server error",
			catalogName:      "my-catalog",
			itemName:         "my-item",
			deviceStoreErr:   fmt.Errorf("db connection lost"),
			expectStatusCode: http.StatusInternalServerError,
		},
		{
			name:             "When the fleet store returns an error it should return an internal server error",
			catalogName:      "my-catalog",
			itemName:         "my-item",
			fleetStoreErr:    fmt.Errorf("db connection lost"),
			expectStatusCode: http.StatusInternalServerError,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ds := newFakeDeviceStore()
			ds.err = tt.deviceStoreErr
			fs := newFakeFleetStore()
			fs.err = tt.fleetStoreErr

			if tt.osDevices != nil {
				ds.osDevices[tt.catalogName+"/"+tt.itemName] = &domain.DeviceList{Items: tt.osDevices}
			}

			if tt.appDevices != nil {
				for i := range tt.appDevices {
					if tt.appDevices[i].Spec == nil {
						appName := "web-app"
						appSpec := makeContainerAppSpec(t, tt.catalogName, tt.itemName, "2.0.0", nil, &appName)
						tt.appDevices[i].Spec = &domain.DeviceSpec{
							Applications: &[]domain.ApplicationProviderSpec{appSpec},
						}
					}
				}
				ds.appDevices[tt.catalogName+"/"+tt.itemName] = &domain.DeviceList{Items: tt.appDevices}
			}

			if tt.volDevices != nil {
				for i := range tt.volDevices {
					if tt.volDevices[i].Spec == nil {
						appName := "data-app"
						appSpec := makeContainerAppWithVolumeRef(t, tt.catalogName, tt.itemName, "3.0.0", nil, &appName)
						tt.volDevices[i].Spec = &domain.DeviceSpec{
							Applications: &[]domain.ApplicationProviderSpec{appSpec},
						}
					}
				}
				ds.volDevices[tt.catalogName+"/"+tt.itemName] = &domain.DeviceList{Items: tt.volDevices}
			}

			if tt.osFleets != nil {
				fs.osFleets[tt.catalogName+"/"+tt.itemName] = &domain.FleetList{Items: tt.osFleets}
			}

			if tt.appFleets != nil {
				fs.appFleets[tt.catalogName+"/"+tt.itemName] = &domain.FleetList{Items: tt.appFleets}
			}

			if tt.volFleets != nil {
				fs.volFleets[tt.catalogName+"/"+tt.itemName] = &domain.FleetList{Items: tt.volFleets}
			}

			h, _, _ := newTestHandlerWithStores(ds, fs)
			result, status := h.GetCatalogItemDeployments(context.Background(), uuid.New(), tt.catalogName, tt.itemName, domain.GetCatalogItemDeploymentsParams{})

			require.Equal(t, tt.expectStatusCode, status.Code)

			if tt.expectStatusCode != http.StatusOK {
				return
			}

			require.NotNil(t, result)
			require.Equal(t, domain.QualifiedV1Alpha1, result.ApiVersion)
			require.Equal(t, domain.CatalogItemDeploymentListKind, result.Kind)
			require.Len(t, result.Items, tt.expectDeployments)

			for _, dep := range result.Items {
				require.Equal(t, tt.catalogName, dep.Catalog)
				require.Equal(t, tt.itemName, dep.CatalogItem)
				require.Equal(t, domain.QualifiedV1Alpha1, dep.ApiVersion)
				require.Equal(t, domain.CatalogItemDeploymentKind, dep.Kind)
				require.NotNil(t, dep.DeployedTo)
				require.NotNil(t, dep.DeployedTo.ResourceKind)
				require.NotNil(t, dep.DeployedTo.ResourceName)
			}

			if tt.expectAppNames != nil {
				var appNames []string
				for _, dep := range result.Items {
					if dep.ApplicationName != nil {
						appNames = append(appNames, *dep.ApplicationName)
					}
				}
				require.ElementsMatch(t, tt.expectAppNames, appNames)
			}

			if tt.expectVersions != nil {
				var versions []string
				for _, dep := range result.Items {
					versions = append(versions, dep.Version)
				}
				require.ElementsMatch(t, tt.expectVersions, versions)
			}

			if tt.expectDeployedTo != nil {
				var actual []expectedDeployedTo
				for _, dep := range result.Items {
					actual = append(actual, expectedDeployedTo{
						resourceKind: *dep.DeployedTo.ResourceKind,
						resourceName: *dep.DeployedTo.ResourceName,
					})
				}
				require.ElementsMatch(t, tt.expectDeployedTo, actual)
			}
		})
	}
}

func TestGetCatalogItemDeploymentsPagination(t *testing.T) {
	ds := newFakeDeviceStore()
	fs := newFakeFleetStore()

	catalogName := "my-catalog"
	itemName := "my-item"

	ds.osDevices[catalogName+"/"+itemName] = &domain.DeviceList{
		Items: []domain.Device{
			makeDeviceWithOsRef("dev-a", catalogName, itemName, "1.0.0", nil),
			makeDeviceWithOsRef("dev-b", catalogName, itemName, "1.0.0", nil),
			makeDeviceWithOsRef("dev-c", catalogName, itemName, "1.0.0", nil),
		},
	}
	fs.osFleets[catalogName+"/"+itemName] = &domain.FleetList{
		Items: []domain.Fleet{
			makeFleetWithOsRef("fleet-a", catalogName, itemName, "1.0.0", nil),
			makeFleetWithOsRef("fleet-b", catalogName, itemName, "1.0.0", nil),
		},
	}

	h, _, _ := newTestHandlerWithStores(ds, fs)

	t.Run("When limit is set it should return at most limit items with a continue token", func(t *testing.T) {
		limit := int32(2)
		result, status := h.GetCatalogItemDeployments(context.Background(), uuid.New(), catalogName, itemName, domain.GetCatalogItemDeploymentsParams{
			Limit: &limit,
		})
		require.Equal(t, int32(http.StatusOK), status.Code)
		require.Len(t, result.Items, 2)
		require.NotNil(t, result.Metadata.Continue)
		require.NotNil(t, result.Metadata.RemainingItemCount)
		require.Equal(t, int64(3), *result.Metadata.RemainingItemCount)
	})

	t.Run("When continue token is provided it should return the next page", func(t *testing.T) {
		limit := int32(2)
		result1, status := h.GetCatalogItemDeployments(context.Background(), uuid.New(), catalogName, itemName, domain.GetCatalogItemDeploymentsParams{
			Limit: &limit,
		})
		require.Equal(t, int32(http.StatusOK), status.Code)
		require.NotNil(t, result1.Metadata.Continue)

		result2, status := h.GetCatalogItemDeployments(context.Background(), uuid.New(), catalogName, itemName, domain.GetCatalogItemDeploymentsParams{
			Limit:    &limit,
			Continue: result1.Metadata.Continue,
		})
		require.Equal(t, int32(http.StatusOK), status.Code)
		require.Len(t, result2.Items, 2)
		require.NotNil(t, result2.Metadata.Continue)
		require.Equal(t, int64(1), *result2.Metadata.RemainingItemCount)

		result3, status := h.GetCatalogItemDeployments(context.Background(), uuid.New(), catalogName, itemName, domain.GetCatalogItemDeploymentsParams{
			Limit:    &limit,
			Continue: result2.Metadata.Continue,
		})
		require.Equal(t, int32(http.StatusOK), status.Code)
		require.Len(t, result3.Items, 1)
		require.Nil(t, result3.Metadata.Continue)
	})

	t.Run("When no limit is set it should return all items without a continue token", func(t *testing.T) {
		result, status := h.GetCatalogItemDeployments(context.Background(), uuid.New(), catalogName, itemName, domain.GetCatalogItemDeploymentsParams{})
		require.Equal(t, int32(http.StatusOK), status.Code)
		require.Len(t, result.Items, 5)
		require.Nil(t, result.Metadata.Continue)
	})

	t.Run("When paginating it should return all items across pages without duplicates", func(t *testing.T) {
		limit := int32(2)
		var allItems []domain.CatalogItemDeployment
		params := domain.GetCatalogItemDeploymentsParams{Limit: &limit}

		for {
			result, status := h.GetCatalogItemDeployments(context.Background(), uuid.New(), catalogName, itemName, params)
			require.Equal(t, int32(http.StatusOK), status.Code)
			allItems = append(allItems, result.Items...)
			if result.Metadata.Continue == nil {
				break
			}
			params.Continue = result.Metadata.Continue
		}

		require.Len(t, allItems, 5)
		seen := make(map[string]bool)
		for _, dep := range allItems {
			key := *dep.DeployedTo.ResourceKind + "/" + *dep.DeployedTo.ResourceName
			require.False(t, seen[key], "duplicate deployment: %s", key)
			seen[key] = true
		}
	})
}
