package service

import (
	"context"
	"fmt"
	"time"

	coredomain "github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/imagebuilder_api/domain"
	flightctlstore "github.com/flightctl/flightctl/internal/store"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"gorm.io/gorm"
)

// DummyImagePromotionStore is an in-memory implementation of ibstore.ImagePromotionStore.
type DummyImagePromotionStore struct {
	promotions map[string]*domain.ImagePromotion
	nextRV     int64
}

func NewDummyImagePromotionStore() *DummyImagePromotionStore {
	return &DummyImagePromotionStore{
		promotions: make(map[string]*domain.ImagePromotion),
		nextRV:     1,
	}
}

func (s *DummyImagePromotionStore) Create(ctx context.Context, orgId uuid.UUID, pub *domain.ImagePromotion) (*domain.ImagePromotion, error) {
	name := lo.FromPtr(pub.Metadata.Name)
	if name == "" {
		return nil, flterrors.ErrResourceNameIsNil
	}
	if _, exists := s.promotions[name]; exists {
		return nil, flterrors.ErrDuplicateName
	}
	var created domain.ImagePromotion
	deepCopy(pub, &created)
	rv := s.nextRV
	s.nextRV++
	created.Metadata.ResourceVersion = lo.ToPtr(fmt.Sprintf("%d", rv))
	now := time.Now()
	created.Metadata.CreationTimestamp = &now
	s.promotions[name] = &created
	return &created, nil
}

func (s *DummyImagePromotionStore) Get(ctx context.Context, orgId uuid.UUID, name string) (*domain.ImagePromotion, error) {
	p, ok := s.promotions[name]
	if !ok {
		return nil, flterrors.ErrResourceNotFound
	}
	var result domain.ImagePromotion
	deepCopy(p, &result)
	return &result, nil
}

func (s *DummyImagePromotionStore) List(ctx context.Context, orgId uuid.UUID, listParams flightctlstore.ListParams) (*domain.ImagePromotionList, error) {
	items := make([]domain.ImagePromotion, 0, len(s.promotions))
	for _, p := range s.promotions {
		var item domain.ImagePromotion
		deepCopy(p, &item)
		items = append(items, item)
	}
	return &domain.ImagePromotionList{Items: items}, nil
}

func (s *DummyImagePromotionStore) Delete(ctx context.Context, orgId uuid.UUID, name string) (*domain.ImagePromotion, error) {
	p, ok := s.promotions[name]
	if !ok {
		return nil, nil
	}
	var deleted domain.ImagePromotion
	deepCopy(p, &deleted)
	delete(s.promotions, name)
	return &deleted, nil
}

func (s *DummyImagePromotionStore) Update(ctx context.Context, orgId uuid.UUID, pub *domain.ImagePromotion) (*domain.ImagePromotion, error) {
	name := lo.FromPtr(pub.Metadata.Name)
	if _, ok := s.promotions[name]; !ok {
		return nil, flterrors.ErrResourceNotFound
	}
	var updated domain.ImagePromotion
	deepCopy(pub, &updated)
	rv := s.nextRV
	s.nextRV++
	updated.Metadata.ResourceVersion = lo.ToPtr(fmt.Sprintf("%d", rv))
	s.promotions[name] = &updated
	return &updated, nil
}

func (s *DummyImagePromotionStore) UpdateStatus(ctx context.Context, orgId uuid.UUID, pub *domain.ImagePromotion) (*domain.ImagePromotion, error) {
	name := lo.FromPtr(pub.Metadata.Name)
	existing, ok := s.promotions[name]
	if !ok {
		return nil, flterrors.ErrResourceNotFound
	}
	var updated domain.ImagePromotion
	deepCopy(existing, &updated)
	updated.Status = pub.Status
	rv := s.nextRV
	s.nextRV++
	updated.Metadata.ResourceVersion = lo.ToPtr(fmt.Sprintf("%d", rv))
	s.promotions[name] = &updated
	return &updated, nil
}

func (s *DummyImagePromotionStore) ListPendingForBuild(ctx context.Context, orgId uuid.UUID, imageBuildRef string) ([]domain.ImagePromotion, error) {
	var result []domain.ImagePromotion
	for _, p := range s.promotions {
		if p.Spec.Source.ImageBuildRef != imageBuildRef {
			continue
		}
		reason := getPromotionReadyReason(p)
		if reason == string(domain.ImagePromotionConditionReasonWaitingForArtifacts) ||
			reason == string(domain.ImagePromotionConditionReasonAmendmentFailed) {
			var item domain.ImagePromotion
			deepCopy(p, &item)
			result = append(result, item)
		}
	}
	return result, nil
}

func (s *DummyImagePromotionStore) InitialMigration(ctx context.Context) error {
	return nil
}

// DummyCatalogStore is an in-memory implementation of catalogstore.Store.
// It is used by both ImagePromotionService (for catalog reads) and DummyCatalogItemWriter (for writes).
type DummyCatalogStore struct {
	catalogs map[string]bool
	items    map[string]*coredomain.CatalogItem // key: "catalogName/itemName"
}

func NewDummyCatalogStore() *DummyCatalogStore {
	return &DummyCatalogStore{
		catalogs: make(map[string]bool),
		items:    make(map[string]*coredomain.CatalogItem),
	}
}

func (s *DummyCatalogStore) AddCatalog(name string) {
	s.catalogs[name] = true
}

func (s *DummyCatalogStore) AddItem(catalogName string, item *coredomain.CatalogItem) {
	key := catalogName + "/" + lo.FromPtr(item.Metadata.Name)
	var stored coredomain.CatalogItem
	deepCopy(item, &stored)
	s.items[key] = &stored
}

func (s *DummyCatalogStore) GetStoredItem(catalogName, itemName string) *coredomain.CatalogItem {
	item := s.items[catalogName+"/"+itemName]
	if item == nil {
		return nil
	}
	var result coredomain.CatalogItem
	deepCopy(item, &result)
	return &result
}

func (s *DummyCatalogStore) InitialMigration(ctx context.Context) error { return nil }

func (s *DummyCatalogStore) Get(ctx context.Context, orgId uuid.UUID, name string) (*coredomain.Catalog, error) {
	if !s.catalogs[name] {
		return nil, flterrors.ErrResourceNotFound
	}
	return &coredomain.Catalog{
		Metadata: coredomain.ObjectMeta{Name: lo.ToPtr(name)},
	}, nil
}

func (s *DummyCatalogStore) GetItem(ctx context.Context, orgId uuid.UUID, catalogName string, itemName string) (*coredomain.CatalogItem, error) {
	key := catalogName + "/" + itemName
	item, ok := s.items[key]
	if !ok {
		return nil, flterrors.ErrResourceNotFound
	}
	var result coredomain.CatalogItem
	deepCopy(item, &result)
	return &result, nil
}

func (s *DummyCatalogStore) CreateItem(ctx context.Context, orgId uuid.UUID, catalogName string, item *coredomain.CatalogItem) (*coredomain.CatalogItem, error) {
	if !s.catalogs[catalogName] {
		return nil, flterrors.ErrParentResourceNotFound
	}
	key := catalogName + "/" + lo.FromPtr(item.Metadata.Name)
	if _, exists := s.items[key]; exists {
		return nil, flterrors.ErrDuplicateName
	}
	var stored coredomain.CatalogItem
	deepCopy(item, &stored)
	s.items[key] = &stored
	return &stored, nil
}

func (s *DummyCatalogStore) UpdateItem(ctx context.Context, orgId uuid.UUID, catalogName string, item *coredomain.CatalogItem) (*coredomain.CatalogItem, error) {
	key := catalogName + "/" + lo.FromPtr(item.Metadata.Name)
	if _, exists := s.items[key]; !exists {
		return nil, flterrors.ErrResourceNotFound
	}
	var stored coredomain.CatalogItem
	deepCopy(item, &stored)
	s.items[key] = &stored
	return &stored, nil
}

func (s *DummyCatalogStore) CreateOrUpdateItem(ctx context.Context, orgId uuid.UUID, catalogName string, item *coredomain.CatalogItem) (*coredomain.CatalogItem, bool, error) {
	if !s.catalogs[catalogName] {
		return nil, false, flterrors.ErrParentResourceNotFound
	}
	key := catalogName + "/" + lo.FromPtr(item.Metadata.Name)
	_, created := s.items[key]
	created = !created
	var stored coredomain.CatalogItem
	deepCopy(item, &stored)
	s.items[key] = &stored
	return &stored, created, nil
}

func (s *DummyCatalogStore) Create(ctx context.Context, orgId uuid.UUID, catalog *coredomain.Catalog, callbackEvent flightctlstore.EventCallback) (*coredomain.Catalog, error) {
	return nil, nil
}
func (s *DummyCatalogStore) Update(ctx context.Context, orgId uuid.UUID, catalog *coredomain.Catalog, callbackEvent flightctlstore.EventCallback) (*coredomain.Catalog, error) {
	return nil, nil
}
func (s *DummyCatalogStore) CreateOrUpdate(ctx context.Context, orgId uuid.UUID, catalog *coredomain.Catalog, callbackEvent flightctlstore.EventCallback) (*coredomain.Catalog, bool, error) {
	return nil, false, nil
}
func (s *DummyCatalogStore) List(ctx context.Context, orgId uuid.UUID, listParams flightctlstore.ListParams) (*coredomain.CatalogList, error) {
	return &coredomain.CatalogList{}, nil
}
func (s *DummyCatalogStore) Delete(ctx context.Context, orgId uuid.UUID, name string, callback flightctlstore.RemoveOwnerCallback, callbackEvent flightctlstore.EventCallback) error {
	return nil
}
func (s *DummyCatalogStore) UpdateStatus(ctx context.Context, orgId uuid.UUID, resource *coredomain.Catalog, eventCallback flightctlstore.EventCallback) (*coredomain.Catalog, error) {
	return nil, nil
}
func (s *DummyCatalogStore) Count(ctx context.Context, orgId uuid.UUID, listParams flightctlstore.ListParams) (int64, error) {
	return 0, nil
}
func (s *DummyCatalogStore) UnsetOwner(ctx context.Context, tx *gorm.DB, orgId uuid.UUID, owner string) error {
	return nil
}
func (s *DummyCatalogStore) UnsetItemOwner(ctx context.Context, tx *gorm.DB, orgId uuid.UUID, owner string) error {
	return nil
}
func (s *DummyCatalogStore) ListAllItems(ctx context.Context, orgId uuid.UUID, listParams flightctlstore.ListParams) (*coredomain.CatalogItemList, error) {
	return &coredomain.CatalogItemList{}, nil
}
func (s *DummyCatalogStore) ListItems(ctx context.Context, orgId uuid.UUID, catalogName string, listParams flightctlstore.ListParams) (*coredomain.CatalogItemList, error) {
	return &coredomain.CatalogItemList{}, nil
}
func (s *DummyCatalogStore) DeleteItem(ctx context.Context, orgId uuid.UUID, catalogName string, itemName string) error {
	return nil
}
