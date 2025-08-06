package service

import (
	"context"
	"reflect"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/service/common"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
)

// EventHandler handles all event emission logic for the service
type EventHandler struct {
	store store.Store
	log   logrus.FieldLogger
}

// NewEventHandler creates a new EventHandler instance
func NewEventHandler(store store.Store, log logrus.FieldLogger) *EventHandler {
	return &EventHandler{
		store: store,
		log:   log,
	}
}

// CreateEvent creates an event in the store
func (h *EventHandler) CreateEvent(ctx context.Context, event *api.Event) {
	if event == nil {
		return
	}

	orgId := getOrgIdFromContext(ctx)

	err := h.store.Event().Create(ctx, orgId, event)
	if err != nil {
		h.log.Errorf("failed emitting <%s> resource updated %s event for %s %s/%s: %v", *event.Metadata.Name, event.Reason, event.InvolvedObject.Kind, orgId, event.InvolvedObject.Name, err)
	}
}

//////////////////////////////////////////////////////
//                    Common Events               //
//////////////////////////////////////////////////////

// HandleGenericResourceUpdatedEvents handles generic resource update event emission logic
func (h *EventHandler) HandleGenericResourceUpdatedEvents(ctx context.Context, resourceKind api.ResourceKind, orgId uuid.UUID, name string, oldResource, newResource interface{}, created bool, err error) {
	if err != nil {
		status := StoreErrorToApiStatus(err, created, string(resourceKind), &name)
		h.CreateEvent(ctx, common.GetResourceCreatedOrUpdatedFailureEvent(ctx, created, resourceKind, name, status, nil))
	} else {
		h.CreateEvent(ctx, common.GetResourceCreatedOrUpdatedSuccessEvent(ctx, created, resourceKind, name, nil, nil))
	}
}

// HandleGenericResourceDeletedEvents handles generic resource deletion event emission logic
func (h *EventHandler) HandleGenericResourceDeletedEvents(ctx context.Context, resourceKind api.ResourceKind, _ uuid.UUID, name string, _, _ interface{}, created bool, err error) {
	if err != nil {
		status := StoreErrorToApiStatus(err, created, string(resourceKind), &name)
		h.CreateEvent(ctx, common.GetResourceDeletedFailureEvent(ctx, resourceKind, name, status))
	} else {
		h.CreateEvent(ctx, common.GetResourceDeletedSuccessEvent(ctx, resourceKind, name))
	}
}

//////////////////////////////////////////////////////
//                    Device Events                 //
//////////////////////////////////////////////////////

// HandleDeviceUpdatedEvents handles all device-related event emission logic
func (h *EventHandler) HandleDeviceUpdatedEvents(ctx context.Context, resourceKind api.ResourceKind, orgId uuid.UUID, name string, oldResource, newResource interface{}, created bool, err error) {
	if err != nil {
		status := StoreErrorToApiStatus(err, created, api.DeviceKind, &name)
		h.CreateEvent(ctx, common.GetResourceCreatedOrUpdatedFailureEvent(ctx, created, api.DeviceKind, name, status, nil))
		return
	}
	var (
		oldDevice, newDevice *api.Device
		ok                   bool
	)
	if oldDevice, newDevice, ok = castResources[api.Device](oldResource, newResource); !ok {
		return
	}

	// Only generate status change events when the device is not being created
	if !created {
		statusUpdates := common.ComputeDeviceStatusChanges(ctx, oldDevice, newDevice, orgId, h.store)
		for _, update := range statusUpdates {
			h.CreateEvent(ctx, common.GetDeviceEventFromUpdateDetails(ctx, name, update))
		}
	}

	// Generate resource creation/update events
	if created {
		h.CreateEvent(ctx, common.GetResourceCreatedOrUpdatedSuccessEvent(ctx, true, api.DeviceKind, name, nil, h.log))
	} else {
		updateDetails := h.computeDeviceResourceUpdatedDetails(oldDevice, newDevice)
		// Generate ResourceUpdated event if there are spec changes or status changes
		if updateDetails != nil {
			h.CreateEvent(ctx, common.GetResourceCreatedOrUpdatedSuccessEvent(ctx, false, api.DeviceKind, name, updateDetails, h.log))
		}
	}
}

// HandleDeviceDecommissionEvents handles device decommission event emission logic
func (h *EventHandler) HandleDeviceDecommissionEvents(ctx context.Context, _ api.ResourceKind, _ uuid.UUID, name string, _, _ interface{}, created bool, err error) {
	if err != nil {
		status := StoreErrorToApiStatus(err, created, api.DeviceKind, &name)
		h.CreateEvent(ctx, common.GetDeviceDecommissionedFailureEvent(ctx, created, api.DeviceKind, name, status))
	} else {
		h.CreateEvent(ctx, common.GetDeviceDecommissionedSuccessEvent(ctx, created, api.DeviceKind, name, nil, nil))
	}
}

// computeDeviceResourceUpdatedDetails determines which fields were updated by comparing old and new resources
func (h *EventHandler) computeDeviceResourceUpdatedDetails(oldDevice, newDevice *api.Device) *api.ResourceUpdatedDetails {
	if oldDevice == nil || newDevice == nil {
		return nil
	}

	updateDetails := &api.ResourceUpdatedDetails{
		UpdatedFields: []api.ResourceUpdatedDetailsUpdatedFields{},
	}

	// Check if spec changed
	if oldDevice.Metadata.Generation != nil && newDevice.Metadata.Generation != nil &&
		*oldDevice.Metadata.Generation != *newDevice.Metadata.Generation {
		updateDetails.UpdatedFields = append(updateDetails.UpdatedFields, api.Spec)
	}

	// Check if labels changed
	if !reflect.DeepEqual(oldDevice.Metadata.Labels, newDevice.Metadata.Labels) {
		updateDetails.UpdatedFields = append(updateDetails.UpdatedFields, api.Labels)
	}

	// Check if owner changed
	if !util.StringsAreEqual(oldDevice.Metadata.Owner, newDevice.Metadata.Owner) {
		updateDetails.UpdatedFields = append(updateDetails.UpdatedFields, api.Owner)
		if oldDevice.Metadata.Owner != nil {
			updateDetails.PreviousOwner = oldDevice.Metadata.Owner
		}
		if newDevice.Metadata.Owner != nil {
			updateDetails.NewOwner = newDevice.Metadata.Owner
		}
	}

	// Only return details if there were actual updates
	if len(updateDetails.UpdatedFields) == 0 {
		return nil
	}

	return updateDetails
}

//////////////////////////////////////////////////////
//                    Fleet Events                 //
//////////////////////////////////////////////////////

func (h *EventHandler) EmitFleetRolloutStartedEvent(ctx context.Context, templateVersionName string, fleetName string, immediateRollout bool) {
	event := common.GetFleetRolloutStartedEvent(ctx, templateVersionName, fleetName, immediateRollout, false)
	if event != nil {
		h.CreateEvent(ctx, event)
	}
}

// HandleFleetUpdatedEvents handles all fleet-related event emission logic
func (h *EventHandler) HandleFleetUpdatedEvents(ctx context.Context, resourceKind api.ResourceKind, orgId uuid.UUID, name string, oldResource, newResource interface{}, created bool, err error) {
	var (
		oldFleet, newFleet *api.Fleet
		ok                 bool
		event              *api.Event
	)
	if oldFleet, newFleet, ok = castResources[api.Fleet](oldResource, newResource); !ok {
		return
	}
	if err != nil {
		status := StoreErrorToApiStatus(err, created, api.FleetKind, &name)
		event = common.GetResourceCreatedOrUpdatedFailureEvent(ctx, created, api.FleetKind, name, status, nil)
	} else {
		// Compute ResourceUpdatedDetails at service level
		var updateDetails *api.ResourceUpdatedDetails
		if !created && oldFleet != nil && newFleet != nil {
			updateDetails = h.computeFleetResourceUpdatedDetails(oldFleet, newFleet)
		}
		event = common.GetResourceCreatedOrUpdatedSuccessEvent(ctx, created, api.FleetKind, name, updateDetails, nil)
	}

	// Emit a created/updated event (if nil, no event is emitted)
	h.CreateEvent(ctx, event)

	deployingTemplateVersion, exists := newFleet.GetAnnotation(api.FleetAnnotationDeployingTemplateVersion)
	if !exists {
		return
	}

	// Emit fleet rollout events if applicable
	h.emitFleetRolloutNewEvent(ctx, name, deployingTemplateVersion, oldFleet, newFleet)
	h.emitFleetRolloutCompletionEvents(ctx, name, deployingTemplateVersion, oldFleet, newFleet)
	h.emitFleetRolloutFailedEvent(ctx, name, deployingTemplateVersion, oldFleet, newFleet)
}

func (h *EventHandler) emitFleetRolloutNewEvent(ctx context.Context, name string, deployingTemplateVersion string, oldFleet, newFleet *api.Fleet) {
	if !newFleet.IsRolloutNew(oldFleet) {
		return
	}
	h.CreateEvent(ctx, common.GetFleetRolloutNewEvent(ctx, name))
}

func (h *EventHandler) emitFleetRolloutCompletionEvents(ctx context.Context, name string, deployingTemplateVersion string, oldFleet, newFleet *api.Fleet) {
	batchCompleted, report := newFleet.IsRolloutBatchCompleted(oldFleet)
	if !batchCompleted {
		return
	}
	h.CreateEvent(ctx, common.GetFleetRolloutBatchCompletedEvent(ctx, name, deployingTemplateVersion, report))
	if report.BatchName == api.FinalImplicitBatchName {
		h.CreateEvent(ctx, common.GetFleetRolloutCompletedEvent(ctx, name, deployingTemplateVersion))
	}
}

func (h *EventHandler) emitFleetRolloutFailedEvent(ctx context.Context, name string, deployingTemplateVersion string, oldFleet, newFleet *api.Fleet) {
	newCondition := api.FindStatusCondition(newFleet.Status.Conditions, api.ConditionTypeFleetRolloutInProgress)
	if newCondition == nil || newCondition.Reason != api.RolloutSuspendedReason {
		return
	}
	oldCondition := api.FindStatusCondition(oldFleet.Status.Conditions, api.ConditionTypeFleetRolloutInProgress)
	if oldCondition != nil && oldCondition.Reason == api.RolloutSuspendedReason {
		return
	}

	h.CreateEvent(ctx, common.GetFleetRolloutFailedEvent(ctx, name, deployingTemplateVersion, newCondition.Message))
}

// computeFleetResourceUpdatedDetails determines which fields were updated by comparing old and new fleet resources
func (h *EventHandler) computeFleetResourceUpdatedDetails(oldFleet, newFleet *api.Fleet) *api.ResourceUpdatedDetails {
	if oldFleet == nil || newFleet == nil {
		return nil
	}

	updateDetails := &api.ResourceUpdatedDetails{
		UpdatedFields: []api.ResourceUpdatedDetailsUpdatedFields{},
	}

	// Check if spec changed
	if oldFleet.Metadata.Generation != nil && newFleet.Metadata.Generation != nil &&
		*oldFleet.Metadata.Generation != *newFleet.Metadata.Generation {
		updateDetails.UpdatedFields = append(updateDetails.UpdatedFields, api.Spec)
	}

	// Check if labels changed
	if !reflect.DeepEqual(oldFleet.Metadata.Labels, newFleet.Metadata.Labels) {
		updateDetails.UpdatedFields = append(updateDetails.UpdatedFields, api.Labels)
	}

	// Check if owner changed
	if !util.StringsAreEqual(oldFleet.Metadata.Owner, newFleet.Metadata.Owner) {
		updateDetails.UpdatedFields = append(updateDetails.UpdatedFields, api.Owner)
		if oldFleet.Metadata.Owner != nil {
			updateDetails.PreviousOwner = oldFleet.Metadata.Owner
		}
		if newFleet.Metadata.Owner != nil {
			updateDetails.NewOwner = newFleet.Metadata.Owner
		}
	}

	// Only return details if there were actual updates
	if len(updateDetails.UpdatedFields) == 0 {
		return nil
	}

	return updateDetails
}

//////////////////////////////////////////////////////
//                    Repository Events             //
//////////////////////////////////////////////////////

// HandleRepositoryUpdatedEvents handles repository update event emission logic
func (h *EventHandler) HandleRepositoryUpdatedEvents(ctx context.Context, resourceKind api.ResourceKind, orgId uuid.UUID, name string, oldResource, newResource interface{}, created bool, err error) {
	if err != nil {
		status := StoreErrorToApiStatus(err, created, api.RepositoryKind, &name)
		h.CreateEvent(ctx, common.GetResourceCreatedOrUpdatedFailureEvent(ctx, created, api.RepositoryKind, name, status, nil))
		return
	}

	var (
		oldRepository, newRepository *api.Repository
		ok                           bool
	)
	if oldRepository, newRepository, ok = castResources[api.Repository](oldResource, newResource); !ok {
		return
	}

	// Emit success event for create/update
	if created {
		h.CreateEvent(ctx, common.GetResourceCreatedOrUpdatedSuccessEvent(ctx, created, api.RepositoryKind, name, nil, h.log))
	} else if oldRepository != nil && newRepository != nil {
		// Check if the Accessible condition changed
		var oldConditions, newConditions []api.Condition
		if oldRepository.Status != nil {
			oldConditions = oldRepository.Status.Conditions
		}
		if newRepository.Status != nil {
			newConditions = newRepository.Status.Conditions
		}

		oldAccessible := api.FindStatusCondition(oldConditions, api.ConditionTypeRepositoryAccessible)
		newAccessible := api.FindStatusCondition(newConditions, api.ConditionTypeRepositoryAccessible)

		if hasConditionChanged(oldAccessible, newAccessible) {
			if api.IsStatusConditionTrue(newConditions, api.ConditionTypeRepositoryAccessible) {
				h.CreateEvent(ctx, common.GetRepositoryAccessibleEvent(ctx, name))
			} else {
				message := "Repository access failed"
				if newAccessible != nil && newAccessible.Message != "" {
					message = newAccessible.Message
				}
				h.CreateEvent(ctx, common.GetRepositoryInaccessibleEvent(ctx, name, message))
			}
		}

		// Also emit the standard update event
		h.CreateEvent(ctx, common.GetResourceCreatedOrUpdatedSuccessEvent(ctx, created, api.RepositoryKind, name, nil, h.log))
	}
}

//////////////////////////////////////////////////////
//               EnrollmentRequest Events           //
//////////////////////////////////////////////////////

// HandleEnrollmentRequestApprovedEvents handles enrollment request approval event emission logic
func (h *EventHandler) HandleEnrollmentRequestApprovedEvents(ctx context.Context, resourceKind api.ResourceKind, _ uuid.UUID, name string, oldResource, newResource interface{}, created bool, err error) {
	if err != nil {
		status := StoreErrorToApiStatus(err, created, string(resourceKind), &name)
		h.CreateEvent(ctx, common.GetEnrollmentRequestApprovalFailedEvent(ctx, name, status, h.log))
	} else {
		// For enrollment request approval, we always emit the approved event on successful update
		// since this callback is only called when the approval process succeeds
		h.CreateEvent(ctx, common.GetEnrollmentRequestApprovedEvent(ctx, name, h.log))
	}
}

//////////////////////////////////////////////////////
//                 ResourceSync Events              //
//////////////////////////////////////////////////////

// HandleResourceSyncUpdatedEvents handles all resource sync-related event emission logic
func (h *EventHandler) HandleResourceSyncUpdatedEvents(ctx context.Context, resourceKind api.ResourceKind, orgId uuid.UUID, name string, oldResource, newResource interface{}, created bool, err error) {
	if err != nil {
		status := StoreErrorToApiStatus(err, created, string(resourceKind), &name)
		h.CreateEvent(ctx, common.GetResourceCreatedOrUpdatedFailureEvent(ctx, created, resourceKind, name, status, nil))
		return
	}

	var (
		oldResourceSync, newResourceSync *api.ResourceSync
		ok                               bool
	)
	if oldResourceSync, newResourceSync, ok = castResources[api.ResourceSync](oldResource, newResource); !ok {
		return
	}

	// Emit success event for create/update
	if created {
		h.CreateEvent(ctx, common.GetResourceCreatedOrUpdatedSuccessEvent(ctx, created, resourceKind, name, nil, h.log))
	} else if oldResourceSync != nil && newResourceSync != nil {
		updateDetails := h.computeResourceSyncResourceUpdatedDetails(oldResourceSync, newResourceSync)
		h.CreateEvent(ctx, common.GetResourceCreatedOrUpdatedSuccessEvent(ctx, created, resourceKind, name, updateDetails, h.log))
	}

	// Emit condition-specific events
	h.emitResourceSyncConditionEvents(ctx, name, oldResourceSync, newResourceSync)
}

func (h *EventHandler) computeResourceSyncResourceUpdatedDetails(oldResourceSync, newResourceSync *api.ResourceSync) *api.ResourceUpdatedDetails {
	if oldResourceSync == nil || newResourceSync == nil {
		return nil
	}

	updateDetails := &api.ResourceUpdatedDetails{
		UpdatedFields: []api.ResourceUpdatedDetailsUpdatedFields{},
	}

	// Check if spec changed
	if oldResourceSync.Metadata.Generation != nil && newResourceSync.Metadata.Generation != nil &&
		*oldResourceSync.Metadata.Generation != *newResourceSync.Metadata.Generation {
		updateDetails.UpdatedFields = append(updateDetails.UpdatedFields, api.Spec)
	}

	// Note: Status changes are not tracked in ResourceUpdatedDetails

	if len(updateDetails.UpdatedFields) == 0 {
		return nil
	}

	return updateDetails
}

func (h *EventHandler) emitResourceSyncConditionEvents(ctx context.Context, name string, oldResourceSync, newResourceSync *api.ResourceSync) {
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
		h.CreateEvent(ctx, common.GetResourceSyncCommitDetectedEvent(ctx, name, newCommit))
	}

	// Check for condition changes
	var oldConditions, newConditions []api.Condition
	if oldResourceSync.Status != nil {
		oldConditions = oldResourceSync.Status.Conditions
	}
	if newResourceSync.Status != nil {
		newConditions = newResourceSync.Status.Conditions
	}

	// Accessible condition
	oldAccessible := api.FindStatusCondition(oldConditions, api.ConditionTypeResourceSyncAccessible)
	newAccessible := api.FindStatusCondition(newConditions, api.ConditionTypeResourceSyncAccessible)
	if hasConditionChanged(oldAccessible, newAccessible) {
		if api.IsStatusConditionTrue(newConditions, api.ConditionTypeResourceSyncAccessible) {
			h.CreateEvent(ctx, common.GetResourceSyncAccessibleEvent(ctx, name))
		} else {
			message := "Repository access failed"
			if newAccessible != nil && newAccessible.Message != "" {
				message = newAccessible.Message
			}
			h.CreateEvent(ctx, common.GetResourceSyncInaccessibleEvent(ctx, name, message))
		}
	}

	// ResourceParsed condition
	oldParsed := api.FindStatusCondition(oldConditions, api.ConditionTypeResourceSyncResourceParsed)
	newParsed := api.FindStatusCondition(newConditions, api.ConditionTypeResourceSyncResourceParsed)
	if hasConditionChanged(oldParsed, newParsed) {
		if api.IsStatusConditionTrue(newConditions, api.ConditionTypeResourceSyncResourceParsed) {
			h.CreateEvent(ctx, common.GetResourceSyncParsedEvent(ctx, name))
		} else {
			message := "Resource parsing failed"
			if newParsed != nil && newParsed.Message != "" {
				message = newParsed.Message
			}
			h.CreateEvent(ctx, common.GetResourceSyncParsingFailedEvent(ctx, name, message))
		}
	}

	// Synced condition
	oldSynced := api.FindStatusCondition(oldConditions, api.ConditionTypeResourceSyncSynced)
	newSynced := api.FindStatusCondition(newConditions, api.ConditionTypeResourceSyncSynced)
	if hasConditionChanged(oldSynced, newSynced) {
		if api.IsStatusConditionTrue(newConditions, api.ConditionTypeResourceSyncSynced) {
			h.CreateEvent(ctx, common.GetResourceSyncSyncedEvent(ctx, name))
		} else {
			message := "Resource sync failed"
			if newSynced != nil && newSynced.Message != "" {
				message = newSynced.Message
			}
			h.CreateEvent(ctx, common.GetResourceSyncSyncFailedEvent(ctx, name, message))
		}
	}
}

//////////////////////////////////////////////////////
//                    Helper Functions              //
//////////////////////////////////////////////////////

// castResources safely casts both old and new interface{} resources to the specified type T
// Returns ok=true only if both resources are either nil or successfully cast to *T
func castResources[T any](oldResource, newResource interface{}) (oldTyped, newTyped *T, ok bool) {
	// Check old resource
	if oldResource != nil {
		if oldTyped, ok = oldResource.(*T); !ok {
			return nil, nil, false
		}
	}

	// Check new resource
	if newResource != nil {
		if newTyped, ok = newResource.(*T); !ok {
			return nil, nil, false
		}
	}

	return oldTyped, newTyped, true
}
