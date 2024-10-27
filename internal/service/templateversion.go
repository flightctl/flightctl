package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/api/server"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/selector"
	"github.com/go-openapi/swag"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
)

func TemplateVersionFromReader(r io.Reader) (*api.TemplateVersion, error) {
	var templateVersion api.TemplateVersion
	decoder := json.NewDecoder(r)
	decoder.DisallowUnknownFields()
	err := decoder.Decode(&templateVersion)
	return &templateVersion, err
}

// (GET /api/v1/api/v1/fleets/{fleet}/templateVersions)
func (h *ServiceHandler) ListTemplateVersions(ctx context.Context, request server.ListTemplateVersionsRequestObject) (server.ListTemplateVersionsResponseObject, error) {
	orgId := store.NullOrgId
	labelSelector := ""
	if request.Params.LabelSelector != nil {
		labelSelector = *request.Params.LabelSelector
	}

	labelMap, err := labels.ConvertSelectorToLabelsMap(labelSelector)
	if err != nil {
		return server.ListTemplateVersions400JSONResponse{Message: err.Error()}, nil
	}

	cont, err := store.ParseContinueString(request.Params.Continue)
	if err != nil {
		return server.ListTemplateVersions400JSONResponse{Message: fmt.Sprintf("failed to parse continue parameter: %v", err)}, nil
	}

	var fieldSelector fields.Selector
	if request.Params.FieldSelector != nil {
		if fieldSelector, err = fields.ParseSelector(*request.Params.FieldSelector); err != nil {
			return server.ListTemplateVersions400JSONResponse{Message: fmt.Sprintf("failed to parse field selector: %v", err)}, nil
		}
	}

	var sortField *store.SortField
	if request.Params.SortBy != nil {
		sortField = &store.SortField{
			FieldName: selector.SelectorFieldName(*request.Params.SortBy),
			Order:     *request.Params.SortOrder,
		}
	}

	listParams := store.ListParams{
		Labels:        labelMap,
		Limit:         int(swag.Int32Value(request.Params.Limit)),
		Continue:      cont,
		FleetName:     &request.Fleet,
		FieldSelector: fieldSelector,
		SortBy:        sortField,
	}
	if listParams.Limit == 0 {
		listParams.Limit = store.MaxRecordsPerListRequest
	}
	if listParams.Limit > store.MaxRecordsPerListRequest {
		return server.ListTemplateVersions400JSONResponse{Message: fmt.Sprintf("limit cannot exceed %d", store.MaxRecordsPerListRequest)}, nil
	}

	result, err := h.store.TemplateVersion().List(ctx, orgId, listParams)
	if err == nil {
		return server.ListTemplateVersions200JSONResponse(*result), nil
	}

	var se *selector.SelectorError

	switch {
	case selector.AsSelectorError(err, &se):
		return server.ListTemplateVersions400JSONResponse{Message: se.Error()}, nil
	default:
		return nil, err
	}
}

// (DELETE /api/v1/api/v1/fleets/{fleet}/templateVersions)
func (h *ServiceHandler) DeleteTemplateVersions(ctx context.Context, request server.DeleteTemplateVersionsRequestObject) (server.DeleteTemplateVersionsResponseObject, error) {
	orgId := store.NullOrgId

	err := h.store.TemplateVersion().DeleteAll(ctx, orgId, &request.Fleet)
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
	case flterrors.ErrResourceNotFound:
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
	case flterrors.ErrResourceNotFound:
		return server.DeleteTemplateVersion404JSONResponse{}, nil
	default:
		return nil, err
	}
}
