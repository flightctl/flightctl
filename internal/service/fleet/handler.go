package fleet

import (
	"context"
	"errors"
	"net/http"
	"reflect"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/service/common"
	"github.com/flightctl/flightctl/internal/service/events"
	"github.com/flightctl/flightctl/internal/store"
	catalogstore "github.com/flightctl/flightctl/internal/store/catalog"
	fleetstore "github.com/flightctl/flightctl/internal/store/fleet"
	"github.com/flightctl/flightctl/internal/store/selector"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
)

type ServiceHandler struct {
	store             fleetstore.Store
	catalogStore      catalogstore.Store
	events            events.Service
	log               logrus.FieldLogger
	callEventCallback store.EventCallbackCaller
}

// NewServiceHandler creates a new fleet ServiceHandler instance.
// catalogStore is optional — when nil, catalog item ref validation is skipped.
func NewServiceHandler(fleetStore fleetstore.Store, catalogStore catalogstore.Store, events events.Service, log logrus.FieldLogger) *ServiceHandler {
	return &ServiceHandler{
		store:             fleetStore,
		catalogStore:      catalogStore,
		events:            events,
		log:               log,
		callEventCallback: store.CallEventCallback(domain.FleetKind, log),
	}
}

var _ Service = (*ServiceHandler)(nil)

// SanitizeFleet clears status and managed metadata from an untrusted fleet document
// (HTTP body or ResourceSync YAML). Callers that must set Owner must not use this.
func SanitizeFleet(fleet *domain.Fleet) {
	if fleet == nil {
		return
	}
	fleet.Status = nil
	common.NilOutManagedObjectMetaProperties(&fleet.Metadata)
	if fleet.Spec.Template.Metadata != nil {
		common.NilOutManagedObjectMetaProperties(fleet.Spec.Template.Metadata)
	}
}

// CreateFleetFromUntrusted sanitizes an untrusted fleet document, then creates it.
func CreateFleetFromUntrusted(ctx context.Context, svc Service, orgId uuid.UUID, fleet domain.Fleet) (*domain.Fleet, domain.Status) {
	SanitizeFleet(&fleet)
	return svc.CreateFleet(ctx, orgId, fleet)
}

// ReplaceFleetFromUntrusted sanitizes an untrusted fleet document, then replaces it.
func ReplaceFleetFromUntrusted(ctx context.Context, svc Service, orgId uuid.UUID, name string, fleet domain.Fleet, enforceOwnership bool) (*domain.Fleet, domain.Status) {
	SanitizeFleet(&fleet)
	return svc.ReplaceFleet(ctx, orgId, name, fleet, enforceOwnership)
}

func (h *ServiceHandler) CreateFleet(ctx context.Context, orgId uuid.UUID, fleet domain.Fleet) (*domain.Fleet, domain.Status) {
	if errs := fleet.Validate(); len(errs) > 0 {
		return nil, domain.StatusBadRequest(errors.Join(errs...).Error())
	}

	if status := common.ValidateCatalogItemRefs(ctx, orgId, h.catalogStore, &fleet.Spec.Template.Spec); status != domain.StatusOK() {
		return nil, status
	}

	name := lo.FromPtr(fleet.Metadata.Name)
	result, err := h.store.Create(ctx, orgId, &fleet)
	h.callEventCallback(ctx, h.callbackFleetUpdated, orgId, name, nil, result, err == nil, err)
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

func (h *ServiceHandler) ReplaceFleet(ctx context.Context, orgId uuid.UUID, name string, fleet domain.Fleet, enforceOwnership bool) (*domain.Fleet, domain.Status) {
	if errs := fleet.Validate(); len(errs) > 0 {
		return nil, domain.StatusBadRequest(errors.Join(errs...).Error())
	}
	if name != *fleet.Metadata.Name {
		return nil, domain.StatusBadRequest("resource name specified in metadata does not match name in path")
	}

	if status := common.ValidateCatalogItemRefs(ctx, orgId, h.catalogStore, &fleet.Spec.Template.Spec); status != domain.StatusOK() {
		return nil, status
	}

	result, before, created, err := h.store.Mutate(ctx, orgId, name, nil, func(m *fleetstore.FleetMutation) error {
		creating := m.Fleet == nil
		if creating {
			m.Fleet = &domain.Fleet{
				ApiVersion: domain.FleetAPIVersion,
				Kind:       domain.FleetKind,
				Metadata:   fleet.Metadata,
				Spec:       fleet.Spec,
				Status:     &domain.FleetStatus{Conditions: []domain.Condition{}},
			}
			m.Fleet.Metadata.Name = lo.ToPtr(name)
			return pruneFleetLifecycleOnCurrent(h.log, m.Fleet)
		}
		current := m.Fleet
		if err := common.CheckResourceVersionConflict(&current.Metadata, &fleet.Metadata); err != nil {
			return err
		}
		if enforceOwnership && fleetOwnershipConflict(current, &fleet) {
			return flterrors.ErrUpdatingResourceWithOwnerNotAllowed
		}
		current.Spec = fleet.Spec
		if fleet.Metadata.Labels != nil {
			current.Metadata.Labels = fleet.Metadata.Labels
		}
		if fleet.Metadata.Annotations != nil {
			current.Metadata.Annotations = fleet.Metadata.Annotations
		}
		if fleet.Metadata.Owner != nil {
			current.Metadata.Owner = fleet.Metadata.Owner
		}
		return pruneFleetLifecycleOnCurrent(h.log, current)
	})
	h.callEventCallback(ctx, h.callbackFleetUpdated, orgId, name, before, result, created, err)
	return result, common.StoreErrorToApiStatus(err, created, domain.FleetKind, &name)
}

// fleetOwnershipConflict reports whether replacing/patching an owned fleet's spec or
// labels would silently override changes made by its owning controller.
func fleetOwnershipConflict(existing, incoming *domain.Fleet) bool {
	if len(lo.FromPtr(existing.Metadata.Owner)) == 0 {
		return false
	}
	if !domain.FleetSpecsAreEqual(existing.Spec, incoming.Spec) {
		return true
	}
	return !reflect.DeepEqual(existing.Metadata.Labels, incoming.Metadata.Labels)
}

func (h *ServiceHandler) DeleteFleet(ctx context.Context, orgId uuid.UUID, name string, enforceOwnership bool) domain.Status {
	f, err := h.store.Get(ctx, orgId, name)
	if err != nil {
		if errors.Is(err, flterrors.ErrResourceNotFound) {
			return domain.StatusOK() // idempotent delete
		}
		return common.StoreErrorToApiStatus(err, false, domain.FleetKind, &name)
	}

	if enforceOwnership && len(lo.FromPtr(f.Metadata.Owner)) != 0 {
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
	result, _, err := h.store.UpdateStatus(ctx, orgId, &fleet)
	return result, common.StoreErrorToApiStatus(err, false, domain.FleetKind, &name)
}

// Only metadata.labels and spec can be patched. If we try to patch other fields, HTTP 400 Bad Request is returned.
func (h *ServiceHandler) PatchFleet(ctx context.Context, orgId uuid.UUID, name string, patch domain.PatchRequest, enforceOwnership bool) (*domain.Fleet, domain.Status) {
	currentObj, err := h.store.Get(ctx, orgId, name)
	if err != nil {
		return nil, common.StoreErrorToApiStatus(err, false, domain.FleetKind, &name)
	}

	if status := validateFleetPatch(ctx, orgId, h.catalogStore, currentObj, patch, name); status.Code != http.StatusOK {
		return nil, status
	}

	result, before, _, err := h.store.Mutate(ctx, orgId, name, currentObj, func(m *fleetstore.FleetMutation) error {
		if err := m.RequireExisting(); err != nil {
			return err
		}
		current := m.Fleet
		patched, err := applyFleetPatch(ctx, current, patch, name)
		if err != nil {
			return err
		}
		if status := common.ValidateCatalogItemRefs(ctx, orgId, h.catalogStore, &patched.Spec.Template.Spec); status != domain.StatusOK() {
			return common.ApiStatusToErr(status)
		}
		if enforceOwnership && fleetOwnershipConflict(current, patched) {
			return flterrors.ErrUpdatingResourceWithOwnerNotAllowed
		}
		// Annotations/owner were nil'd as managed fields; keep current's then prune lifecycle.
		current.Spec = patched.Spec
		current.Metadata.Labels = patched.Metadata.Labels
		return pruneFleetLifecycleOnCurrent(h.log, current)
	})
	h.callEventCallback(ctx, h.callbackFleetUpdated, orgId, name, before, result, false, err)
	return result, common.StoreErrorToApiStatus(err, false, domain.FleetKind, &name)
}

func validateFleetPatch(ctx context.Context, orgId uuid.UUID, catalogStore catalogstore.Store, current *domain.Fleet, patch domain.PatchRequest, name string) domain.Status {
	patched, err := applyFleetPatch(ctx, current, patch, name)
	if err != nil {
		return domain.StatusBadRequest(err.Error())
	}
	return common.ValidateCatalogItemRefs(ctx, orgId, catalogStore, &patched.Spec.Template.Spec)
}

func applyFleetPatch(ctx context.Context, current *domain.Fleet, patch domain.PatchRequest, name string) (*domain.Fleet, error) {
	patched := &domain.Fleet{}
	if err := common.ApplyJSONPatch(ctx, current, patched, patch, "/fleets/"+name); err != nil {
		return nil, err
	}
	if errs := patched.Validate(); len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	if errs := current.ValidateUpdate(patched); len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	common.NilOutManagedObjectMetaProperties(&patched.Metadata)
	patched.Metadata.ResourceVersion = nil
	return patched, nil
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
	result, before, _, err := h.store.Mutate(ctx, orgId, name, nil, func(m *fleetstore.FleetMutation) error {
		if err := m.RequireExisting(); err != nil {
			return err
		}
		current := m.Fleet
		if current.Status == nil {
			current.Status = &domain.FleetStatus{}
		}
		if current.Status.Conditions == nil {
			current.Status.Conditions = []domain.Condition{}
		}
		changed := false
		for _, condition := range conditions {
			if domain.SetStatusCondition(&current.Status.Conditions, condition) {
				changed = true
			}
		}
		if !changed {
			return store.ErrMutateSkipWrite
		}
		return nil
	})
	h.callEventCallback(ctx, h.callbackFleetUpdated, orgId, name, before, result, false, err)
	return common.StoreErrorToApiStatus(err, false, domain.FleetKind, &name)
}

func (h *ServiceHandler) UpdateFleetAnnotations(ctx context.Context, orgId uuid.UUID, name string, annotations map[string]string, deleteKeys []string) domain.Status {
	result, before, err := h.store.UpdateAnnotations(ctx, orgId, name, annotations, deleteKeys)
	h.callEventCallback(ctx, h.callbackFleetUpdated, orgId, name, before, result, false, err)
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
