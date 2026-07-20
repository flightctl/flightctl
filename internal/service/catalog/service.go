package catalog

import (
	"context"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/google/uuid"
)

type Service interface {
	CreateCatalog(ctx context.Context, orgId uuid.UUID, catalog domain.Catalog) (*domain.Catalog, domain.Status)
	ListCatalogs(ctx context.Context, orgId uuid.UUID, params domain.ListCatalogsParams) (*domain.CatalogList, domain.Status)
	GetCatalog(ctx context.Context, orgId uuid.UUID, name string) (*domain.Catalog, domain.Status)
	ReplaceCatalog(ctx context.Context, orgId uuid.UUID, name string, catalog domain.Catalog, enforceOwnership bool) (*domain.Catalog, domain.Status)
	DeleteCatalog(ctx context.Context, orgId uuid.UUID, name string, enforceOwnership bool) domain.Status
	PatchCatalog(ctx context.Context, orgId uuid.UUID, name string, patch domain.PatchRequest, enforceOwnership bool) (*domain.Catalog, domain.Status)
	GetCatalogStatus(ctx context.Context, orgId uuid.UUID, name string) (*domain.Catalog, domain.Status)
	ReplaceCatalogStatus(ctx context.Context, orgId uuid.UUID, name string, catalog domain.Catalog) (*domain.Catalog, domain.Status)
	PatchCatalogStatus(ctx context.Context, orgId uuid.UUID, name string, patch domain.PatchRequest) (*domain.Catalog, domain.Status)
	ListAllCatalogItems(ctx context.Context, orgId uuid.UUID, params domain.ListAllCatalogItemsParams) (*domain.CatalogItemList, domain.Status)
	ListCatalogItems(ctx context.Context, orgId uuid.UUID, catalogName string, params domain.ListCatalogItemsParams) (*domain.CatalogItemList, domain.Status)
	GetCatalogItem(ctx context.Context, orgId uuid.UUID, catalogName string, itemName string) (*domain.CatalogItem, domain.Status)
	CreateCatalogItem(ctx context.Context, orgId uuid.UUID, catalogName string, item domain.CatalogItem) (*domain.CatalogItem, domain.Status)
	ReplaceCatalogItem(ctx context.Context, orgId uuid.UUID, catalogName string, itemName string, item domain.CatalogItem, enforceOwnership bool) (*domain.CatalogItem, domain.Status)
	PatchCatalogItem(ctx context.Context, orgId uuid.UUID, catalogName string, itemName string, patch domain.PatchRequest, enforceOwnership bool) (*domain.CatalogItem, domain.Status)
	DeleteCatalogItem(ctx context.Context, orgId uuid.UUID, catalogName string, itemName string, enforceOwnership bool) domain.Status
}
