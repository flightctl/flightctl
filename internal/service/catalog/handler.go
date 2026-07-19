package catalog

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/service/common"
	"github.com/flightctl/flightctl/internal/service/events"
	"github.com/flightctl/flightctl/internal/store"
	catalogstore "github.com/flightctl/flightctl/internal/store/catalog"
	devicestore "github.com/flightctl/flightctl/internal/store/device"
	fleetstore "github.com/flightctl/flightctl/internal/store/fleet"
	"github.com/flightctl/flightctl/internal/store/selector"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
)

type ServiceHandler struct {
	store       catalogstore.Store
	deviceStore devicestore.Store
	fleetStore  fleetstore.Store
	events      events.Service
	log         logrus.FieldLogger
}

// NewServiceHandler creates a new catalog ServiceHandler instance.
func NewServiceHandler(store catalogstore.Store, deviceStore devicestore.Store, fleetStore fleetstore.Store, events events.Service, log logrus.FieldLogger) *ServiceHandler {
	return &ServiceHandler{store: store, deviceStore: deviceStore, fleetStore: fleetStore, events: events, log: log}
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

	result, err := h.store.Create(ctx, orgId, &catalog)
	h.callbackCatalogUpdated(ctx, domain.CatalogKind, orgId, lo.FromPtr(catalog.Metadata.Name), nil, result, true, err)
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
			!domain.CatalogSpecsAreEqual(existing.Spec, catalog.Spec) {
			return nil, common.StoreErrorToApiStatus(flterrors.ErrUpdatingResourceWithOwnerNotAllowed, false, domain.CatalogKind, &name)
		}
	}

	result, oldCatalog, created, err := h.store.CreateOrUpdate(ctx, orgId, &catalog)
	h.callbackCatalogUpdated(ctx, domain.CatalogKind, orgId, name, oldCatalog, result, created, err)
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

	// Product rule: refuse deleting a non-empty catalog. The service chooses store.Delete
	// (TX primitive that returns ErrResourceNotEmpty when items exist) and maps the error.
	deleted, err := h.store.Delete(ctx, orgId, name)
	if err == nil && deleted {
		h.callbackCatalogDeleted(ctx, domain.CatalogKind, orgId, name, nil, nil, false, nil)
	}
	return common.StoreErrorToApiStatus(err, false, domain.CatalogKind, &name)
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
		!domain.CatalogSpecsAreEqual(currentObj.Spec, newObj.Spec) {
		return nil, common.StoreErrorToApiStatus(flterrors.ErrUpdatingResourceWithOwnerNotAllowed, false, domain.CatalogKind, &name)
	}

	result, oldCatalog, err := h.store.Update(ctx, orgId, newObj)
	h.callbackCatalogUpdated(ctx, domain.CatalogKind, orgId, name, oldCatalog, result, false, err)
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

	result, oldCatalog, err := h.store.UpdateStatus(ctx, orgId, &catalog)
	h.callbackCatalogUpdated(ctx, domain.CatalogKind, orgId, name, oldCatalog, result, false, err)
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

	result, oldCatalog, err := h.store.UpdateStatus(ctx, orgId, newObj)
	h.callbackCatalogUpdated(ctx, domain.CatalogKind, orgId, name, oldCatalog, result, false, err)
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

	existing, getErr := h.store.GetItem(ctx, orgId, catalogName, itemName)
	if getErr != nil {
		if !errors.Is(getErr, flterrors.ErrResourceNotFound) && !errors.Is(getErr, flterrors.ErrParentResourceNotFound) {
			return nil, common.StoreErrorToApiStatus(getErr, false, domain.CatalogItemKind, &itemName)
		}
		existing = nil
	}

	if existing != nil {
		if enforceOwnership && len(lo.FromPtr(existing.Metadata.Owner)) != 0 &&
			!domain.CatalogItemSpecsAreEqual(existing.Spec, item.Spec) {
			return nil, common.StoreErrorToApiStatus(flterrors.ErrUpdatingResourceWithOwnerNotAllowed, false, domain.CatalogItemKind, &itemName)
		}

		if status := h.validateInUseVersions(ctx, orgId, catalogName, itemName, existing.Spec, item.Spec); status.Code != 200 {
			return nil, status
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
		!domain.CatalogItemSpecsAreEqual(currentObj.Spec, newObj.Spec) {
		return nil, common.StoreErrorToApiStatus(flterrors.ErrUpdatingResourceWithOwnerNotAllowed, false, domain.CatalogItemKind, &itemName)
	}

	if status := h.validateInUseVersions(ctx, orgId, catalogName, itemName, currentObj.Spec, newObj.Spec); status.Code != 200 {
		return nil, status
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

	deployedVersions, dErr := h.getDeployedVersions(ctx, orgId, catalogName, itemName)
	if dErr != nil {
		return domain.StatusInternalServerError(dErr.Error())
	}
	if len(deployedVersions) > 0 {
		versions := make([]string, 0, len(deployedVersions))
		for v := range deployedVersions {
			versions = append(versions, v)
		}
		sort.Strings(versions)
		return domain.StatusConflict(fmt.Sprintf("cannot delete catalog item because the following versions are in use by devices or fleets: %s", strings.Join(versions, ", ")))
	}

	err = h.store.DeleteItem(ctx, orgId, catalogName, itemName)
	if errors.Is(err, flterrors.ErrParentResourceNotFound) {
		return domain.StatusResourceNotFound(domain.CatalogKind, catalogName)
	}
	return common.StoreErrorToApiStatus(err, false, domain.CatalogItemKind, &itemName)
}

func (h *ServiceHandler) getDeployedVersions(ctx context.Context, orgId uuid.UUID, catalogName, itemName string) (map[string]bool, error) {
	listParams := store.ListParams{Limit: common.MaxRecordsPerListRequest}
	versions := make(map[string]bool)

	if err := h.collectDeviceVersions(ctx, orgId, catalogName, itemName, listParams, versions); err != nil {
		return nil, err
	}
	if err := h.collectFleetVersions(ctx, orgId, catalogName, itemName, listParams, versions); err != nil {
		return nil, err
	}

	return versions, nil
}

func (h *ServiceHandler) collectDeviceVersions(ctx context.Context, orgId uuid.UUID, catalogName, itemName string, listParams store.ListParams, versions map[string]bool) error {
	if h.deviceStore == nil {
		return nil
	}

	if err := h.collectDeviceOsVersions(ctx, orgId, catalogName, itemName, listParams, versions); err != nil {
		return err
	}
	if err := h.collectDeviceAppVersions(ctx, orgId, catalogName, itemName, listParams, versions); err != nil {
		return err
	}
	return h.collectDeviceVolVersions(ctx, orgId, catalogName, itemName, listParams, versions)
}

func (h *ServiceHandler) collectDeviceOsVersions(ctx context.Context, orgId uuid.UUID, catalogName, itemName string, listParams store.ListParams, versions map[string]bool) error {
	for lp := listParams; ; {
		osDevices, err := h.deviceStore.ListDevicesByOsCatalogItemRef(ctx, orgId, catalogName, itemName, lp)
		if err != nil {
			return err
		}
		for _, dev := range osDevices.Items {
			if dev.Spec != nil && dev.Spec.Os != nil && dev.Spec.Os.CatalogItemRef != nil {
				versions[dev.Spec.Os.CatalogItemRef.Version] = true
			}
		}
		if osDevices.Metadata.Continue == nil {
			break
		}
		cont, err := store.ParseContinueString(osDevices.Metadata.Continue)
		if err != nil {
			return err
		}
		lp.Continue = cont
	}
	return nil
}

func (h *ServiceHandler) collectDeviceAppVersions(ctx context.Context, orgId uuid.UUID, catalogName, itemName string, listParams store.ListParams, versions map[string]bool) error {
	for lp := listParams; ; {
		appDevices, err := h.deviceStore.ListDevicesByAppCatalogItemRef(ctx, orgId, catalogName, itemName, lp)
		if err != nil {
			return err
		}
		for _, dev := range appDevices.Items {
			if dev.Spec == nil || dev.Spec.Applications == nil {
				continue
			}
			for _, app := range *dev.Spec.Applications {
				ref, _ := common.ExtractAppCatalogItemRef(&app)
				if ref != nil && ref.Catalog == catalogName && ref.Item == itemName {
					versions[ref.Version] = true
				}
			}
		}
		if appDevices.Metadata.Continue == nil {
			break
		}
		cont, err := store.ParseContinueString(appDevices.Metadata.Continue)
		if err != nil {
			return err
		}
		lp.Continue = cont
	}
	return nil
}

func (h *ServiceHandler) collectDeviceVolVersions(ctx context.Context, orgId uuid.UUID, catalogName, itemName string, listParams store.ListParams, versions map[string]bool) error {
	for lp := listParams; ; {
		volDevices, err := h.deviceStore.ListDevicesByVolumeCatalogItemRef(ctx, orgId, catalogName, itemName, lp)
		if err != nil {
			return err
		}
		for _, dev := range volDevices.Items {
			if dev.Spec == nil || dev.Spec.Applications == nil {
				continue
			}
			for _, app := range *dev.Spec.Applications {
				refs, _ := common.ExtractVolumeCatalogItemRefs(&app)
				for _, ref := range refs {
					if ref.Catalog == catalogName && ref.Item == itemName {
						versions[ref.Version] = true
					}
				}
			}
		}
		if volDevices.Metadata.Continue == nil {
			break
		}
		cont, err := store.ParseContinueString(volDevices.Metadata.Continue)
		if err != nil {
			return err
		}
		lp.Continue = cont
	}
	return nil
}

func (h *ServiceHandler) collectFleetVersions(ctx context.Context, orgId uuid.UUID, catalogName, itemName string, listParams store.ListParams, versions map[string]bool) error {
	if h.fleetStore == nil {
		return nil
	}

	for lp := listParams; ; {
		osFleets, err := h.fleetStore.ListFleetsByOsCatalogItemRef(ctx, orgId, catalogName, itemName, lp)
		if err != nil {
			return err
		}
		for _, fleet := range osFleets.Items {
			spec := fleet.Spec.Template.Spec
			if spec.Os != nil && spec.Os.CatalogItemRef != nil {
				versions[spec.Os.CatalogItemRef.Version] = true
			}
		}
		if osFleets.Metadata.Continue == nil {
			break
		}
		cont, err := store.ParseContinueString(osFleets.Metadata.Continue)
		if err != nil {
			return err
		}
		lp.Continue = cont
	}

	for lp := listParams; ; {
		appFleets, err := h.fleetStore.ListFleetsByAppCatalogItemRef(ctx, orgId, catalogName, itemName, lp)
		if err != nil {
			return err
		}
		for _, fleet := range appFleets.Items {
			spec := fleet.Spec.Template.Spec
			if spec.Applications == nil {
				continue
			}
			for _, app := range *spec.Applications {
				ref, _ := common.ExtractAppCatalogItemRef(&app)
				if ref != nil && ref.Catalog == catalogName && ref.Item == itemName {
					versions[ref.Version] = true
				}
			}
		}
		if appFleets.Metadata.Continue == nil {
			break
		}
		cont, err := store.ParseContinueString(appFleets.Metadata.Continue)
		if err != nil {
			return err
		}
		lp.Continue = cont
	}

	for lp := listParams; ; {
		volFleets, err := h.fleetStore.ListFleetsByVolumeCatalogItemRef(ctx, orgId, catalogName, itemName, lp)
		if err != nil {
			return err
		}
		for _, fleet := range volFleets.Items {
			spec := fleet.Spec.Template.Spec
			if spec.Applications == nil {
				continue
			}
			for _, app := range *spec.Applications {
				refs, _ := common.ExtractVolumeCatalogItemRefs(&app)
				for _, ref := range refs {
					if ref.Catalog == catalogName && ref.Item == itemName {
						versions[ref.Version] = true
					}
				}
			}
		}
		if volFleets.Metadata.Continue == nil {
			break
		}
		cont, err := store.ParseContinueString(volFleets.Metadata.Continue)
		if err != nil {
			return err
		}
		lp.Continue = cont
	}

	return nil
}

func (h *ServiceHandler) validateInUseVersions(ctx context.Context, orgId uuid.UUID, catalogName, itemName string, oldSpec, newSpec domain.CatalogItemSpec) domain.Status {
	if domain.CatalogItemSpecsAreEqual(oldSpec, newSpec) {
		return domain.StatusOK()
	}

	deployedVersions, err := h.getDeployedVersions(ctx, orgId, catalogName, itemName)
	if err != nil {
		return domain.StatusInternalServerError(err.Error())
	}
	if len(deployedVersions) == 0 {
		return domain.StatusOK()
	}

	oldVersionMap := make(map[string]domain.CatalogItemVersion, len(oldSpec.Versions))
	for _, v := range oldSpec.Versions {
		oldVersionMap[v.Version] = v
	}
	newVersionMap := make(map[string]domain.CatalogItemVersion, len(newSpec.Versions))
	for _, v := range newSpec.Versions {
		newVersionMap[v.Version] = v
	}

	var affected []string
	for ver := range deployedVersions {
		newV, newExists := newVersionMap[ver]
		if !newExists {
			if _, oldExists := oldVersionMap[ver]; oldExists {
				affected = append(affected, ver)
			}
			continue
		}
		if oldV, oldExists := oldVersionMap[ver]; oldExists && !domain.CatalogItemVersionsAreEqual(oldV, newV) {
			affected = append(affected, ver)
		}
	}

	if len(affected) > 0 {
		sort.Strings(affected)
		return domain.StatusConflict(fmt.Sprintf("cannot modify or remove catalog item versions that are in use by devices or fleets: %s", strings.Join(affected, ", ")))
	}

	return domain.StatusOK()
}

func (h *ServiceHandler) GetCatalogItemDeployments(ctx context.Context, orgId uuid.UUID, catalogName string, itemName string, params domain.GetCatalogItemDeploymentsParams) (*domain.CatalogItemDeploymentList, domain.Status) {
	listParams, status := common.PrepareListParams(params.Continue, nil, nil, params.Limit)
	if status != domain.StatusOK() {
		return nil, status
	}

	var deployments []domain.CatalogItemDeployment

	if h.deviceStore != nil {
		devDeps, err := h.collectDeviceDeployments(ctx, orgId, catalogName, itemName)
		if err != nil {
			return nil, domain.StatusInternalServerError(err.Error())
		}
		deployments = append(deployments, devDeps...)
	}

	if h.fleetStore != nil {
		fleetDeps, err := h.collectFleetDeployments(ctx, orgId, catalogName, itemName)
		if err != nil {
			return nil, domain.StatusInternalServerError(err.Error())
		}
		deployments = append(deployments, fleetDeps...)
	}

	if deployments == nil {
		deployments = []domain.CatalogItemDeployment{}
	}

	sort.Slice(deployments, func(i, j int) bool {
		return deploymentSortKey(&deployments[i]) < deploymentSortKey(&deployments[j])
	})

	return paginateDeployments(deployments, listParams), domain.StatusOK()
}

func deploymentSortKey(d *domain.CatalogItemDeployment) string {
	kind := ""
	name := ""
	app := ""
	if d.DeployedTo != nil {
		if d.DeployedTo.ResourceKind != nil {
			kind = *d.DeployedTo.ResourceKind
		}
		if d.DeployedTo.ResourceName != nil {
			name = *d.DeployedTo.ResourceName
		}
	}
	if d.ApplicationName != nil {
		app = *d.ApplicationName
	}
	return kind + "/" + name + "/" + app
}

func paginateDeployments(deployments []domain.CatalogItemDeployment, listParams *store.ListParams) *domain.CatalogItemDeploymentList {
	offset := 0
	if listParams.Continue != nil && len(listParams.Continue.Names) > 0 {
		if v, err := strconv.Atoi(listParams.Continue.Names[0]); err == nil {
			offset = v
		}
	}

	if offset > len(deployments) {
		offset = len(deployments)
	}

	end := offset + listParams.Limit
	if end > len(deployments) {
		end = len(deployments)
	}

	page := deployments[offset:end]

	result := &domain.CatalogItemDeploymentList{
		ApiVersion: domain.QualifiedV1Alpha1,
		Kind:       domain.CatalogItemDeploymentListKind,
		Items:      page,
	}

	if end < len(deployments) {
		remaining := int64(len(deployments) - end)
		result.Metadata.Continue = store.BuildContinueString([]string{fmt.Sprintf("%d", end)}, remaining)
		result.Metadata.RemainingItemCount = &remaining
	}

	return result
}

func (h *ServiceHandler) collectDeviceDeployments(ctx context.Context, orgId uuid.UUID, catalogName, itemName string) ([]domain.CatalogItemDeployment, error) {
	var deployments []domain.CatalogItemDeployment
	lp := store.ListParams{Limit: common.MaxRecordsPerListRequest}

	for osLp := lp; ; {
		osDevices, err := h.deviceStore.ListDevicesByOsCatalogItemRef(ctx, orgId, catalogName, itemName, osLp)
		if err != nil {
			return nil, err
		}
		for _, dev := range osDevices.Items {
			if dev.Spec == nil || dev.Spec.Os == nil || dev.Spec.Os.CatalogItemRef == nil {
				continue
			}
			ref := dev.Spec.Os.CatalogItemRef
			deployments = append(deployments, domain.CatalogItemDeployment{
				ApiVersion:  domain.QualifiedV1Alpha1,
				Kind:        domain.CatalogItemDeploymentKind,
				Catalog:     ref.Catalog,
				CatalogItem: ref.Item,
				Version:     ref.Version,
				Channel:     ref.Channel,
				DeployedTo:  deployedTo(domain.DeviceKind, dev.Metadata.Name),
			})
		}
		if osDevices.Metadata.Continue == nil {
			break
		}
		cont, err := store.ParseContinueString(osDevices.Metadata.Continue)
		if err != nil {
			return nil, err
		}
		osLp.Continue = cont
	}

	for appLp := lp; ; {
		appDevices, err := h.deviceStore.ListDevicesByAppCatalogItemRef(ctx, orgId, catalogName, itemName, appLp)
		if err != nil {
			return nil, err
		}
		deviceAppDeployments(appDevices.Items, catalogName, itemName, &deployments)
		if appDevices.Metadata.Continue == nil {
			break
		}
		cont, err := store.ParseContinueString(appDevices.Metadata.Continue)
		if err != nil {
			return nil, err
		}
		appLp.Continue = cont
	}

	for volLp := lp; ; {
		volDevices, err := h.deviceStore.ListDevicesByVolumeCatalogItemRef(ctx, orgId, catalogName, itemName, volLp)
		if err != nil {
			return nil, err
		}
		deviceVolumeDeployments(volDevices.Items, catalogName, itemName, &deployments)
		if volDevices.Metadata.Continue == nil {
			break
		}
		cont, err := store.ParseContinueString(volDevices.Metadata.Continue)
		if err != nil {
			return nil, err
		}
		volLp.Continue = cont
	}

	return deployments, nil
}

func (h *ServiceHandler) collectFleetDeployments(ctx context.Context, orgId uuid.UUID, catalogName, itemName string) ([]domain.CatalogItemDeployment, error) {
	var deployments []domain.CatalogItemDeployment
	lp := store.ListParams{Limit: common.MaxRecordsPerListRequest}

	for osLp := lp; ; {
		osFleets, err := h.fleetStore.ListFleetsByOsCatalogItemRef(ctx, orgId, catalogName, itemName, osLp)
		if err != nil {
			return nil, err
		}
		for _, fleet := range osFleets.Items {
			spec := fleet.Spec.Template.Spec
			if spec.Os == nil || spec.Os.CatalogItemRef == nil {
				continue
			}
			ref := spec.Os.CatalogItemRef
			deployments = append(deployments, domain.CatalogItemDeployment{
				ApiVersion:  domain.QualifiedV1Alpha1,
				Kind:        domain.CatalogItemDeploymentKind,
				Catalog:     ref.Catalog,
				CatalogItem: ref.Item,
				Version:     ref.Version,
				Channel:     ref.Channel,
				DeployedTo:  deployedTo(domain.FleetKind, fleet.Metadata.Name),
			})
		}
		if osFleets.Metadata.Continue == nil {
			break
		}
		cont, err := store.ParseContinueString(osFleets.Metadata.Continue)
		if err != nil {
			return nil, err
		}
		osLp.Continue = cont
	}

	for appLp := lp; ; {
		appFleets, err := h.fleetStore.ListFleetsByAppCatalogItemRef(ctx, orgId, catalogName, itemName, appLp)
		if err != nil {
			return nil, err
		}
		fleetAppDeployments(appFleets.Items, catalogName, itemName, &deployments)
		if appFleets.Metadata.Continue == nil {
			break
		}
		cont, err := store.ParseContinueString(appFleets.Metadata.Continue)
		if err != nil {
			return nil, err
		}
		appLp.Continue = cont
	}

	for volLp := lp; ; {
		volFleets, err := h.fleetStore.ListFleetsByVolumeCatalogItemRef(ctx, orgId, catalogName, itemName, volLp)
		if err != nil {
			return nil, err
		}
		fleetVolumeDeployments(volFleets.Items, catalogName, itemName, &deployments)
		if volFleets.Metadata.Continue == nil {
			break
		}
		cont, err := store.ParseContinueString(volFleets.Metadata.Continue)
		if err != nil {
			return nil, err
		}
		volLp.Continue = cont
	}

	return deployments, nil
}

func deployedTo(kind string, name *string) *struct {
	ResourceKind *string `json:"resourceKind,omitempty"`
	ResourceName *string `json:"resourceName,omitempty"`
} {
	return &struct {
		ResourceKind *string `json:"resourceKind,omitempty"`
		ResourceName *string `json:"resourceName,omitempty"`
	}{
		ResourceKind: lo.ToPtr(kind),
		ResourceName: name,
	}
}

func deviceAppDeployments(devices []domain.Device, catalogName, itemName string, deployments *[]domain.CatalogItemDeployment) {
	for _, dev := range devices {
		if dev.Spec == nil || dev.Spec.Applications == nil {
			continue
		}
		for _, app := range *dev.Spec.Applications {
			ref, appName := common.ExtractAppCatalogItemRef(&app)
			if ref == nil || ref.Catalog != catalogName || ref.Item != itemName {
				continue
			}
			*deployments = append(*deployments, domain.CatalogItemDeployment{
				ApiVersion:      domain.QualifiedV1Alpha1,
				Kind:            domain.CatalogItemDeploymentKind,
				Catalog:         ref.Catalog,
				CatalogItem:     ref.Item,
				Version:         ref.Version,
				Channel:         ref.Channel,
				ApplicationName: appName,
				DeployedTo:      deployedTo(domain.DeviceKind, dev.Metadata.Name),
			})
		}
	}
}

func deviceVolumeDeployments(devices []domain.Device, catalogName, itemName string, deployments *[]domain.CatalogItemDeployment) {
	for _, dev := range devices {
		if dev.Spec == nil || dev.Spec.Applications == nil {
			continue
		}
		for _, app := range *dev.Spec.Applications {
			refs, appName := common.ExtractVolumeCatalogItemRefs(&app)
			for _, ref := range refs {
				if ref.Catalog != catalogName || ref.Item != itemName {
					continue
				}
				*deployments = append(*deployments, domain.CatalogItemDeployment{
					ApiVersion:      domain.QualifiedV1Alpha1,
					Kind:            domain.CatalogItemDeploymentKind,
					Catalog:         ref.Catalog,
					CatalogItem:     ref.Item,
					Version:         ref.Version,
					Channel:         ref.Channel,
					ApplicationName: appName,
					DeployedTo:      deployedTo(domain.DeviceKind, dev.Metadata.Name),
				})
			}
		}
	}
}

func fleetAppDeployments(fleets []domain.Fleet, catalogName, itemName string, deployments *[]domain.CatalogItemDeployment) {
	for _, fleet := range fleets {
		spec := fleet.Spec.Template.Spec
		if spec.Applications == nil {
			continue
		}
		for _, app := range *spec.Applications {
			ref, appName := common.ExtractAppCatalogItemRef(&app)
			if ref == nil || ref.Catalog != catalogName || ref.Item != itemName {
				continue
			}
			*deployments = append(*deployments, domain.CatalogItemDeployment{
				ApiVersion:      domain.QualifiedV1Alpha1,
				Kind:            domain.CatalogItemDeploymentKind,
				Catalog:         ref.Catalog,
				CatalogItem:     ref.Item,
				Version:         ref.Version,
				Channel:         ref.Channel,
				ApplicationName: appName,
				DeployedTo:      deployedTo(domain.FleetKind, fleet.Metadata.Name),
			})
		}
	}
}

func fleetVolumeDeployments(fleets []domain.Fleet, catalogName, itemName string, deployments *[]domain.CatalogItemDeployment) {
	for _, fleet := range fleets {
		spec := fleet.Spec.Template.Spec
		if spec.Applications == nil {
			continue
		}
		for _, app := range *spec.Applications {
			refs, appName := common.ExtractVolumeCatalogItemRefs(&app)
			for _, ref := range refs {
				if ref.Catalog != catalogName || ref.Item != itemName {
					continue
				}
				*deployments = append(*deployments, domain.CatalogItemDeployment{
					ApiVersion:      domain.QualifiedV1Alpha1,
					Kind:            domain.CatalogItemDeploymentKind,
					Catalog:         ref.Catalog,
					CatalogItem:     ref.Item,
					Version:         ref.Version,
					Channel:         ref.Channel,
					ApplicationName: appName,
					DeployedTo:      deployedTo(domain.FleetKind, fleet.Metadata.Name),
				})
			}
		}
	}
}

// callbackCatalogUpdated is the catalog-specific callback that handles catalog events
func (h *ServiceHandler) callbackCatalogUpdated(ctx context.Context, resourceKind domain.ResourceKind, orgId uuid.UUID, name string, oldResource, newResource interface{}, created bool, err error) {
	store.SafeEventCallback(h.log, func() {
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
	})
}

// callbackCatalogDeleted is the catalog-specific callback that handles catalog deletion events
func (h *ServiceHandler) callbackCatalogDeleted(ctx context.Context, resourceKind domain.ResourceKind, orgId uuid.UUID, name string, oldResource, newResource interface{}, created bool, err error) {
	store.SafeEventCallback(h.log, func() {
		h.events.HandleGenericResourceDeletedEvents(ctx, resourceKind, orgId, name, oldResource, newResource, created, err)
	})
}

func (h *ServiceHandler) UnsetOwner(ctx context.Context, orgId uuid.UUID, owner string) error {
	return h.store.UnsetOwner(ctx, store.DB(ctx, nil), orgId, owner)
}

func (h *ServiceHandler) UnsetItemOwner(ctx context.Context, orgId uuid.UUID, owner string) error {
	return h.store.UnsetItemOwner(ctx, store.DB(ctx, nil), orgId, owner)
}
