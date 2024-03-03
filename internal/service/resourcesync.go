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

// (POST /api/v1/resourcesyncs)
func (h *ServiceHandler) CreateResourceSync(ctx context.Context, request server.CreateResourceSyncRequestObject) (server.CreateResourceSyncResponseObject, error) {
	orgId := store.NullOrgId

	result, err := h.store.ResourceSync().Create(ctx, orgId, request.Body)
	switch err {
	case nil:
		return server.CreateResourceSync201JSONResponse(*result), nil
	default:
		return nil, err
	}
}

// (GET /api/v1/resourcesyncs)
func (h *ServiceHandler) ListResourceSync(ctx context.Context, request server.ListResourceSyncRequestObject) (server.ListResourceSyncResponseObject, error) {
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
		return server.ListResourceSync400Response{}, fmt.Errorf("failed to parse continue parameter: %w", err)
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
		return server.ListResourceSync400Response{}, fmt.Errorf("limit cannot exceed %d", store.MaxRecordsPerListRequest)
	}

	result, err := h.store.ResourceSync().List(ctx, orgId, listParams)
	switch err {
	case nil:
		return server.ListResourceSync200JSONResponse(*result), nil
	default:
		return nil, err
	}
}

// (DELETE /api/v1/resourcesyncs)
func (h *ServiceHandler) DeleteResourceSyncs(ctx context.Context, request server.DeleteResourceSyncsRequestObject) (server.DeleteResourceSyncsResponseObject, error) {
	orgId := store.NullOrgId

	err := h.store.ResourceSync().DeleteAll(ctx, orgId, h.store.Fleet().UnsetOwnerByKind)
	switch err {
	case nil:
		return server.DeleteResourceSyncs200JSONResponse{}, nil
	default:
		return nil, err
	}
}

// (GET /api/v1/resourcesyncs/{name})
func (h *ServiceHandler) ReadResourceSync(ctx context.Context, request server.ReadResourceSyncRequestObject) (server.ReadResourceSyncResponseObject, error) {
	orgId := store.NullOrgId

	result, err := h.store.ResourceSync().Get(ctx, orgId, request.Name)
	switch err {
	case nil:
		return server.ReadResourceSync200JSONResponse(*result), nil
	case gorm.ErrRecordNotFound:
		return server.ReadResourceSync404Response{}, nil
	default:
		return nil, err
	}
}

// (PUT /api/v1/resourcesyncs/{name})
func (h *ServiceHandler) ReplaceResourceSync(ctx context.Context, request server.ReplaceResourceSyncRequestObject) (server.ReplaceResourceSyncResponseObject, error) {
	orgId := store.NullOrgId
	if request.Body.Metadata.Name == nil || request.Name != *request.Body.Metadata.Name {
		return server.ReplaceResourceSync400Response{}, nil
	}

	result, created, err := h.store.ResourceSync().CreateOrUpdate(ctx, orgId, request.Body)
	switch err {
	case nil:
		if created {
			return server.ReplaceResourceSync201JSONResponse(*result), nil
		} else {
			return server.ReplaceResourceSync200JSONResponse(*result), nil
		}
	case gorm.ErrRecordNotFound:
		return server.ReplaceResourceSync404Response{}, nil
	default:
		return nil, err
	}
}

// (DELETE /api/v1/resourcesyncs/{name})
func (h *ServiceHandler) DeleteResourceSync(ctx context.Context, request server.DeleteResourceSyncRequestObject) (server.DeleteResourceSyncResponseObject, error) {
	orgId := store.NullOrgId
	err := h.store.ResourceSync().Delete(ctx, orgId, request.Name, h.store.Fleet().UnsetOwner)
	switch err {
	case nil:
		return server.DeleteResourceSync200JSONResponse{}, nil
	case gorm.ErrRecordNotFound:
		return server.DeleteResourceSync404Response{}, nil
	default:
		return nil, err
	}
}
