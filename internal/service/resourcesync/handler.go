package resourcesync

import (
	"context"
	"errors"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/service/common"
	"github.com/flightctl/flightctl/internal/service/events"
	catalogstore "github.com/flightctl/flightctl/internal/store/catalog"
	fleetstore "github.com/flightctl/flightctl/internal/store/fleet"
	resourcesyncstore "github.com/flightctl/flightctl/internal/store/resourcesync"
	"github.com/flightctl/flightctl/internal/store/selector"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

type ServiceHandler struct {
	store        resourcesyncstore.Store
	catalogStore catalogstore.Store
	fleetStore   fleetstore.Store
	events       events.Service
	log          logrus.FieldLogger
}

// NewServiceHandler creates a new resourcesync ServiceHandler instance.
func NewServiceHandler(store resourcesyncstore.Store, catalogStore catalogstore.Store, fleetStore fleetstore.Store, events events.Service, log logrus.FieldLogger) *ServiceHandler {
	return &ServiceHandler{store: store, catalogStore: catalogStore, fleetStore: fleetStore, events: events, log: log}
}

var _ Service = (*ServiceHandler)(nil)

// SanitizeResourceSync clears status and managed metadata from an untrusted resourcesync
// document (HTTP body).
func SanitizeResourceSync(rs *domain.ResourceSync) {
	if rs == nil {
		return
	}
	rs.Status = nil
	common.NilOutManagedObjectMetaProperties(&rs.Metadata)
}

// CreateResourceSyncFromUntrusted sanitizes an untrusted resourcesync document, then creates it.
func CreateResourceSyncFromUntrusted(ctx context.Context, svc Service, orgId uuid.UUID, rs domain.ResourceSync) (*domain.ResourceSync, domain.Status) {
	SanitizeResourceSync(&rs)
	return svc.CreateResourceSync(ctx, orgId, rs)
}

// ReplaceResourceSyncFromUntrusted sanitizes an untrusted resourcesync document, then replaces it.
func ReplaceResourceSyncFromUntrusted(ctx context.Context, svc Service, orgId uuid.UUID, name string, rs domain.ResourceSync) (*domain.ResourceSync, domain.Status) {
	SanitizeResourceSync(&rs)
	return svc.ReplaceResourceSync(ctx, orgId, name, rs)
}

func (h *ServiceHandler) CreateResourceSync(ctx context.Context, orgId uuid.UUID, rs domain.ResourceSync) (*domain.ResourceSync, domain.Status) {
	if errs := rs.Validate(); len(errs) > 0 {
		return nil, domain.StatusBadRequest(errors.Join(errs...).Error())
	}

	result, err := h.store.Create(ctx, orgId, &rs, h.callbackResourceSyncUpdated)
	return result, common.StoreErrorToApiStatus(err, true, domain.ResourceSyncKind, rs.Metadata.Name)
}

func (h *ServiceHandler) ListResourceSyncs(ctx context.Context, orgId uuid.UUID, params domain.ListResourceSyncsParams) (*domain.ResourceSyncList, domain.Status) {
	listParams, status := common.PrepareListParams(params.Continue, params.LabelSelector, params.FieldSelector, params.Limit)
	if status != domain.StatusOK() {
		return nil, status
	}

	result, err := h.store.List(ctx, orgId, *listParams)
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

func (h *ServiceHandler) GetResourceSync(ctx context.Context, orgId uuid.UUID, name string) (*domain.ResourceSync, domain.Status) {
	result, err := h.store.Get(ctx, orgId, name)
	return result, common.StoreErrorToApiStatus(err, false, domain.ResourceSyncKind, &name)
}

func (h *ServiceHandler) ReplaceResourceSync(ctx context.Context, orgId uuid.UUID, name string, rs domain.ResourceSync) (*domain.ResourceSync, domain.Status) {
	if errs := rs.Validate(); len(errs) > 0 {
		return nil, domain.StatusBadRequest(errors.Join(errs...).Error())
	}
	if name != *rs.Metadata.Name {
		return nil, domain.StatusBadRequest("resource name specified in metadata does not match name in path")
	}

	result, created, err := h.store.CreateOrUpdate(ctx, orgId, &rs, h.callbackResourceSyncUpdated)
	return result, common.StoreErrorToApiStatus(err, created, domain.ResourceSyncKind, &name)
}

func (h *ServiceHandler) DeleteResourceSync(ctx context.Context, orgId uuid.UUID, name string) domain.Status {
	callback := func(ctx context.Context, tx *gorm.DB, orgId uuid.UUID, owner string) error {
		if err := h.catalogStore.UnsetItemOwner(ctx, tx, orgId, owner); err != nil {
			return err
		}
		if err := h.catalogStore.UnsetOwner(ctx, tx, orgId, owner); err != nil {
			return err
		}
		return h.fleetStore.UnsetOwner(ctx, tx, orgId, owner)
	}

	err := h.store.Delete(ctx, orgId, name, callback, h.callbackResourceSyncDeleted)
	status := common.StoreErrorToApiStatus(err, false, domain.ResourceSyncKind, &name)
	return status
}

// Only metadata.labels and spec can be patched. If we try to patch other fields, HTTP 400 Bad Request is returned.
func (h *ServiceHandler) PatchResourceSync(ctx context.Context, orgId uuid.UUID, name string, patch domain.PatchRequest) (*domain.ResourceSync, domain.Status) {
	currentObj, err := h.store.Get(ctx, orgId, name)
	if err != nil {
		return nil, common.StoreErrorToApiStatus(err, false, domain.ResourceSyncKind, &name)
	}

	newObj := &domain.ResourceSync{}
	err = common.ApplyJSONPatch(ctx, currentObj, newObj, patch, "/resourcesyncs/"+name)
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
	result, err := h.store.Update(ctx, orgId, newObj, h.callbackResourceSyncUpdated)
	return result, common.StoreErrorToApiStatus(err, false, domain.ResourceSyncKind, &name)
}

func (h *ServiceHandler) ReplaceResourceSyncStatus(ctx context.Context, orgId uuid.UUID, name string, resourceSync domain.ResourceSync) (*domain.ResourceSync, domain.Status) {
	if errs := resourceSync.Validate(); len(errs) > 0 {
		return nil, domain.StatusBadRequest(errors.Join(errs...).Error())
	}
	if name != *resourceSync.Metadata.Name {
		return nil, domain.StatusBadRequest("resource name specified in metadata does not match name in path")
	}

	result, err := h.store.UpdateStatus(ctx, orgId, &resourceSync, h.callbackResourceSyncUpdated)
	return result, common.StoreErrorToApiStatus(err, false, domain.ResourceSyncKind, &name)
}

// callbackResourceSyncUpdated is the resource sync-specific callback that handles resource sync events
func (h *ServiceHandler) callbackResourceSyncUpdated(ctx context.Context, resourceKind domain.ResourceKind, orgId uuid.UUID, name string, oldResource, newResource interface{}, created bool, err error) {
	if err != nil {
		status := common.StoreErrorToApiStatus(err, created, string(resourceKind), &name)
		h.events.CreateEvent(ctx, orgId, common.GetResourceCreatedOrUpdatedFailureEvent(ctx, created, resourceKind, name, status, nil))
		return
	}

	var (
		oldResourceSync, newResourceSync *domain.ResourceSync
		ok                               bool
	)
	if oldResourceSync, newResourceSync, ok = common.CastResources[domain.ResourceSync](oldResource, newResource); !ok {
		return
	}

	// Emit success event for create/update
	if created {
		h.events.CreateEvent(ctx, orgId, common.GetResourceCreatedOrUpdatedSuccessEvent(ctx, created, resourceKind, name, nil, h.log, nil))
	} else if oldResourceSync != nil && newResourceSync != nil {
		updateDetails := common.ComputeResourceUpdatedDetails(oldResourceSync.Metadata, newResourceSync.Metadata)
		h.events.CreateEvent(ctx, orgId, common.GetResourceCreatedOrUpdatedSuccessEvent(ctx, created, resourceKind, name, updateDetails, h.log, nil))
	}

	// Emit condition-specific events
	emitResourceSyncConditionEvents(ctx, h.events, orgId, name, oldResourceSync, newResourceSync)
}

func emitResourceSyncConditionEvents(ctx context.Context, eventsService events.Service, orgId uuid.UUID, name string, oldResourceSync, newResourceSync *domain.ResourceSync) {
	if oldResourceSync == nil || newResourceSync == nil {
		return
	}

	// Check for commit hash changes
	var oldCommit, newCommit string
	if oldResourceSync.Status != nil {
		oldCommit = util.DefaultIfNil(oldResourceSync.Status.ObservedCommit, "")
	}
	if newResourceSync.Status != nil {
		newCommit = util.DefaultIfNil(newResourceSync.Status.ObservedCommit, "")
	}
	if oldCommit != newCommit && newCommit != "" {
		eventsService.CreateEvent(ctx, orgId, common.GetResourceSyncCommitDetectedEvent(ctx, name, newCommit))
	}

	// Check for condition changes
	var oldConditions, newConditions []domain.Condition
	if oldResourceSync.Status != nil {
		oldConditions = oldResourceSync.Status.Conditions
	}
	if newResourceSync.Status != nil {
		newConditions = newResourceSync.Status.Conditions
	}

	// Accessible condition
	oldAccessible := domain.FindStatusCondition(oldConditions, domain.ConditionTypeResourceSyncAccessible)
	newAccessible := domain.FindStatusCondition(newConditions, domain.ConditionTypeResourceSyncAccessible)
	if common.HasConditionChanged(oldAccessible, newAccessible) {
		if domain.IsStatusConditionTrue(newConditions, domain.ConditionTypeResourceSyncAccessible) {
			eventsService.CreateEvent(ctx, orgId, common.GetResourceSyncAccessibleEvent(ctx, name))
		} else {
			message := "Repository access failed"
			if newAccessible != nil && newAccessible.Message != "" {
				message = newAccessible.Message
			}
			eventsService.CreateEvent(ctx, orgId, common.GetResourceSyncInaccessibleEvent(ctx, name, message))
		}
	}

	// ResourceParsed condition
	oldParsed := domain.FindStatusCondition(oldConditions, domain.ConditionTypeResourceSyncResourceParsed)
	newParsed := domain.FindStatusCondition(newConditions, domain.ConditionTypeResourceSyncResourceParsed)
	if common.HasConditionChanged(oldParsed, newParsed) {
		if domain.IsStatusConditionTrue(newConditions, domain.ConditionTypeResourceSyncResourceParsed) {
			eventsService.CreateEvent(ctx, orgId, common.GetResourceSyncParsedEvent(ctx, name))
		} else {
			message := "Resource parsing failed"
			if newParsed != nil && newParsed.Message != "" {
				message = newParsed.Message
			}
			eventsService.CreateEvent(ctx, orgId, common.GetResourceSyncParsingFailedEvent(ctx, name, message))
		}
	}

	// Synced condition
	oldSynced := domain.FindStatusCondition(oldConditions, domain.ConditionTypeResourceSyncSynced)
	newSynced := domain.FindStatusCondition(newConditions, domain.ConditionTypeResourceSyncSynced)
	if common.HasConditionChanged(oldSynced, newSynced) {
		if domain.IsStatusConditionTrue(newConditions, domain.ConditionTypeResourceSyncSynced) {
			eventsService.CreateEvent(ctx, orgId, common.GetResourceSyncSyncedEvent(ctx, name))
		} else {
			// Only emit failure event if it's an actual failure, not just "NewHashDetected"
			// "NewHashDetected" is a normal state change, not a failure
			// The commit detected event is already emitted when the hash changes
			if newSynced != nil && newSynced.Reason != domain.ResourceSyncNewHashDetectedReason {
				message := "Resource sync failed"
				if newSynced.Message != "" {
					message = newSynced.Message
				}
				eventsService.CreateEvent(ctx, orgId, common.GetResourceSyncSyncFailedEvent(ctx, name, message))
			}
		}
	}
}

// callbackResourceSyncDeleted is the resource sync-specific callback that handles resource sync deletion events
func (h *ServiceHandler) callbackResourceSyncDeleted(ctx context.Context, resourceKind domain.ResourceKind, orgId uuid.UUID, name string, oldResource, newResource interface{}, created bool, err error) {
	h.events.HandleGenericResourceDeletedEvents(ctx, resourceKind, orgId, name, oldResource, newResource, created, err)
}
