package fleet

import (
	"context"
	"errors"
	"reflect"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/service/common"
	"github.com/flightctl/flightctl/internal/service/events"
	fleetstore "github.com/flightctl/flightctl/internal/store/fleet"
	"github.com/flightctl/flightctl/internal/store/selector"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
)

type ServiceHandler struct {
	store  fleetstore.Store
	events events.Service
	log    logrus.FieldLogger
}

// NewServiceHandler creates a new fleet ServiceHandler instance.
func NewServiceHandler(store fleetstore.Store, events events.Service, log logrus.FieldLogger) *ServiceHandler {
	return &ServiceHandler{store: store, events: events, log: log}
}

var _ Service = (*ServiceHandler)(nil)

func (h *ServiceHandler) CreateFleet(ctx context.Context, orgId uuid.UUID, fleet domain.Fleet) (*domain.Fleet, domain.Status) {
	// don't set fields that are managed by the service
	fleet.Status = nil
	common.NilOutManagedObjectMetaProperties(&fleet.Metadata)
	if fleet.Spec.Template.Metadata != nil {
		common.NilOutManagedObjectMetaProperties(fleet.Spec.Template.Metadata)
	}

	if errs := fleet.Validate(); len(errs) > 0 {
		return nil, domain.StatusBadRequest(errors.Join(errs...).Error())
	}

	result, err := h.store.Create(ctx, orgId, &fleet, h.callbackFleetUpdated)
	return result, common.StoreErrorToApiStatus(err, true, domain.FleetKind, fleet.Metadata.Name)
}

func (h *ServiceHandler) ListFleets(ctx context.Context, orgId uuid.UUID, params domain.ListFleetsParams) (*domain.FleetList, domain.Status) {
	listParams, status := common.PrepareListParams(params.Continue, params.LabelSelector, params.FieldSelector, params.Limit)
	if status != domain.StatusOK() {
		return nil, status
	}

	result, err := h.store.List(ctx, orgId, *listParams, fleetstore.ListWithDevicesSummary(util.DefaultBoolIfNil(params.AddDevicesSummary, false)))
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
	result, err := h.store.Get(ctx, orgId, name, fleetstore.GetWithDeviceSummary(util.DefaultBoolIfNil(params.AddDevicesSummary, false)))
	return result, common.StoreErrorToApiStatus(err, false, domain.FleetKind, &name)
}

func (h *ServiceHandler) ReplaceFleet(ctx context.Context, orgId uuid.UUID, name string, fleet domain.Fleet) (*domain.Fleet, domain.Status) {
	// don't overwrite fields that are managed by the service
	isInternal := common.IsInternalRequest(ctx)
	if !isInternal {
		fleet.Status = nil
		common.NilOutManagedObjectMetaProperties(&fleet.Metadata)
		if fleet.Spec.Template.Metadata != nil {
			common.NilOutManagedObjectMetaProperties(fleet.Spec.Template.Metadata)
		}
	}

	if errs := fleet.Validate(); len(errs) > 0 {
		return nil, domain.StatusBadRequest(errors.Join(errs...).Error())
	}
	if name != *fleet.Metadata.Name {
		return nil, domain.StatusBadRequest("resource name specified in metadata does not match name in path")
	}

	// External updates of owned fleets cannot change spec or labels (ResourceSync may).
	// Internal requests skip this check (store fromAPI=false path).
	if !isInternal {
		existing, getErr := h.store.Get(ctx, orgId, name)
		if getErr != nil {
			if !errors.Is(getErr, flterrors.ErrResourceNotFound) {
				return nil, common.StoreErrorToApiStatus(getErr, false, domain.FleetKind, &name)
			}
		} else if len(lo.FromPtr(existing.Metadata.Owner)) != 0 && !common.IsResourceSyncRequest(ctx) {
			if !domain.FleetSpecsAreEqual(existing.Spec, fleet.Spec) {
				return nil, common.StoreErrorToApiStatus(flterrors.ErrUpdatingResourceWithOwnerNotAllowed, false, domain.FleetKind, &name)
			}
			if !reflect.DeepEqual(existing.Metadata.Labels, fleet.Metadata.Labels) {
				return nil, common.StoreErrorToApiStatus(flterrors.ErrUpdatingResourceWithOwnerNotAllowed, false, domain.FleetKind, &name)
			}
		}
	}

	result, created, err := h.store.CreateOrUpdate(ctx, orgId, &fleet, nil, !isInternal, h.callbackFleetUpdated)
	return result, common.StoreErrorToApiStatus(err, created, domain.FleetKind, &name)
}

func (h *ServiceHandler) DeleteFleet(ctx context.Context, orgId uuid.UUID, name string) domain.Status {
	f, err := h.store.Get(ctx, orgId, name)
	if err != nil {
		if errors.Is(err, flterrors.ErrResourceNotFound) {
			return domain.StatusOK() // idempotent delete
		}
		return common.StoreErrorToApiStatus(err, false, domain.FleetKind, &name)
	}

	if f.Metadata.Owner != nil && !common.IsResourceSyncRequest(ctx) {
		return domain.StatusConflict(flterrors.ErrDeletingResourceWithOwnerNotAllowed.Error())
	}

	err = h.store.Delete(ctx, orgId, name, h.callbackFleetDeleted)
	return common.StoreErrorToApiStatus(err, false, domain.FleetKind, &name)
}

func (h *ServiceHandler) GetFleetStatus(ctx context.Context, orgId uuid.UUID, name string) (*domain.Fleet, domain.Status) {
	result, err := h.store.Get(ctx, orgId, name)
	return result, common.StoreErrorToApiStatus(err, false, domain.FleetKind, &name)
}

func (h *ServiceHandler) ReplaceFleetStatus(ctx context.Context, orgId uuid.UUID, name string, fleet domain.Fleet) (*domain.Fleet, domain.Status) {
	result, err := h.store.UpdateStatus(ctx, orgId, &fleet)
	return result, common.StoreErrorToApiStatus(err, false, domain.FleetKind, &name)
}

// Only metadata.labels and spec can be patched. If we try to patch other fields, HTTP 400 Bad Request is returned.
func (h *ServiceHandler) PatchFleet(ctx context.Context, orgId uuid.UUID, name string, patch domain.PatchRequest) (*domain.Fleet, domain.Status) {
	currentObj, err := h.store.Get(ctx, orgId, name)
	if err != nil {
		return nil, common.StoreErrorToApiStatus(err, false, domain.FleetKind, &name)
	}

	newObj := &domain.Fleet{}
	err = common.ApplyJSONPatch(ctx, currentObj, newObj, patch, "/fleets/"+name)
	if err != nil {
		return nil, domain.StatusBadRequest(err.Error())
	}

	if errs := newObj.Validate(); len(errs) > 0 {
		return nil, domain.StatusBadRequest(errors.Join(errs...).Error())
	}

	if errs := currentObj.ValidateUpdate(newObj); len(errs) > 0 {
		return nil, domain.StatusBadRequest(errors.Join(errs...).Error())
	}

	common.NilOutManagedObjectMetaProperties(&newObj.Metadata)
	newObj.Metadata.ResourceVersion = nil

	// Owned fleets cannot change spec or labels via patch (ResourceSync may).
	if len(lo.FromPtr(currentObj.Metadata.Owner)) != 0 && !common.IsResourceSyncRequest(ctx) {
		if !domain.FleetSpecsAreEqual(currentObj.Spec, newObj.Spec) {
			return nil, common.StoreErrorToApiStatus(flterrors.ErrUpdatingResourceWithOwnerNotAllowed, false, domain.FleetKind, &name)
		}
		if !reflect.DeepEqual(currentObj.Metadata.Labels, newObj.Metadata.Labels) {
			return nil, common.StoreErrorToApiStatus(flterrors.ErrUpdatingResourceWithOwnerNotAllowed, false, domain.FleetKind, &name)
		}
	}

	result, err := h.store.Update(ctx, orgId, newObj, nil, true, h.callbackFleetUpdated)
	return result, common.StoreErrorToApiStatus(err, false, domain.FleetKind, &name)
}

func (h *ServiceHandler) ListFleetRolloutDeviceSelection(ctx context.Context, orgId uuid.UUID) (*domain.FleetList, domain.Status) {
	result, err := h.store.ListRolloutDeviceSelection(ctx, orgId)
	return result, common.StoreErrorToApiStatus(err, false, domain.FleetKind, nil)
}

func (h *ServiceHandler) ListDisruptionBudgetFleets(ctx context.Context, orgId uuid.UUID) (*domain.FleetList, domain.Status) {
	result, err := h.store.ListDisruptionBudgetFleets(ctx, orgId)
	return result, common.StoreErrorToApiStatus(err, false, domain.FleetKind, nil)
}

func (h *ServiceHandler) UpdateFleetConditions(ctx context.Context, orgId uuid.UUID, name string, conditions []domain.Condition) domain.Status {
	err := h.store.UpdateConditions(ctx, orgId, name, conditions, h.callbackFleetUpdated)
	return common.StoreErrorToApiStatus(err, false, domain.FleetKind, &name)
}

func (h *ServiceHandler) UpdateFleetAnnotations(ctx context.Context, orgId uuid.UUID, name string, annotations map[string]string, deleteKeys []string) domain.Status {
	err := h.store.UpdateAnnotations(ctx, orgId, name, annotations, deleteKeys, h.callbackFleetUpdated)
	return common.StoreErrorToApiStatus(err, false, domain.FleetKind, &name)
}

func (h *ServiceHandler) OverwriteFleetRepositoryRefs(ctx context.Context, orgId uuid.UUID, name string, repositoryNames ...string) domain.Status {
	err := h.store.OverwriteRepositoryRefs(ctx, orgId, name, repositoryNames...)
	return common.StoreErrorToApiStatus(err, false, domain.FleetKind, &name)
}

func (h *ServiceHandler) GetFleetRepositoryRefs(ctx context.Context, orgId uuid.UUID, name string) (*domain.RepositoryList, domain.Status) {
	result, err := h.store.GetRepositoryRefs(ctx, orgId, name)
	return result, common.StoreErrorToApiStatus(err, false, domain.FleetKind, &name)
}

// callbackFleetUpdated is the fleet-specific callback that handles fleet events
func (h *ServiceHandler) callbackFleetUpdated(ctx context.Context, resourceKind domain.ResourceKind, orgId uuid.UUID, name string, oldResource, newResource interface{}, created bool, err error) {
	EmitFleetUpdatedEvent(ctx, h.events, h.log, resourceKind, orgId, name, oldResource, newResource, created, err)
}

// callbackFleetDeleted is the fleet-specific callback that handles fleet deletion events
func (h *ServiceHandler) callbackFleetDeleted(ctx context.Context, resourceKind domain.ResourceKind, orgId uuid.UUID, name string, oldResource, newResource interface{}, created bool, err error) {
	h.events.HandleGenericResourceDeletedEvents(ctx, resourceKind, orgId, name, oldResource, newResource, created, err)
}
