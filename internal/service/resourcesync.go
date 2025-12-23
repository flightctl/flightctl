package service

import (
	"context"
	"errors"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/store/selector"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

func (h *ServiceHandler) CreateResourceSync(ctx context.Context, orgId uuid.UUID, rs api.ResourceSync) (*api.ResourceSync, api.Status) {
	// don't set fields that are managed by the service
	rs.Status = nil
	NilOutManagedObjectMetaProperties(&rs.Metadata)

	if errs := rs.Validate(); len(errs) > 0 {
		return nil, api.StatusBadRequest(errors.Join(errs...).Error())
	}

	result, err := h.store.ResourceSync().Create(ctx, orgId, &rs, h.callbackResourceSyncUpdated)
	return result, StoreErrorToApiStatus(err, true, api.ResourceSyncKind, rs.Metadata.Name)
}

func (h *ServiceHandler) ListResourceSyncs(ctx context.Context, orgId uuid.UUID, params api.ListResourceSyncsParams) (*api.ResourceSyncList, api.Status) {
	listParams, status := prepareListParams(params.Continue, params.LabelSelector, params.FieldSelector, params.Limit)
	if status != api.StatusOK() {
		return nil, status
	}

	result, err := h.store.ResourceSync().List(ctx, orgId, *listParams)
	if err == nil {
		return result, api.StatusOK()
	}

	var se *selector.SelectorError

	switch {
	case selector.AsSelectorError(err, &se):
		return nil, api.StatusBadRequest(se.Error())
	default:
		return nil, api.StatusInternalServerError(err.Error())
	}
}

func (h *ServiceHandler) GetResourceSync(ctx context.Context, orgId uuid.UUID, name string) (*api.ResourceSync, api.Status) {
	result, err := h.store.ResourceSync().Get(ctx, orgId, name)
	return result, StoreErrorToApiStatus(err, false, api.ResourceSyncKind, &name)
}

func (h *ServiceHandler) ReplaceResourceSync(ctx context.Context, orgId uuid.UUID, name string, rs api.ResourceSync) (*api.ResourceSync, api.Status) {
	// don't overwrite fields that are managed by the service
	rs.Status = nil
	NilOutManagedObjectMetaProperties(&rs.Metadata)
	if errs := rs.Validate(); len(errs) > 0 {
		return nil, api.StatusBadRequest(errors.Join(errs...).Error())
	}
	if name != *rs.Metadata.Name {
		return nil, api.StatusBadRequest("resource name specified in metadata does not match name in path")
	}

	result, created, err := h.store.ResourceSync().CreateOrUpdate(ctx, orgId, &rs, h.callbackResourceSyncUpdated)
	return result, StoreErrorToApiStatus(err, created, api.ResourceSyncKind, &name)
}

func (h *ServiceHandler) DeleteResourceSync(ctx context.Context, orgId uuid.UUID, name string) api.Status {
	callback := func(ctx context.Context, tx *gorm.DB, orgId uuid.UUID, owner string) error {
		return h.store.Fleet().UnsetOwner(ctx, tx, orgId, owner)
	}

	err := h.store.ResourceSync().Delete(ctx, orgId, name, callback, h.callbackResourceSyncDeleted)
	status := StoreErrorToApiStatus(err, false, api.ResourceSyncKind, &name)
	return status
}

// Only metadata.labels and spec can be patched. If we try to patch other fields, HTTP 400 Bad Request is returned.
func (h *ServiceHandler) PatchResourceSync(ctx context.Context, orgId uuid.UUID, name string, patch api.PatchRequest) (*api.ResourceSync, api.Status) {
	currentObj, err := h.store.ResourceSync().Get(ctx, orgId, name)
	if err != nil {
		return nil, StoreErrorToApiStatus(err, false, api.ResourceSyncKind, &name)
	}

	newObj := &api.ResourceSync{}
	err = ApplyJSONPatch(ctx, currentObj, newObj, patch, "/api/v1/resourcesyncs/"+name)
	if err != nil {
		return nil, api.StatusBadRequest(err.Error())
	}

	if errs := newObj.Validate(); len(errs) > 0 {
		return nil, api.StatusBadRequest(errors.Join(errs...).Error())
	}
	if errs := currentObj.ValidateUpdate(newObj); len(errs) > 0 {
		return nil, api.StatusBadRequest(errors.Join(errs...).Error())
	}

	NilOutManagedObjectMetaProperties(&newObj.Metadata)
	newObj.Metadata.ResourceVersion = nil
	result, err := h.store.ResourceSync().Update(ctx, orgId, newObj, h.callbackResourceSyncUpdated)
	return result, StoreErrorToApiStatus(err, false, api.ResourceSyncKind, &name)
}

func (h *ServiceHandler) ReplaceResourceSyncStatus(ctx context.Context, orgId uuid.UUID, name string, resourceSync api.ResourceSync) (*api.ResourceSync, api.Status) {
	if errs := resourceSync.Validate(); len(errs) > 0 {
		return nil, api.StatusBadRequest(errors.Join(errs...).Error())
	}
	if name != *resourceSync.Metadata.Name {
		return nil, api.StatusBadRequest("resource name specified in metadata does not match name in path")
	}

	result, err := h.store.ResourceSync().UpdateStatus(ctx, orgId, &resourceSync, h.callbackResourceSyncUpdated)
	return result, StoreErrorToApiStatus(err, false, api.ResourceSyncKind, &name)
}

// callbackResourceSyncUpdated is the resource sync-specific callback that handles resource sync events
func (h *ServiceHandler) callbackResourceSyncUpdated(ctx context.Context, resourceKind api.ResourceKind, orgId uuid.UUID, name string, oldResource, newResource interface{}, created bool, err error) {
	h.eventHandler.HandleResourceSyncUpdatedEvents(ctx, resourceKind, orgId, name, oldResource, newResource, created, err)
}

// callbackResourceSyncDeleted is the resource sync-specific callback that handles resource sync deletion events
func (h *ServiceHandler) callbackResourceSyncDeleted(ctx context.Context, resourceKind api.ResourceKind, orgId uuid.UUID, name string, oldResource, newResource interface{}, created bool, err error) {
	h.eventHandler.HandleGenericResourceDeletedEvents(ctx, resourceKind, orgId, name, oldResource, newResource, created, err)
}
