package service

import (
	"context"
	"errors"
	"fmt"
	"reflect"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/selector"
	"github.com/go-openapi/swag"
	"github.com/google/uuid"
)

func (h *ServiceHandler) CreateRepository(ctx context.Context, repo api.Repository) (*api.Repository, api.Status) {
	orgId := store.NullOrgId

	// don't set fields that are managed by the service
	repo.Status = nil
	NilOutManagedObjectMetaProperties(&repo.Metadata)

	if errs := repo.Validate(); len(errs) > 0 {
		return nil, api.StatusBadRequest(errors.Join(errs...).Error())
	}

	result, err := h.store.Repository().Create(ctx, orgId, &repo, h.callbackManager.RepositoryUpdatedCallback)
	return result, StoreErrorToApiStatus(err, true, api.RepositoryKind, repo.Metadata.Name)
}

func (h *ServiceHandler) ListRepositories(ctx context.Context, params api.ListRepositoriesParams) (*api.RepositoryList, api.Status) {
	orgId := store.NullOrgId

	cont, err := store.ParseContinueString(params.Continue)
	if err != nil {
		return nil, api.StatusBadRequest(fmt.Sprintf("failed to parse continue parameter: %v", err))
	}

	var fieldSelector *selector.FieldSelector
	if params.FieldSelector != nil {
		if fieldSelector, err = selector.NewFieldSelector(*params.FieldSelector); err != nil {
			return nil, api.StatusBadRequest(fmt.Sprintf("failed to parse field selector: %v", err))
		}
	}

	var labelSelector *selector.LabelSelector
	if params.LabelSelector != nil {
		if labelSelector, err = selector.NewLabelSelector(*params.LabelSelector); err != nil {
			return nil, api.StatusBadRequest(fmt.Sprintf("failed to parse label selector: %v", err))
		}
	}

	listParams := store.ListParams{
		Limit:         int(swag.Int32Value(params.Limit)),
		Continue:      cont,
		FieldSelector: fieldSelector,
		LabelSelector: labelSelector,
	}
	if listParams.Limit == 0 {
		listParams.Limit = MaxRecordsPerListRequest
	} else if listParams.Limit > MaxRecordsPerListRequest {
		return nil, api.StatusBadRequest(fmt.Sprintf("limit cannot exceed %d", MaxRecordsPerListRequest))
	} else if listParams.Limit < 0 {
		return nil, api.StatusBadRequest("limit cannot be negative")
	}

	result, err := h.store.Repository().List(ctx, orgId, listParams)
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

func (h *ServiceHandler) DeleteRepositories(ctx context.Context) api.Status {
	orgId := store.NullOrgId

	err := h.store.Repository().DeleteAll(ctx, orgId, h.callbackManager.AllRepositoriesDeletedCallback)
	return StoreErrorToApiStatus(err, false, api.RepositoryKind, nil)
}

func (h *ServiceHandler) GetRepository(ctx context.Context, name string) (*api.Repository, api.Status) {
	orgId := store.NullOrgId

	result, err := h.store.Repository().Get(ctx, orgId, name)
	return result, StoreErrorToApiStatus(err, false, api.RepositoryKind, &name)
}

func (h *ServiceHandler) ReplaceRepository(ctx context.Context, name string, repo api.Repository) (*api.Repository, api.Status) {
	orgId := store.NullOrgId

	// don't overwrite fields that are managed by the service
	repo.Status = nil
	NilOutManagedObjectMetaProperties(&repo.Metadata)

	if errs := repo.Validate(); len(errs) > 0 {
		return nil, api.StatusBadRequest(errors.Join(errs...).Error())
	}
	if name != *repo.Metadata.Name {
		return nil, api.StatusBadRequest("resource name specified in metadata does not match name in path")
	}

	result, created, err := h.store.Repository().CreateOrUpdate(ctx, orgId, &repo, h.callbackManager.RepositoryUpdatedCallback)
	return result, StoreErrorToApiStatus(err, created, api.RepositoryKind, &name)
}

func (h *ServiceHandler) DeleteRepository(ctx context.Context, name string) api.Status {
	orgId := store.NullOrgId

	err := h.store.Repository().Delete(ctx, orgId, name, h.callbackManager.RepositoryUpdatedCallback)
	return StoreErrorToApiStatus(err, false, api.RepositoryKind, &name)
}

// Only metadata.labels and spec can be patched. If we try to patch other fields, HTTP 400 Bad Request is returned.
func (h *ServiceHandler) PatchRepository(ctx context.Context, name string, patch api.PatchRequest) (*api.Repository, api.Status) {
	orgId := store.NullOrgId

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

	var updateCallback func(uuid.UUID, *api.Repository, *api.Repository)

	if h.callbackManager != nil {
		updateCallback = h.callbackManager.RepositoryUpdatedCallback
	}
	result, err := h.store.Repository().Update(ctx, orgId, newObj, updateCallback)
	return result, StoreErrorToApiStatus(err, false, api.RepositoryKind, &name)
}

func (h *ServiceHandler) ReplaceRepositoryStatus(ctx context.Context, name string, repository api.Repository) (*api.Repository, api.Status) {
	orgId := store.NullOrgId

	if name != *repository.Metadata.Name {
		return nil, api.StatusBadRequest("resource name specified in metadata does not match name in path")
	}

	result, err := h.store.Repository().UpdateStatus(ctx, orgId, &repository)
	return result, StoreErrorToApiStatus(err, false, api.RepositoryKind, &name)
}

func (h *ServiceHandler) GetRepositoryFleetReferences(ctx context.Context, name string) (*api.FleetList, api.Status) {
	orgId := store.NullOrgId

	result, err := h.store.Repository().GetFleetRefs(ctx, orgId, name)
	return result, StoreErrorToApiStatus(err, false, api.RepositoryKind, &name)
}

func (h *ServiceHandler) GetRepositoryDeviceReferences(ctx context.Context, name string) (*api.DeviceList, api.Status) {
	orgId := store.NullOrgId

	result, err := h.store.Repository().GetDeviceRefs(ctx, orgId, name)
	return result, StoreErrorToApiStatus(err, false, api.RepositoryKind, &name)
}
