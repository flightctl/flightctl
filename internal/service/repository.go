package service

import (
	"context"
	"errors"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/store/selector"
	"github.com/google/uuid"
	"github.com/samber/lo"
)

func (h *ServiceHandler) CreateRepository(ctx context.Context, orgId uuid.UUID, repository api.Repository) (*api.Repository, api.Status) {
	// don't set fields that are managed by the service
	repository.Status = nil
	NilOutManagedObjectMetaProperties(&repository.Metadata)

	if errs := repository.Validate(); len(errs) > 0 {
		return nil, api.StatusBadRequest(errors.Join(errs...).Error())
	}

	result, err := h.store.Repository().Create(ctx, orgId, &repository, h.callbackRepositoryUpdated)
	return result, StoreErrorToApiStatus(err, true, api.RepositoryKind, repository.Metadata.Name)
}

func (h *ServiceHandler) ListRepositories(ctx context.Context, orgId uuid.UUID, params api.ListRepositoriesParams) (*api.RepositoryList, api.Status) {
	listParams, status := prepareListParams(params.Continue, params.LabelSelector, params.FieldSelector, params.Limit)
	if status != api.StatusOK() {
		return nil, status
	}

	result, err := h.store.Repository().List(ctx, orgId, *listParams)
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

func (h *ServiceHandler) GetRepository(ctx context.Context, orgId uuid.UUID, name string) (*api.Repository, api.Status) {
	result, err := h.store.Repository().Get(ctx, orgId, name)
	return result, StoreErrorToApiStatus(err, false, api.RepositoryKind, &name)
}

func (h *ServiceHandler) ReplaceRepository(ctx context.Context, orgId uuid.UUID, name string, repository api.Repository) (*api.Repository, api.Status) {
	// don't overwrite fields that are managed by the service for external requests
	if !IsInternalRequest(ctx) {
		repository.Status = nil
		NilOutManagedObjectMetaProperties(&repository.Metadata)
	}

	if errs := repository.Validate(); len(errs) > 0 {
		return nil, api.StatusBadRequest(errors.Join(errs...).Error())
	}
	if name != *repository.Metadata.Name {
		return nil, api.StatusBadRequest("resource name specified in metadata does not match name in path")
	}

	result, created, err := h.store.Repository().CreateOrUpdate(ctx, orgId, &repository, h.callbackRepositoryUpdated)
	return result, StoreErrorToApiStatus(err, created, api.RepositoryKind, &name)
}

func (h *ServiceHandler) DeleteRepository(ctx context.Context, orgId uuid.UUID, name string) api.Status {
	err := h.store.Repository().Delete(ctx, orgId, name, h.callbackRepositoryDeleted)
	return StoreErrorToApiStatus(err, false, api.RepositoryKind, &name)
}

func (h *ServiceHandler) PatchRepository(ctx context.Context, orgId uuid.UUID, name string, patch api.PatchRequest) (*api.Repository, api.Status) {
	currentObj, err := h.store.Repository().Get(ctx, orgId, name)
	if err != nil {
		return nil, StoreErrorToApiStatus(err, false, api.RepositoryKind, &name)
	}

	newObj := &api.Repository{}
	err = ApplyJSONPatch(ctx, currentObj, newObj, patch, "/api/v1/repositories/"+name)
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

	result, err := h.store.Repository().Update(ctx, orgId, newObj, h.callbackRepositoryUpdated)
	return result, StoreErrorToApiStatus(err, false, api.RepositoryKind, &name)
}

func (h *ServiceHandler) ReplaceRepositoryStatusByError(ctx context.Context, orgId uuid.UUID, name string, repository api.Repository, err error) (*api.Repository, api.Status) {
	if name != lo.FromPtr(repository.Metadata.Name) {
		return nil, api.StatusBadRequest("resource name specified in metadata does not match name in path")
	}

	// This is the only Condition for Repository
	changed := api.SetStatusConditionByError(&repository.Status.Conditions, api.ConditionTypeRepositoryAccessible, "Accessible", "Inaccessible", err)
	if !changed {
		// Nothing to do
		return &repository, api.StatusOK()
	}

	result, err := h.store.Repository().UpdateStatus(ctx, orgId, &repository, h.callbackRepositoryUpdated)
	return result, StoreErrorToApiStatus(err, false, api.RepositoryKind, &name)
}

func (h *ServiceHandler) GetRepositoryFleetReferences(ctx context.Context, orgId uuid.UUID, name string) (*api.FleetList, api.Status) {
	result, err := h.store.Repository().GetFleetRefs(ctx, orgId, name)
	return result, StoreErrorToApiStatus(err, false, api.RepositoryKind, &name)
}

func (h *ServiceHandler) GetRepositoryDeviceReferences(ctx context.Context, orgId uuid.UUID, name string) (*api.DeviceList, api.Status) {
	result, err := h.store.Repository().GetDeviceRefs(ctx, orgId, name)
	return result, StoreErrorToApiStatus(err, false, api.RepositoryKind, &name)
}

// callbackRepositoryUpdated is the repository-specific callback that handles repository update events
func (h *ServiceHandler) callbackRepositoryUpdated(ctx context.Context, resourceKind api.ResourceKind, orgId uuid.UUID, name string, oldResource, newResource interface{}, created bool, err error) {
	h.eventHandler.HandleRepositoryUpdatedEvents(ctx, resourceKind, orgId, name, oldResource, newResource, created, err)
}

// callbackRepositoryDeleted is the repository-specific callback that handles repository deletion events
func (h *ServiceHandler) callbackRepositoryDeleted(ctx context.Context, resourceKind api.ResourceKind, orgId uuid.UUID, name string, oldResource, newResource interface{}, created bool, err error) {
	h.eventHandler.HandleGenericResourceDeletedEvents(ctx, resourceKind, orgId, name, oldResource, newResource, created, err)
}
