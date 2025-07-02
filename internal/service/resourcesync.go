package service

import (
	"context"
	"errors"
	"reflect"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/selector"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

func (h *ServiceHandler) CreateResourceSync(ctx context.Context, rs api.ResourceSync) (*api.ResourceSync, api.Status) {
	orgId := store.NullOrgId

	// don't set fields that are managed by the service
	rs.Status = nil
	NilOutManagedObjectMetaProperties(&rs.Metadata)

	if errs := rs.Validate(); len(errs) > 0 {
		return nil, api.StatusBadRequest(errors.Join(errs...).Error())
	}

	result, err := h.store.ResourceSync().Create(ctx, orgId, &rs)
	status := StoreErrorToApiStatus(err, true, api.ResourceSyncKind, rs.Metadata.Name)
	h.CreateEvent(ctx, GetResourceCreatedOrUpdatedEvent(ctx, true, api.ResourceSyncKind, *rs.Metadata.Name, status, nil, h.log))
	return result, status
}

func (h *ServiceHandler) ListResourceSyncs(ctx context.Context, params api.ListResourceSyncsParams) (*api.ResourceSyncList, api.Status) {
	orgId := store.NullOrgId

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

func (h *ServiceHandler) GetResourceSync(ctx context.Context, name string) (*api.ResourceSync, api.Status) {
	orgId := store.NullOrgId

	result, err := h.store.ResourceSync().Get(ctx, orgId, name)
	return result, StoreErrorToApiStatus(err, false, api.ResourceSyncKind, &name)
}

func (h *ServiceHandler) ReplaceResourceSync(ctx context.Context, name string, rs api.ResourceSync) (*api.ResourceSync, api.Status) {
	orgId := store.NullOrgId

	// don't overwrite fields that are managed by the service
	rs.Status = nil
	NilOutManagedObjectMetaProperties(&rs.Metadata)
	if errs := rs.Validate(); len(errs) > 0 {
		return nil, api.StatusBadRequest(errors.Join(errs...).Error())
	}
	if name != *rs.Metadata.Name {
		return nil, api.StatusBadRequest("resource name specified in metadata does not match name in path")
	}

	result, created, updateDesc, err := h.store.ResourceSync().CreateOrUpdate(ctx, orgId, &rs)
	status := StoreErrorToApiStatus(err, created, api.ResourceSyncKind, &name)
	h.CreateEvent(ctx, GetResourceCreatedOrUpdatedEvent(ctx, created, api.ResourceSyncKind, name, status, &updateDesc, h.log))
	return result, status
}

func (h *ServiceHandler) DeleteResourceSync(ctx context.Context, name string) api.Status {
	orgId := store.NullOrgId

	var deleted bool
	callback := func(ctx context.Context, tx *gorm.DB, orgId uuid.UUID, owner string) error {
		deleted = true
		return h.store.Fleet().UnsetOwner(ctx, tx, orgId, owner)
	}

	err := h.store.ResourceSync().Delete(ctx, orgId, name, callback)
	status := StoreErrorToApiStatus(err, false, api.ResourceSyncKind, &name)
	if deleted || err != nil {
		h.CreateEvent(ctx, GetResourceDeletedEvent(ctx, api.ResourceSyncKind, name, status, h.log))
	}
	return status
}

// Only metadata.labels and spec can be patched. If we try to patch other fields, HTTP 400 Bad Request is returned.
func (h *ServiceHandler) PatchResourceSync(ctx context.Context, name string, patch api.PatchRequest) (*api.ResourceSync, api.Status) {
	orgId := store.NullOrgId

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
	if newObj.Metadata.Name == nil || *currentObj.Metadata.Name != *newObj.Metadata.Name {
		return nil, api.StatusBadRequest("metadata.name is immutable")
	}
	if currentObj.ApiVersion != newObj.ApiVersion {
		return nil, api.StatusBadRequest("apiVersion is immutable")
	}
	if currentObj.Kind != newObj.Kind {
		return nil, api.StatusBadRequest("kind is immutable")
	}
	if !reflect.DeepEqual(currentObj.Status, newObj.Status) {
		return nil, api.StatusBadRequest("status is immutable")
	}

	NilOutManagedObjectMetaProperties(&newObj.Metadata)
	newObj.Metadata.ResourceVersion = nil
	result, updateDesc, err := h.store.ResourceSync().Update(ctx, orgId, newObj)
	status := StoreErrorToApiStatus(err, false, api.ResourceSyncKind, &name)
	h.CreateEvent(ctx, GetResourceCreatedOrUpdatedEvent(ctx, false, api.ResourceSyncKind, name, status, &updateDesc, h.log))
	return result, status
}

func (h *ServiceHandler) ReplaceResourceSyncStatus(ctx context.Context, name string, resourceSync api.ResourceSync) (*api.ResourceSync, api.Status) {
	orgId := store.NullOrgId

	if name != *resourceSync.Metadata.Name {
		return nil, api.StatusBadRequest("resource name specified in metadata does not match name in path")
	}

	result, err := h.store.ResourceSync().UpdateStatus(ctx, orgId, &resourceSync)
	return result, StoreErrorToApiStatus(err, false, api.ResourceSyncKind, &name)
}
