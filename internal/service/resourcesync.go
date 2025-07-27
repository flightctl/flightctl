package service

import (
	"context"
	"errors"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/store/selector"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

func (h *ServiceHandler) CreateResourceSync(ctx context.Context, rs api.ResourceSync) (*api.ResourceSync, api.Status) {
	orgId := getOrgIdFromContext(ctx)

	// don't set fields that are managed by the service
	rs.Status = nil
	NilOutManagedObjectMetaProperties(&rs.Metadata)

	if errs := rs.Validate(); len(errs) > 0 {
		return nil, api.StatusBadRequest(errors.Join(errs...).Error())
	}

	result, err := h.store.ResourceSync().Create(ctx, orgId, &rs, h.eventCallback)
	return result, StoreErrorToApiStatus(err, true, api.ResourceSyncKind, rs.Metadata.Name)
}

func (h *ServiceHandler) ListResourceSyncs(ctx context.Context, params api.ListResourceSyncsParams) (*api.ResourceSyncList, api.Status) {
	orgId := getOrgIdFromContext(ctx)

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
	orgId := getOrgIdFromContext(ctx)

	result, err := h.store.ResourceSync().Get(ctx, orgId, name)
	return result, StoreErrorToApiStatus(err, false, api.ResourceSyncKind, &name)
}

func (h *ServiceHandler) ReplaceResourceSync(ctx context.Context, name string, rs api.ResourceSync) (*api.ResourceSync, api.Status) {
	orgId := getOrgIdFromContext(ctx)

	// don't overwrite fields that are managed by the service
	rs.Status = nil
	NilOutManagedObjectMetaProperties(&rs.Metadata)
	if errs := rs.Validate(); len(errs) > 0 {
		return nil, api.StatusBadRequest(errors.Join(errs...).Error())
	}
	if name != *rs.Metadata.Name {
		return nil, api.StatusBadRequest("resource name specified in metadata does not match name in path")
	}

	result, created, err := h.store.ResourceSync().CreateOrUpdate(ctx, orgId, &rs, h.eventCallback)
	return result, StoreErrorToApiStatus(err, created, api.ResourceSyncKind, &name)
}

func (h *ServiceHandler) DeleteResourceSync(ctx context.Context, name string) api.Status {
	orgId := getOrgIdFromContext(ctx)

	callback := func(ctx context.Context, tx *gorm.DB, orgId uuid.UUID, owner string) error {
		return h.store.Fleet().UnsetOwner(ctx, tx, orgId, owner)
	}

	err := h.store.ResourceSync().Delete(ctx, orgId, name, callback, h.eventDeleteCallback)
	status := StoreErrorToApiStatus(err, false, api.ResourceSyncKind, &name)
	return status
}

// Only metadata.labels and spec can be patched. If we try to patch other fields, HTTP 400 Bad Request is returned.
func (h *ServiceHandler) PatchResourceSync(ctx context.Context, name string, patch api.PatchRequest) (*api.ResourceSync, api.Status) {
	orgId := getOrgIdFromContext(ctx)

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
	result, err := h.store.ResourceSync().Update(ctx, orgId, newObj, h.eventDeleteCallback)
	return result, StoreErrorToApiStatus(err, false, api.ResourceSyncKind, &name)
}

func (h *ServiceHandler) ReplaceResourceSyncStatus(ctx context.Context, name string, resourceSync api.ResourceSync) (*api.ResourceSync, api.Status) {
	orgId := getOrgIdFromContext(ctx)

	if name != *resourceSync.Metadata.Name {
		return nil, api.StatusBadRequest("resource name specified in metadata does not match name in path")
	}

	result, err := h.store.ResourceSync().UpdateStatus(ctx, orgId, &resourceSync)
	return result, StoreErrorToApiStatus(err, false, api.ResourceSyncKind, &name)
}
