package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/api/server"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/kvstore"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/selector"
	"github.com/go-openapi/swag"
)

func TemplateVersionFromReader(r io.Reader) (*api.TemplateVersion, error) {
	var templateVersion api.TemplateVersion
	decoder := json.NewDecoder(r)
	decoder.DisallowUnknownFields()
	err := decoder.Decode(&templateVersion)
	return &templateVersion, err
}

// (GET api/v1/fleets/{fleet}/templateVersions)
func (h *ServiceHandler) ListTemplateVersions(ctx context.Context, request server.ListTemplateVersionsRequestObject) (server.ListTemplateVersionsResponseObject, error) {
	orgId := store.NullOrgId

	cont, err := store.ParseContinueString(request.Params.Continue)
	if err != nil {
		return server.ListTemplateVersions400JSONResponse{Message: fmt.Sprintf("failed to parse continue parameter: %v", err)}, nil
	}

	var fieldSelector *selector.FieldSelector
	if fieldSelector, err = selector.NewFieldSelectorFromMap(map[string]string{"metadata.owner": request.Fleet}); err != nil {
		return server.ListTemplateVersions400JSONResponse{Message: fmt.Sprintf("failed to parse field selector: %v", err)}, nil
	}

	// If additional field selectors are provided, merge them
	if request.Params.FieldSelector != nil {
		additionalSelector, err := selector.NewFieldSelector(*request.Params.FieldSelector)
		if err != nil {
			return server.ListTemplateVersions400JSONResponse{Message: fmt.Sprintf("failed to parse additional field selector: %v", err)}, nil
		}
		fieldSelector.Add(additionalSelector)
	}

	var labelSelector *selector.LabelSelector
	if request.Params.LabelSelector != nil {
		if labelSelector, err = selector.NewLabelSelector(*request.Params.LabelSelector); err != nil {
			return server.ListTemplateVersions400JSONResponse{Message: fmt.Sprintf("failed to parse label selector: %v", err)}, nil
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

// (DELETE api/v1/fleets/{fleet}/templateVersions)
func (h *ServiceHandler) DeleteTemplateVersions(ctx context.Context, request server.DeleteTemplateVersionsRequestObject) (server.DeleteTemplateVersionsResponseObject, error) {
	orgId := store.NullOrgId

	var (
		fieldSelector *selector.FieldSelector
		err           error
	)
	if fieldSelector, err = selector.NewFieldSelectorFromMap(map[string]string{"metadata.owner": request.Fleet}); err != nil {
		return server.DeleteTemplateVersions403JSONResponse{Message: Forbidden}, nil
	}

	// Iterate through the relevant templateVersions, 100 at a time, and delete each one's config storage
	listParams := store.ListParams{
		Limit:         100,
		FieldSelector: fieldSelector,
	}
	for {
		result, err := h.store.TemplateVersion().List(ctx, orgId, listParams)
		if err != nil {
			h.log.Warnf("failed deleting KV storage for templateVersions in org %s", orgId)
			break
		}
		for _, tv := range result.Items {
			tvkey := kvstore.TemplateVersionKey{OrgID: orgId, Fleet: tv.Spec.Fleet, TemplateVersion: *tv.Metadata.Name}
			err := h.kvStore.DeleteKeysForTemplateVersion(ctx, tvkey.ComposeKey())
			if err != nil {
				h.log.Warnf("failed deleting KV storage for templateVersion %s/%s/%s", orgId, tv.Spec.Fleet, *tv.Metadata.Name)
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
	orgId := store.NullOrgId

	result, err := h.store.TemplateVersion().Get(ctx, orgId, request.Fleet, request.Name)
	switch {
	case err == nil:
		return server.ReadTemplateVersion200JSONResponse(*result), nil
	case errors.Is(err, flterrors.ErrResourceNotFound):
		return server.ReadTemplateVersion404JSONResponse{}, nil
	default:
		return nil, err
	}
}

// (DELETE /api/v1/fleets/{fleet}/templateVersions/{name})
func (h *ServiceHandler) DeleteTemplateVersion(ctx context.Context, request server.DeleteTemplateVersionRequestObject) (server.DeleteTemplateVersionResponseObject, error) {
	orgId := store.NullOrgId

	tvkey := kvstore.TemplateVersionKey{OrgID: orgId, Fleet: request.Fleet, TemplateVersion: request.Name}
	err := h.kvStore.DeleteKeysForTemplateVersion(ctx, tvkey.ComposeKey())
	if err != nil {
		h.log.Warnf("failed deleting KV storage for templateVersion %s/%s/%s", orgId, request.Fleet, request.Name)
	}

	err = h.store.TemplateVersion().Delete(ctx, orgId, request.Fleet, request.Name)
	switch {
	case err == nil:
		return server.DeleteTemplateVersion200JSONResponse{}, nil
	case errors.Is(err, flterrors.ErrResourceNotFound):
		return server.DeleteTemplateVersion404JSONResponse{}, nil
	default:
		return nil, err
	}
}
