package service

import (
	"context"
	"errors"
	"fmt"
	"reflect"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/api/server"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/service/common"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/go-openapi/swag"
	"k8s.io/apimachinery/pkg/labels"
)

// (POST /api/v1/repositories)
func (h *ServiceHandler) CreateRepository(ctx context.Context, request server.CreateRepositoryRequestObject) (server.CreateRepositoryResponseObject, error) {
	orgId := store.NullOrgId

	// don't set fields that are managed by the service
	request.Body.Status = nil
	common.NilOutManagedObjectMetaProperties(&request.Body.Metadata)

	if errs := request.Body.Validate(); len(errs) > 0 {
		return server.CreateRepository400JSONResponse{Message: errors.Join(errs...).Error()}, nil
	}

	result, err := h.store.Repository().Create(ctx, orgId, request.Body, h.callbackManager.RepositoryUpdatedCallback)
	switch err {
	case nil:
		return server.CreateRepository201JSONResponse(*result), nil
	case flterrors.ErrResourceIsNil:
		return server.CreateRepository400JSONResponse{Message: err.Error()}, nil

	default:
		return nil, err
	}
}

// (GET /api/v1/repositories)
func (h *ServiceHandler) ListRepositories(ctx context.Context, request server.ListRepositoriesRequestObject) (server.ListRepositoriesResponseObject, error) {
	orgId := store.NullOrgId
	labelSelector := ""
	if request.Params.LabelSelector != nil {
		labelSelector = *request.Params.LabelSelector
	}

	labelMap, err := labels.ConvertSelectorToLabelsMap(labelSelector)
	if err != nil {
		return server.ListRepositories400JSONResponse{Message: err.Error()}, nil
	}

	cont, err := store.ParseContinueString(request.Params.Continue)
	if err != nil {
		return server.ListRepositories400JSONResponse{Message: fmt.Sprintf("failed to parse continue parameter: %v", err)}, nil
	}

	listParams := store.ListParams{
		Labels:   labelMap,
		Limit:    int(swag.Int32Value(request.Params.Limit)),
		Continue: cont,
	}
	if listParams.Limit == 0 {
		listParams.Limit = store.MaxRecordsPerListRequest
	}
	if listParams.Limit > store.MaxRecordsPerListRequest {
		return server.ListRepositories400JSONResponse{Message: fmt.Sprintf("limit cannot exceed %d", store.MaxRecordsPerListRequest)}, nil
	}

	result, err := h.store.Repository().List(ctx, orgId, listParams)
	switch err {
	case nil:
		return server.ListRepositories200JSONResponse(*result), nil
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
		return server.DeleteRepositories200JSONResponse{}, nil
	default:
		return nil, err
	}
}

// (GET /api/v1/repositories/{name})
func (h *ServiceHandler) ReadRepository(ctx context.Context, request server.ReadRepositoryRequestObject) (server.ReadRepositoryResponseObject, error) {
	orgId := store.NullOrgId

	result, err := h.store.Repository().Get(ctx, orgId, request.Name)
	switch err {
	case nil:
		return server.ReadRepository200JSONResponse(*result), nil
	case flterrors.ErrResourceNotFound:
		return server.ReadRepository404JSONResponse{}, nil
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
		return server.ReplaceRepository400JSONResponse{Message: errors.Join(errs...).Error()}, nil
	}
	if request.Name != *request.Body.Metadata.Name {
		return server.ReplaceRepository400JSONResponse{Message: "resource name specified in metadata does not match name in path"}, nil
	}

	result, created, err := h.store.Repository().CreateOrUpdate(ctx, orgId, request.Body, h.callbackManager.RepositoryUpdatedCallback)
	switch err {
	case nil:
		if created {
			return server.ReplaceRepository201JSONResponse(*result), nil
		} else {
			return server.ReplaceRepository200JSONResponse(*result), nil
		}
	case flterrors.ErrResourceIsNil:
		return server.ReplaceRepository400JSONResponse{Message: err.Error()}, nil
	case flterrors.ErrResourceNameIsNil:
		return server.ReplaceRepository400JSONResponse{Message: err.Error()}, nil
	case flterrors.ErrResourceNotFound:
		return server.ReplaceRepository404JSONResponse{}, nil
	case flterrors.ErrResourceVersionConflict:
		return server.ReplaceRepository409JSONResponse{Message: err.Error()}, nil
	default:
		return nil, err
	}
}

// (DELETE /api/v1/repositories/{name})
func (h *ServiceHandler) DeleteRepository(ctx context.Context, request server.DeleteRepositoryRequestObject) (server.DeleteRepositoryResponseObject, error) {
	orgId := store.NullOrgId

	err := h.store.Repository().Delete(ctx, orgId, request.Name, h.callbackManager.RepositoryUpdatedCallback)
	switch err {
	case nil:
		return server.DeleteRepository200JSONResponse{}, nil
	case flterrors.ErrResourceNotFound:
		return server.DeleteRepository404JSONResponse{}, nil
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
		switch err {
		case flterrors.ErrResourceIsNil, flterrors.ErrResourceNameIsNil:
			return server.PatchRepository400JSONResponse{Message: err.Error()}, nil
		case flterrors.ErrResourceNotFound:
			return server.PatchRepository404JSONResponse{}, nil
		default:
			return nil, err
		}
	}

	newObj := &v1alpha1.Repository{}
	err = ApplyJSONPatch(ctx, currentObj, newObj, *request.Body, "/api/v1/repositories/"+request.Name)
	if err != nil {
		return server.PatchRepository400JSONResponse{Message: err.Error()}, nil
	}

	if newObj.Metadata.Name == nil || *currentObj.Metadata.Name != *newObj.Metadata.Name {
		return server.PatchRepository400JSONResponse{Message: "metadata.name is immutable"}, nil
	}
	if currentObj.ApiVersion != newObj.ApiVersion {
		return server.PatchRepository400JSONResponse{Message: "apiVersion is immutable"}, nil
	}
	if currentObj.Kind != newObj.Kind {
		return server.PatchRepository400JSONResponse{Message: "kind is immutable"}, nil
	}
	if !reflect.DeepEqual(currentObj.Status, newObj.Status) {
		return server.PatchRepository400JSONResponse{Message: "status is immutable"}, nil
	}

	common.NilOutManagedObjectMetaProperties(&newObj.Metadata)

	var updateCallback func(repo *model.Repository)

	if h.callbackManager != nil {
		updateCallback = h.callbackManager.RepositoryUpdatedCallback
	}
	result, _, err := h.store.Repository().CreateOrUpdate(ctx, orgId, newObj, updateCallback)

	switch err {
	case nil:
		return server.PatchRepository200JSONResponse(*result), nil
	case flterrors.ErrResourceIsNil, flterrors.ErrResourceNameIsNil:
		return server.PatchRepository400JSONResponse{Message: err.Error()}, nil
	case flterrors.ErrResourceNotFound:
		return server.PatchRepository404JSONResponse{}, nil
	default:
		return nil, err
	}
}
