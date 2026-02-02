package domain

import v1alpha1 "github.com/flightctl/flightctl/api/core/v1alpha1"

// Catalog domain types use v1alpha1 as the internal representation.
// Catalog resources are only available in v1alpha1 (alpha-stage feature).

type Catalog = v1alpha1.Catalog
type CatalogList = v1alpha1.CatalogList
type CatalogSpec = v1alpha1.CatalogSpec
type CatalogStatus = v1alpha1.CatalogStatus

type CatalogItem = v1alpha1.CatalogItem
type CatalogItemMeta = v1alpha1.CatalogItemMeta
type CatalogItemList = v1alpha1.CatalogItemList
type CatalogItemSpec = v1alpha1.CatalogItemSpec
type CatalogItemVersion = v1alpha1.CatalogItemVersion
type CatalogItemConfigurable = v1alpha1.CatalogItemConfigurable
type CatalogItemReference = v1alpha1.CatalogItemReference
type CatalogItemArtifact = v1alpha1.CatalogItemArtifact
type CatalogItemDeprecation = v1alpha1.CatalogItemDeprecation

type CatalogItemCategory = v1alpha1.CatalogItemCategory
type CatalogItemVisibility = v1alpha1.CatalogItemVisibility
type CatalogItemType = v1alpha1.CatalogItemType
type CatalogItemArtifactType = v1alpha1.CatalogItemArtifactType

const (
	CatalogItemCategorySystem      = v1alpha1.CatalogItemCategorySystem
	CatalogItemCategoryApplication = v1alpha1.CatalogItemCategoryApplication

	CatalogItemVisibilityDraft     = v1alpha1.CatalogItemVisibilityDraft
	CatalogItemVisibilityPublished = v1alpha1.CatalogItemVisibilityPublished

	CatalogItemTypeOS        = v1alpha1.CatalogItemTypeOS
	CatalogItemTypeFirmware  = v1alpha1.CatalogItemTypeFirmware
	CatalogItemTypeDriver    = v1alpha1.CatalogItemTypeDriver
	CatalogItemTypeContainer = v1alpha1.CatalogItemTypeContainer
	CatalogItemTypeHelm      = v1alpha1.CatalogItemTypeHelm
	CatalogItemTypeQuadlet   = v1alpha1.CatalogItemTypeQuadlet
	CatalogItemTypeCompose   = v1alpha1.CatalogItemTypeCompose
	CatalogItemTypeData      = v1alpha1.CatalogItemTypeData

	// CatalogItemArtifactType constants (bootc-image-builder output formats)
	CatalogItemArtifactTypeContainer   = v1alpha1.CatalogItemArtifactTypeContainer
	CatalogItemArtifactTypeQcow2       = v1alpha1.CatalogItemArtifactTypeQcow2
	CatalogItemArtifactTypeAmi         = v1alpha1.CatalogItemArtifactTypeAmi
	CatalogItemArtifactTypeIso         = v1alpha1.CatalogItemArtifactTypeIso
	CatalogItemArtifactTypeAnacondaIso = v1alpha1.CatalogItemArtifactTypeAnacondaIso
	CatalogItemArtifactTypeVmdk        = v1alpha1.CatalogItemArtifactTypeVmdk
	CatalogItemArtifactTypeVhd         = v1alpha1.CatalogItemArtifactTypeVhd
	CatalogItemArtifactTypeRaw         = v1alpha1.CatalogItemArtifactTypeRaw
	CatalogItemArtifactTypeGce         = v1alpha1.CatalogItemArtifactTypeGce
)

type ListCatalogsParams = v1alpha1.ListCatalogsParams
type ListCatalogItemsParams = v1alpha1.ListCatalogItemsParams
