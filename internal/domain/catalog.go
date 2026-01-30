package domain

import v1beta1 "github.com/flightctl/flightctl/api/core/v1beta1"

type Catalog = v1beta1.Catalog
type CatalogList = v1beta1.CatalogList
type CatalogSpec = v1beta1.CatalogSpec
type CatalogStatus = v1beta1.CatalogStatus

type CatalogItem = v1beta1.CatalogItem
type CatalogItemList = v1beta1.CatalogItemList
type CatalogItemSpec = v1beta1.CatalogItemSpec
type CatalogItemVersion = v1beta1.CatalogItemVersion
type CatalogItemConfigurable = v1beta1.CatalogItemConfigurable
type CatalogItemReference = v1beta1.CatalogItemReference
type CatalogItemRelatedReference = v1beta1.CatalogItemRelatedReference
type CatalogItemDeprecation = v1beta1.CatalogItemDeprecation

type CatalogItemCategory = v1beta1.CatalogItemCategory
type CatalogItemVisibility = v1beta1.CatalogItemVisibility
type CatalogItemType = v1beta1.CatalogItemType
type CatalogItemArtifactType = v1beta1.CatalogItemArtifactType

const (
	CatalogItemCategorySystem      = v1beta1.CatalogItemCategorySystem
	CatalogItemCategoryApplication = v1beta1.CatalogItemCategoryApplication
	CatalogItemCategoryAsset       = v1beta1.CatalogItemCategoryAsset

	CatalogItemVisibilityDraft     = v1beta1.CatalogItemVisibilityDraft
	CatalogItemVisibilityPublished = v1beta1.CatalogItemVisibilityPublished

	// CatalogItemType constants
	CatalogItemTypeOS        = v1beta1.CatalogItemTypeOS
	CatalogItemTypeFirmware  = v1beta1.CatalogItemTypeFirmware
	CatalogItemTypeContainer = v1beta1.CatalogItemTypeContainer
	CatalogItemTypeHelm      = v1beta1.CatalogItemTypeHelm
	CatalogItemTypeQuadlet   = v1beta1.CatalogItemTypeQuadlet
	CatalogItemTypeCompose   = v1beta1.CatalogItemTypeCompose
	CatalogItemTypeData      = v1beta1.CatalogItemTypeData

	// CatalogItemArtifactType constants (bootc-image-builder output formats)
	CatalogItemArtifactTypeContainer   = v1beta1.CatalogItemArtifactTypeContainer
	CatalogItemArtifactTypeQcow2       = v1beta1.CatalogItemArtifactTypeQcow2
	CatalogItemArtifactTypeAmi         = v1beta1.CatalogItemArtifactTypeAmi
	CatalogItemArtifactTypeIso         = v1beta1.CatalogItemArtifactTypeIso
	CatalogItemArtifactTypeAnacondaIso = v1beta1.CatalogItemArtifactTypeAnacondaIso
	CatalogItemArtifactTypeVmdk        = v1beta1.CatalogItemArtifactTypeVmdk
	CatalogItemArtifactTypeVhd         = v1beta1.CatalogItemArtifactTypeVhd
	CatalogItemArtifactTypeRaw         = v1beta1.CatalogItemArtifactTypeRaw
	CatalogItemArtifactTypeGce         = v1beta1.CatalogItemArtifactTypeGce
)

type ListCatalogsParams = v1beta1.ListCatalogsParams
type ListCatalogItemsParams = v1beta1.ListCatalogItemsParams
