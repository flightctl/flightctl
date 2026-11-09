package fleet

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strconv"

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
	descriptors       common.ApprovedDescriptorProvider
	getRepository     common.RepositoryGetter
}

// HandlerOption configures optional dependencies of a fleet ServiceHandler.
type HandlerOption func(*ServiceHandler)

// WithApprovedDescriptorProvider sets the source of approved firmware updater
// releases. Without it, every lookup reports the releases as unavailable.
func WithApprovedDescriptorProvider(provider common.ApprovedDescriptorProvider) HandlerOption {
	return func(h *ServiceHandler) {
		if provider != nil {
			h.descriptors = provider
		}
	}
}

// WithRepositoryGetter sets the tenant-scoped Repository lookup that firmware
// admission uses to validate the literal artifact and signature repositories
// a fleet template references. Without it, firmware that references a
// literal repository is rejected.
func WithRepositoryGetter(getRepository common.RepositoryGetter) HandlerOption {
	return func(h *ServiceHandler) {
		h.getRepository = getRepository
	}
}

// NewServiceHandler creates a new fleet ServiceHandler instance.
// catalogStore is optional — when nil, catalog item ref validation is skipped.
func NewServiceHandler(fleetStore fleetstore.Store, catalogStore catalogstore.Store, events events.Service, log logrus.FieldLogger, opts ...HandlerOption) *ServiceHandler {
	h := &ServiceHandler{
		store:             fleetStore,
		catalogStore:      catalogStore,
		events:            events,
		log:               log,
		callEventCallback: store.CallEventCallback(domain.FleetKind, log),
		descriptors:       common.UnavailableDescriptorProvider{},
	}
	for _, opt := range opts {
		opt(h)
	}
	return h
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
	if _, status := h.admitFirmware(ctx, orgId, name, nil, &fleet.Spec.Template.Spec, true); status.Code != http.StatusOK {
		return nil, status
	}

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

	var callbackStatus domain.Status
	result, before, created, err := h.store.Mutate(ctx, orgId, name, nil, func(m *fleetstore.FleetMutation) error {
		creating := m.Fleet == nil
		if creating {
			if _, admitted := h.admitFirmware(ctx, orgId, name, nil, &fleet.Spec.Template.Spec, true); admitted.Code != http.StatusOK {
				callbackStatus = admitted
				return common.ApiStatusToErr(admitted)
			}
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
		// A replacement that omits the template firmware keeps the stored firmware.
		incoming := fleet
		incoming.Spec.Template.Spec = *common.PreserveOmittedFirmware(&fleet.Spec.Template.Spec, &current.Spec.Template.Spec)
		if enforceOwnership && fleetOwnershipConflict(current, &incoming) {
			return flterrors.ErrUpdatingResourceWithOwnerNotAllowed
		}
		if _, admitted := h.admitFirmware(ctx, orgId, name, &current.Spec.Template.Spec, &incoming.Spec.Template.Spec, false); admitted.Code != http.StatusOK {
			callbackStatus = admitted
			return common.ApiStatusToErr(admitted)
		}
		current.Spec = incoming.Spec
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
	if err != nil && callbackStatus.Code != 0 {
		return result, callbackStatus
	}
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

	if status := h.validateFleetPatch(ctx, orgId, currentObj, patch, name); status.Code != http.StatusOK {
		return nil, status
	}

	var callbackStatus domain.Status
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
		if _, admitted := h.admitFirmware(ctx, orgId, name, &current.Spec.Template.Spec, &patched.Spec.Template.Spec, false); admitted.Code != http.StatusOK {
			callbackStatus = admitted
			return common.ApiStatusToErr(admitted)
		}
		// Annotations/owner were nil'd as managed fields; keep current's then prune lifecycle.
		current.Spec = patched.Spec
		current.Metadata.Labels = patched.Metadata.Labels
		return pruneFleetLifecycleOnCurrent(h.log, current)
	})
	h.callEventCallback(ctx, h.callbackFleetUpdated, orgId, name, before, result, false, err)
	if err != nil && callbackStatus.Code != 0 {
		return result, callbackStatus
	}
	return result, common.StoreErrorToApiStatus(err, false, domain.FleetKind, &name)
}

func (h *ServiceHandler) validateFleetPatch(ctx context.Context, orgId uuid.UUID, current *domain.Fleet, patch domain.PatchRequest, name string) domain.Status {
	patched, err := applyFleetPatch(ctx, current, patch, name)
	if err != nil {
		return domain.StatusBadRequest(err.Error())
	}
	if status := common.ValidateCatalogItemRefs(ctx, orgId, h.catalogStore, &patched.Spec.Template.Spec); status != domain.StatusOK() {
		return status
	}
	_, status := h.admitFirmware(ctx, orgId, name, &current.Spec.Template.Spec, &patched.Spec.Template.Spec, false)
	return status
}

func applyFleetPatch(ctx context.Context, current *domain.Fleet, patch domain.PatchRequest, name string) (*domain.Fleet, error) {
	if err := common.ValidatePatchedFirmware(current, patch, common.FleetFirmwarePatchPath); err != nil {
		return nil, err
	}
	patched := &domain.Fleet{}
	if err := common.ApplyJSONPatch(ctx, current, patched, patch, "/fleets/"+name); err != nil {
		return nil, err
	}
	// Only an explicit removal withdraws the template firmware; any other patch
	// that leaves it absent keeps the stored firmware.
	if !common.FirmwareRemovedByPatch(patch, common.FleetFirmwarePatchPath) {
		patched.Spec.Template.Spec = *common.PreserveOmittedFirmware(&patched.Spec.Template.Spec, &current.Spec.Template.Spec)
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

// admitFirmware validates the desired firmware of a fleet's device template
// that a write is about to store. Every check whose inputs are concrete runs
// now, including authorization of a literal updater release and the tenant
// lookup of every literal repository. Checks that depend on template
// parameters are deferred until the template is rendered for each device;
// accepting them is not a claim that the rendered firmware is valid. A write
// that keeps the stored template spec stores nothing new, and admission never
// writes repository references.
func (h *ServiceHandler) admitFirmware(ctx context.Context, orgId uuid.UUID, name string, stored, candidate *domain.DeviceSpec, creating bool) (common.FirmwareAdmission, domain.Status) {
	if candidate == nil || candidate.Firmware == nil {
		return common.FirmwareAdmission{}, domain.StatusOK()
	}
	if !creating && stored != nil && domain.DeviceSpecsAreEqual(*stored, *candidate) {
		return common.FirmwareAdmission{}, domain.StatusOK()
	}
	admission, status := common.ValidateFirmwareIntent(ctx, orgId, h.descriptors, h.getRepository, candidate, common.FirmwareFleetTemplate)
	if status.Code == http.StatusOK && len(admission.Deferred) > 0 {
		h.log.WithField("deferredChecks", admission.Deferred).Debugf("Fleet %s firmware has %d checks that wait for template rendering", name, len(admission.Deferred))
	}
	return admission, status
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
		var existing []domain.Condition
		if current.Status.Conditions != nil {
			existing = current.Status.Conditions
		}
		merged, changed := common.MergeStatusConditions(existing, conditions)
		if !changed {
			return store.ErrMutateSkipWrite
		}
		current.Status.Conditions = merged
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

// SetDeltaPrepareIdentity records the generation and resource version that
// validation prepared. The generation and current marker are checked inside
// the same optimistic mutation so an older validation cannot replace a newer
// marker.
func (h *ServiceHandler) SetDeltaPrepareIdentity(ctx context.Context, orgId uuid.UUID, name string, sourceResourceVersion, sourceGeneration int64) (bool, domain.Status) {
	accepted := false
	result, before, _, err := h.store.Mutate(ctx, orgId, name, nil, func(m *fleetstore.FleetMutation) error {
		accepted = false
		if err := m.RequireExisting(); err != nil {
			return err
		}
		if sourceResourceVersion <= 0 || sourceGeneration <= 0 || m.Fleet.Metadata.Generation == nil || *m.Fleet.Metadata.Generation != sourceGeneration {
			return store.ErrMutateSkipWrite
		}

		annotations := lo.FromPtr(m.Fleet.Metadata.Annotations)
		if annotations == nil {
			annotations = map[string]string{}
		}
		if currentResourceVersion, exists := annotations[domain.FleetAnnotationDeltaPrepareResourceVersion]; exists {
			current, err := strconv.ParseInt(currentResourceVersion, 10, 64)
			if err != nil || current > sourceResourceVersion {
				return store.ErrMutateSkipWrite
			}
		}

		resourceVersion := strconv.FormatInt(sourceResourceVersion, 10)
		generation := strconv.FormatInt(sourceGeneration, 10)
		if annotations[domain.FleetAnnotationDeltaPrepareResourceVersion] == resourceVersion && annotations[domain.FleetAnnotationDeltaPrepareGeneration] == generation {
			accepted = true
			return store.ErrMutateSkipWrite
		}
		annotations[domain.FleetAnnotationDeltaPrepareResourceVersion] = resourceVersion
		annotations[domain.FleetAnnotationDeltaPrepareGeneration] = generation
		m.Fleet.Metadata.Annotations = &annotations
		accepted = true
		return nil
	})
	h.callEventCallback(ctx, h.callbackFleetUpdated, orgId, name, before, result, false, err)
	if err != nil {
		return false, common.StoreErrorToApiStatus(err, false, domain.FleetKind, &name)
	}
	return accepted, domain.StatusOK()
}

// ReplaceFleetRepositoryRefsIfCurrent replaces the repository references of a
// fleet with the complete set computed from its device template: the
// template's configuration repositories plus the literal artifact and
// signature repositories of its firmware. The set is computed before the store
// transaction opens; it is written only while the fleet still has the given
// generation. When the firmware references cannot be known, nothing is
// written.
func (h *ServiceHandler) ReplaceFleetRepositoryRefsIfCurrent(ctx context.Context, orgId uuid.UUID, name string, generation int64, templateSpec *domain.DeviceSpec) (common.RepositoryRefsOutcome, domain.Status) {
	names, complete, err := common.CollectRepositoryDependencies(ctx, h.descriptors, templateSpec, common.FirmwareFleetTemplate)
	if err != nil {
		return 0, domain.StatusInternalServerError(fmt.Sprintf("computing repository references of fleet %q: %v", name, err))
	}
	if !complete {
		return common.RepositoryRefsIncomplete, domain.StatusOK()
	}
	applied, err := h.store.ReplaceRepositoryRefsIfCurrent(ctx, orgId, name, generation, names...)
	if err != nil {
		return 0, common.StoreErrorToApiStatus(err, false, domain.FleetKind, &name)
	}
	if !applied {
		return common.RepositoryRefsStale, domain.StatusOK()
	}
	return common.RepositoryRefsReplaced, domain.StatusOK()
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

func (h *ServiceHandler) UnsetOwner(ctx context.Context, orgId uuid.UUID, owner string) error {
	return h.store.UnsetOwner(ctx, store.DB(ctx, nil), orgId, owner)
}
