package service

import (
	"context"
	"errors"
	"fmt"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/kvstore"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/selector"
	"github.com/google/uuid"
)

func (h *ServiceHandler) CreateTemplateVersion(ctx context.Context, tv api.TemplateVersion, immediateRollout bool) (*api.TemplateVersion, api.Status) {
	orgId := store.NullOrgId

	if errs := tv.Validate(); len(errs) > 0 {
		return nil, api.StatusBadRequest(errors.Join(errs...).Error())
	}

	var callback store.TemplateVersionStoreCallback = func(u uuid.UUID, before *api.TemplateVersion, after *api.TemplateVersion) {
		h.log.Infof("fleet %s: template version %s created with rollout device selection, not executing task for immediate rollout", tv.Spec.Fleet, *tv.Metadata.Name)
	}
	if immediateRollout {
		callback = h.callbackManager.TemplateVersionCreatedCallback
	}

	result, err := h.store.TemplateVersion().Create(ctx, orgId, &tv, callback)
	return result, StoreErrorToApiStatus(err, true, api.TemplateVersionKind, tv.Metadata.Name)
}

func (h *ServiceHandler) ListTemplateVersions(ctx context.Context, fleet string, params api.ListTemplateVersionsParams) (*api.TemplateVersionList, api.Status) {
	var err error

	orgId := store.NullOrgId

	listParams, status := prepareListParams(params.Continue, params.LabelSelector, params.FieldSelector, params.Limit)
	if status != api.StatusOK() {
		return nil, status
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

	listParams.FieldSelector = fieldSelector
	result, err := h.store.TemplateVersion().List(ctx, orgId, *listParams)
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
	return StoreErrorToApiStatus(err, false, api.TemplateVersionKind, nil)
}

func (h *ServiceHandler) GetTemplateVersion(ctx context.Context, fleet string, name string) (*api.TemplateVersion, api.Status) {
	orgId := store.NullOrgId

	result, err := h.store.TemplateVersion().Get(ctx, orgId, fleet, name)
	return result, StoreErrorToApiStatus(err, false, api.TemplateVersionKind, &name)
}

func (h *ServiceHandler) DeleteTemplateVersion(ctx context.Context, fleet string, name string) api.Status {
	orgId := store.NullOrgId

	tvkey := kvstore.TemplateVersionKey{OrgID: orgId, Fleet: fleet, TemplateVersion: name}
	err := h.kvStore.DeleteKeysForTemplateVersion(ctx, tvkey.ComposeKey())
	if err != nil {
		h.log.Warnf("failed deleting KV storage for templateVersion %s/%s/%s", orgId, fleet, name)
	}

	err = h.store.TemplateVersion().Delete(ctx, orgId, fleet, name)
	return StoreErrorToApiStatus(err, false, api.TemplateVersionKind, &name)
}

func (h *ServiceHandler) GetLatestTemplateVersion(ctx context.Context, fleet string) (*api.TemplateVersion, api.Status) {
	orgId := store.NullOrgId

	result, err := h.store.TemplateVersion().GetLatest(ctx, orgId, fleet)
	return result, StoreErrorToApiStatus(err, false, api.TemplateVersionKind, nil)
}
