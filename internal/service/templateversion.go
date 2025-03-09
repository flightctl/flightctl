package service

import (
	"context"
	"errors"
	"fmt"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/kvstore"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/selector"
	"github.com/go-openapi/swag"
)

func (h *ServiceHandler) ListTemplateVersions(ctx context.Context, fleet string, params api.ListTemplateVersionsParams) (*api.TemplateVersionList, api.Status) {
	orgId := store.NullOrgId

	cont, err := store.ParseContinueString(params.Continue)
	if err != nil {
		return nil, api.StatusBadRequest(fmt.Sprintf("failed to parse continue parameter: %v", err))
	}

	var fieldSelector *selector.FieldSelector
	if fieldSelector, err = selector.NewFieldSelectorFromMap(map[string]string{"metadata.owner": fleet}); err != nil {
		return nil, api.StatusBadRequest(fmt.Sprintf("failed to parse field selector: %v", err))
	}

	// If additional field selectors are provided, merge them
	if params.FieldSelector != nil {
		additionalSelector, err := selector.NewFieldSelector(*params.FieldSelector)
		if err != nil {
			return nil, api.StatusBadRequest(fmt.Sprintf("failed to parse additional field selector: %v", err))
		}
		fieldSelector.Add(additionalSelector)
	}

	var labelSelector *selector.LabelSelector
	if params.LabelSelector != nil {
		if labelSelector, err = selector.NewLabelSelector(*params.LabelSelector); err != nil {
			return nil, api.StatusBadRequest(fmt.Sprintf("failed to parse label selector: %v", err))
		}
	}
	listParams := store.ListParams{
		Limit:         int(swag.Int32Value(params.Limit)),
		Continue:      cont,
		FieldSelector: fieldSelector,
		LabelSelector: labelSelector,
	}
	if listParams.Limit == 0 {
		listParams.Limit = store.MaxRecordsPerListRequest
	} else if listParams.Limit > store.MaxRecordsPerListRequest {
		return nil, api.StatusBadRequest(fmt.Sprintf("limit cannot exceed %d", store.MaxRecordsPerListRequest))
	} else if listParams.Limit < 0 {
		return nil, api.StatusBadRequest("limit cannot be negative")
	}

	result, err := h.store.TemplateVersion().List(ctx, orgId, listParams)
	if err == nil {
		return result, api.StatusOK()
	}

	var se *selector.SelectorError

	switch {
	case selector.AsSelectorError(err, &se):
		return nil, api.StatusBadRequest(se.Error())
	default:
		return nil, api.StatusInternalServerError(err.Error())
	}
}

func (h *ServiceHandler) DeleteTemplateVersions(ctx context.Context, fleet string) api.Status {
	orgId := store.NullOrgId

	var (
		fieldSelector *selector.FieldSelector
		err           error
	)
	if fieldSelector, err = selector.NewFieldSelectorFromMap(map[string]string{"metadata.owner": fleet}); err != nil {
		return api.StatusInternalServerError(fmt.Sprintf("failed creating field selector: %v", err))
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

	err = h.store.TemplateVersion().DeleteAll(ctx, orgId, &fleet)
	switch err {
	case nil:
		return api.StatusOK()
	default:
		return api.StatusInternalServerError(err.Error())
	}
}

func (h *ServiceHandler) GetTemplateVersion(ctx context.Context, fleet string, name string) (*api.TemplateVersion, api.Status) {
	orgId := store.NullOrgId

	result, err := h.store.TemplateVersion().Get(ctx, orgId, fleet, name)
	switch {
	case err == nil:
		return result, api.StatusOK()
	case errors.Is(err, flterrors.ErrResourceNotFound):
		return nil, api.StatusResourceNotFound(api.TemplateVersionKind, name)
	default:
		return nil, api.StatusInternalServerError(err.Error())
	}
}

func (h *ServiceHandler) DeleteTemplateVersion(ctx context.Context, fleet string, name string) (*api.TemplateVersion, api.Status) {
	orgId := store.NullOrgId

	tvkey := kvstore.TemplateVersionKey{OrgID: orgId, Fleet: fleet, TemplateVersion: name}
	err := h.kvStore.DeleteKeysForTemplateVersion(ctx, tvkey.ComposeKey())
	if err != nil {
		h.log.Warnf("failed deleting KV storage for templateVersion %s/%s/%s", orgId, fleet, name)
	}

	err = h.store.TemplateVersion().Delete(ctx, orgId, fleet, name)
	switch {
	case err == nil:
		return &api.TemplateVersion{}, api.StatusOK()
	case errors.Is(err, flterrors.ErrResourceNotFound):
		return nil, api.StatusResourceNotFound("TemplateVersion", name)
	default:
		return nil, api.StatusInternalServerError(err.Error())
	}
}
