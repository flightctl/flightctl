package service

import (
	"context"
	"fmt"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/server"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/go-openapi/swag"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"k8s.io/apimachinery/pkg/labels"
)

type RepositoryStoreInterface interface {
	CreateRepository(ctx context.Context, orgId uuid.UUID, repository *api.Repository) (*api.RepositoryRead, error)
	ListRepositories(ctx context.Context, orgId uuid.UUID, listParams ListParams) (*api.RepositoryList, error)
	DeleteRepositories(ctx context.Context, orgId uuid.UUID) error
	GetRepository(ctx context.Context, orgId uuid.UUID, name string) (*api.RepositoryRead, error)
	CreateOrUpdateRepository(ctx context.Context, orgId uuid.UUID, repository *api.Repository) (*api.RepositoryRead, bool, error)
	DeleteRepository(ctx context.Context, orgId uuid.UUID, name string) error
	ListAllRepositoriesInternal() ([]model.InternalRepository, error)
	UpdateRepositoryStatusInternal(orgId uuid.UUID, repository *api.Repository) error
}

// (POST /api/v1/repositories)
func (h *ServiceHandler) CreateRepository(ctx context.Context, request server.CreateRepositoryRequestObject) (server.CreateRepositoryResponseObject, error) {
	orgId := NullOrgId

	result, err := h.repositoryStore.CreateRepository(ctx, orgId, request.Body)
	switch err {
	case nil:
		return server.CreateRepository201JSONResponse(*result), nil
	default:
		return nil, err
	}
}

// (GET /api/v1/repositories)
func (h *ServiceHandler) ListRepositories(ctx context.Context, request server.ListRepositoriesRequestObject) (server.ListRepositoriesResponseObject, error) {
	orgId := NullOrgId
	labelSelector := ""
	if request.Params.LabelSelector != nil {
		labelSelector = *request.Params.LabelSelector
	}

	labelMap, err := labels.ConvertSelectorToLabelsMap(labelSelector)
	if err != nil {
		return nil, err
	}

	cont, err := ParseContinueString(request.Params.Continue)
	if err != nil {
		return server.ListRepositories400Response{}, fmt.Errorf("failed to parse continue parameter: %s", err)
	}

	listParams := ListParams{
		Labels:   labelMap,
		Limit:    int(swag.Int32Value(request.Params.Limit)),
		Continue: cont,
	}
	if listParams.Limit == 0 {
		listParams.Limit = MaxRecordsPerListRequest
	}
	if listParams.Limit > MaxRecordsPerListRequest {
		return server.ListRepositories400Response{}, fmt.Errorf("limit cannot exceed %d", MaxRecordsPerListRequest)
	}

	result, err := h.repositoryStore.ListRepositories(ctx, orgId, listParams)
	switch err {
	case nil:
		return server.ListRepositories200JSONResponse(*result), nil
	default:
		return nil, err
	}
}

// (DELETE /api/v1/repositories)
func (h *ServiceHandler) DeleteRepositories(ctx context.Context, request server.DeleteRepositoriesRequestObject) (server.DeleteRepositoriesResponseObject, error) {
	orgId := NullOrgId

	err := h.repositoryStore.DeleteRepositories(ctx, orgId)
	switch err {
	case nil:
		return server.DeleteRepositories200JSONResponse{}, nil
	default:
		return nil, err
	}
}

// (GET /api/v1/repositories/{name})
func (h *ServiceHandler) ReadRepository(ctx context.Context, request server.ReadRepositoryRequestObject) (server.ReadRepositoryResponseObject, error) {
	orgId := NullOrgId

	result, err := h.repositoryStore.GetRepository(ctx, orgId, request.Name)
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
	orgId := NullOrgId

	result, created, err := h.repositoryStore.CreateOrUpdateRepository(ctx, orgId, request.Body)
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
	orgId := NullOrgId

	err := h.repositoryStore.DeleteRepository(ctx, orgId, request.Name)
	switch err {
	case nil:
		return server.DeleteRepository200JSONResponse{}, nil
	case gorm.ErrRecordNotFound:
		return server.DeleteRepository404Response{}, nil
	default:
		return nil, err
	}
}
