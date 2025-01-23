package service

import (
	"context"
	"errors"
	"fmt"
	"reflect"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/api/server"
	"github.com/flightctl/flightctl/internal/auth"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/service/common"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/selector"
	"github.com/go-openapi/swag"
	"github.com/google/uuid"
)

// (POST /api/v1/repositories)
func (h *ServiceHandler) CreateRepository(ctx context.Context, request server.CreateRepositoryRequestObject) (server.CreateRepositoryResponseObject, error) {
	allowed, err := auth.GetAuthZ().CheckPermission(ctx, "repositories", "create")
	if err != nil {
		h.log.WithError(err).Error("failed to check authorization permission")
		return server.CreateRepository503JSONResponse{Message: AuthorizationServerUnavailable}, nil
	}
	if !allowed {
		return server.CreateRepository403JSONResponse{Message: Forbidden}, nil
	}
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
	case flterrors.ErrResourceIsNil, flterrors.ErrIllegalResourceVersionFormat:
		return server.CreateRepository400JSONResponse{Message: err.Error()}, nil
	case flterrors.ErrDuplicateName:
		return server.CreateRepository409JSONResponse{Message: err.Error()}, nil
	default:
		return nil, err
	}
}

// (GET /api/v1/repositories)
func (h *ServiceHandler) ListRepositories(ctx context.Context, request server.ListRepositoriesRequestObject) (server.ListRepositoriesResponseObject, error) {
	allowed, err := auth.GetAuthZ().CheckPermission(ctx, "repositories", "list")
	if err != nil {
		h.log.WithError(err).Error("failed to check authorization permission")
		return server.ListRepositories503JSONResponse{Message: AuthorizationServerUnavailable}, nil
	}
	if !allowed {
		return server.ListRepositories403JSONResponse{Message: Forbidden}, nil
	}
	orgId := store.NullOrgId

	cont, err := store.ParseContinueString(request.Params.Continue)
	if err != nil {
		return server.ListRepositories400JSONResponse{Message: fmt.Sprintf("failed to parse continue parameter: %v", err)}, nil
	}

	var fieldSelector *selector.FieldSelector
	if request.Params.FieldSelector != nil {
		if fieldSelector, err = selector.NewFieldSelector(*request.Params.FieldSelector); err != nil {
			return server.ListRepositories400JSONResponse{Message: fmt.Sprintf("failed to parse field selector: %v", err)}, nil
		}
	}

	var labelSelector *selector.LabelSelector
	if request.Params.LabelSelector != nil {
		if labelSelector, err = selector.NewLabelSelector(*request.Params.LabelSelector); err != nil {
			return server.ListRepositories400JSONResponse{Message: fmt.Sprintf("failed to parse label selector: %v", err)}, nil
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
		return server.ListRepositories400JSONResponse{Message: fmt.Sprintf("limit cannot exceed %d", store.MaxRecordsPerListRequest)}, nil
	}

	result, err := h.store.Repository().List(ctx, orgId, listParams)
	if err == nil {
		return server.ListRepositories200JSONResponse(*result), nil
	}

	var se *selector.SelectorError

	switch {
	case selector.AsSelectorError(err, &se):
		return server.ListRepositories400JSONResponse{Message: se.Error()}, nil
	default:
		return nil, err
	}
}

// (DELETE /api/v1/repositories)
func (h *ServiceHandler) DeleteRepositories(ctx context.Context, request server.DeleteRepositoriesRequestObject) (server.DeleteRepositoriesResponseObject, error) {
	allowed, err := auth.GetAuthZ().CheckPermission(ctx, "repositories", "deletecollection")
	if err != nil {
		h.log.WithError(err).Error("failed to check authorization permission")
		return server.DeleteRepositories503JSONResponse{Message: AuthorizationServerUnavailable}, nil
	}
	if !allowed {
		return server.DeleteRepositories403JSONResponse{Message: Forbidden}, nil
	}
	orgId := store.NullOrgId

	err = h.store.Repository().DeleteAll(ctx, orgId, h.callbackManager.AllRepositoriesDeletedCallback)
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
	allowed, err := auth.GetAuthZ().CheckPermission(ctx, "repositories", "update")
	if err != nil {
		h.log.WithError(err).Error("failed to check authorization permission")
		return server.ReplaceRepository503JSONResponse{Message: AuthorizationServerUnavailable}, nil
	}
	if !allowed {
		return server.ReplaceRepository403JSONResponse{Message: Forbidden}, nil
	}
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
	case flterrors.ErrNoRowsUpdated, flterrors.ErrResourceVersionConflict:
		return server.ReplaceRepository409JSONResponse{}, nil
	default:
		return nil, err
	}
}

// (DELETE /api/v1/repositories/{name})
func (h *ServiceHandler) DeleteRepository(ctx context.Context, request server.DeleteRepositoryRequestObject) (server.DeleteRepositoryResponseObject, error) {
	allowed, err := auth.GetAuthZ().CheckPermission(ctx, "repositories", "delete")
	if err != nil {
		h.log.WithError(err).Error("failed to check authorization permission")
		return server.DeleteRepository503JSONResponse{Message: AuthorizationServerUnavailable}, nil
	}
	if !allowed {
		return server.DeleteRepository403JSONResponse{Message: Forbidden}, nil
	}
	orgId := store.NullOrgId

	err = h.store.Repository().Delete(ctx, orgId, request.Name, h.callbackManager.RepositoryUpdatedCallback)
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
	allowed, err := auth.GetAuthZ().CheckPermission(ctx, "repositories", "patch")
	if err != nil {
		h.log.WithError(err).Error("failed to check authorization permission")
		return server.PatchRepository503JSONResponse{Message: AuthorizationServerUnavailable}, nil
	}
	if !allowed {
		return server.PatchRepository403JSONResponse{Message: Forbidden}, nil
	}
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

	newObj := &api.Repository{}
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
	newObj.Metadata.ResourceVersion = nil

	var updateCallback func(uuid.UUID, *api.Repository, *api.Repository)

	if h.callbackManager != nil {
		updateCallback = h.callbackManager.RepositoryUpdatedCallback
	}
	result, err := h.store.Repository().Update(ctx, orgId, newObj, updateCallback)

	switch err {
	case nil:
		return server.PatchRepository200JSONResponse(*result), nil
	case flterrors.ErrResourceIsNil, flterrors.ErrResourceNameIsNil:
		return server.PatchRepository400JSONResponse{Message: err.Error()}, nil
	case flterrors.ErrResourceNotFound:
		return server.PatchRepository404JSONResponse{}, nil
	case flterrors.ErrNoRowsUpdated, flterrors.ErrResourceVersionConflict:
		return server.PatchRepository409JSONResponse{}, nil
	default:
		return nil, err
	}
}
