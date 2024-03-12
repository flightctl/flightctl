package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/api/server"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/go-openapi/swag"
	"gorm.io/gorm"
	"k8s.io/apimachinery/pkg/labels"
)

func TemplateVersionFromReader(r io.Reader) (*api.TemplateVersion, error) {
	var templateVersion api.TemplateVersion
	decoder := json.NewDecoder(r)
	decoder.DisallowUnknownFields()
	err := decoder.Decode(&templateVersion)
	return &templateVersion, err
}

// (POST /api/v1/templateVersions)
func (h *ServiceHandler) CreateTemplateVersion(ctx context.Context, request server.CreateTemplateVersionRequestObject) (server.CreateTemplateVersionResponseObject, error) {
	orgId := store.NullOrgId
	if request.Body.Metadata.Name == nil {
		return server.CreateTemplateVersion400JSONResponse{Message: "metadata.name not specified"}, nil
	}

	// don't set fields that are managed by the service
	request.Body.Status = nil
	NilOutManagedObjectMetaProperties(&request.Body.Metadata)

	result, err := h.store.TemplateVersion().Create(ctx, orgId, request.Body, h.taskManager.TemplateVersionCreatedCallback)

	switch err {
	case nil:
		return server.CreateTemplateVersion201JSONResponse(*result), nil
	case gorm.ErrRecordNotFound:
		return server.CreateTemplateVersion400JSONResponse{Message: "specified fleet not found"}, nil
	case gorm.ErrInvalidData:
		return server.CreateTemplateVersion409JSONResponse{Message: "a template version with this name and fleet already exists"}, nil

	default:
		return nil, err
	}
}

// (GET /api/v1/templateVersions)
func (h *ServiceHandler) ListTemplateVersions(ctx context.Context, request server.ListTemplateVersionsRequestObject) (server.ListTemplateVersionsResponseObject, error) {
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
		return server.ListTemplateVersions400JSONResponse{Message: fmt.Sprintf("failed to parse continue parameter: %v", err)}, nil
	}

	listParams := store.ListParams{
		Labels:   labelMap,
		Limit:    int(swag.Int32Value(request.Params.Limit)),
		Continue: cont,
		Owner:    request.Params.Owner,
	}
	if listParams.Limit == 0 {
		listParams.Limit = store.MaxRecordsPerListRequest
	}
	if listParams.Limit > store.MaxRecordsPerListRequest {
		return server.ListTemplateVersions400JSONResponse{Message: fmt.Sprintf("limit cannot exceed %d", store.MaxRecordsPerListRequest)}, nil
	}

	result, err := h.store.TemplateVersion().List(ctx, orgId, listParams)
	switch err {
	case nil:
		return server.ListTemplateVersions200JSONResponse(*result), nil
	default:
		return nil, err
	}
}

// (DELETE /api/v1/templateVersions)
func (h *ServiceHandler) DeleteTemplateVersions(ctx context.Context, request server.DeleteTemplateVersionsRequestObject) (server.DeleteTemplateVersionsResponseObject, error) {
	orgId := store.NullOrgId

	err := h.store.TemplateVersion().DeleteAll(ctx, orgId, request.Params.Owner)
	switch err {
	case nil:
		return server.DeleteTemplateVersions200JSONResponse{}, nil
	default:
		return nil, err
	}
}

// (GET /api/v1/fleets/{fleet}/templateVersions/{name})
func (h *ServiceHandler) ReadTemplateVersion(ctx context.Context, request server.ReadTemplateVersionRequestObject) (server.ReadTemplateVersionResponseObject, error) {
	orgId := store.NullOrgId

	result, err := h.store.TemplateVersion().Get(ctx, orgId, request.Fleet, request.Name)
	switch err {
	case nil:
		return server.ReadTemplateVersion200JSONResponse(*result), nil
	case gorm.ErrRecordNotFound:
		return server.ReadTemplateVersion404JSONResponse{}, nil
	default:
		return nil, err
	}
}

// (DELETE /api/v1/fleets/{fleet}/templateVersions/{name})
func (h *ServiceHandler) DeleteTemplateVersion(ctx context.Context, request server.DeleteTemplateVersionRequestObject) (server.DeleteTemplateVersionResponseObject, error) {
	orgId := store.NullOrgId

	err := h.store.TemplateVersion().Delete(ctx, orgId, request.Fleet, request.Name)
	switch err {
	case nil:
		return server.DeleteTemplateVersion200JSONResponse{}, nil
	case gorm.ErrRecordNotFound:
		return server.DeleteTemplateVersion404JSONResponse{}, nil
	default:
		return nil, err
	}
}
