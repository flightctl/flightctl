package service

import (
	"context"
	"errors"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/store/selector"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

func (h *ServiceHandler) CreateCatalog(ctx context.Context, orgId uuid.UUID, catalog domain.Catalog) (*domain.Catalog, domain.Status) {
	// don't set fields that are managed by the service
	catalog.Status = nil
	NilOutManagedObjectMetaProperties(&catalog.Metadata)

	if errs := catalog.Validate(); len(errs) > 0 {
		return nil, domain.StatusBadRequest(errors.Join(errs...).Error())
	}

	result, err := h.store.Catalog().Create(ctx, orgId, &catalog, h.callbackCatalogUpdated)
	return result, StoreErrorToApiStatus(err, true, domain.CatalogKind, catalog.Metadata.Name)
}

func (h *ServiceHandler) ListCatalogs(ctx context.Context, orgId uuid.UUID, params domain.ListCatalogsParams) (*domain.CatalogList, domain.Status) {
	listParams, status := prepareListParams(params.Continue, params.LabelSelector, params.FieldSelector, params.Limit)
	if status != domain.StatusOK() {
		return nil, status
	}

	result, err := h.store.Catalog().List(ctx, orgId, *listParams)
	if err == nil {
		return result, domain.StatusOK()
	}

	var se *selector.SelectorError

	switch {
	case selector.AsSelectorError(err, &se):
		return nil, domain.StatusBadRequest(se.Error())
	default:
		return nil, domain.StatusInternalServerError(err.Error())
	}
}

func (h *ServiceHandler) GetCatalog(ctx context.Context, orgId uuid.UUID, name string) (*domain.Catalog, domain.Status) {
	result, err := h.store.Catalog().Get(ctx, orgId, name)
	return result, StoreErrorToApiStatus(err, false, domain.CatalogKind, &name)
}

func (h *ServiceHandler) ReplaceCatalog(ctx context.Context, orgId uuid.UUID, name string, catalog domain.Catalog) (*domain.Catalog, domain.Status) {
	// don't overwrite fields that are managed by the service
	catalog.Status = nil
	NilOutManagedObjectMetaProperties(&catalog.Metadata)
	if errs := catalog.Validate(); len(errs) > 0 {
		return nil, domain.StatusBadRequest(errors.Join(errs...).Error())
	}
	if name != *catalog.Metadata.Name {
		return nil, domain.StatusBadRequest("resource name specified in metadata does not match name in path")
	}

	result, created, err := h.store.Catalog().CreateOrUpdate(ctx, orgId, &catalog, h.callbackCatalogUpdated)
	return result, StoreErrorToApiStatus(err, created, domain.CatalogKind, &name)
}

func (h *ServiceHandler) DeleteCatalog(ctx context.Context, orgId uuid.UUID, name string) domain.Status {
	callback := func(ctx context.Context, tx *gorm.DB, orgId uuid.UUID, owner string) error {
		// No owned resources for Catalog currently
		return nil
	}

	err := h.store.Catalog().Delete(ctx, orgId, name, callback, h.callbackCatalogDeleted)
	status := StoreErrorToApiStatus(err, false, domain.CatalogKind, &name)
	return status
}

// Only metadata.labels and spec can be patched. If we try to patch other fields, HTTP 400 Bad Request is returned.
func (h *ServiceHandler) PatchCatalog(ctx context.Context, orgId uuid.UUID, name string, patch domain.PatchRequest) (*domain.Catalog, domain.Status) {
	currentObj, err := h.store.Catalog().Get(ctx, orgId, name)
	if err != nil {
		return nil, StoreErrorToApiStatus(err, false, domain.CatalogKind, &name)
	}

	newObj := &domain.Catalog{}
	err = ApplyJSONPatch(ctx, currentObj, newObj, patch, "/catalogs/"+name)
	if err != nil {
		return nil, domain.StatusBadRequest(err.Error())
	}

	if errs := newObj.Validate(); len(errs) > 0 {
		return nil, domain.StatusBadRequest(errors.Join(errs...).Error())
	}
	if errs := currentObj.ValidateUpdate(newObj); len(errs) > 0 {
		return nil, domain.StatusBadRequest(errors.Join(errs...).Error())
	}

	NilOutManagedObjectMetaProperties(&newObj.Metadata)
	newObj.Metadata.ResourceVersion = nil
	result, err := h.store.Catalog().Update(ctx, orgId, newObj, h.callbackCatalogUpdated)
	return result, StoreErrorToApiStatus(err, false, domain.CatalogKind, &name)
}

func (h *ServiceHandler) GetCatalogStatus(ctx context.Context, orgId uuid.UUID, name string) (*domain.Catalog, domain.Status) {
	return h.GetCatalog(ctx, orgId, name)
}

func (h *ServiceHandler) ReplaceCatalogStatus(ctx context.Context, orgId uuid.UUID, name string, catalog domain.Catalog) (*domain.Catalog, domain.Status) {
	if errs := catalog.Validate(); len(errs) > 0 {
		return nil, domain.StatusBadRequest(errors.Join(errs...).Error())
	}
	if name != *catalog.Metadata.Name {
		return nil, domain.StatusBadRequest("resource name specified in metadata does not match name in path")
	}

	result, err := h.store.Catalog().UpdateStatus(ctx, orgId, &catalog, h.callbackCatalogUpdated)
	return result, StoreErrorToApiStatus(err, false, domain.CatalogKind, &name)
}

func (h *ServiceHandler) PatchCatalogStatus(ctx context.Context, orgId uuid.UUID, name string, patch domain.PatchRequest) (*domain.Catalog, domain.Status) {
	currentObj, err := h.store.Catalog().Get(ctx, orgId, name)
	if err != nil {
		return nil, StoreErrorToApiStatus(err, false, domain.CatalogKind, &name)
	}

	newObj := &domain.Catalog{}
	err = ApplyJSONPatch(ctx, currentObj, newObj, patch, "/catalogs/"+name+"/status")
	if err != nil {
		return nil, domain.StatusBadRequest(err.Error())
	}

	if errs := newObj.Validate(); len(errs) > 0 {
		return nil, domain.StatusBadRequest(errors.Join(errs...).Error())
	}

	result, err := h.store.Catalog().UpdateStatus(ctx, orgId, newObj, h.callbackCatalogUpdated)
	return result, StoreErrorToApiStatus(err, false, domain.CatalogKind, &name)
}

func (h *ServiceHandler) ListCatalogItems(ctx context.Context, orgId uuid.UUID, catalogName string, params domain.ListCatalogItemsParams) (*domain.CatalogItemList, domain.Status) {
	listParams, status := prepareListParams(params.Continue, params.LabelSelector, nil, params.Limit)
	if status != domain.StatusOK() {
		return nil, status
	}

	result, err := h.store.Catalog().ListItems(ctx, orgId, catalogName, *listParams)
	if err == nil {
		return result, domain.StatusOK()
	}

	if errors.Is(err, flterrors.ErrParentResourceNotFound) {
		return nil, domain.StatusResourceNotFound(domain.CatalogKind, catalogName)
	}

	var se *selector.SelectorError

	switch {
	case selector.AsSelectorError(err, &se):
		return nil, domain.StatusBadRequest(se.Error())
	default:
		return nil, StoreErrorToApiStatus(err, false, domain.CatalogKind, &catalogName)
	}
}

func (h *ServiceHandler) GetCatalogItem(ctx context.Context, orgId uuid.UUID, catalogName string, itemName string) (*domain.CatalogItem, domain.Status) {
	result, err := h.store.Catalog().GetItem(ctx, orgId, catalogName, itemName)
	if errors.Is(err, flterrors.ErrParentResourceNotFound) {
		return nil, domain.StatusResourceNotFound(domain.CatalogKind, catalogName)
	}
	return result, StoreErrorToApiStatus(err, false, domain.CatalogItemKind, &itemName)
}

func (h *ServiceHandler) CreateCatalogItem(ctx context.Context, orgId uuid.UUID, catalogName string, item domain.CatalogItem) (*domain.CatalogItem, domain.Status) {
	NilOutManagedCatalogItemMetaProperties(&item.Metadata)

	if errs := item.Validate(); len(errs) > 0 {
		return nil, domain.StatusBadRequest(errors.Join(errs...).Error())
	}

	result, err := h.store.Catalog().CreateItem(ctx, orgId, catalogName, &item)
	if errors.Is(err, flterrors.ErrParentResourceNotFound) {
		return nil, domain.StatusResourceNotFound(domain.CatalogKind, catalogName)
	}
	return result, StoreErrorToApiStatus(err, true, domain.CatalogItemKind, item.Metadata.Name)
}

func (h *ServiceHandler) ReplaceCatalogItem(ctx context.Context, orgId uuid.UUID, catalogName string, itemName string, item domain.CatalogItem) (*domain.CatalogItem, domain.Status) {
	NilOutManagedCatalogItemMetaProperties(&item.Metadata)

	if errs := item.Validate(); len(errs) > 0 {
		return nil, domain.StatusBadRequest(errors.Join(errs...).Error())
	}
	if itemName != *item.Metadata.Name {
		return nil, domain.StatusBadRequest("resource name specified in metadata does not match name in path")
	}

	result, created, err := h.store.Catalog().CreateOrUpdateItem(ctx, orgId, catalogName, &item)
	if errors.Is(err, flterrors.ErrParentResourceNotFound) {
		return nil, domain.StatusResourceNotFound(domain.CatalogKind, catalogName)
	}
	return result, StoreErrorToApiStatus(err, created, domain.CatalogItemKind, &itemName)
}

func (h *ServiceHandler) DeleteCatalogItem(ctx context.Context, orgId uuid.UUID, catalogName string, itemName string) domain.Status {
	err := h.store.Catalog().DeleteItem(ctx, orgId, catalogName, itemName)
	if errors.Is(err, flterrors.ErrParentResourceNotFound) {
		return domain.StatusResourceNotFound(domain.CatalogKind, catalogName)
	}
	return StoreErrorToApiStatus(err, false, domain.CatalogItemKind, &itemName)
}

// callbackCatalogUpdated is the catalog-specific callback that handles catalog events
func (h *ServiceHandler) callbackCatalogUpdated(ctx context.Context, resourceKind domain.ResourceKind, orgId uuid.UUID, name string, oldResource, newResource interface{}, created bool, err error) {
	h.eventHandler.HandleCatalogUpdatedEvents(ctx, resourceKind, orgId, name, oldResource, newResource, created, err)
}

// callbackCatalogDeleted is the catalog-specific callback that handles catalog deletion events
func (h *ServiceHandler) callbackCatalogDeleted(ctx context.Context, resourceKind domain.ResourceKind, orgId uuid.UUID, name string, oldResource, newResource interface{}, created bool, err error) {
	h.eventHandler.HandleGenericResourceDeletedEvents(ctx, resourceKind, orgId, name, oldResource, newResource, created, err)
}
