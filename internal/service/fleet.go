package service

import (
	"context"
	"errors"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/selector"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/google/uuid"
)

func (h *ServiceHandler) CreateFleet(ctx context.Context, orgId uuid.UUID, fleet domain.Fleet) (*domain.Fleet, domain.Status) {
	// don't set fields that are managed by the service
	fleet.Status = nil
	NilOutManagedObjectMetaProperties(&fleet.Metadata)
	if fleet.Spec.Template.Metadata != nil {
		NilOutManagedObjectMetaProperties(fleet.Spec.Template.Metadata)
	}

	if errs := fleet.Validate(); len(errs) > 0 {
		return nil, domain.StatusBadRequest(errors.Join(errs...).Error())
	}

	result, err := h.store.Fleet().Create(ctx, orgId, &fleet, h.callbackFleetUpdated)
	return result, StoreErrorToApiStatus(err, true, domain.FleetKind, fleet.Metadata.Name)
}

func (h *ServiceHandler) ListFleets(ctx context.Context, orgId uuid.UUID, params domain.ListFleetsParams) (*domain.FleetList, domain.Status) {
	listParams, status := prepareListParams(params.Continue, params.LabelSelector, params.FieldSelector, params.Limit)
	if status != domain.StatusOK() {
		return nil, status
	}

	result, err := h.store.Fleet().List(ctx, orgId, *listParams, store.ListWithDevicesSummary(util.DefaultBoolIfNil(params.AddDevicesSummary, false)))
	if err == nil {
		return result, domain.StatusOK()
	}

	var se *selector.SelectorError

	switch {
	case selector.AsSelectorError(err, &se):
		return nil, domain.StatusBadRequest(se.Error())
	default:
		return nil, domain.StatusInternalServerError(err.Error())
	}
}

func (h *ServiceHandler) GetFleet(ctx context.Context, orgId uuid.UUID, name string, params domain.GetFleetParams) (*domain.Fleet, domain.Status) {
	result, err := h.store.Fleet().Get(ctx, orgId, name, store.GetWithDeviceSummary(util.DefaultBoolIfNil(params.AddDevicesSummary, false)))
	return result, StoreErrorToApiStatus(err, false, domain.FleetKind, &name)
}

func (h *ServiceHandler) ReplaceFleet(ctx context.Context, orgId uuid.UUID, name string, fleet domain.Fleet) (*domain.Fleet, domain.Status) {
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
		return nil, domain.StatusBadRequest(errors.Join(errs...).Error())
	}
	if name != *fleet.Metadata.Name {
		return nil, domain.StatusBadRequest("resource name specified in metadata does not match name in path")
	}

	result, created, err := h.store.Fleet().CreateOrUpdate(ctx, orgId, &fleet, nil, !isInternal, h.callbackFleetUpdated)
	return result, StoreErrorToApiStatus(err, created, domain.FleetKind, &name)
}

func (h *ServiceHandler) DeleteFleet(ctx context.Context, orgId uuid.UUID, name string) domain.Status {
	f, err := h.store.Fleet().Get(ctx, orgId, name)
	if err != nil {
		if errors.Is(err, flterrors.ErrResourceNotFound) {
			return domain.StatusOK() // idempotent delete
		}
		return StoreErrorToApiStatus(err, false, domain.FleetKind, &name)
	}
	if f.Metadata.Owner != nil {
		// Can't delete via api
		return domain.StatusConflict("unauthorized to delete fleet because it is owned by another resource")
	}

	err = h.store.Fleet().Delete(ctx, orgId, name, h.callbackFleetDeleted)
	return StoreErrorToApiStatus(err, false, domain.FleetKind, &name)
}

func (h *ServiceHandler) GetFleetStatus(ctx context.Context, orgId uuid.UUID, name string) (*domain.Fleet, domain.Status) {
	result, err := h.store.Fleet().Get(ctx, orgId, name)
	return result, StoreErrorToApiStatus(err, false, domain.FleetKind, &name)
}

func (h *ServiceHandler) ReplaceFleetStatus(ctx context.Context, orgId uuid.UUID, name string, fleet domain.Fleet) (*domain.Fleet, domain.Status) {
	result, err := h.store.Fleet().UpdateStatus(ctx, orgId, &fleet)
	return result, StoreErrorToApiStatus(err, false, domain.FleetKind, &name)
}

// Only metadata.labels and spec can be patched. If we try to patch other fields, HTTP 400 Bad Request is returned.
func (h *ServiceHandler) PatchFleet(ctx context.Context, orgId uuid.UUID, name string, patch domain.PatchRequest) (*domain.Fleet, domain.Status) {
	currentObj, err := h.store.Fleet().Get(ctx, orgId, name)
	if err != nil {
		return nil, StoreErrorToApiStatus(err, false, domain.FleetKind, &name)
	}

	newObj := &domain.Fleet{}
	err = ApplyJSONPatch(ctx, currentObj, newObj, patch, "/fleets/"+name)
	if err != nil {
		return nil, domain.StatusBadRequest(err.Error())
	}

	if errs := newObj.Validate(); len(errs) > 0 {
		return nil, domain.StatusBadRequest(errors.Join(errs...).Error())
	}

	if errs := currentObj.ValidateUpdate(newObj); len(errs) > 0 {
		return nil, domain.StatusBadRequest(errors.Join(errs...).Error())
	}

	NilOutManagedObjectMetaProperties(&newObj.Metadata)
	newObj.Metadata.ResourceVersion = nil

	result, err := h.store.Fleet().Update(ctx, orgId, newObj, nil, true, h.callbackFleetUpdated)
	return result, StoreErrorToApiStatus(err, false, domain.FleetKind, &name)
}

func (h *ServiceHandler) ListFleetRolloutDeviceSelection(ctx context.Context, orgId uuid.UUID) (*domain.FleetList, domain.Status) {
	result, err := h.store.Fleet().ListRolloutDeviceSelection(ctx, orgId)
	return result, StoreErrorToApiStatus(err, false, domain.FleetKind, nil)
}

func (h *ServiceHandler) ListDisruptionBudgetFleets(ctx context.Context, orgId uuid.UUID) (*domain.FleetList, domain.Status) {
	result, err := h.store.Fleet().ListDisruptionBudgetFleets(ctx, orgId)
	return result, StoreErrorToApiStatus(err, false, domain.FleetKind, nil)
}

func (h *ServiceHandler) UpdateFleetConditions(ctx context.Context, orgId uuid.UUID, name string, conditions []domain.Condition) domain.Status {
	err := h.store.Fleet().UpdateConditions(ctx, orgId, name, conditions, h.callbackFleetUpdated)
	return StoreErrorToApiStatus(err, false, domain.FleetKind, &name)
}

func (h *ServiceHandler) UpdateFleetAnnotations(ctx context.Context, orgId uuid.UUID, name string, annotations map[string]string, deleteKeys []string) domain.Status {
	err := h.store.Fleet().UpdateAnnotations(ctx, orgId, name, annotations, deleteKeys, h.callbackFleetUpdated)
	return StoreErrorToApiStatus(err, false, domain.FleetKind, &name)
}

func (h *ServiceHandler) OverwriteFleetRepositoryRefs(ctx context.Context, orgId uuid.UUID, name string, repositoryNames ...string) domain.Status {
	err := h.store.Fleet().OverwriteRepositoryRefs(ctx, orgId, name, repositoryNames...)
	return StoreErrorToApiStatus(err, false, domain.FleetKind, &name)
}

func (h *ServiceHandler) GetFleetRepositoryRefs(ctx context.Context, orgId uuid.UUID, name string) (*domain.RepositoryList, domain.Status) {
	result, err := h.store.Fleet().GetRepositoryRefs(ctx, orgId, name)
	return result, StoreErrorToApiStatus(err, false, domain.FleetKind, &name)
}

// callbackFleetUpdated is the fleet-specific callback that handles fleet events
func (h *ServiceHandler) callbackFleetUpdated(ctx context.Context, resourceKind domain.ResourceKind, orgId uuid.UUID, name string, oldResource, newResource interface{}, created bool, err error) {
	h.eventHandler.HandleFleetUpdatedEvents(ctx, resourceKind, orgId, name, oldResource, newResource, created, err)
}

// callbackFleetDeleted is the fleet-specific callback that handles fleet deletion events
func (h *ServiceHandler) callbackFleetDeleted(ctx context.Context, resourceKind domain.ResourceKind, orgId uuid.UUID, name string, oldResource, newResource interface{}, created bool, err error) {
	h.eventHandler.HandleGenericResourceDeletedEvents(ctx, resourceKind, orgId, name, oldResource, newResource, created, err)
}
