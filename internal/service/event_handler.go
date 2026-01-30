package service

import (
	"context"
	"reflect"

	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/domain"
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
func (h *EventHandler) CreateEvent(ctx context.Context, orgId uuid.UUID, event *domain.Event) {
	if event == nil {
		return
	}

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
func (h *EventHandler) HandleGenericResourceDeletedEvents(ctx context.Context, resourceKind domain.ResourceKind, orgId uuid.UUID, name string, _, _ interface{}, created bool, err error) {
	if err != nil {
		status := StoreErrorToApiStatus(err, created, string(resourceKind), &name)
		h.CreateEvent(ctx, orgId, common.GetResourceDeletedFailureEvent(ctx, resourceKind, name, status))
	} else {
		h.CreateEvent(ctx, orgId, common.GetResourceDeletedSuccessEvent(ctx, resourceKind, name))
	}
}

//////////////////////////////////////////////////////
//                    Device Events                 //
//////////////////////////////////////////////////////

// HandleDeviceUpdatedEvents handles all device-related event emission logic
func (h *EventHandler) HandleDeviceUpdatedEvents(ctx context.Context, resourceKind domain.ResourceKind, orgId uuid.UUID, name string, oldResource, newResource interface{}, created bool, err error) {
	if err != nil {
		status := StoreErrorToApiStatus(err, created, domain.DeviceKind, &name)
		h.CreateEvent(ctx, orgId, common.GetResourceCreatedOrUpdatedFailureEvent(ctx, created, domain.DeviceKind, name, status, nil))
		return
	}
	var (
		oldDevice, newDevice *domain.Device
		ok                   bool
	)
	if oldDevice, newDevice, ok = castResources[domain.Device](oldResource, newResource); !ok {
		return
	}

	// Only generate status change events when the device is not being created
	if !created {
		statusUpdates := common.ComputeDeviceStatusChanges(ctx, oldDevice, newDevice, orgId, h.store)

		// Deduplicate DeviceDisconnected events - if multiple status fields changed to Unknown,
		// only emit one DeviceDisconnected event
		deviceDisconnectedEmitted := false
		for _, update := range statusUpdates {
			if update.Reason == domain.EventReasonDeviceDisconnected {
				if !deviceDisconnectedEmitted {
					h.CreateEvent(ctx, orgId, common.GetDeviceEventFromUpdateDetails(ctx, name, update))
					deviceDisconnectedEmitted = true
				}
			} else {
				h.CreateEvent(ctx, orgId, common.GetDeviceEventFromUpdateDetails(ctx, name, update))
			}
		}
	}

	// Generate resource creation/update events
	if created {
		h.CreateEvent(ctx, orgId, common.GetResourceCreatedOrUpdatedSuccessEvent(ctx, true, domain.DeviceKind, name, nil, h.log, nil))
	} else {
		updateDetails := h.computeResourceUpdatedDetails(oldDevice.Metadata, newDevice.Metadata)
		// Generate ResourceUpdated event if there are spec changes or status changes
		if updateDetails != nil {
			annotations := map[string]string{}
			delayDeviceRender, ok := ctx.Value(consts.DelayDeviceRenderCtxKey).(bool)
			if ok && delayDeviceRender {
				annotations[domain.EventAnnotationDelayDeviceRender] = "true"
			}

			h.CreateEvent(ctx, orgId, common.GetResourceCreatedOrUpdatedSuccessEvent(ctx, false, domain.DeviceKind, name, updateDetails, h.log, annotations))
		}
	}
}

// HandleDeviceDecommissionEvents handles device decommission event emission logic
func (h *EventHandler) HandleDeviceDecommissionEvents(ctx context.Context, _ domain.ResourceKind, orgId uuid.UUID, name string, _, _ interface{}, created bool, err error) {
	if err != nil {
		status := StoreErrorToApiStatus(err, created, domain.DeviceKind, &name)
		h.CreateEvent(ctx, orgId, common.GetDeviceDecommissionedFailureEvent(ctx, created, domain.DeviceKind, name, status))
	} else {
		h.CreateEvent(ctx, orgId, common.GetDeviceDecommissionedSuccessEvent(ctx, created, domain.DeviceKind, name, nil, nil))
	}
}

//////////////////////////////////////////////////////
//                    Fleet Events                 //
//////////////////////////////////////////////////////

func (h *EventHandler) EmitFleetRolloutStartedEvent(ctx context.Context, orgId uuid.UUID, templateVersionName string, fleetName string, immediateRollout bool) {
	event := common.GetFleetRolloutStartedEvent(ctx, templateVersionName, fleetName, immediateRollout, false)
	if event != nil {
		h.CreateEvent(ctx, orgId, event)
	}
}

// HandleFleetUpdatedEvents handles all fleet-related event emission logic
func (h *EventHandler) HandleFleetUpdatedEvents(ctx context.Context, resourceKind domain.ResourceKind, orgId uuid.UUID, name string, oldResource, newResource interface{}, created bool, err error) {
	var (
		oldFleet, newFleet *domain.Fleet
		ok                 bool
		event              *domain.Event
	)
	if oldFleet, newFleet, ok = castResources[domain.Fleet](oldResource, newResource); !ok {
		return
	}

	if err != nil {
		status := StoreErrorToApiStatus(err, created, domain.FleetKind, &name)
		event = common.GetResourceCreatedOrUpdatedFailureEvent(ctx, created, domain.FleetKind, name, status, nil)
	} else {
		// Compute ResourceUpdatedDetails at service level
		var updateDetails *domain.ResourceUpdatedDetails
		if !created && oldFleet != nil && newFleet != nil {
			updateDetails = h.computeResourceUpdatedDetails(oldFleet.Metadata, newFleet.Metadata)
			// Check if spec.template or spec.selector changed - if so, remove spec from updateDetails and add spec.template or spec.selector
			if updateDetails != nil && lo.Contains(updateDetails.UpdatedFields, domain.Spec) {
				removeSpec := false
				if !reflect.DeepEqual(oldFleet.Spec.Template, newFleet.Spec.Template) {
					updateDetails.UpdatedFields = append(updateDetails.UpdatedFields, domain.SpecTemplate)
					removeSpec = true
				}
				if !reflect.DeepEqual(oldFleet.Spec.Selector, newFleet.Spec.Selector) {
					updateDetails.UpdatedFields = append(updateDetails.UpdatedFields, domain.SpecSelector)
					removeSpec = true
				}
				if removeSpec {
					updateDetails.UpdatedFields = lo.Filter(updateDetails.UpdatedFields, func(field domain.ResourceUpdatedDetailsUpdatedFields, _ int) bool {
						return field != domain.Spec
					})
				}
			}
		}
		event = common.GetResourceCreatedOrUpdatedSuccessEvent(ctx, created, domain.FleetKind, name, updateDetails, h.log, nil)
	}

	// Emit a created/updated event (if nil, no event is emitted)
	h.CreateEvent(ctx, orgId, event)

	// Emit fleet validation events if applicable
	h.emitFleetValidEvents(ctx, orgId, name, oldFleet, newFleet)

	// Guard against nil newFleet (e.g., in delete operations)
	if newFleet == nil {
		return
	}

	deployingTemplateVersion, exists := newFleet.GetAnnotation(domain.FleetAnnotationDeployingTemplateVersion)
	if !exists {
		return
	}

	// Emit fleet rollout events if applicable
	h.emitFleetRolloutNewEvent(ctx, orgId, name, deployingTemplateVersion, oldFleet, newFleet)
	h.emitFleetRolloutBatchCompletedEvent(ctx, orgId, name, deployingTemplateVersion, oldFleet, newFleet)
	h.emitFleetRolloutCompletedEvent(ctx, orgId, name, deployingTemplateVersion, oldFleet, newFleet)
	h.emitFleetRolloutFailedEvent(ctx, orgId, name, deployingTemplateVersion, oldFleet, newFleet)
}

func (h *EventHandler) emitFleetRolloutNewEvent(ctx context.Context, orgId uuid.UUID, name string, deployingTemplateVersion string, oldFleet, newFleet *domain.Fleet) {
	if newFleet == nil {
		return
	}
	if !newFleet.IsRolloutNew(oldFleet) {
		return
	}
	h.CreateEvent(ctx, orgId, common.GetFleetRolloutNewEvent(ctx, name))
}

func (h *EventHandler) emitFleetRolloutBatchCompletedEvent(ctx context.Context, orgId uuid.UUID, name string, deployingTemplateVersion string, oldFleet, newFleet *domain.Fleet) {
	if newFleet == nil {
		return
	}
	batchCompleted, report := newFleet.IsRolloutBatchCompleted(oldFleet)
	if !batchCompleted {
		return
	}
	h.CreateEvent(ctx, orgId, common.GetFleetRolloutBatchCompletedEvent(ctx, name, deployingTemplateVersion, report))

	if report.BatchName == domain.FinalImplicitBatchName {
		h.CreateEvent(ctx, orgId, common.GetFleetRolloutCompletedEvent(ctx, name, deployingTemplateVersion))
	}
}

func (h *EventHandler) emitFleetValidEvents(ctx context.Context, orgId uuid.UUID, name string, oldFleet, newFleet *domain.Fleet) {
	if newFleet == nil || newFleet.Status == nil {
		return
	}

	// Get old and new conditions
	var oldConditions []domain.Condition
	if oldFleet != nil && oldFleet.Status != nil {
		oldConditions = oldFleet.Status.Conditions
	}
	newConditions := newFleet.Status.Conditions

	oldCondition := domain.FindStatusCondition(oldConditions, domain.ConditionTypeFleetValid)
	newCondition := domain.FindStatusCondition(newConditions, domain.ConditionTypeFleetValid)

	if newCondition == nil {
		return
	}

	// Check if the condition has changed
	if !hasConditionChanged(oldCondition, newCondition) {
		return
	}

	// Emit events based on the condition status
	if newCondition.Status == domain.ConditionStatusTrue {
		h.CreateEvent(ctx, orgId, common.GetFleetSpecValidEvent(ctx, name))
	} else {
		// Fleet became invalid
		message := "Unknown"
		if newCondition.Message != "" {
			message = newCondition.Message
		}
		h.CreateEvent(ctx, orgId, common.GetFleetSpecInvalidEvent(ctx, name, message))
	}
}

func (h *EventHandler) emitFleetRolloutCompletedEvent(ctx context.Context, orgId uuid.UUID, name string, deployingTemplateVersion string, oldFleet, newFleet *domain.Fleet) {
	if newFleet == nil || newFleet.Status == nil {
		return
	}
	newCondition := domain.FindStatusCondition(newFleet.Status.Conditions, domain.ConditionTypeFleetRolloutInProgress)
	if newCondition == nil || newCondition.Reason != domain.RolloutInactiveReason {
		return
	}
	var oldConditions []domain.Condition
	if oldFleet != nil && oldFleet.Status != nil {
		oldConditions = oldFleet.Status.Conditions
	}
	oldCondition := domain.FindStatusCondition(oldConditions, domain.ConditionTypeFleetRolloutInProgress)
	if oldCondition != nil && oldCondition.Reason == domain.RolloutInactiveReason {
		return
	}

	h.CreateEvent(ctx, orgId, common.GetFleetRolloutCompletedEvent(ctx, name, deployingTemplateVersion))
}

func (h *EventHandler) emitFleetRolloutFailedEvent(ctx context.Context, orgId uuid.UUID, name string, deployingTemplateVersion string, oldFleet, newFleet *domain.Fleet) {
	if newFleet == nil || newFleet.Status == nil {
		return
	}
	newCondition := domain.FindStatusCondition(newFleet.Status.Conditions, domain.ConditionTypeFleetRolloutInProgress)
	if newCondition == nil || newCondition.Reason != domain.RolloutSuspendedReason {
		return
	}
	var oldConditions []domain.Condition
	if oldFleet != nil && oldFleet.Status != nil {
		oldConditions = oldFleet.Status.Conditions
	}
	oldCondition := domain.FindStatusCondition(oldConditions, domain.ConditionTypeFleetRolloutInProgress)
	if oldCondition != nil && oldCondition.Reason == domain.RolloutSuspendedReason {
		return
	}

	h.CreateEvent(ctx, orgId, common.GetFleetRolloutFailedEvent(ctx, name, deployingTemplateVersion, newCondition.Message))
}

//////////////////////////////////////////////////////
//                    Repository Events             //
//////////////////////////////////////////////////////

// HandleRepositoryUpdatedEvents handles repository update event emission logic
func (h *EventHandler) HandleRepositoryUpdatedEvents(ctx context.Context, resourceKind domain.ResourceKind, orgId uuid.UUID, name string, oldResource, newResource interface{}, created bool, err error) {
	if err != nil {
		status := StoreErrorToApiStatus(err, created, domain.RepositoryKind, &name)
		h.CreateEvent(ctx, orgId, common.GetResourceCreatedOrUpdatedFailureEvent(ctx, created, domain.RepositoryKind, name, status, nil))
		return
	}

	var (
		oldRepository, newRepository *domain.Repository
		ok                           bool
	)
	if oldRepository, newRepository, ok = castResources[domain.Repository](oldResource, newResource); !ok {
		return
	}

	// Emit success event for create/update
	if created {
		h.CreateEvent(ctx, orgId, common.GetResourceCreatedOrUpdatedSuccessEvent(ctx, created, domain.RepositoryKind, name, nil, h.log, nil))
	} else if oldRepository != nil && newRepository != nil {
		// Check if the Accessible condition changed
		var oldConditions, newConditions []domain.Condition
		if oldRepository.Status != nil {
			oldConditions = oldRepository.Status.Conditions
		}
		if newRepository.Status != nil {
			newConditions = newRepository.Status.Conditions
		}

		oldAccessible := domain.FindStatusCondition(oldConditions, domain.ConditionTypeRepositoryAccessible)
		newAccessible := domain.FindStatusCondition(newConditions, domain.ConditionTypeRepositoryAccessible)

		if hasConditionChanged(oldAccessible, newAccessible) {
			if domain.IsStatusConditionTrue(newConditions, domain.ConditionTypeRepositoryAccessible) {
				h.CreateEvent(ctx, orgId, common.GetRepositoryAccessibleEvent(ctx, name))
			} else {
				message := "Repository access failed"
				if newAccessible != nil && newAccessible.Message != "" {
					message = newAccessible.Message
				}
				h.CreateEvent(ctx, orgId, common.GetRepositoryInaccessibleEvent(ctx, name, message))
			}
		}

		updateDetails := h.computeResourceUpdatedDetails(oldRepository.Metadata, newRepository.Metadata)

		// Also emit the standard update event
		h.CreateEvent(ctx, orgId, common.GetResourceCreatedOrUpdatedSuccessEvent(ctx, created, domain.RepositoryKind, name, updateDetails, h.log, nil))
	}
}

//////////////////////////////////////////////////////
//                 AuthProvider Events              //
//////////////////////////////////////////////////////

// HandleAuthProviderUpdatedEvents handles auth provider update event emission logic
func (h *EventHandler) HandleAuthProviderUpdatedEvents(ctx context.Context, resourceKind domain.ResourceKind, orgId uuid.UUID, name string, oldResource, newResource interface{}, created bool, err error) {
	if err != nil {
		status := StoreErrorToApiStatus(err, created, domain.AuthProviderKind, &name)
		h.CreateEvent(ctx, orgId, common.GetResourceCreatedOrUpdatedFailureEvent(ctx, created, domain.AuthProviderKind, name, status, nil))
		return
	}

	// Emit success event for create
	if created {
		h.CreateEvent(ctx, orgId, common.GetResourceCreatedOrUpdatedSuccessEvent(ctx, created, domain.AuthProviderKind, name, nil, h.log, nil))
	} else {
		// Handle update events
		var oldAuthProvider, newAuthProvider *domain.AuthProvider
		var ok bool
		if oldAuthProvider, newAuthProvider, ok = castResources[domain.AuthProvider](oldResource, newResource); !ok {
			return
		}

		updateDetails := h.computeResourceUpdatedDetails(oldAuthProvider.Metadata, newAuthProvider.Metadata)
		// Generate ResourceUpdated event if there are spec changes
		if updateDetails != nil {
			h.CreateEvent(ctx, orgId, common.GetResourceCreatedOrUpdatedSuccessEvent(ctx, false, domain.AuthProviderKind, name, updateDetails, h.log, nil))
		}
	}
}

// HandleAuthProviderDeletedEvents handles auth provider deletion event emission logic
func (h *EventHandler) HandleAuthProviderDeletedEvents(ctx context.Context, resourceKind domain.ResourceKind, orgId uuid.UUID, name string, oldResource, newResource interface{}, created bool, err error) {
	h.HandleGenericResourceDeletedEvents(ctx, resourceKind, orgId, name, oldResource, newResource, created, err)
}

//////////////////////////////////////////////////////
//               EnrollmentRequest Events           //
//////////////////////////////////////////////////////

// HandleEnrollmentRequestUpdatedEvents handles enrollment request update event emission logic
func (h *EventHandler) HandleEnrollmentRequestUpdatedEvents(ctx context.Context, resourceKind domain.ResourceKind, orgId uuid.UUID, name string, oldResource, newResource interface{}, created bool, err error) {
	if err != nil {
		status := StoreErrorToApiStatus(err, created, string(resourceKind), &name)
		h.CreateEvent(ctx, orgId, common.GetResourceCreatedOrUpdatedFailureEvent(ctx, created, resourceKind, name, status, nil))
	} else {
		// Compute ResourceUpdatedDetails for updates
		var updateDetails *domain.ResourceUpdatedDetails
		if !created {
			var (
				oldEnrollmentRequest, newEnrollmentRequest *domain.EnrollmentRequest
				ok                                         bool
			)
			if oldEnrollmentRequest, newEnrollmentRequest, ok = castResources[domain.EnrollmentRequest](oldResource, newResource); ok && oldEnrollmentRequest != nil && newEnrollmentRequest != nil {
				updateDetails = h.computeResourceUpdatedDetails(oldEnrollmentRequest.Metadata, newEnrollmentRequest.Metadata)
			}
		}
		h.CreateEvent(ctx, orgId, common.GetResourceCreatedOrUpdatedSuccessEvent(ctx, created, resourceKind, name, updateDetails, h.log, nil))
	}
}

// HandleEnrollmentRequestApprovedEvents handles enrollment request approval event emission logic
func (h *EventHandler) HandleEnrollmentRequestApprovedEvents(ctx context.Context, resourceKind domain.ResourceKind, orgId uuid.UUID, name string, oldResource, newResource interface{}, created bool, err error) {
	if err != nil {
		status := StoreErrorToApiStatus(err, created, string(resourceKind), &name)
		h.CreateEvent(ctx, orgId, common.GetEnrollmentRequestApprovalFailedEvent(ctx, name, status, h.log))
	} else {
		// For enrollment request approval, we always emit the approved event on successful update
		// since this callback is only called when the approval process succeeds
		h.CreateEvent(ctx, orgId, common.GetEnrollmentRequestApprovedEvent(ctx, name, h.log))
	}
}

//////////////////////////////////////////////////////
//                 ResourceSync Events              //
//////////////////////////////////////////////////////

// HandleResourceSyncUpdatedEvents handles all resource sync-related event emission logic
func (h *EventHandler) HandleResourceSyncUpdatedEvents(ctx context.Context, resourceKind domain.ResourceKind, orgId uuid.UUID, name string, oldResource, newResource interface{}, created bool, err error) {
	if err != nil {
		status := StoreErrorToApiStatus(err, created, string(resourceKind), &name)
		h.CreateEvent(ctx, orgId, common.GetResourceCreatedOrUpdatedFailureEvent(ctx, created, resourceKind, name, status, nil))
		return
	}

	var (
		oldResourceSync, newResourceSync *domain.ResourceSync
		ok                               bool
	)
	if oldResourceSync, newResourceSync, ok = castResources[domain.ResourceSync](oldResource, newResource); !ok {
		return
	}

	// Emit success event for create/update
	if created {
		h.CreateEvent(ctx, orgId, common.GetResourceCreatedOrUpdatedSuccessEvent(ctx, created, resourceKind, name, nil, h.log, nil))
	} else if oldResourceSync != nil && newResourceSync != nil {
		updateDetails := h.computeResourceUpdatedDetails(oldResourceSync.Metadata, newResourceSync.Metadata)
		h.CreateEvent(ctx, orgId, common.GetResourceCreatedOrUpdatedSuccessEvent(ctx, created, resourceKind, name, updateDetails, h.log, nil))
	}

	// Emit condition-specific events
	h.emitResourceSyncConditionEvents(ctx, orgId, name, oldResourceSync, newResourceSync)
}

//////////////////////////////////////////////////////
//            CertificateSigningRequest Events      //
//////////////////////////////////////////////////////

// HandleCertificateSigningRequestUpdatedEvents handles certificate signing request update event emission logic
func (h *EventHandler) HandleCertificateSigningRequestUpdatedEvents(ctx context.Context, resourceKind domain.ResourceKind, orgId uuid.UUID, name string, oldResource, newResource interface{}, created bool, err error) {
	if err != nil {
		status := StoreErrorToApiStatus(err, created, string(resourceKind), &name)
		h.CreateEvent(ctx, orgId, common.GetResourceCreatedOrUpdatedFailureEvent(ctx, created, resourceKind, name, status, nil))
	} else {
		// Compute ResourceUpdatedDetails for updates
		var updateDetails *domain.ResourceUpdatedDetails
		if !created {
			var (
				oldCSR, newCSR *domain.CertificateSigningRequest
				ok             bool
			)
			if oldCSR, newCSR, ok = castResources[domain.CertificateSigningRequest](oldResource, newResource); ok && oldCSR != nil && newCSR != nil {
				updateDetails = h.computeResourceUpdatedDetails(oldCSR.Metadata, newCSR.Metadata)
			}
		}
		h.CreateEvent(ctx, orgId, common.GetResourceCreatedOrUpdatedSuccessEvent(ctx, created, resourceKind, name, updateDetails, h.log, nil))
	}
}

//////////////////////////////////////////////////////
//                TemplateVersion Events            //
//////////////////////////////////////////////////////

// HandleTemplateVersionUpdatedEvents handles template version update event emission logic
func (h *EventHandler) HandleTemplateVersionUpdatedEvents(ctx context.Context, resourceKind domain.ResourceKind, orgId uuid.UUID, name string, oldResource, newResource interface{}, created bool, err error) {
	if err != nil {
		status := StoreErrorToApiStatus(err, created, string(resourceKind), &name)
		h.CreateEvent(ctx, orgId, common.GetResourceCreatedOrUpdatedFailureEvent(ctx, created, resourceKind, name, status, nil))
	} else {
		// Compute ResourceUpdatedDetails for updates
		var updateDetails *domain.ResourceUpdatedDetails
		if !created {
			var (
				oldTemplateVersion, newTemplateVersion *domain.TemplateVersion
				ok                                     bool
			)
			if oldTemplateVersion, newTemplateVersion, ok = castResources[domain.TemplateVersion](oldResource, newResource); ok && oldTemplateVersion != nil && newTemplateVersion != nil {
				updateDetails = h.computeResourceUpdatedDetails(oldTemplateVersion.Metadata, newTemplateVersion.Metadata)
			}
		}
		h.CreateEvent(ctx, orgId, common.GetResourceCreatedOrUpdatedSuccessEvent(ctx, created, resourceKind, name, updateDetails, h.log, nil))
	}
}

// HandleCatalogUpdatedEvents handles catalog update event emission logic
func (h *EventHandler) HandleCatalogUpdatedEvents(ctx context.Context, resourceKind domain.ResourceKind, orgId uuid.UUID, name string, oldResource, newResource interface{}, created bool, err error) {
	if err != nil {
		status := StoreErrorToApiStatus(err, created, string(resourceKind), &name)
		h.CreateEvent(ctx, orgId, common.GetResourceCreatedOrUpdatedFailureEvent(ctx, created, resourceKind, name, status, nil))
	} else {
		// Compute ResourceUpdatedDetails for updates
		var updateDetails *domain.ResourceUpdatedDetails
		if !created {
			var (
				oldCatalog, newCatalog *domain.Catalog
				ok                     bool
			)
			if oldCatalog, newCatalog, ok = castResources[domain.Catalog](oldResource, newResource); ok && oldCatalog != nil && newCatalog != nil {
				updateDetails = h.computeResourceUpdatedDetails(oldCatalog.Metadata, newCatalog.Metadata)
			}
		}
		h.CreateEvent(ctx, orgId, common.GetResourceCreatedOrUpdatedSuccessEvent(ctx, created, resourceKind, name, updateDetails, h.log, nil))
	}
}

func (h *EventHandler) emitResourceSyncConditionEvents(ctx context.Context, orgId uuid.UUID, name string, oldResourceSync, newResourceSync *domain.ResourceSync) {
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
		h.CreateEvent(ctx, orgId, common.GetResourceSyncCommitDetectedEvent(ctx, name, newCommit))
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
	if hasConditionChanged(oldAccessible, newAccessible) {
		if domain.IsStatusConditionTrue(newConditions, domain.ConditionTypeResourceSyncAccessible) {
			h.CreateEvent(ctx, orgId, common.GetResourceSyncAccessibleEvent(ctx, name))
		} else {
			message := "Repository access failed"
			if newAccessible != nil && newAccessible.Message != "" {
				message = newAccessible.Message
			}
			h.CreateEvent(ctx, orgId, common.GetResourceSyncInaccessibleEvent(ctx, name, message))
		}
	}

	// ResourceParsed condition
	oldParsed := domain.FindStatusCondition(oldConditions, domain.ConditionTypeResourceSyncResourceParsed)
	newParsed := domain.FindStatusCondition(newConditions, domain.ConditionTypeResourceSyncResourceParsed)
	if hasConditionChanged(oldParsed, newParsed) {
		if domain.IsStatusConditionTrue(newConditions, domain.ConditionTypeResourceSyncResourceParsed) {
			h.CreateEvent(ctx, orgId, common.GetResourceSyncParsedEvent(ctx, name))
		} else {
			message := "Resource parsing failed"
			if newParsed != nil && newParsed.Message != "" {
				message = newParsed.Message
			}
			h.CreateEvent(ctx, orgId, common.GetResourceSyncParsingFailedEvent(ctx, name, message))
		}
	}

	// Synced condition
	oldSynced := domain.FindStatusCondition(oldConditions, domain.ConditionTypeResourceSyncSynced)
	newSynced := domain.FindStatusCondition(newConditions, domain.ConditionTypeResourceSyncSynced)
	if hasConditionChanged(oldSynced, newSynced) {
		if domain.IsStatusConditionTrue(newConditions, domain.ConditionTypeResourceSyncSynced) {
			h.CreateEvent(ctx, orgId, common.GetResourceSyncSyncedEvent(ctx, name))
		} else {
			// Only emit failure event if it's an actual failure, not just "NewHashDetected"
			// "NewHashDetected" is a normal state change, not a failure
			// The commit detected event is already emitted when the hash changes
			if newSynced != nil && newSynced.Reason != domain.ResourceSyncNewHashDetectedReason {
				message := "Resource sync failed"
				if newSynced.Message != "" {
					message = newSynced.Message
				}
				h.CreateEvent(ctx, orgId, common.GetResourceSyncSyncFailedEvent(ctx, name, message))
			}
		}
	}
}

//////////////////////////////////////////////////////
//                    Helper Functions              //
//////////////////////////////////////////////////////

// computeResourceUpdatedDetails determines which fields were updated by comparing old and new ObjectMeta
func (h *EventHandler) computeResourceUpdatedDetails(oldMetadata, newMetadata domain.ObjectMeta) *domain.ResourceUpdatedDetails {
	updateDetails := &domain.ResourceUpdatedDetails{
		UpdatedFields: []domain.ResourceUpdatedDetailsUpdatedFields{},
	}

	// Check if spec changed (Generation field)
	if oldMetadata.Generation != nil && newMetadata.Generation != nil &&
		*oldMetadata.Generation != *newMetadata.Generation {
		updateDetails.UpdatedFields = append(updateDetails.UpdatedFields, domain.Spec)
	}

	// Check if labels changed
	if !reflect.DeepEqual(oldMetadata.Labels, newMetadata.Labels) {
		updateDetails.UpdatedFields = append(updateDetails.UpdatedFields, domain.Labels)
	}

	// Check if owner changed
	if !util.StringsAreEqual(oldMetadata.Owner, newMetadata.Owner) {
		updateDetails.UpdatedFields = append(updateDetails.UpdatedFields, domain.Owner)
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
