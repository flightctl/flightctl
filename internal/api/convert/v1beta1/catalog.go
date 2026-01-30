package v1beta1

import (
	apiv1beta1 "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/domain"
)

// CatalogConverter converts between v1beta1 API types and domain types for Catalog resources.
type CatalogConverter interface {
	ToDomain(apiv1beta1.Catalog) domain.Catalog
	FromDomain(*domain.Catalog) *apiv1beta1.Catalog
	ListFromDomain(*domain.CatalogList) *apiv1beta1.CatalogList

	// CatalogItem conversions
	ItemToDomain(apiv1beta1.CatalogItem) domain.CatalogItem
	ItemFromDomain(*domain.CatalogItem) *apiv1beta1.CatalogItem
	ItemListFromDomain(*domain.CatalogItemList) *apiv1beta1.CatalogItemList

	// Params conversions
	ListParamsToDomain(apiv1beta1.ListCatalogsParams) domain.ListCatalogsParams
	ListItemsParamsToDomain(apiv1beta1.ListCatalogItemsParams) domain.ListCatalogItemsParams
}

type catalogConverter struct{}

// NewCatalogConverter creates a new CatalogConverter.
func NewCatalogConverter() CatalogConverter {
	return &catalogConverter{}
}

func (c *catalogConverter) ToDomain(catalog apiv1beta1.Catalog) domain.Catalog {
	return catalog
}

func (c *catalogConverter) FromDomain(catalog *domain.Catalog) *apiv1beta1.Catalog {
	return catalog
}

func (c *catalogConverter) ListFromDomain(l *domain.CatalogList) *apiv1beta1.CatalogList {
	return l
}

func (c *catalogConverter) ItemToDomain(item apiv1beta1.CatalogItem) domain.CatalogItem {
	return item
}

func (c *catalogConverter) ItemFromDomain(item *domain.CatalogItem) *apiv1beta1.CatalogItem {
	return item
}

func (c *catalogConverter) ItemListFromDomain(l *domain.CatalogItemList) *apiv1beta1.CatalogItemList {
	return l
}

func (c *catalogConverter) ListParamsToDomain(p apiv1beta1.ListCatalogsParams) domain.ListCatalogsParams {
	return p
}

func (c *catalogConverter) ListItemsParamsToDomain(p apiv1beta1.ListCatalogItemsParams) domain.ListCatalogItemsParams {
	return p
}
