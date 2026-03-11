package tasks

import (
	"context"
	"embed"
	"io/fs"
	"net/http"
	"testing"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/service"
	"github.com/flightctl/flightctl/internal/util"
	billy "github.com/go-git/go-billy/v5"
	"github.com/go-git/go-billy/v5/memfs"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

//go:embed testdata/catalog_initial/*
var catalogInitialFS embed.FS

//go:embed testdata/catalog_updated/*
var catalogUpdatedFS embed.FS

//go:embed testdata/catalog_multiversion/*
var catalogMultiversionFS embed.FS

// copyEmbedToMemfs copies an embed.FS subtree into a billy memfs at the given target path.
func copyEmbedToMemfs(efs embed.FS, root string, mfs billy.Filesystem, target string) error {
	return fs.WalkDir(efs, root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		// Compute relative path from root
		rel := path[len(root):]
		if rel == "" {
			return nil
		}
		dest := target + rel

		if d.IsDir() {
			return mfs.MkdirAll(dest, 0o755)
		}
		data, err := efs.ReadFile(path)
		if err != nil {
			return err
		}
		f, err := mfs.Create(dest)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = f.Write(data)
		return err
	})
}

// loadFixtures copies the embed.FS into a memfs and extracts resources.
func loadFixtures(t *testing.T, efs embed.FS, root string) []GenericResourceMap {
	t.Helper()
	mfs := memfs.New()
	err := copyEmbedToMemfs(efs, root, mfs, "/catalog")
	require.NoError(t, err)

	rs := NewResourceSync(nil, logrus.New(), nil, nil)
	resources, err := rs.extractResourcesFromDir(mfs, "/catalog")
	require.NoError(t, err)
	return resources
}

func okStatus() domain.Status {
	return domain.Status{Code: http.StatusOK}
}

func createdStatus() domain.Status {
	return domain.Status{Code: http.StatusCreated}
}

func notFoundStatus() domain.Status {
	return domain.Status{Code: http.StatusNotFound, Message: "not found"}
}

func newTestRS(name string) *domain.ResourceSync {
	return &domain.ResourceSync{
		Metadata: domain.ObjectMeta{
			Name:       lo.ToPtr(name),
			Generation: lo.ToPtr(int64(1)),
		},
		Spec: domain.ResourceSyncSpec{
			Repository:     "test-repo",
			TargetRevision: "main",
			Path:           "/catalog",
		},
		Status: &domain.ResourceSyncStatus{
			Conditions: []domain.Condition{},
		},
	}
}

func TestSyncCatalogs_InitialCreate(t *testing.T) {
	ctrl := gomock.NewController(t)

	resources := loadFixtures(t, catalogInitialFS, "testdata/catalog_initial")

	mockSvc := service.NewMockService(ctrl)
	log := logrus.New()
	rs := NewResourceSync(mockSvc, log, nil, nil)

	orgId := uuid.New()
	resourceName := "test-rs"
	rsObj := newTestRS(resourceName)
	owner := util.SetResourceOwner(domain.ResourceSyncKind, resourceName)

	// Parse catalogs and items
	catalogResources := filterByKind(resources, domain.CatalogKind)
	itemResources := filterByKind(resources, domain.CatalogItemKind)

	catalogs, err := rs.parseCatalogs(catalogResources)
	require.NoError(t, err)
	assert.Len(t, catalogs, 1)

	items, err := rs.parseCatalogItems(itemResources)
	require.NoError(t, err)
	assert.Len(t, items, 2)

	// --- SyncCatalogs expectations ---
	// GetCatalog for conflict check (not found = no conflict)
	mockSvc.EXPECT().GetCatalog(gomock.Any(), orgId, "platform-apps").
		Return(nil, notFoundStatus())

	// ListCatalogs for pre-owned (empty -- first sync)
	mockSvc.EXPECT().ListCatalogs(gomock.Any(), orgId, gomock.Any()).
		Return(&domain.CatalogList{Items: []domain.Catalog{}}, okStatus())

	// ReplaceCatalog for platform-apps (created)
	mockSvc.EXPECT().ReplaceCatalog(gomock.Any(), orgId, "platform-apps", gomock.Any()).
		Return(&domain.Catalog{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr("platform-apps"), Owner: owner},
		}, createdStatus())

	catalogsToRemove, err := rs.SyncCatalogs(context.Background(), log, orgId, rsObj, catalogs, resourceName)
	require.NoError(t, err)
	assert.Empty(t, catalogsToRemove)

	// --- SyncCatalogItems expectations ---
	// GetCatalogItem conflict checks (not found)
	mockSvc.EXPECT().GetCatalogItem(gomock.Any(), orgId, "platform-apps", "prometheus").
		Return(nil, notFoundStatus())
	mockSvc.EXPECT().GetCatalogItem(gomock.Any(), orgId, "platform-apps", "nginx").
		Return(nil, notFoundStatus())
	// GetCatalog parent ownership checks (not found -- catalog is new)
	mockSvc.EXPECT().GetCatalog(gomock.Any(), orgId, "platform-apps").
		Return(&domain.Catalog{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr("platform-apps"), Owner: owner},
		}, okStatus())

	// ListAllCatalogItems for pre-owned (empty)
	mockSvc.EXPECT().ListAllCatalogItems(gomock.Any(), orgId, gomock.Any()).
		Return(&domain.CatalogItemList{Items: []domain.CatalogItem{}}, okStatus())

	// ReplaceCatalogItem for prometheus and nginx
	mockSvc.EXPECT().ReplaceCatalogItem(gomock.Any(), orgId, "platform-apps", "prometheus", gomock.Any()).
		Return(&domain.CatalogItem{
			Metadata: domain.CatalogItemMeta{Name: lo.ToPtr("prometheus"), Catalog: "platform-apps", Owner: owner},
		}, createdStatus())
	mockSvc.EXPECT().ReplaceCatalogItem(gomock.Any(), orgId, "platform-apps", "nginx", gomock.Any()).
		Return(&domain.CatalogItem{
			Metadata: domain.CatalogItemMeta{Name: lo.ToPtr("nginx"), Catalog: "platform-apps", Owner: owner},
		}, createdStatus())

	itemsToRemove, err := rs.SyncCatalogItems(context.Background(), log, orgId, rsObj, items, resourceName)
	require.NoError(t, err)
	assert.Empty(t, itemsToRemove)
}

func TestSyncCatalogs_UpdateAndRemove(t *testing.T) {
	ctrl := gomock.NewController(t)

	resources := loadFixtures(t, catalogUpdatedFS, "testdata/catalog_updated")

	mockSvc := service.NewMockService(ctrl)
	log := logrus.New()
	rs := NewResourceSync(mockSvc, log, nil, nil)

	orgId := uuid.New()
	resourceName := "test-rs"
	rsObj := newTestRS(resourceName)
	owner := util.SetResourceOwner(domain.ResourceSyncKind, resourceName)

	catalogResources := filterByKind(resources, domain.CatalogKind)
	itemResources := filterByKind(resources, domain.CatalogItemKind)

	catalogs, err := rs.parseCatalogs(catalogResources)
	require.NoError(t, err)

	items, err := rs.parseCatalogItems(itemResources)
	require.NoError(t, err)
	assert.Len(t, items, 2) // prometheus (updated) + redis (new)

	// --- SyncCatalogs ---
	// Conflict check: catalog exists, owned by us
	mockSvc.EXPECT().GetCatalog(gomock.Any(), orgId, "platform-apps").
		Return(&domain.Catalog{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr("platform-apps"), Owner: owner},
		}, okStatus())

	// ListCatalogs pre-owned: platform-apps already exists
	mockSvc.EXPECT().ListCatalogs(gomock.Any(), orgId, gomock.Any()).
		Return(&domain.CatalogList{
			Items: []domain.Catalog{
				{Metadata: domain.ObjectMeta{Name: lo.ToPtr("platform-apps"), Owner: owner}},
			},
		}, okStatus())

	// ReplaceCatalog (update)
	mockSvc.EXPECT().ReplaceCatalog(gomock.Any(), orgId, "platform-apps", gomock.Any()).
		Return(&domain.Catalog{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr("platform-apps"), Owner: owner},
		}, okStatus())

	catalogsToRemove, err := rs.SyncCatalogs(context.Background(), log, orgId, rsObj, catalogs, resourceName)
	require.NoError(t, err)
	assert.Empty(t, catalogsToRemove) // platform-apps still present

	// --- SyncCatalogItems ---
	// Conflict checks
	mockSvc.EXPECT().GetCatalogItem(gomock.Any(), orgId, "platform-apps", "prometheus").
		Return(&domain.CatalogItem{
			Metadata: domain.CatalogItemMeta{Name: lo.ToPtr("prometheus"), Catalog: "platform-apps", Owner: owner},
		}, okStatus())
	mockSvc.EXPECT().GetCatalogItem(gomock.Any(), orgId, "platform-apps", "redis").
		Return(nil, notFoundStatus())

	// Parent catalog ownership check
	mockSvc.EXPECT().GetCatalog(gomock.Any(), orgId, "platform-apps").
		Return(&domain.Catalog{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr("platform-apps"), Owner: owner},
		}, okStatus())

	// Pre-owned items: prometheus + nginx (nginx will be stale)
	mockSvc.EXPECT().ListAllCatalogItems(gomock.Any(), orgId, gomock.Any()).
		Return(&domain.CatalogItemList{
			Items: []domain.CatalogItem{
				{Metadata: domain.CatalogItemMeta{Name: lo.ToPtr("prometheus"), Catalog: "platform-apps", Owner: owner}},
				{Metadata: domain.CatalogItemMeta{Name: lo.ToPtr("nginx"), Catalog: "platform-apps", Owner: owner}},
			},
		}, okStatus())

	// ReplaceCatalogItem for prometheus (update) and redis (create)
	mockSvc.EXPECT().ReplaceCatalogItem(gomock.Any(), orgId, "platform-apps", "prometheus", gomock.Any()).
		Return(&domain.CatalogItem{
			Metadata: domain.CatalogItemMeta{Name: lo.ToPtr("prometheus"), Catalog: "platform-apps", Owner: owner},
		}, okStatus())
	mockSvc.EXPECT().ReplaceCatalogItem(gomock.Any(), orgId, "platform-apps", "redis", gomock.Any()).
		Return(&domain.CatalogItem{
			Metadata: domain.CatalogItemMeta{Name: lo.ToPtr("redis"), Catalog: "platform-apps", Owner: owner},
		}, createdStatus())

	itemsToRemove, err := rs.SyncCatalogItems(context.Background(), log, orgId, rsObj, items, resourceName)
	require.NoError(t, err)
	assert.Equal(t, []string{"platform-apps/nginx"}, itemsToRemove)

	// --- Delete stale items ---
	mockSvc.EXPECT().DeleteCatalogItem(gomock.Any(), orgId, "platform-apps", "nginx").
		Return(okStatus())

	err = rs.deleteStaleCatalogItems(context.Background(), log, orgId, rsObj, itemsToRemove, resourceName)
	require.NoError(t, err)
}

func TestSyncCatalogs_RemoveAll(t *testing.T) {
	ctrl := gomock.NewController(t)

	mockSvc := service.NewMockService(ctrl)
	log := logrus.New()
	rs := NewResourceSync(mockSvc, log, nil, nil)

	orgId := uuid.New()
	resourceName := "test-rs"
	rsObj := newTestRS(resourceName)
	owner := util.SetResourceOwner(domain.ResourceSyncKind, resourceName)

	preOwnedCatalogs := []domain.Catalog{
		{Metadata: domain.ObjectMeta{Name: lo.ToPtr("platform-apps"), Owner: owner}},
	}
	preOwnedItems := []domain.CatalogItem{
		{Metadata: domain.CatalogItemMeta{Name: lo.ToPtr("prometheus"), Catalog: "platform-apps", Owner: owner}},
		{Metadata: domain.CatalogItemMeta{Name: lo.ToPtr("nginx"), Catalog: "platform-apps", Owner: owner}},
	}

	// Empty resource set -- SyncCatalogs lists pre-owned and returns them all as stale.
	mockSvc.EXPECT().ListCatalogs(gomock.Any(), orgId, gomock.Any()).Return(
		&domain.CatalogList{Items: preOwnedCatalogs, Metadata: domain.ListMeta{}}, okStatus())

	catalogsToRemove, err := rs.SyncCatalogs(context.Background(), log, orgId, rsObj, nil, resourceName)
	require.NoError(t, err)
	assert.Equal(t, []string{"platform-apps"}, catalogsToRemove)

	// Empty resource set -- SyncCatalogItems lists pre-owned and returns them all as stale.
	mockSvc.EXPECT().ListAllCatalogItems(gomock.Any(), orgId, gomock.Any()).Return(
		&domain.CatalogItemList{Items: preOwnedItems, Metadata: domain.ListMeta{}}, okStatus())

	itemsToRemove, err := rs.SyncCatalogItems(context.Background(), log, orgId, rsObj, nil, resourceName)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"platform-apps/prometheus", "platform-apps/nginx"}, itemsToRemove)

	// Delete items first, then catalogs
	mockSvc.EXPECT().DeleteCatalogItem(gomock.Any(), orgId, "platform-apps", "prometheus").Return(okStatus())
	mockSvc.EXPECT().DeleteCatalogItem(gomock.Any(), orgId, "platform-apps", "nginx").Return(okStatus())

	err = rs.deleteStaleCatalogItems(context.Background(), log, orgId, rsObj, itemsToRemove, resourceName)
	require.NoError(t, err)

	mockSvc.EXPECT().DeleteCatalog(gomock.Any(), orgId, "platform-apps").Return(okStatus())

	err = rs.deleteStaleCatalogs(context.Background(), log, orgId, rsObj, catalogsToRemove, resourceName)
	require.NoError(t, err)
}

func TestSyncCatalogs_DeleteOrdering(t *testing.T) {
	ctrl := gomock.NewController(t)

	mockSvc := service.NewMockService(ctrl)
	log := logrus.New()
	rs := NewResourceSync(mockSvc, log, nil, nil)

	orgId := uuid.New()
	resourceName := "test-rs"
	rsObj := newTestRS(resourceName)

	itemsToRemove := []string{"platform-apps/prometheus", "platform-apps/nginx"}
	catalogsToRemove := []string{"platform-apps"}

	// Enforce ordering: all item deletes happen before catalog deletes.
	deleteItem1 := mockSvc.EXPECT().DeleteCatalogItem(gomock.Any(), orgId, "platform-apps", "prometheus").Return(okStatus())
	deleteItem2 := mockSvc.EXPECT().DeleteCatalogItem(gomock.Any(), orgId, "platform-apps", "nginx").Return(okStatus())
	mockSvc.EXPECT().DeleteCatalog(gomock.Any(), orgId, "platform-apps").
		Return(okStatus()).
		After(deleteItem1).
		After(deleteItem2)

	// Delete items first
	err := rs.deleteStaleCatalogItems(context.Background(), log, orgId, rsObj, itemsToRemove, resourceName)
	require.NoError(t, err)

	// Then delete catalogs
	err = rs.deleteStaleCatalogs(context.Background(), log, orgId, rsObj, catalogsToRemove, resourceName)
	require.NoError(t, err)
}

func TestSyncCatalogs_MultiversionParse(t *testing.T) {
	resources := loadFixtures(t, catalogMultiversionFS, "testdata/catalog_multiversion")

	rs := NewResourceSync(nil, logrus.New(), nil, nil)

	catalogResources := filterByKind(resources, domain.CatalogKind)
	itemResources := filterByKind(resources, domain.CatalogItemKind)

	catalogs, err := rs.parseCatalogs(catalogResources)
	require.NoError(t, err)
	assert.Len(t, catalogs, 1)
	assert.Equal(t, "infrastructure", *catalogs[0].Metadata.Name)
	assert.NotNil(t, catalogs[0].Spec.DisplayName)
	assert.Equal(t, "Infrastructure", *catalogs[0].Spec.DisplayName)

	items, err := rs.parseCatalogItems(itemResources)
	require.NoError(t, err)
	assert.Len(t, items, 3)

	// Build a lookup by name for stable assertion order
	itemMap := make(map[string]*domain.CatalogItem)
	for _, item := range items {
		itemMap[*item.Metadata.Name] = item
	}

	// flightctl -- helm, 2 versions with replaces chain
	fc := itemMap["flightctl"]
	require.NotNil(t, fc)
	assert.Equal(t, domain.CatalogItemType("helm"), fc.Spec.Type)
	require.Len(t, fc.Spec.Artifacts, 1)
	assert.Equal(t, "quay.io/flightctl/charts/flightctl", fc.Spec.Artifacts[0].Uri)
	require.Len(t, fc.Spec.Versions, 2)

	assert.Equal(t, "1.0.0", fc.Spec.Versions[0].Version)
	assert.Equal(t, "v1.0.0", fc.Spec.Versions[0].References["container"])
	assert.ElementsMatch(t, []string{"stable", "candidate"}, fc.Spec.Versions[0].Channels)
	assert.Nil(t, fc.Spec.Versions[0].Replaces)

	assert.Equal(t, "1.0.2", fc.Spec.Versions[1].Version)
	assert.Equal(t, "v1.0.2", fc.Spec.Versions[1].References["container"])
	assert.ElementsMatch(t, []string{"stable", "candidate"}, fc.Spec.Versions[1].Channels)
	require.NotNil(t, fc.Spec.Versions[1].Replaces)
	assert.Equal(t, "1.0.0", *fc.Spec.Versions[1].Replaces)

	// prometheus -- quadlet, 3 versions with linear replaces chain
	prom := itemMap["prometheus"]
	require.NotNil(t, prom)
	assert.Equal(t, domain.CatalogItemType("quadlet"), prom.Spec.Type)
	require.Len(t, prom.Spec.Versions, 3)

	assert.Equal(t, "1.7.0", prom.Spec.Versions[0].Version)
	assert.Equal(t, []string{"stable"}, prom.Spec.Versions[0].Channels)
	assert.Nil(t, prom.Spec.Versions[0].Replaces)

	assert.Equal(t, "1.8.0", prom.Spec.Versions[1].Version)
	assert.ElementsMatch(t, []string{"stable", "candidate"}, prom.Spec.Versions[1].Channels)
	assert.Equal(t, "1.7.0", *prom.Spec.Versions[1].Replaces)

	assert.Equal(t, "1.9.0", prom.Spec.Versions[2].Version)
	assert.Equal(t, []string{"candidate"}, prom.Spec.Versions[2].Channels)
	assert.Equal(t, "1.8.0", *prom.Spec.Versions[2].Replaces)

	// caddy -- container, 2 versions
	caddy := itemMap["caddy"]
	require.NotNil(t, caddy)
	assert.Equal(t, domain.CatalogItemType("container"), caddy.Spec.Type)
	require.Len(t, caddy.Spec.Versions, 2)
	assert.Equal(t, "2.7.6", caddy.Spec.Versions[0].Version)
	assert.Equal(t, "2.8.4", caddy.Spec.Versions[1].Version)
	assert.Equal(t, "2.7.6", *caddy.Spec.Versions[1].Replaces)
}

func TestSyncCatalogs_MultiversionSync(t *testing.T) {
	ctrl := gomock.NewController(t)

	resources := loadFixtures(t, catalogMultiversionFS, "testdata/catalog_multiversion")

	mockSvc := service.NewMockService(ctrl)
	log := logrus.New()
	rs := NewResourceSync(mockSvc, log, nil, nil)

	orgId := uuid.New()
	resourceName := "test-rs"
	rsObj := newTestRS(resourceName)
	owner := util.SetResourceOwner(domain.ResourceSyncKind, resourceName)

	catalogResources := filterByKind(resources, domain.CatalogKind)
	itemResources := filterByKind(resources, domain.CatalogItemKind)

	catalogs, err := rs.parseCatalogs(catalogResources)
	require.NoError(t, err)

	items, err := rs.parseCatalogItems(itemResources)
	require.NoError(t, err)
	require.Len(t, items, 3)

	// --- SyncCatalogs ---
	mockSvc.EXPECT().GetCatalog(gomock.Any(), orgId, "infrastructure").
		Return(nil, notFoundStatus())
	mockSvc.EXPECT().ListCatalogs(gomock.Any(), orgId, gomock.Any()).
		Return(&domain.CatalogList{Items: []domain.Catalog{}}, okStatus())
	mockSvc.EXPECT().ReplaceCatalog(gomock.Any(), orgId, "infrastructure", gomock.Any()).
		Return(&domain.Catalog{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr("infrastructure"), Owner: owner},
		}, createdStatus())

	catalogsToRemove, err := rs.SyncCatalogs(context.Background(), log, orgId, rsObj, catalogs, resourceName)
	require.NoError(t, err)
	assert.Empty(t, catalogsToRemove)

	// --- SyncCatalogItems ---
	// Conflict checks -- all new
	mockSvc.EXPECT().GetCatalogItem(gomock.Any(), orgId, "infrastructure", "flightctl").
		Return(nil, notFoundStatus())
	mockSvc.EXPECT().GetCatalogItem(gomock.Any(), orgId, "infrastructure", "prometheus").
		Return(nil, notFoundStatus())
	mockSvc.EXPECT().GetCatalogItem(gomock.Any(), orgId, "infrastructure", "caddy").
		Return(nil, notFoundStatus())

	// Parent ownership -- catalog owned by us
	mockSvc.EXPECT().GetCatalog(gomock.Any(), orgId, "infrastructure").
		Return(&domain.Catalog{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr("infrastructure"), Owner: owner},
		}, okStatus())

	// Pre-owned: empty
	mockSvc.EXPECT().ListAllCatalogItems(gomock.Any(), orgId, gomock.Any()).
		Return(&domain.CatalogItemList{Items: []domain.CatalogItem{}}, okStatus())

	// Capture what gets passed to ReplaceCatalogItem to verify version data round-trips.
	var capturedItems []domain.CatalogItem
	mockSvc.EXPECT().ReplaceCatalogItem(gomock.Any(), orgId, "infrastructure", gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, _ uuid.UUID, catalogName, itemName string, item domain.CatalogItem) (*domain.CatalogItem, domain.Status) {
			capturedItems = append(capturedItems, item)
			return &domain.CatalogItem{
				Metadata: domain.CatalogItemMeta{Name: lo.ToPtr(itemName), Catalog: catalogName, Owner: owner},
			}, createdStatus()
		}).Times(3)

	itemsToRemove, err := rs.SyncCatalogItems(context.Background(), log, orgId, rsObj, items, resourceName)
	require.NoError(t, err)
	assert.Empty(t, itemsToRemove)

	// Verify the captured items preserve multi-version data
	require.Len(t, capturedItems, 3)

	capturedMap := make(map[string]domain.CatalogItem)
	for _, item := range capturedItems {
		capturedMap[*item.Metadata.Name] = item
	}

	// Helm item should have 2 versions with replaces
	fc := capturedMap["flightctl"]
	require.Len(t, fc.Spec.Versions, 2)
	assert.Equal(t, "1.0.0", fc.Spec.Versions[0].Version)
	assert.Equal(t, "1.0.2", fc.Spec.Versions[1].Version)
	assert.Equal(t, "1.0.0", *fc.Spec.Versions[1].Replaces)

	// Quadlet item should have 3 versions with linear replaces chain
	prom := capturedMap["prometheus"]
	require.Len(t, prom.Spec.Versions, 3)
	assert.Nil(t, prom.Spec.Versions[0].Replaces)
	assert.Equal(t, "1.7.0", *prom.Spec.Versions[1].Replaces)
	assert.Equal(t, "1.8.0", *prom.Spec.Versions[2].Replaces)

	// Container item should have 2 versions
	caddy := capturedMap["caddy"]
	require.Len(t, caddy.Spec.Versions, 2)
	assert.Equal(t, "2.7.6", *caddy.Spec.Versions[1].Replaces)
}
