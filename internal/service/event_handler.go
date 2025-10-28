package service

import (
	"context"
	"reflect"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/service/common"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/internal/worker_client"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
)

// EventHandler handles all event emission logic for the service
type EventHandler struct {
	store        store.Store
	workerClient worker_client.WorkerClient
	log          logrus.FieldLogger
}

// NewEventHandler creates a new EventHandler instance
func NewEventHandler(store store.Store, workerClient worker_client.WorkerClient, log logrus.FieldLogger) *EventHandler {
	return &EventHandler{
		store:        store,
		workerClient: workerClient,
		log:          log,
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
		h.log.Errorf("failed emitting event <%s> (%s) for %s %s/%s: %v",
			*event.Metadata.Name, event.Reason, event.InvolvedObject.Kind, orgId, event.InvolvedObject.Name, err)
		return
	}

	if h.workerClient != nil {
		h.workerClient.EmitEvent(ctx, orgId, event)
	}
}

//////////////////////////////////////////////////////
//                    Common Events               //
//////////////////////////////////////////////////////

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

		// Deduplicate DeviceDisconnected events - if multiple status fields changed to Unknown,
		// only emit one DeviceDisconnected event
		deviceDisconnectedEmitted := false
		for _, update := range statusUpdates {
			if update.Reason == api.EventReasonDeviceDisconnected {
				if !deviceDisconnectedEmitted {
					h.CreateEvent(ctx, common.GetDeviceEventFromUpdateDetails(ctx, name, update))
					deviceDisconnectedEmitted = true
				}
			} else {
				h.CreateEvent(ctx, common.GetDeviceEventFromUpdateDetails(ctx, name, update))
			}
		}
	}

	// Generate resource creation/update events
	if created {
		h.CreateEvent(ctx, common.GetResourceCreatedOrUpdatedSuccessEvent(ctx, true, api.DeviceKind, name, nil, h.log, nil))
	} else {
		updateDetails := h.computeResourceUpdatedDetails(oldDevice.Metadata, newDevice.Metadata)
		// Generate ResourceUpdated event if there are spec changes or status changes
		if updateDetails != nil {
			annotations := map[string]string{}
			delayDeviceRender, ok := ctx.Value(consts.DelayDeviceRenderCtxKey).(bool)
			if ok && delayDeviceRender {
				annotations[api.EventAnnotationDelayDeviceRender] = "true"
			}

			h.CreateEvent(ctx, common.GetResourceCreatedOrUpdatedSuccessEvent(ctx, false, api.DeviceKind, name, updateDetails, h.log, annotations))
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
			updateDetails = h.computeResourceUpdatedDetails(oldFleet.Metadata, newFleet.Metadata)
			// Check if spec.template or spec.selector changed - if so, remove spec from updateDetails and add spec.template or spec.selector
			if updateDetails != nil && lo.Contains(updateDetails.UpdatedFields, api.Spec) {
				removeSpec := false
				if !reflect.DeepEqual(oldFleet.Spec.Template, newFleet.Spec.Template) {
					updateDetails.UpdatedFields = append(updateDetails.UpdatedFields, api.SpecTemplate)
					removeSpec = true
				}
				if !reflect.DeepEqual(oldFleet.Spec.Selector, newFleet.Spec.Selector) {
					updateDetails.UpdatedFields = append(updateDetails.UpdatedFields, api.SpecSelector)
					removeSpec = true
				}
				if removeSpec {
					updateDetails.UpdatedFields = lo.Filter(updateDetails.UpdatedFields, func(field api.ResourceUpdatedDetailsUpdatedFields, _ int) bool {
						return field != api.Spec
					})
				}
			}
		}
		event = common.GetResourceCreatedOrUpdatedSuccessEvent(ctx, created, api.FleetKind, name, updateDetails, h.log, nil)
	}

	// Emit a created/updated event (if nil, no event is emitted)
	h.CreateEvent(ctx, event)

	// Emit fleet validation events if applicable
	h.emitFleetValidEvents(ctx, name, oldFleet, newFleet)

	deployingTemplateVersion, exists := newFleet.GetAnnotation(api.FleetAnnotationDeployingTemplateVersion)
	if !exists {
		return
	}

	// Emit fleet rollout events if applicable
	h.emitFleetRolloutNewEvent(ctx, name, deployingTemplateVersion, oldFleet, newFleet)
	h.emitFleetRolloutBatchCompletedEvent(ctx, name, deployingTemplateVersion, oldFleet, newFleet)
	h.emitFleetRolloutCompletedEvent(ctx, name, deployingTemplateVersion, oldFleet, newFleet)
	h.emitFleetRolloutFailedEvent(ctx, name, deployingTemplateVersion, oldFleet, newFleet)
}

func (h *EventHandler) emitFleetRolloutNewEvent(ctx context.Context, name string, deployingTemplateVersion string, oldFleet, newFleet *api.Fleet) {
	if !newFleet.IsRolloutNew(oldFleet) {
		return
	}
	h.CreateEvent(ctx, common.GetFleetRolloutNewEvent(ctx, name))
}

func (h *EventHandler) emitFleetRolloutBatchCompletedEvent(ctx context.Context, name string, deployingTemplateVersion string, oldFleet, newFleet *api.Fleet) {
	batchCompleted, report := newFleet.IsRolloutBatchCompleted(oldFleet)
	if !batchCompleted {
		return
	}
	h.CreateEvent(ctx, common.GetFleetRolloutBatchCompletedEvent(ctx, name, deployingTemplateVersion, report))

	if report.BatchName == api.FinalImplicitBatchName {
		h.CreateEvent(ctx, common.GetFleetRolloutCompletedEvent(ctx, name, deployingTemplateVersion))
	}
}

func (h *EventHandler) emitFleetValidEvents(ctx context.Context, name string, oldFleet, newFleet *api.Fleet) {
	if newFleet.Status == nil {
		return
	}

	// Get old and new conditions
	var oldConditions []api.Condition
	if oldFleet != nil && oldFleet.Status != nil {
		oldConditions = oldFleet.Status.Conditions
	}
	newConditions := newFleet.Status.Conditions

	oldCondition := api.FindStatusCondition(oldConditions, api.ConditionTypeFleetValid)
	newCondition := api.FindStatusCondition(newConditions, api.ConditionTypeFleetValid)

	if newCondition == nil {
		return
	}

	// Check if the condition has changed
	if !hasConditionChanged(oldCondition, newCondition) {
		return
	}

	// Emit events based on the condition status
	if newCondition.Status == api.ConditionStatusTrue {
		h.CreateEvent(ctx, common.GetFleetSpecValidEvent(ctx, name))
	} else {
		// Fleet became invalid
		message := "Unknown"
		if newCondition.Message != "" {
			message = newCondition.Message
		}
		h.CreateEvent(ctx, common.GetFleetSpecInvalidEvent(ctx, name, message))
	}
}

func (h *EventHandler) emitFleetRolloutCompletedEvent(ctx context.Context, name string, deployingTemplateVersion string, oldFleet, newFleet *api.Fleet) {
	if newFleet.Status == nil {
		return
	}
	newCondition := api.FindStatusCondition(newFleet.Status.Conditions, api.ConditionTypeFleetRolloutInProgress)
	if newCondition == nil || newCondition.Reason != api.RolloutInactiveReason {
		return
	}
	var oldConditions []api.Condition
	if oldFleet != nil && oldFleet.Status != nil {
		oldConditions = oldFleet.Status.Conditions
	}
	oldCondition := api.FindStatusCondition(oldConditions, api.ConditionTypeFleetRolloutInProgress)
	if oldCondition != nil && oldCondition.Reason == api.RolloutInactiveReason {
		return
	}

	h.CreateEvent(ctx, common.GetFleetRolloutCompletedEvent(ctx, name, deployingTemplateVersion))
}

func (h *EventHandler) emitFleetRolloutFailedEvent(ctx context.Context, name string, deployingTemplateVersion string, oldFleet, newFleet *api.Fleet) {
	if newFleet.Status == nil {
		return
	}
	newCondition := api.FindStatusCondition(newFleet.Status.Conditions, api.ConditionTypeFleetRolloutInProgress)
	if newCondition == nil || newCondition.Reason != api.RolloutSuspendedReason {
		return
	}
	var oldConditions []api.Condition
	if oldFleet != nil && oldFleet.Status != nil {
		oldConditions = oldFleet.Status.Conditions
	}
	oldCondition := api.FindStatusCondition(oldConditions, api.ConditionTypeFleetRolloutInProgress)
	if oldCondition != nil && oldCondition.Reason == api.RolloutSuspendedReason {
		return
	}

	h.CreateEvent(ctx, common.GetFleetRolloutFailedEvent(ctx, name, deployingTemplateVersion, newCondition.Message))
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
		h.CreateEvent(ctx, common.GetResourceCreatedOrUpdatedSuccessEvent(ctx, created, api.RepositoryKind, name, nil, h.log, nil))
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

		updateDetails := h.computeResourceUpdatedDetails(oldRepository.Metadata, newRepository.Metadata)

		// Also emit the standard update event
		h.CreateEvent(ctx, common.GetResourceCreatedOrUpdatedSuccessEvent(ctx, created, api.RepositoryKind, name, updateDetails, h.log, nil))
	}
}

//////////////////////////////////////////////////////
//                 AuthProvider Events              //
//////////////////////////////////////////////////////

// HandleAuthProviderUpdatedEvents handles auth provider update event emission logic
func (h *EventHandler) HandleAuthProviderUpdatedEvents(ctx context.Context, resourceKind api.ResourceKind, orgId uuid.UUID, name string, oldResource, newResource interface{}, created bool, err error) {
	if err != nil {
		status := StoreErrorToApiStatus(err, created, api.AuthProviderKind, &name)
		h.CreateEvent(ctx, common.GetResourceCreatedOrUpdatedFailureEvent(ctx, created, api.AuthProviderKind, name, status, nil))
		return
	}

	// Emit success event for create
	if created {
		h.CreateEvent(ctx, common.GetResourceCreatedOrUpdatedSuccessEvent(ctx, created, api.AuthProviderKind, name, nil, h.log, nil))
	} else {
		// Handle update events
		var oldAuthProvider, newAuthProvider *api.AuthProvider
		var ok bool
		if oldAuthProvider, newAuthProvider, ok = castResources[api.AuthProvider](oldResource, newResource); !ok {
			return
		}

		updateDetails := h.computeResourceUpdatedDetails(oldAuthProvider.Metadata, newAuthProvider.Metadata)
		// Generate ResourceUpdated event if there are spec changes
		if updateDetails != nil {
			h.CreateEvent(ctx, common.GetResourceCreatedOrUpdatedSuccessEvent(ctx, false, api.AuthProviderKind, name, updateDetails, h.log, nil))
		}
	}
}

// HandleAuthProviderDeletedEvents handles auth provider deletion event emission logic
func (h *EventHandler) HandleAuthProviderDeletedEvents(ctx context.Context, resourceKind api.ResourceKind, orgId uuid.UUID, name string, oldResource, newResource interface{}, created bool, err error) {
	h.HandleGenericResourceDeletedEvents(ctx, resourceKind, orgId, name, oldResource, newResource, created, err)
}

//////////////////////////////////////////////////////
//               EnrollmentRequest Events           //
//////////////////////////////////////////////////////

// HandleEnrollmentRequestUpdatedEvents handles enrollment request update event emission logic
func (h *EventHandler) HandleEnrollmentRequestUpdatedEvents(ctx context.Context, resourceKind api.ResourceKind, orgId uuid.UUID, name string, oldResource, newResource interface{}, created bool, err error) {
	if err != nil {
		status := StoreErrorToApiStatus(err, created, string(resourceKind), &name)
		h.CreateEvent(ctx, common.GetResourceCreatedOrUpdatedFailureEvent(ctx, created, resourceKind, name, status, nil))
	} else {
		// Compute ResourceUpdatedDetails for updates
		var updateDetails *api.ResourceUpdatedDetails
		if !created {
			var (
				oldEnrollmentRequest, newEnrollmentRequest *api.EnrollmentRequest
				ok                                         bool
			)
			if oldEnrollmentRequest, newEnrollmentRequest, ok = castResources[api.EnrollmentRequest](oldResource, newResource); ok && oldEnrollmentRequest != nil && newEnrollmentRequest != nil {
				updateDetails = h.computeResourceUpdatedDetails(oldEnrollmentRequest.Metadata, newEnrollmentRequest.Metadata)
			}
		}
		h.CreateEvent(ctx, common.GetResourceCreatedOrUpdatedSuccessEvent(ctx, created, resourceKind, name, updateDetails, h.log, nil))
	}
}

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
		h.CreateEvent(ctx, common.GetResourceCreatedOrUpdatedSuccessEvent(ctx, created, resourceKind, name, nil, h.log, nil))
	} else if oldResourceSync != nil && newResourceSync != nil {
		updateDetails := h.computeResourceUpdatedDetails(oldResourceSync.Metadata, newResourceSync.Metadata)
		h.CreateEvent(ctx, common.GetResourceCreatedOrUpdatedSuccessEvent(ctx, created, resourceKind, name, updateDetails, h.log, nil))
	}

	// Emit condition-specific events
	h.emitResourceSyncConditionEvents(ctx, name, oldResourceSync, newResourceSync)
}

//////////////////////////////////////////////////////
//            CertificateSigningRequest Events      //
//////////////////////////////////////////////////////

// HandleCertificateSigningRequestUpdatedEvents handles certificate signing request update event emission logic
func (h *EventHandler) HandleCertificateSigningRequestUpdatedEvents(ctx context.Context, resourceKind api.ResourceKind, orgId uuid.UUID, name string, oldResource, newResource interface{}, created bool, err error) {
	if err != nil {
		status := StoreErrorToApiStatus(err, created, string(resourceKind), &name)
		h.CreateEvent(ctx, common.GetResourceCreatedOrUpdatedFailureEvent(ctx, created, resourceKind, name, status, nil))
	} else {
		// Compute ResourceUpdatedDetails for updates
		var updateDetails *api.ResourceUpdatedDetails
		if !created {
			var (
				oldCSR, newCSR *api.CertificateSigningRequest
				ok             bool
			)
			if oldCSR, newCSR, ok = castResources[api.CertificateSigningRequest](oldResource, newResource); ok && oldCSR != nil && newCSR != nil {
				updateDetails = h.computeResourceUpdatedDetails(oldCSR.Metadata, newCSR.Metadata)
			}
		}
		h.CreateEvent(ctx, common.GetResourceCreatedOrUpdatedSuccessEvent(ctx, created, resourceKind, name, updateDetails, h.log, nil))
	}
}

//////////////////////////////////////////////////////
//                TemplateVersion Events            //
//////////////////////////////////////////////////////

// HandleTemplateVersionUpdatedEvents handles template version update event emission logic
func (h *EventHandler) HandleTemplateVersionUpdatedEvents(ctx context.Context, resourceKind api.ResourceKind, orgId uuid.UUID, name string, oldResource, newResource interface{}, created bool, err error) {
	if err != nil {
		status := StoreErrorToApiStatus(err, created, string(resourceKind), &name)
		h.CreateEvent(ctx, common.GetResourceCreatedOrUpdatedFailureEvent(ctx, created, resourceKind, name, status, nil))
	} else {
		// Compute ResourceUpdatedDetails for updates
		var updateDetails *api.ResourceUpdatedDetails
		if !created {
			var (
				oldTemplateVersion, newTemplateVersion *api.TemplateVersion
				ok                                     bool
			)
			if oldTemplateVersion, newTemplateVersion, ok = castResources[api.TemplateVersion](oldResource, newResource); ok && oldTemplateVersion != nil && newTemplateVersion != nil {
				updateDetails = h.computeResourceUpdatedDetails(oldTemplateVersion.Metadata, newTemplateVersion.Metadata)
			}
		}
		h.CreateEvent(ctx, common.GetResourceCreatedOrUpdatedSuccessEvent(ctx, created, resourceKind, name, updateDetails, h.log, nil))
	}
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

// computeResourceUpdatedDetails determines which fields were updated by comparing old and new ObjectMeta
func (h *EventHandler) computeResourceUpdatedDetails(oldMetadata, newMetadata api.ObjectMeta) *api.ResourceUpdatedDetails {
	updateDetails := &api.ResourceUpdatedDetails{
		UpdatedFields: []api.ResourceUpdatedDetailsUpdatedFields{},
	}

	// Check if spec changed (Generation field)
	if oldMetadata.Generation != nil && newMetadata.Generation != nil &&
		*oldMetadata.Generation != *newMetadata.Generation {
		updateDetails.UpdatedFields = append(updateDetails.UpdatedFields, api.Spec)
	}

	// Check if labels changed
	if !reflect.DeepEqual(oldMetadata.Labels, newMetadata.Labels) {
		updateDetails.UpdatedFields = append(updateDetails.UpdatedFields, api.Labels)
	}

	// Check if owner changed
	if !util.StringsAreEqual(oldMetadata.Owner, newMetadata.Owner) {
		updateDetails.UpdatedFields = append(updateDetails.UpdatedFields, api.Owner)
		if oldMetadata.Owner != nil {
			updateDetails.PreviousOwner = oldMetadata.Owner
		}
		if newMetadata.Owner != nil {
			updateDetails.NewOwner = newMetadata.Owner
		}
	}

	// Only return details if there were actual updates
	if len(updateDetails.UpdatedFields) == 0 {
		return nil
	}

	return updateDetails
}

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
