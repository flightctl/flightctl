package catalog

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/service/common"
	"github.com/flightctl/flightctl/internal/service/events"
	"github.com/flightctl/flightctl/internal/store"
	catalogstore "github.com/flightctl/flightctl/internal/store/catalog"
	devicestore "github.com/flightctl/flightctl/internal/store/device"
	"github.com/flightctl/flightctl/internal/store/selector"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

type ServiceHandler struct {
	store       catalogstore.Store
	deviceStore devicestore.Store
	events      events.Service
	log         logrus.FieldLogger
}

// NewServiceHandler creates a new catalog ServiceHandler instance.
func NewServiceHandler(store catalogstore.Store, deviceStore devicestore.Store, events events.Service, log logrus.FieldLogger) *ServiceHandler {
	return &ServiceHandler{store: store, deviceStore: deviceStore, events: events, log: log}
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
			!domain.CatalogSpecsAreEqual(existing.Spec, catalog.Spec) {
			return nil, common.StoreErrorToApiStatus(flterrors.ErrUpdatingResourceWithOwnerNotAllowed, false, domain.CatalogKind, &name)
		}
	}

	result, created, err := h.store.CreateOrUpdate(ctx, orgId, &catalog, h.callbackCatalogUpdated)
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
		!domain.CatalogSpecsAreEqual(currentObj.Spec, newObj.Spec) {
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
		return domain.StatusConflict(fmt.Sprintf("cannot delete catalog item because the following versions are in use by devices: %s", strings.Join(versions, ", ")))
	}

	err = h.store.DeleteItem(ctx, orgId, catalogName, itemName)
	if errors.Is(err, flterrors.ErrParentResourceNotFound) {
		return domain.StatusResourceNotFound(domain.CatalogKind, catalogName)
	}
	return common.StoreErrorToApiStatus(err, false, domain.CatalogItemKind, &itemName)
}

func (h *ServiceHandler) getDeployedVersions(ctx context.Context, orgId uuid.UUID, catalogName, itemName string) (map[string]bool, error) {
	if h.deviceStore == nil {
		return nil, nil
	}
	listParams := store.ListParams{Limit: common.MaxRecordsPerListRequest}
	versions := make(map[string]bool)

	osDevices, err := h.deviceStore.ListDevicesByOsCatalogItemRef(ctx, orgId, catalogName, itemName, listParams)
	if err != nil {
		return nil, err
	}
	for _, dev := range osDevices.Items {
		if dev.Spec != nil && dev.Spec.Os != nil && dev.Spec.Os.CatalogItemRef != nil {
			versions[dev.Spec.Os.CatalogItemRef.Version] = true
		}
	}

	appDevices, err := h.deviceStore.ListDevicesByAppCatalogItemRef(ctx, orgId, catalogName, itemName, listParams)
	if err != nil {
		return nil, err
	}
	for _, dev := range appDevices.Items {
		if dev.Spec == nil || dev.Spec.Applications == nil {
			continue
		}
		for _, app := range *dev.Spec.Applications {
			ref, _ := extractAppCatalogItemRef(&app)
			if ref != nil && ref.Catalog == catalogName && ref.Item == itemName {
				versions[ref.Version] = true
			}
		}
	}

	volDevices, err := h.deviceStore.ListDevicesByVolumeCatalogItemRef(ctx, orgId, catalogName, itemName, listParams)
	if err != nil {
		return nil, err
	}
	for _, dev := range volDevices.Items {
		if dev.Spec == nil || dev.Spec.Applications == nil {
			continue
		}
		for _, app := range *dev.Spec.Applications {
			refs, _ := extractVolumesCatalogItemRefs(&app)
			for _, ref := range refs {
				if ref.Catalog == catalogName && ref.Item == itemName {
					versions[ref.Version] = true
				}
			}
		}
	}

	return versions, nil
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
			affected = append(affected, ver)
			continue
		}
		if oldV, oldExists := oldVersionMap[ver]; oldExists && !domain.CatalogItemVersionsAreEqual(oldV, newV) {
			affected = append(affected, ver)
		}
	}

	if len(affected) > 0 {
		sort.Strings(affected)
		return domain.StatusConflict(fmt.Sprintf("cannot modify or remove catalog item versions that are in use by devices: %s", strings.Join(affected, ", ")))
	}

	return domain.StatusOK()
}

func (h *ServiceHandler) GetCatalogItemDeployments(ctx context.Context, orgId uuid.UUID, catalogName string, itemName string) (*domain.CatalogItemDeploymentList, domain.Status) {
	listParams := store.ListParams{Limit: common.MaxRecordsPerListRequest}

	var deployments []domain.CatalogItemDeployment

	osDevices, err := h.deviceStore.ListDevicesByOsCatalogItemRef(ctx, orgId, catalogName, itemName, listParams)
	if err != nil {
		return nil, domain.StatusInternalServerError(err.Error())
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
		})
	}

	appDevices, err := h.deviceStore.ListDevicesByAppCatalogItemRef(ctx, orgId, catalogName, itemName, listParams)
	if err != nil {
		return nil, domain.StatusInternalServerError(err.Error())
	}
	for _, dev := range appDevices.Items {
		if dev.Spec == nil || dev.Spec.Applications == nil {
			continue
		}
		for _, app := range *dev.Spec.Applications {
			ref, appName := extractAppCatalogItemRef(&app)
			if ref == nil || ref.Catalog != catalogName || ref.Item != itemName {
				continue
			}
			deployments = append(deployments, domain.CatalogItemDeployment{
				ApiVersion:      domain.QualifiedV1Alpha1,
				Kind:            domain.CatalogItemDeploymentKind,
				Catalog:         ref.Catalog,
				CatalogItem:     ref.Item,
				Version:         ref.Version,
				Channel:         ref.Channel,
				ApplicationName: appName,
			})
		}
	}

	volDevices, err := h.deviceStore.ListDevicesByVolumeCatalogItemRef(ctx, orgId, catalogName, itemName, listParams)
	if err != nil {
		return nil, domain.StatusInternalServerError(err.Error())
	}
	for _, dev := range volDevices.Items {
		if dev.Spec == nil || dev.Spec.Applications == nil {
			continue
		}
		for _, app := range *dev.Spec.Applications {
			refs, appName := extractVolumesCatalogItemRefs(&app)
			for _, ref := range refs {
				if ref.Catalog != catalogName || ref.Item != itemName {
					continue
				}
				deployments = append(deployments, domain.CatalogItemDeployment{
					ApiVersion:      domain.QualifiedV1Alpha1,
					Kind:            domain.CatalogItemDeploymentKind,
					Catalog:         ref.Catalog,
					CatalogItem:     ref.Item,
					Version:         ref.Version,
					Channel:         ref.Channel,
					ApplicationName: appName,
				})
			}
		}
	}

	return &domain.CatalogItemDeploymentList{
		ApiVersion: domain.QualifiedV1Alpha1,
		Kind:       domain.CatalogItemDeploymentListKind,
		Items:      deployments,
	}, domain.StatusOK()
}

func extractAppCatalogItemRef(app *domain.ApplicationProviderSpec) (*domain.CatalogItemRefSpec, *string) {
	appType, err := app.GetAppType()
	if err != nil {
		return nil, nil
	}

	var source domain.CatalogItemRefSource
	var name *string
	switch appType {
	case domain.AppTypeContainer:
		a, err := app.AsContainerApplication()
		if err != nil {
			return nil, nil
		}
		source = &a
		name = a.Name
	case domain.AppTypeHelm:
		a, err := app.AsHelmApplication()
		if err != nil {
			return nil, nil
		}
		source = &a
		name = a.Name
	case domain.AppTypeCompose:
		a, err := app.AsComposeApplication()
		if err != nil {
			return nil, nil
		}
		source = &a
		name = a.Name
	case domain.AppTypeQuadlet:
		a, err := app.AsQuadletApplication()
		if err != nil {
			return nil, nil
		}
		source = &a
		name = a.Name
	default:
		return nil, nil
	}

	spec, err := source.AsCatalogItemRefApplicationProviderSpec()
	if err != nil {
		return nil, nil
	}
	return &spec.CatalogItemRef, name
}

func extractVolumesCatalogItemRefs(app *domain.ApplicationProviderSpec) ([]domain.CatalogItemRefSpec, *string) {
	appType, err := app.GetAppType()
	if err != nil {
		return nil, nil
	}

	var volumes *[]domain.ApplicationVolume
	var name *string
	switch appType {
	case domain.AppTypeContainer:
		a, err := app.AsContainerApplication()
		if err != nil {
			return nil, nil
		}
		volumes = a.Volumes
		name = a.Name
	case domain.AppTypeCompose:
		a, err := app.AsComposeApplication()
		if err != nil {
			return nil, nil
		}
		volumes = a.Volumes
		name = a.Name
	case domain.AppTypeQuadlet:
		a, err := app.AsQuadletApplication()
		if err != nil {
			return nil, nil
		}
		volumes = a.Volumes
		name = a.Name
	default:
		return nil, nil
	}

	if volumes == nil {
		return nil, nil
	}

	var refs []domain.CatalogItemRefSpec
	for _, vol := range *volumes {
		volType, err := vol.Type()
		if err != nil {
			continue
		}
		switch volType {
		case domain.ImageApplicationVolumeProviderType:
			provider, err := vol.AsImageVolumeProviderSpec()
			if err != nil || provider.Image.CatalogItemRef == nil {
				continue
			}
			refs = append(refs, *provider.Image.CatalogItemRef)
		case domain.ImageMountApplicationVolumeProviderType:
			provider, err := vol.AsImageMountVolumeProviderSpec()
			if err != nil || provider.Image.CatalogItemRef == nil {
				continue
			}
			refs = append(refs, *provider.Image.CatalogItemRef)
		}
	}
	return refs, name
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
