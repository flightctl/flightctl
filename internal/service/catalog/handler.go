package catalog

import (
	"context"
	"errors"
	"reflect"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/service/common"
	"github.com/flightctl/flightctl/internal/service/events"
	catalogstore "github.com/flightctl/flightctl/internal/store/catalog"
	"github.com/flightctl/flightctl/internal/store/selector"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

type ServiceHandler struct {
	store  catalogstore.Store
	events events.Service
	log    logrus.FieldLogger
}

// NewServiceHandler creates a new catalog ServiceHandler instance.
func NewServiceHandler(store catalogstore.Store, events events.Service, log logrus.FieldLogger) *ServiceHandler {
	return &ServiceHandler{store: store, events: events, log: log}
}

var _ Service = (*ServiceHandler)(nil)

// NilOutManagedCatalogItemMetaProperties clears the CatalogItemMeta fields that are managed
// by the service and must not be set by API callers. Catalog-specific; deliberately left
// un-relocated to internal/service/common (no other resource needs it).
func NilOutManagedCatalogItemMetaProperties(om *domain.CatalogItemMeta) {
	if om == nil {
		return
	}
	om.Generation = nil
	om.Owner = nil
	om.Annotations = nil
	om.CreationTimestamp = nil
	om.DeletionTimestamp = nil
}

// SanitizeCatalog clears status and managed metadata from an untrusted catalog document
// (HTTP body or ResourceSync YAML). Callers that must set Owner must not use this.
func SanitizeCatalog(catalog *domain.Catalog) {
	if catalog == nil {
		return
	}
	catalog.Status = nil
	common.NilOutManagedObjectMetaProperties(&catalog.Metadata)
}

// SanitizeCatalogItem clears managed metadata from an untrusted catalog item document.
func SanitizeCatalogItem(item *domain.CatalogItem) {
	if item == nil {
		return
	}
	NilOutManagedCatalogItemMetaProperties(&item.Metadata)
}

// CreateCatalogFromUntrusted sanitizes an untrusted catalog document, then creates it.
func CreateCatalogFromUntrusted(ctx context.Context, svc Service, orgId uuid.UUID, catalog domain.Catalog) (*domain.Catalog, domain.Status) {
	SanitizeCatalog(&catalog)
	return svc.CreateCatalog(ctx, orgId, catalog)
}

// ReplaceCatalogFromUntrusted sanitizes an untrusted catalog document, then replaces it.
func ReplaceCatalogFromUntrusted(ctx context.Context, svc Service, orgId uuid.UUID, name string, catalog domain.Catalog, enforceOwnership bool) (*domain.Catalog, domain.Status) {
	SanitizeCatalog(&catalog)
	return svc.ReplaceCatalog(ctx, orgId, name, catalog, enforceOwnership)
}

// CreateCatalogItemFromUntrusted sanitizes an untrusted catalog item document, then creates it.
func CreateCatalogItemFromUntrusted(ctx context.Context, svc Service, orgId uuid.UUID, catalogName string, item domain.CatalogItem) (*domain.CatalogItem, domain.Status) {
	SanitizeCatalogItem(&item)
	return svc.CreateCatalogItem(ctx, orgId, catalogName, item)
}

// ReplaceCatalogItemFromUntrusted sanitizes an untrusted catalog item document, then replaces it.
func ReplaceCatalogItemFromUntrusted(ctx context.Context, svc Service, orgId uuid.UUID, catalogName, itemName string, item domain.CatalogItem, enforceOwnership bool) (*domain.CatalogItem, domain.Status) {
	SanitizeCatalogItem(&item)
	return svc.ReplaceCatalogItem(ctx, orgId, catalogName, itemName, item, enforceOwnership)
}

func (h *ServiceHandler) CreateCatalog(ctx context.Context, orgId uuid.UUID, catalog domain.Catalog) (*domain.Catalog, domain.Status) {
	if errs := catalog.Validate(); len(errs) > 0 {
		return nil, domain.StatusBadRequest(errors.Join(errs...).Error())
	}

	result, err := h.store.Create(ctx, orgId, &catalog, h.callbackCatalogUpdated)
	return result, common.StoreErrorToApiStatus(err, true, domain.CatalogKind, catalog.Metadata.Name)
}

func (h *ServiceHandler) ListCatalogs(ctx context.Context, orgId uuid.UUID, params domain.ListCatalogsParams) (*domain.CatalogList, domain.Status) {
	listParams, status := common.PrepareListParams(params.Continue, params.LabelSelector, params.FieldSelector, params.Limit)
	if status != domain.StatusOK() {
		return nil, status
	}

	result, err := h.store.List(ctx, orgId, *listParams)
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
	result, err := h.store.Get(ctx, orgId, name)
	return result, common.StoreErrorToApiStatus(err, false, domain.CatalogKind, &name)
}

func (h *ServiceHandler) ReplaceCatalog(ctx context.Context, orgId uuid.UUID, name string, catalog domain.Catalog, enforceOwnership bool) (*domain.Catalog, domain.Status) {
	if errs := catalog.Validate(); len(errs) > 0 {
		return nil, domain.StatusBadRequest(errors.Join(errs...).Error())
	}
	if name != *catalog.Metadata.Name {
		return nil, domain.StatusBadRequest("resource name specified in metadata does not match name in path")
	}

	if enforceOwnership {
		existing, getErr := h.store.Get(ctx, orgId, name)
		if getErr != nil {
			if !errors.Is(getErr, flterrors.ErrResourceNotFound) {
				return nil, common.StoreErrorToApiStatus(getErr, false, domain.CatalogKind, &name)
			}
		} else if len(lo.FromPtr(existing.Metadata.Owner)) != 0 &&
			!reflect.DeepEqual(existing.Spec, catalog.Spec) {
			return nil, common.StoreErrorToApiStatus(flterrors.ErrUpdatingResourceWithOwnerNotAllowed, false, domain.CatalogKind, &name)
		}
	}

	result, created, err := h.store.CreateOrUpdate(ctx, orgId, &catalog, false, h.callbackCatalogUpdated)
	return result, common.StoreErrorToApiStatus(err, created, domain.CatalogKind, &name)
}

func (h *ServiceHandler) DeleteCatalog(ctx context.Context, orgId uuid.UUID, name string, enforceOwnership bool) domain.Status {
	c, err := h.store.Get(ctx, orgId, name)
	if err != nil {
		if errors.Is(err, flterrors.ErrResourceNotFound) {
			return domain.StatusOK() // idempotent delete
		}
		return common.StoreErrorToApiStatus(err, false, domain.CatalogKind, &name)
	}

	if enforceOwnership && len(lo.FromPtr(c.Metadata.Owner)) != 0 {
		return domain.StatusConflict(flterrors.ErrDeletingResourceWithOwnerNotAllowed.Error())
	}

	callback := func(ctx context.Context, tx *gorm.DB, orgId uuid.UUID, owner string) error {
		// No owned resources for Catalog currently
		return nil
	}

	err = h.store.Delete(ctx, orgId, name, callback, h.callbackCatalogDeleted)
	status := common.StoreErrorToApiStatus(err, false, domain.CatalogKind, &name)
	return status
}

// Only metadata.labels and spec can be patched. If we try to patch other fields, HTTP 400 Bad Request is returned.
func (h *ServiceHandler) PatchCatalog(ctx context.Context, orgId uuid.UUID, name string, patch domain.PatchRequest, enforceOwnership bool) (*domain.Catalog, domain.Status) {
	currentObj, err := h.store.Get(ctx, orgId, name)
	if err != nil {
		return nil, common.StoreErrorToApiStatus(err, false, domain.CatalogKind, &name)
	}

	newObj := &domain.Catalog{}
	err = common.ApplyJSONPatch(ctx, currentObj, newObj, patch, "/catalogs/"+name, domain.GetV1Alpha1Swagger)
	if err != nil {
		return nil, domain.StatusBadRequest(err.Error())
	}

	if errs := newObj.Validate(); len(errs) > 0 {
		return nil, domain.StatusBadRequest(errors.Join(errs...).Error())
	}
	if errs := currentObj.ValidateUpdate(newObj); len(errs) > 0 {
		return nil, domain.StatusBadRequest(errors.Join(errs...).Error())
	}

	common.NilOutManagedObjectMetaProperties(&newObj.Metadata)
	newObj.Metadata.ResourceVersion = nil

	if enforceOwnership &&
		len(lo.FromPtr(currentObj.Metadata.Owner)) != 0 &&
		!reflect.DeepEqual(currentObj.Spec, newObj.Spec) {
		return nil, common.StoreErrorToApiStatus(flterrors.ErrUpdatingResourceWithOwnerNotAllowed, false, domain.CatalogKind, &name)
	}

	result, err := h.store.Update(ctx, orgId, newObj, h.callbackCatalogUpdated)
	return result, common.StoreErrorToApiStatus(err, false, domain.CatalogKind, &name)
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

	result, err := h.store.UpdateStatus(ctx, orgId, &catalog, h.callbackCatalogUpdated)
	return result, common.StoreErrorToApiStatus(err, false, domain.CatalogKind, &name)
}

func (h *ServiceHandler) PatchCatalogStatus(ctx context.Context, orgId uuid.UUID, name string, patch domain.PatchRequest) (*domain.Catalog, domain.Status) {
	currentObj, err := h.store.Get(ctx, orgId, name)
	if err != nil {
		return nil, common.StoreErrorToApiStatus(err, false, domain.CatalogKind, &name)
	}

	newObj := &domain.Catalog{}
	err = common.ApplyJSONPatch(ctx, currentObj, newObj, patch, "/catalogs/"+name+"/status", domain.GetV1Alpha1Swagger)
	if err != nil {
		return nil, domain.StatusBadRequest(err.Error())
	}

	if errs := newObj.Validate(); len(errs) > 0 {
		return nil, domain.StatusBadRequest(errors.Join(errs...).Error())
	}

	result, err := h.store.UpdateStatus(ctx, orgId, newObj, h.callbackCatalogUpdated)
	return result, common.StoreErrorToApiStatus(err, false, domain.CatalogKind, &name)
}

func (h *ServiceHandler) ListAllCatalogItems(ctx context.Context, orgId uuid.UUID, params domain.ListAllCatalogItemsParams) (*domain.CatalogItemList, domain.Status) {
	listParams, status := common.PrepareListParams(params.Continue, params.LabelSelector, params.FieldSelector, params.Limit)
	if status != domain.StatusOK() {
		return nil, status
	}

	result, err := h.store.ListAllItems(ctx, orgId, *listParams)
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

func (h *ServiceHandler) ListCatalogItems(ctx context.Context, orgId uuid.UUID, catalogName string, params domain.ListCatalogItemsParams) (*domain.CatalogItemList, domain.Status) {
	listParams, status := common.PrepareListParams(params.Continue, params.LabelSelector, nil, params.Limit)
	if status != domain.StatusOK() {
		return nil, status
	}

	result, err := h.store.ListItems(ctx, orgId, catalogName, *listParams)
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
		return nil, common.StoreErrorToApiStatus(err, false, domain.CatalogKind, &catalogName)
	}
}

func (h *ServiceHandler) GetCatalogItem(ctx context.Context, orgId uuid.UUID, catalogName string, itemName string) (*domain.CatalogItem, domain.Status) {
	result, err := h.store.GetItem(ctx, orgId, catalogName, itemName)
	if errors.Is(err, flterrors.ErrParentResourceNotFound) {
		return nil, domain.StatusResourceNotFound(domain.CatalogKind, catalogName)
	}
	return result, common.StoreErrorToApiStatus(err, false, domain.CatalogItemKind, &itemName)
}

func (h *ServiceHandler) CreateCatalogItem(ctx context.Context, orgId uuid.UUID, catalogName string, item domain.CatalogItem) (*domain.CatalogItem, domain.Status) {
	if errs := item.Validate(); len(errs) > 0 {
		return nil, domain.StatusBadRequest(errors.Join(errs...).Error())
	}

	result, err := h.store.CreateItem(ctx, orgId, catalogName, &item)
	if errors.Is(err, flterrors.ErrParentResourceNotFound) {
		return nil, domain.StatusResourceNotFound(domain.CatalogKind, catalogName)
	}
	return result, common.StoreErrorToApiStatus(err, true, domain.CatalogItemKind, item.Metadata.Name)
}

func (h *ServiceHandler) ReplaceCatalogItem(ctx context.Context, orgId uuid.UUID, catalogName string, itemName string, item domain.CatalogItem, enforceOwnership bool) (*domain.CatalogItem, domain.Status) {
	if errs := item.Validate(); len(errs) > 0 {
		return nil, domain.StatusBadRequest(errors.Join(errs...).Error())
	}
	if itemName != *item.Metadata.Name {
		return nil, domain.StatusBadRequest("resource name specified in metadata does not match name in path")
	}

	if enforceOwnership {
		existing, getErr := h.store.GetItem(ctx, orgId, catalogName, itemName)
		if getErr != nil {
			if !errors.Is(getErr, flterrors.ErrResourceNotFound) && !errors.Is(getErr, flterrors.ErrParentResourceNotFound) {
				return nil, common.StoreErrorToApiStatus(getErr, false, domain.CatalogItemKind, &itemName)
			}
		} else if len(lo.FromPtr(existing.Metadata.Owner)) != 0 &&
			!reflect.DeepEqual(existing.Spec, item.Spec) {
			return nil, common.StoreErrorToApiStatus(flterrors.ErrUpdatingResourceWithOwnerNotAllowed, false, domain.CatalogItemKind, &itemName)
		}
	}

	result, created, err := h.store.CreateOrUpdateItem(ctx, orgId, catalogName, &item)
	if errors.Is(err, flterrors.ErrParentResourceNotFound) {
		return nil, domain.StatusResourceNotFound(domain.CatalogKind, catalogName)
	}
	return result, common.StoreErrorToApiStatus(err, created, domain.CatalogItemKind, &itemName)
}

func (h *ServiceHandler) PatchCatalogItem(ctx context.Context, orgId uuid.UUID, catalogName string, itemName string, patch domain.PatchRequest, enforceOwnership bool) (*domain.CatalogItem, domain.Status) {
	currentObj, err := h.store.GetItem(ctx, orgId, catalogName, itemName)
	if err != nil {
		if errors.Is(err, flterrors.ErrParentResourceNotFound) {
			return nil, domain.StatusResourceNotFound(domain.CatalogKind, catalogName)
		}
		return nil, common.StoreErrorToApiStatus(err, false, domain.CatalogItemKind, &itemName)
	}

	newObj := &domain.CatalogItem{}
	err = common.ApplyJSONPatch(ctx, currentObj, newObj, patch, "/catalogs/"+catalogName+"/items/"+itemName, domain.GetV1Alpha1Swagger)
	if err != nil {
		return nil, domain.StatusBadRequest(err.Error())
	}

	if errs := newObj.Validate(); len(errs) > 0 {
		return nil, domain.StatusBadRequest(errors.Join(errs...).Error())
	}

	if errs := currentObj.ValidateUpdate(newObj); len(errs) > 0 {
		return nil, domain.StatusBadRequest(errors.Join(errs...).Error())
	}

	NilOutManagedCatalogItemMetaProperties(&newObj.Metadata)
	newObj.Metadata.ResourceVersion = nil

	if enforceOwnership &&
		len(lo.FromPtr(currentObj.Metadata.Owner)) != 0 &&
		!reflect.DeepEqual(currentObj.Spec, newObj.Spec) {
		return nil, common.StoreErrorToApiStatus(flterrors.ErrUpdatingResourceWithOwnerNotAllowed, false, domain.CatalogItemKind, &itemName)
	}

	result, err := h.store.UpdateItem(ctx, orgId, catalogName, newObj)
	if errors.Is(err, flterrors.ErrParentResourceNotFound) {
		return nil, domain.StatusResourceNotFound(domain.CatalogKind, catalogName)
	}
	return result, common.StoreErrorToApiStatus(err, false, domain.CatalogItemKind, &itemName)
}

func (h *ServiceHandler) DeleteCatalogItem(ctx context.Context, orgId uuid.UUID, catalogName string, itemName string, enforceOwnership bool) domain.Status {
	existing, err := h.store.GetItem(ctx, orgId, catalogName, itemName)
	if err != nil {
		if errors.Is(err, flterrors.ErrResourceNotFound) || errors.Is(err, flterrors.ErrParentResourceNotFound) {
			return domain.StatusOK() // idempotent delete
		}
		return common.StoreErrorToApiStatus(err, false, domain.CatalogItemKind, &itemName)
	}

	if enforceOwnership && len(lo.FromPtr(existing.Metadata.Owner)) != 0 {
		return domain.StatusConflict(flterrors.ErrDeletingResourceWithOwnerNotAllowed.Error())
	}

	err = h.store.DeleteItem(ctx, orgId, catalogName, itemName)
	if errors.Is(err, flterrors.ErrParentResourceNotFound) {
		return domain.StatusResourceNotFound(domain.CatalogKind, catalogName)
	}
	return common.StoreErrorToApiStatus(err, false, domain.CatalogItemKind, &itemName)
}

// callbackCatalogUpdated is the catalog-specific callback that handles catalog events
func (h *ServiceHandler) callbackCatalogUpdated(ctx context.Context, resourceKind domain.ResourceKind, orgId uuid.UUID, name string, oldResource, newResource interface{}, created bool, err error) {
	if err != nil {
		status := common.StoreErrorToApiStatus(err, created, string(resourceKind), &name)
		h.events.CreateEvent(ctx, orgId, common.GetResourceCreatedOrUpdatedFailureEvent(ctx, created, resourceKind, name, status, nil))
	} else {
		// Compute ResourceUpdatedDetails for updates
		var updateDetails *domain.ResourceUpdatedDetails
		if !created {
			var (
				oldCatalog, newCatalog *domain.Catalog
				ok                     bool
			)
			if oldCatalog, newCatalog, ok = common.CastResources[domain.Catalog](oldResource, newResource); ok && oldCatalog != nil && newCatalog != nil {
				updateDetails = common.ComputeResourceUpdatedDetails(oldCatalog.Metadata, newCatalog.Metadata)
			}
		}
		h.events.CreateEvent(ctx, orgId, common.GetResourceCreatedOrUpdatedSuccessEvent(ctx, created, resourceKind, name, updateDetails, h.log, nil))
	}
}

// callbackCatalogDeleted is the catalog-specific callback that handles catalog deletion events
func (h *ServiceHandler) callbackCatalogDeleted(ctx context.Context, resourceKind domain.ResourceKind, orgId uuid.UUID, name string, oldResource, newResource interface{}, created bool, err error) {
	h.events.HandleGenericResourceDeletedEvents(ctx, resourceKind, orgId, name, oldResource, newResource, created, err)
}
