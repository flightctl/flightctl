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
	"github.com/samber/lo"
)

func (h *ServiceHandler) CreateTemplateVersion(ctx context.Context, tv api.TemplateVersion, immediateRollout bool) (*api.TemplateVersion, api.Status) {
	orgId := store.NullOrgId

	if errs := tv.Validate(); len(errs) > 0 {
		return nil, api.StatusBadRequest(errors.Join(errs...).Error())
	}

	var callback store.TemplateVersionStoreCallback = func(ctx context.Context, u uuid.UUID, before *api.TemplateVersion, after *api.TemplateVersion) {
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

	// sort primarily by created_at with desc (newest first)
	listParams.SortColumns = []store.SortColumn{store.SortByCreatedAt, store.SortByName}
	listParams.SortOrder = lo.ToPtr(store.SortDesc)

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

	_, err = h.store.TemplateVersion().Delete(ctx, orgId, fleet, name)
	return StoreErrorToApiStatus(err, false, api.TemplateVersionKind, &name)
}

func (h *ServiceHandler) GetLatestTemplateVersion(ctx context.Context, fleet string) (*api.TemplateVersion, api.Status) {
	orgId := store.NullOrgId

	result, err := h.store.TemplateVersion().GetLatest(ctx, orgId, fleet)
	return result, StoreErrorToApiStatus(err, false, api.TemplateVersionKind, nil)
}
