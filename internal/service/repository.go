package service

import (
	"context"
	"errors"
	"fmt"
	"reflect"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/api/server"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/service/common"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/selector"
	"github.com/go-openapi/swag"
	"github.com/google/uuid"
)

// (POST /api/v1/repositories)
func (h *ServiceHandler) CreateRepository(ctx context.Context, request server.CreateRepositoryRequestObject) (server.CreateRepositoryResponseObject, error) {
	orgId := store.NullOrgId

	// don't set fields that are managed by the service
	request.Body.Status = nil
	common.NilOutManagedObjectMetaProperties(&request.Body.Metadata)

	if errs := request.Body.Validate(); len(errs) > 0 {
		return server.CreateRepository400JSONResponse(api.StatusBadRequest(errors.Join(errs...).Error())), nil
	}

	result, err := h.store.Repository().Create(ctx, orgId, request.Body, h.callbackManager.RepositoryUpdatedCallback)
	switch {
	case err == nil:
		return server.CreateRepository201JSONResponse(*result), nil
	case errors.Is(err, flterrors.ErrResourceIsNil), errors.Is(err, flterrors.ErrIllegalResourceVersionFormat):
		return server.CreateRepository400JSONResponse(api.StatusBadRequest(err.Error())), nil
	case errors.Is(err, flterrors.ErrDuplicateName):
		return server.CreateRepository409JSONResponse(api.StatusResourceVersionConflict(err.Error())), nil
	default:
		return nil, err
	}
}

// (GET /api/v1/repositories)
func (h *ServiceHandler) ListRepositories(ctx context.Context, request server.ListRepositoriesRequestObject) (server.ListRepositoriesResponseObject, error) {
	orgId := store.NullOrgId

	cont, err := store.ParseContinueString(request.Params.Continue)
	if err != nil {
		return server.ListRepositories400JSONResponse(api.StatusBadRequest(fmt.Sprintf("failed to parse continue parameter: %v", err))), nil
	}

	var fieldSelector *selector.FieldSelector
	if request.Params.FieldSelector != nil {
		if fieldSelector, err = selector.NewFieldSelector(*request.Params.FieldSelector); err != nil {
			return server.ListRepositories400JSONResponse(api.StatusBadRequest(fmt.Sprintf("failed to parse field selector: %v", err))), nil
		}
	}

	var labelSelector *selector.LabelSelector
	if request.Params.LabelSelector != nil {
		if labelSelector, err = selector.NewLabelSelector(*request.Params.LabelSelector); err != nil {
			return server.ListRepositories400JSONResponse(api.StatusBadRequest(fmt.Sprintf("failed to parse label selector: %v", err))), nil
		}
	}

	listParams := store.ListParams{
		Limit:         int(swag.Int32Value(request.Params.Limit)),
		Continue:      cont,
		FieldSelector: fieldSelector,
		LabelSelector: labelSelector,
	}
	if listParams.Limit == 0 {
		listParams.Limit = store.MaxRecordsPerListRequest
	}
	if listParams.Limit > store.MaxRecordsPerListRequest {
		return server.ListRepositories400JSONResponse(api.StatusBadRequest(fmt.Sprintf("limit cannot exceed %d", store.MaxRecordsPerListRequest))), nil
	}

	result, err := h.store.Repository().List(ctx, orgId, listParams)
	if err == nil {
		return server.ListRepositories200JSONResponse(*result), nil
	}

	var se *selector.SelectorError

	switch {
	case selector.AsSelectorError(err, &se):
		return server.ListRepositories400JSONResponse(api.StatusBadRequest(se.Error())), nil
	default:
		return nil, err
	}
}

// (DELETE /api/v1/repositories)
func (h *ServiceHandler) DeleteRepositories(ctx context.Context, request server.DeleteRepositoriesRequestObject) (server.DeleteRepositoriesResponseObject, error) {
	orgId := store.NullOrgId

	err := h.store.Repository().DeleteAll(ctx, orgId, h.callbackManager.AllRepositoriesDeletedCallback)
	switch err {
	case nil:
		return server.DeleteRepositories200JSONResponse(api.StatusOK()), nil
	default:
		return nil, err
	}
}

// (GET /api/v1/repositories/{name})
func (h *ServiceHandler) ReadRepository(ctx context.Context, request server.ReadRepositoryRequestObject) (server.ReadRepositoryResponseObject, error) {
	orgId := store.NullOrgId

	result, err := h.store.Repository().Get(ctx, orgId, request.Name)
	switch {
	case err == nil:
		return server.ReadRepository200JSONResponse(*result), nil
	case errors.Is(err, flterrors.ErrResourceNotFound):
		return server.ReadRepository404JSONResponse(api.StatusResourceNotFound("Repository", request.Name)), nil
	default:
		return nil, err
	}
}

// (PUT /api/v1/repositories/{name})
func (h *ServiceHandler) ReplaceRepository(ctx context.Context, request server.ReplaceRepositoryRequestObject) (server.ReplaceRepositoryResponseObject, error) {
	orgId := store.NullOrgId

	// don't overwrite fields that are managed by the service
	request.Body.Status = nil
	common.NilOutManagedObjectMetaProperties(&request.Body.Metadata)

	if errs := request.Body.Validate(); len(errs) > 0 {
		return server.ReplaceRepository400JSONResponse(api.StatusBadRequest(errors.Join(errs...).Error())), nil
	}
	if request.Name != *request.Body.Metadata.Name {
		return server.ReplaceRepository400JSONResponse(api.StatusBadRequest("resource name specified in metadata does not match name in path")), nil
	}

	result, created, err := h.store.Repository().CreateOrUpdate(ctx, orgId, request.Body, h.callbackManager.RepositoryUpdatedCallback)
	switch {
	case err == nil:
		if created {
			return server.ReplaceRepository201JSONResponse(*result), nil
		} else {
			return server.ReplaceRepository200JSONResponse(*result), nil
		}
	case errors.Is(err, flterrors.ErrResourceIsNil):
		return server.ReplaceRepository400JSONResponse(api.StatusBadRequest(err.Error())), nil
	case errors.Is(err, flterrors.ErrResourceNameIsNil):
		return server.ReplaceRepository400JSONResponse(api.StatusBadRequest(err.Error())), nil
	case errors.Is(err, flterrors.ErrResourceNotFound):
		return server.ReplaceRepository404JSONResponse(api.StatusResourceNotFound("Repository", request.Name)), nil
	case errors.Is(err, flterrors.ErrNoRowsUpdated), errors.Is(err, flterrors.ErrResourceVersionConflict):
		return server.ReplaceRepository409JSONResponse(api.StatusResourceVersionConflict("")), nil
	default:
		return nil, err
	}
}

// (DELETE /api/v1/repositories/{name})
func (h *ServiceHandler) DeleteRepository(ctx context.Context, request server.DeleteRepositoryRequestObject) (server.DeleteRepositoryResponseObject, error) {
	orgId := store.NullOrgId

	err := h.store.Repository().Delete(ctx, orgId, request.Name, h.callbackManager.RepositoryUpdatedCallback)
	switch {
	case err == nil:
		return server.DeleteRepository200JSONResponse{}, nil
	case errors.Is(err, flterrors.ErrResourceNotFound):
		return server.DeleteRepository404JSONResponse(api.StatusResourceNotFound("Repository", request.Name)), nil
	default:
		return nil, err
	}
}

// (PATCH /api/v1/repositories/{name})
// Only metadata.labels and spec can be patched. If we try to patch other fields, HTTP 400 Bad Request is returned.
func (h *ServiceHandler) PatchRepository(ctx context.Context, request server.PatchRepositoryRequestObject) (server.PatchRepositoryResponseObject, error) {
	orgId := store.NullOrgId

	currentObj, err := h.store.Repository().Get(ctx, orgId, request.Name)
	if err != nil {
		switch {
		case errors.Is(err, flterrors.ErrResourceIsNil), errors.Is(err, flterrors.ErrResourceNameIsNil):
			return server.PatchRepository400JSONResponse(api.StatusBadRequest(err.Error())), nil
		case errors.Is(err, flterrors.ErrResourceNotFound):
			return server.PatchRepository404JSONResponse(api.StatusResourceNotFound("Repository", request.Name)), nil
		default:
			return nil, err
		}
	}

	newObj := &api.Repository{}
	err = ApplyJSONPatch(ctx, currentObj, newObj, *request.Body, "/api/v1/repositories/"+request.Name)
	if err != nil {
		return server.PatchRepository400JSONResponse(api.StatusBadRequest(err.Error())), nil
	}

	if newObj.Metadata.Name == nil || *currentObj.Metadata.Name != *newObj.Metadata.Name {
		return server.PatchRepository400JSONResponse(api.StatusBadRequest("metadata.name is immutable")), nil
	}
	if currentObj.ApiVersion != newObj.ApiVersion {
		return server.PatchRepository400JSONResponse(api.StatusBadRequest("apiVersion is immutable")), nil
	}
	if currentObj.Kind != newObj.Kind {
		return server.PatchRepository400JSONResponse(api.StatusBadRequest("kind is immutable")), nil
	}
	if !reflect.DeepEqual(currentObj.Status, newObj.Status) {
		return server.PatchRepository400JSONResponse(api.StatusBadRequest("status is immutable")), nil
	}

	common.NilOutManagedObjectMetaProperties(&newObj.Metadata)
	newObj.Metadata.ResourceVersion = nil

	var updateCallback func(uuid.UUID, *api.Repository, *api.Repository)

	if h.callbackManager != nil {
		updateCallback = h.callbackManager.RepositoryUpdatedCallback
	}
	result, err := h.store.Repository().Update(ctx, orgId, newObj, updateCallback)

	switch {
	case err == nil:
		return server.PatchRepository200JSONResponse(*result), nil
	case errors.Is(err, flterrors.ErrResourceIsNil), errors.Is(err, flterrors.ErrResourceNameIsNil):
		return server.PatchRepository400JSONResponse(api.StatusBadRequest(err.Error())), nil
	case errors.Is(err, flterrors.ErrResourceNotFound):
		return server.PatchRepository404JSONResponse(api.StatusResourceNotFound("Repository", request.Name)), nil
	case errors.Is(err, flterrors.ErrNoRowsUpdated), errors.Is(err, flterrors.ErrResourceVersionConflict):
		return server.PatchRepository409JSONResponse(api.StatusResourceVersionConflict("")), nil
	default:
		return nil, err
	}
}

// Not exposed via REST API; accepts and returns API objects rather than server objects
func (h *ServiceHandler) ReplaceRepositoryStatus(ctx context.Context, repository *api.Repository) (*api.Repository, error) {
	orgId := store.NullOrgId

	result, err := h.store.Repository().UpdateStatus(ctx, orgId, repository)
	if err != nil {
		return nil, err
	}
	return result, nil
}
