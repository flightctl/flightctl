package service

import (
	"context"
	"errors"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/selector"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/google/uuid"
)

func (h *ServiceHandler) CreateFleet(ctx context.Context, fleet api.Fleet) (*api.Fleet, api.Status) {
	orgId := getOrgIdFromContext(ctx)

	// don't set fields that are managed by the service
	fleet.Status = nil
	NilOutManagedObjectMetaProperties(&fleet.Metadata)
	if fleet.Spec.Template.Metadata != nil {
		NilOutManagedObjectMetaProperties(fleet.Spec.Template.Metadata)
	}

	if errs := fleet.Validate(); len(errs) > 0 {
		return nil, api.StatusBadRequest(errors.Join(errs...).Error())
	}

	result, err := h.store.Fleet().Create(ctx, orgId, &fleet, h.callbackManager.FleetUpdatedCallback)
	status := StoreErrorToApiStatus(err, true, api.FleetKind, fleet.Metadata.Name)
	h.CreateEvent(ctx, GetResourceCreatedOrUpdatedEvent(ctx, true, api.FleetKind, *fleet.Metadata.Name, status, nil, h.log))
	return result, status
}

func (h *ServiceHandler) ListFleets(ctx context.Context, params api.ListFleetsParams) (*api.FleetList, api.Status) {
	orgId := getOrgIdFromContext(ctx)

	listParams, status := prepareListParams(params.Continue, params.LabelSelector, params.FieldSelector, params.Limit)
	if status != api.StatusOK() {
		return nil, status
	}

	result, err := h.store.Fleet().List(ctx, orgId, *listParams, store.ListWithDevicesSummary(util.DefaultBoolIfNil(params.AddDevicesSummary, false)))
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

func (h *ServiceHandler) GetFleet(ctx context.Context, name string, params api.GetFleetParams) (*api.Fleet, api.Status) {
	orgId := getOrgIdFromContext(ctx)

	result, err := h.store.Fleet().Get(ctx, orgId, name, store.GetWithDeviceSummary(util.DefaultBoolIfNil(params.AddDevicesSummary, false)))
	return result, StoreErrorToApiStatus(err, false, api.FleetKind, &name)
}

func (h *ServiceHandler) ReplaceFleet(ctx context.Context, name string, fleet api.Fleet) (*api.Fleet, api.Status) {
	orgId := getOrgIdFromContext(ctx)

	// don't overwrite fields that are managed by the service
	isInternal := IsInternalRequest(ctx)
	if !isInternal {
		fleet.Status = nil
		NilOutManagedObjectMetaProperties(&fleet.Metadata)
		if fleet.Spec.Template.Metadata != nil {
			NilOutManagedObjectMetaProperties(fleet.Spec.Template.Metadata)
		}
	}

	if errs := fleet.Validate(); len(errs) > 0 {
		return nil, api.StatusBadRequest(errors.Join(errs...).Error())
	}
	if name != *fleet.Metadata.Name {
		return nil, api.StatusBadRequest("resource name specified in metadata does not match name in path")
	}

	result, created, updateDesc, err := h.store.Fleet().CreateOrUpdate(ctx, orgId, &fleet, nil, !isInternal, h.callbackManager.FleetUpdatedCallback)
	status := StoreErrorToApiStatus(err, created, api.FleetKind, &name)
	h.CreateEvent(ctx, GetResourceCreatedOrUpdatedEvent(ctx, created, api.FleetKind, name, status, &updateDesc, h.log))
	return result, status
}

func (h *ServiceHandler) DeleteFleet(ctx context.Context, name string) api.Status {
	orgId := getOrgIdFromContext(ctx)

	f, err := h.store.Fleet().Get(ctx, orgId, name)
	if err != nil {
		if errors.Is(err, flterrors.ErrResourceNotFound) {
			return api.StatusOK() // idempotent delete
		}
		return StoreErrorToApiStatus(err, false, api.FleetKind, &name)
	}
	if f.Metadata.Owner != nil {
		// Can't delete via api
		return api.StatusConflict("unauthorized to delete fleet because it is owned by another resource")
	}

	deleted, err := h.store.Fleet().Delete(ctx, orgId, name, h.callbackManager.FleetUpdatedCallback)
	status := StoreErrorToApiStatus(err, false, api.FleetKind, &name)
	if deleted || err != nil {
		h.CreateEvent(ctx, GetResourceDeletedEvent(ctx, api.FleetKind, name, status, h.log))
	}
	return status
}

func (h *ServiceHandler) GetFleetStatus(ctx context.Context, name string) (*api.Fleet, api.Status) {
	orgId := getOrgIdFromContext(ctx)

	result, err := h.store.Fleet().Get(ctx, orgId, name)
	return result, StoreErrorToApiStatus(err, false, api.FleetKind, &name)
}

func (h *ServiceHandler) ReplaceFleetStatus(ctx context.Context, name string, fleet api.Fleet) (*api.Fleet, api.Status) {
	orgId := getOrgIdFromContext(ctx)

	result, err := h.store.Fleet().UpdateStatus(ctx, orgId, &fleet)
	return result, StoreErrorToApiStatus(err, false, api.FleetKind, &name)
}

// Only metadata.labels and spec can be patched. If we try to patch other fields, HTTP 400 Bad Request is returned.
func (h *ServiceHandler) PatchFleet(ctx context.Context, name string, patch api.PatchRequest) (*api.Fleet, api.Status) {
	orgId := getOrgIdFromContext(ctx)

	currentObj, err := h.store.Fleet().Get(ctx, orgId, name)
	if err != nil {
		return nil, StoreErrorToApiStatus(err, false, api.FleetKind, &name)
	}

	newObj := &api.Fleet{}
	err = ApplyJSONPatch(ctx, currentObj, newObj, patch, "/api/v1/fleets/"+name)
	if err != nil {
		return nil, api.StatusBadRequest(err.Error())
	}

	if errs := newObj.Validate(); len(errs) > 0 {
		return nil, api.StatusBadRequest(errors.Join(errs...).Error())
	}

	if errs := currentObj.ValidateUpdate(newObj); len(errs) > 0 {
		return nil, api.StatusBadRequest(errors.Join(errs...).Error())
	}

	NilOutManagedObjectMetaProperties(&newObj.Metadata)
	newObj.Metadata.ResourceVersion = nil

	var updateCallback func(context.Context, uuid.UUID, *api.Fleet, *api.Fleet)

	if h.callbackManager != nil {
		updateCallback = h.callbackManager.FleetUpdatedCallback
	}
	result, updateDesc, err := h.store.Fleet().Update(ctx, orgId, newObj, nil, true, updateCallback)
	status := StoreErrorToApiStatus(err, false, api.FleetKind, &name)
	h.CreateEvent(ctx, GetResourceCreatedOrUpdatedEvent(ctx, false, api.FleetKind, name, status, &updateDesc, h.log))
	return result, status
}

func (h *ServiceHandler) ListFleetRolloutDeviceSelection(ctx context.Context) (*api.FleetList, api.Status) {
	orgId := getOrgIdFromContext(ctx)

	result, err := h.store.Fleet().ListRolloutDeviceSelection(ctx, orgId)
	return result, StoreErrorToApiStatus(err, false, api.FleetKind, nil)
}

func (h *ServiceHandler) ListDisruptionBudgetFleets(ctx context.Context) (*api.FleetList, api.Status) {
	orgId := getOrgIdFromContext(ctx)

	result, err := h.store.Fleet().ListDisruptionBudgetFleets(ctx, orgId)
	return result, StoreErrorToApiStatus(err, false, api.FleetKind, nil)
}

func (h *ServiceHandler) UpdateFleetConditions(ctx context.Context, name string, conditions []api.Condition) api.Status {
	orgId := getOrgIdFromContext(ctx)

	err := h.store.Fleet().UpdateConditions(ctx, orgId, name, conditions)
	return StoreErrorToApiStatus(err, false, api.FleetKind, &name)
}

func (h *ServiceHandler) UpdateFleetAnnotations(ctx context.Context, name string, annotations map[string]string, deleteKeys []string) api.Status {
	orgId := getOrgIdFromContext(ctx)

	err := h.store.Fleet().UpdateAnnotations(ctx, orgId, name, annotations, deleteKeys)
	return StoreErrorToApiStatus(err, false, api.FleetKind, &name)
}

func (h *ServiceHandler) OverwriteFleetRepositoryRefs(ctx context.Context, name string, repositoryNames ...string) api.Status {
	orgId := getOrgIdFromContext(ctx)

	err := h.store.Fleet().OverwriteRepositoryRefs(ctx, orgId, name, repositoryNames...)
	return StoreErrorToApiStatus(err, false, api.FleetKind, &name)
}

func (h *ServiceHandler) GetFleetRepositoryRefs(ctx context.Context, name string) (*api.RepositoryList, api.Status) {
	orgId := getOrgIdFromContext(ctx)

	result, err := h.store.Fleet().GetRepositoryRefs(ctx, orgId, name)
	return result, StoreErrorToApiStatus(err, false, api.FleetKind, &name)
}
