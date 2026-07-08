package resourcesync

import (
	"context"
	"errors"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/service/common"
	"github.com/flightctl/flightctl/internal/service/events"
	catalogstore "github.com/flightctl/flightctl/internal/store/catalog"
	fleetstore "github.com/flightctl/flightctl/internal/store/fleet"
	resourcesyncstore "github.com/flightctl/flightctl/internal/store/resourcesync"
	"github.com/flightctl/flightctl/internal/store/selector"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// ServiceHandler implements Service. No `log`, no `workerClient` (neither is referenced in the
// original resourcesync.go). catalogStore/fleetStore are the narrow, already-isolated STORE
// sub-packages needed only by DeleteResourceSync's inline ownership-cleanup callback — not the
// full Catalog/Fleet service handlers.
type ServiceHandler struct {
	store        resourcesyncstore.Store
	catalogStore catalogstore.Store
	fleetStore   fleetstore.Store
	events       events.Service
}

// NewServiceHandler creates a new resourcesync ServiceHandler instance.
func NewServiceHandler(store resourcesyncstore.Store, catalogStore catalogstore.Store, fleetStore fleetstore.Store, events events.Service) *ServiceHandler {
	return &ServiceHandler{store: store, catalogStore: catalogStore, fleetStore: fleetStore, events: events}
}

var _ Service = (*ServiceHandler)(nil)

func (h *ServiceHandler) CreateResourceSync(ctx context.Context, orgId uuid.UUID, rs domain.ResourceSync) (*domain.ResourceSync, domain.Status) {
	// don't set fields that are managed by the service
	rs.Status = nil
	common.NilOutManagedObjectMetaProperties(&rs.Metadata)

	if errs := rs.Validate(); len(errs) > 0 {
		return nil, domain.StatusBadRequest(errors.Join(errs...).Error())
	}

	result, err := h.store.Create(ctx, orgId, &rs, h.callbackResourceSyncUpdated)
	return result, common.StoreErrorToApiStatus(err, true, domain.ResourceSyncKind, rs.Metadata.Name)
}

func (h *ServiceHandler) ListResourceSyncs(ctx context.Context, orgId uuid.UUID, params domain.ListResourceSyncsParams) (*domain.ResourceSyncList, domain.Status) {
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

func (h *ServiceHandler) GetResourceSync(ctx context.Context, orgId uuid.UUID, name string) (*domain.ResourceSync, domain.Status) {
	result, err := h.store.Get(ctx, orgId, name)
	return result, common.StoreErrorToApiStatus(err, false, domain.ResourceSyncKind, &name)
}

func (h *ServiceHandler) ReplaceResourceSync(ctx context.Context, orgId uuid.UUID, name string, rs domain.ResourceSync) (*domain.ResourceSync, domain.Status) {
	// don't overwrite fields that are managed by the service
	if !common.IsInternalRequest(ctx) {
		rs.Status = nil
		common.NilOutManagedObjectMetaProperties(&rs.Metadata)
	}
	if errs := rs.Validate(); len(errs) > 0 {
		return nil, domain.StatusBadRequest(errors.Join(errs...).Error())
	}
	if name != *rs.Metadata.Name {
		return nil, domain.StatusBadRequest("resource name specified in metadata does not match name in path")
	}

	result, created, err := h.store.CreateOrUpdate(ctx, orgId, &rs, h.callbackResourceSyncUpdated)
	return result, common.StoreErrorToApiStatus(err, created, domain.ResourceSyncKind, &name)
}

func (h *ServiceHandler) DeleteResourceSync(ctx context.Context, orgId uuid.UUID, name string) domain.Status {
	callback := func(ctx context.Context, tx *gorm.DB, orgId uuid.UUID, owner string) error {
		if err := h.catalogStore.UnsetItemOwner(ctx, tx, orgId, owner); err != nil {
			return err
		}
		if err := h.catalogStore.UnsetOwner(ctx, tx, orgId, owner); err != nil {
			return err
		}
		return h.fleetStore.UnsetOwner(ctx, tx, orgId, owner)
	}

	err := h.store.Delete(ctx, orgId, name, callback, h.callbackResourceSyncDeleted)
	status := common.StoreErrorToApiStatus(err, false, domain.ResourceSyncKind, &name)
	return status
}

// Only metadata.labels and spec can be patched. If we try to patch other fields, HTTP 400 Bad Request is returned.
func (h *ServiceHandler) PatchResourceSync(ctx context.Context, orgId uuid.UUID, name string, patch domain.PatchRequest) (*domain.ResourceSync, domain.Status) {
	currentObj, err := h.store.Get(ctx, orgId, name)
	if err != nil {
		return nil, common.StoreErrorToApiStatus(err, false, domain.ResourceSyncKind, &name)
	}

	newObj := &domain.ResourceSync{}
	err = common.ApplyJSONPatch(ctx, currentObj, newObj, patch, "/resourcesyncs/"+name)
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
	result, err := h.store.Update(ctx, orgId, newObj, h.callbackResourceSyncUpdated)
	return result, common.StoreErrorToApiStatus(err, false, domain.ResourceSyncKind, &name)
}

func (h *ServiceHandler) ReplaceResourceSyncStatus(ctx context.Context, orgId uuid.UUID, name string, resourceSync domain.ResourceSync) (*domain.ResourceSync, domain.Status) {
	if errs := resourceSync.Validate(); len(errs) > 0 {
		return nil, domain.StatusBadRequest(errors.Join(errs...).Error())
	}
	if name != *resourceSync.Metadata.Name {
		return nil, domain.StatusBadRequest("resource name specified in metadata does not match name in path")
	}

	result, err := h.store.UpdateStatus(ctx, orgId, &resourceSync, h.callbackResourceSyncUpdated)
	return result, common.StoreErrorToApiStatus(err, false, domain.ResourceSyncKind, &name)
}

// callbackResourceSyncUpdated is the resource sync-specific callback that handles resource sync events
func (h *ServiceHandler) callbackResourceSyncUpdated(ctx context.Context, resourceKind domain.ResourceKind, orgId uuid.UUID, name string, oldResource, newResource interface{}, created bool, err error) {
	h.events.HandleResourceSyncUpdatedEvents(ctx, resourceKind, orgId, name, oldResource, newResource, created, err)
}

// callbackResourceSyncDeleted is the resource sync-specific callback that handles resource sync deletion events
func (h *ServiceHandler) callbackResourceSyncDeleted(ctx context.Context, resourceKind domain.ResourceKind, orgId uuid.UUID, name string, oldResource, newResource interface{}, created bool, err error) {
	h.events.HandleGenericResourceDeletedEvents(ctx, resourceKind, orgId, name, oldResource, newResource, created, err)
}
