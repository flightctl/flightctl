package service

import (
	"context"
	"fmt"

	"github.com/flightctl/flightctl/internal/api/server"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/go-openapi/swag"
	"gorm.io/gorm"
	"k8s.io/apimachinery/pkg/labels"
)

// (POST /api/v1/repositories)
func (h *ServiceHandler) CreateRepository(ctx context.Context, request server.CreateRepositoryRequestObject) (server.CreateRepositoryResponseObject, error) {
	orgId := store.NullOrgId

	result, err := h.store.Repository().Create(ctx, orgId, request.Body)
	switch err {
	case nil:
		return server.CreateRepository201JSONResponse(*result), nil
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
		return nil, err
	}

	cont, err := store.ParseContinueString(request.Params.Continue)
	if err != nil {
		return server.ListRepositories400Response{}, fmt.Errorf("failed to parse continue parameter: %w", err)
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
		return server.ListRepositories400Response{}, fmt.Errorf("limit cannot exceed %d", store.MaxRecordsPerListRequest)
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

	err := h.store.Repository().DeleteAll(ctx, orgId)
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
	case gorm.ErrRecordNotFound:
		return server.ReadRepository404Response{}, nil
	default:
		return nil, err
	}
}

// (PUT /api/v1/repositories/{name})
func (h *ServiceHandler) ReplaceRepository(ctx context.Context, request server.ReplaceRepositoryRequestObject) (server.ReplaceRepositoryResponseObject, error) {
	orgId := store.NullOrgId
	if request.Body.Metadata.Name == nil || request.Name != *request.Body.Metadata.Name {
		return server.ReplaceRepository400Response{}, nil
	}

	result, created, err := h.store.Repository().CreateOrUpdate(ctx, orgId, request.Body)
	switch err {
	case nil:
		if created {
			return server.ReplaceRepository201JSONResponse(*result), nil
		} else {
			return server.ReplaceRepository200JSONResponse(*result), nil
		}
	case gorm.ErrRecordNotFound:
		return server.ReplaceRepository404Response{}, nil
	default:
		return nil, err
	}
}

// (DELETE /api/v1/repositories/{name})
func (h *ServiceHandler) DeleteRepository(ctx context.Context, request server.DeleteRepositoryRequestObject) (server.DeleteRepositoryResponseObject, error) {
	orgId := store.NullOrgId

	err := h.store.Repository().Delete(ctx, orgId, request.Name)
	switch err {
	case nil:
		return server.DeleteRepository200JSONResponse{}, nil
	case gorm.ErrRecordNotFound:
		return server.DeleteRepository404Response{}, nil
	default:
		return nil, err
	}
}
