package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/api/server"
	"github.com/flightctl/flightctl/internal/auth"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/selector"
	"github.com/flightctl/flightctl/internal/tasks"
	k8sselector "github.com/flightctl/flightctl/pkg/k8s/selector"
	"github.com/flightctl/flightctl/pkg/k8s/selector/fields"
	"github.com/go-openapi/swag"
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
	allowed, err := auth.GetAuthZ().CheckPermission(ctx, "fleets/templateversions", "list")
	if err != nil {
		h.log.WithError(err).Error("failed to check authorization permission")
		return server.ListTemplateVersions503JSONResponse{Message: AuthorizationServerUnavailable}, nil
	}
	if !allowed {
		return server.ListTemplateVersions403JSONResponse{Message: Forbidden}, nil
	}
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

	var fieldSelector k8sselector.Selector
	if request.Params.FieldSelector != nil {
		if fieldSelector, err = fields.ParseSelector(*request.Params.FieldSelector); err != nil {
			return server.ListTemplateVersions400JSONResponse{Message: fmt.Sprintf("failed to parse field selector: %v", err)}, nil
		}
	}

	listParams := store.ListParams{
		Labels:        labelMap,
		Limit:         int(swag.Int32Value(request.Params.Limit)),
		Continue:      cont,
		FleetName:     &request.Fleet,
		FieldSelector: fieldSelector,
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
	allowed, err := auth.GetAuthZ().CheckPermission(ctx, "fleets/templateversions", "deletecollection")
	if err != nil {
		h.log.WithError(err).Error("failed to check authorization permission")
		return server.DeleteTemplateVersions503JSONResponse{Message: AuthorizationServerUnavailable}, nil
	}
	if !allowed {
		return server.DeleteTemplateVersions403JSONResponse{Message: Forbidden}, nil
	}
	orgId := store.NullOrgId
	// Iterate through the relevant templateVersions, 100 at a time, and delete each one's config storage
	listParams := store.ListParams{Limit: 100, FleetName: &request.Fleet}
	for {
		result, err := h.store.TemplateVersion().List(ctx, orgId, listParams)
		if err != nil {
			h.log.Warnf("failed deleting config storage for templateVersions in org %s", orgId)
			break
		}
		for _, tv := range result.Items {
			tvkey := tasks.TemplateVersionKey{OrgID: orgId, Fleet: tv.Spec.Fleet, TemplateVersion: *tv.Metadata.Name}
			err := h.configStorage.DeleteKeysForTemplateVersion(ctx, tvkey.ComposeKey())
			if err != nil {
				h.log.Warnf("failed deleting config storage for templateVersion %s/%s/%s", orgId, tv.Spec.Fleet, *tv.Metadata.Name)
			}
		}
		if result.Metadata.Continue != nil {
			cont, _ := store.ParseContinueString(result.Metadata.Continue)
			listParams.Continue = cont
		} else {
			break
		}
	}

	err = h.store.TemplateVersion().DeleteAll(ctx, orgId, &request.Fleet)
	switch err {
	case nil:
		return server.DeleteTemplateVersions200JSONResponse{}, nil
	default:
		return nil, err
	}
}

// (GET /api/v1/fleets/{fleet}/templateVersions/{name})
func (h *ServiceHandler) ReadTemplateVersion(ctx context.Context, request server.ReadTemplateVersionRequestObject) (server.ReadTemplateVersionResponseObject, error) {
	allowed, err := auth.GetAuthZ().CheckPermission(ctx, "fleets/templateversions", "get")
	if err != nil {
		h.log.WithError(err).Error("failed to check authorization permission")
		return server.ReadTemplateVersion503JSONResponse{Message: AuthorizationServerUnavailable}, nil
	}
	if !allowed {
		return server.ReadTemplateVersion403JSONResponse{Message: Forbidden}, nil
	}
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
	allowed, err := auth.GetAuthZ().CheckPermission(ctx, "fleets/templateversions", "delete")
	if err != nil {
		h.log.WithError(err).Error("failed to check authorization permission")
		return server.DeleteTemplateVersion503JSONResponse{Message: AuthorizationServerUnavailable}, nil
	}
	if !allowed {
		return server.DeleteTemplateVersion403JSONResponse{Message: Forbidden}, nil
	}
	orgId := store.NullOrgId

	tvkey := tasks.TemplateVersionKey{OrgID: orgId, Fleet: request.Fleet, TemplateVersion: request.Name}
	err = h.configStorage.DeleteKeysForTemplateVersion(ctx, tvkey.ComposeKey())
	if err != nil {
		h.log.Warnf("failed deleting config storage for templateVersion %s/%s/%s", orgId, request.Fleet, request.Name)
	}

	err = h.store.TemplateVersion().Delete(ctx, orgId, request.Fleet, request.Name)
	switch err {
	case nil:
		return server.DeleteTemplateVersion200JSONResponse{}, nil
	case flterrors.ErrResourceNotFound:
		return server.DeleteTemplateVersion404JSONResponse{}, nil
	default:
		return nil, err
	}
}
