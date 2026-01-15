package domain

import (
	"context"

	v1beta1 "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
	"github.com/samber/lo"
)

// ========== Resource Types ==========

type Event = v1beta1.Event
type EventList = v1beta1.EventList
type EventSource = v1beta1.EventSource
type EventDetails = v1beta1.EventDetails

// ========== Event Enums ==========

type EventReason = v1beta1.EventReason
type EventType = v1beta1.EventType

// Event type constants
const (
	EventTypeNormal  = v1beta1.Normal
	EventTypeWarning = v1beta1.Warning

	// Direct aliases for compatibility
	Normal  = v1beta1.Normal
	Warning = v1beta1.Warning
)

// Event reason constants
const (
	EventReasonDeviceApplicationDegraded       = v1beta1.EventReasonDeviceApplicationDegraded
	EventReasonDeviceApplicationError          = v1beta1.EventReasonDeviceApplicationError
	EventReasonDeviceApplicationHealthy        = v1beta1.EventReasonDeviceApplicationHealthy
	EventReasonDeviceCPUCritical               = v1beta1.EventReasonDeviceCPUCritical
	EventReasonDeviceCPUNormal                 = v1beta1.EventReasonDeviceCPUNormal
	EventReasonDeviceCPUWarning                = v1beta1.EventReasonDeviceCPUWarning
	EventReasonDeviceConflictPaused            = v1beta1.EventReasonDeviceConflictPaused
	EventReasonDeviceConflictResolved          = v1beta1.EventReasonDeviceConflictResolved
	EventReasonDeviceConnected                 = v1beta1.EventReasonDeviceConnected
	EventReasonDeviceContentOutOfDate          = v1beta1.EventReasonDeviceContentOutOfDate
	EventReasonDeviceContentUpToDate           = v1beta1.EventReasonDeviceContentUpToDate
	EventReasonDeviceContentUpdating           = v1beta1.EventReasonDeviceContentUpdating
	EventReasonDeviceDecommissionFailed        = v1beta1.EventReasonDeviceDecommissionFailed
	EventReasonDeviceDecommissioned            = v1beta1.EventReasonDeviceDecommissioned
	EventReasonDeviceDisconnected              = v1beta1.EventReasonDeviceDisconnected
	EventReasonDeviceDiskCritical              = v1beta1.EventReasonDeviceDiskCritical
	EventReasonDeviceDiskNormal                = v1beta1.EventReasonDeviceDiskNormal
	EventReasonDeviceDiskWarning               = v1beta1.EventReasonDeviceDiskWarning
	EventReasonDeviceIsRebooting               = v1beta1.EventReasonDeviceIsRebooting
	EventReasonDeviceMemoryCritical            = v1beta1.EventReasonDeviceMemoryCritical
	EventReasonDeviceMemoryNormal              = v1beta1.EventReasonDeviceMemoryNormal
	EventReasonDeviceMemoryWarning             = v1beta1.EventReasonDeviceMemoryWarning
	EventReasonDeviceMultipleOwnersDetected    = v1beta1.EventReasonDeviceMultipleOwnersDetected
	EventReasonDeviceMultipleOwnersResolved    = v1beta1.EventReasonDeviceMultipleOwnersResolved
	EventReasonDeviceSpecInvalid               = v1beta1.EventReasonDeviceSpecInvalid
	EventReasonDeviceSpecValid                 = v1beta1.EventReasonDeviceSpecValid
	EventReasonDeviceUpdateFailed              = v1beta1.EventReasonDeviceUpdateFailed
	EventReasonEnrollmentRequestApprovalFailed = v1beta1.EventReasonEnrollmentRequestApprovalFailed
	EventReasonEnrollmentRequestApproved       = v1beta1.EventReasonEnrollmentRequestApproved
	EventReasonFleetInvalid                    = v1beta1.EventReasonFleetInvalid
	EventReasonFleetRolloutBatchCompleted      = v1beta1.EventReasonFleetRolloutBatchCompleted
	EventReasonFleetRolloutBatchDispatched     = v1beta1.EventReasonFleetRolloutBatchDispatched
	EventReasonFleetRolloutCompleted           = v1beta1.EventReasonFleetRolloutCompleted
	EventReasonFleetRolloutCreated             = v1beta1.EventReasonFleetRolloutCreated
	EventReasonFleetRolloutDeviceSelected      = v1beta1.EventReasonFleetRolloutDeviceSelected
	EventReasonFleetRolloutFailed              = v1beta1.EventReasonFleetRolloutFailed
	EventReasonFleetRolloutStarted             = v1beta1.EventReasonFleetRolloutStarted
	EventReasonFleetValid                      = v1beta1.EventReasonFleetValid
	EventReasonInternalTaskFailed              = v1beta1.EventReasonInternalTaskFailed
	EventReasonInternalTaskPermanentlyFailed   = v1beta1.EventReasonInternalTaskPermanentlyFailed
	EventReasonReferencedRepositoryUpdated     = v1beta1.EventReasonReferencedRepositoryUpdated
	EventReasonRepositoryAccessible            = v1beta1.EventReasonRepositoryAccessible
	EventReasonRepositoryInaccessible          = v1beta1.EventReasonRepositoryInaccessible
	EventReasonResourceCreated                 = v1beta1.EventReasonResourceCreated
	EventReasonResourceCreationFailed          = v1beta1.EventReasonResourceCreationFailed
	EventReasonResourceDeleted                 = v1beta1.EventReasonResourceDeleted
	EventReasonResourceDeletionFailed          = v1beta1.EventReasonResourceDeletionFailed
	EventReasonResourceSyncAccessible          = v1beta1.EventReasonResourceSyncAccessible
	EventReasonResourceSyncCommitDetected      = v1beta1.EventReasonResourceSyncCommitDetected
	EventReasonResourceSyncInaccessible        = v1beta1.EventReasonResourceSyncInaccessible
	EventReasonResourceSyncParsed              = v1beta1.EventReasonResourceSyncParsed
	EventReasonResourceSyncParsingFailed       = v1beta1.EventReasonResourceSyncParsingFailed
	EventReasonResourceSyncSyncFailed          = v1beta1.EventReasonResourceSyncSyncFailed
	EventReasonResourceSyncSynced              = v1beta1.EventReasonResourceSyncSynced
	EventReasonResourceUpdateFailed            = v1beta1.EventReasonResourceUpdateFailed
	EventReasonResourceUpdated                 = v1beta1.EventReasonResourceUpdated
	EventReasonSystemRestored                  = v1beta1.EventReasonSystemRestored
)

// ========== Event Details Types ==========

type InternalTaskFailedDetails = v1beta1.InternalTaskFailedDetails
type InternalTaskFailedDetailsDetailType = v1beta1.InternalTaskFailedDetailsDetailType
type InternalTaskPermanentlyFailedDetails = v1beta1.InternalTaskPermanentlyFailedDetails
type InternalTaskPermanentlyFailedDetailsDetailType = v1beta1.InternalTaskPermanentlyFailedDetailsDetailType
type ReferencedRepositoryUpdatedDetails = v1beta1.ReferencedRepositoryUpdatedDetails
type ReferencedRepositoryUpdatedDetailsDetailType = v1beta1.ReferencedRepositoryUpdatedDetailsDetailType
type ResourceUpdatedDetails = v1beta1.ResourceUpdatedDetails
type ResourceUpdatedDetailsDetailType = v1beta1.ResourceUpdatedDetailsDetailType
type ResourceUpdatedDetailsUpdatedFields = v1beta1.ResourceUpdatedDetailsUpdatedFields

const (
	InternalTaskFailed            = v1beta1.InternalTaskFailed
	InternalTaskPermanentlyFailed = v1beta1.InternalTaskPermanentlyFailed
	ReferencedRepositoryUpdated   = v1beta1.ReferencedRepositoryUpdated
	ResourceUpdated               = v1beta1.ResourceUpdated

	// Updated field constants with prefix (descriptive)
	UpdatedFieldLabels       = v1beta1.Labels
	UpdatedFieldOwner        = v1beta1.Owner
	UpdatedFieldSpec         = v1beta1.Spec
	UpdatedFieldSpecSelector = v1beta1.SpecSelector
	UpdatedFieldSpecTemplate = v1beta1.SpecTemplate

	// Direct aliases for compatibility
	Labels       = v1beta1.Labels
	Owner        = v1beta1.Owner
	Spec         = v1beta1.Spec
	SpecSelector = v1beta1.SpecSelector
	SpecTemplate = v1beta1.SpecTemplate
)

// ========== Utility Functions ==========

// warningReasons contains all event reasons that should result in Warning events
var warningReasons = map[EventReason]struct{}{
	EventReasonResourceCreationFailed:          {},
	EventReasonResourceUpdateFailed:            {},
	EventReasonResourceDeletionFailed:          {},
	EventReasonDeviceDecommissionFailed:        {},
	EventReasonEnrollmentRequestApprovalFailed: {},
	EventReasonDeviceApplicationDegraded:       {},
	EventReasonDeviceApplicationError:          {},
	EventReasonDeviceCPUCritical:               {},
	EventReasonDeviceCPUWarning:                {},
	EventReasonDeviceMemoryCritical:            {},
	EventReasonDeviceMemoryWarning:             {},
	EventReasonDeviceDiskCritical:              {},
	EventReasonDeviceDiskWarning:               {},
	EventReasonDeviceDisconnected:              {},
	EventReasonDeviceConflictPaused:            {},
	EventReasonDeviceSpecInvalid:               {},
	EventReasonFleetInvalid:                    {},
	EventReasonDeviceMultipleOwnersDetected:    {},
	EventReasonDeviceUpdateFailed:              {},
	EventReasonInternalTaskFailed:              {},
	EventReasonInternalTaskPermanentlyFailed:   {},
	EventReasonResourceSyncInaccessible:        {},
	EventReasonResourceSyncParsingFailed:       {},
	EventReasonResourceSyncSyncFailed:          {},
	EventReasonFleetRolloutFailed:              {},
}

// GetEventType determines the event type based on the event reason
func GetEventType(reason EventReason) EventType {
	if _, contains := warningReasons[reason]; contains {
		return EventTypeWarning
	}
	return EventTypeNormal
}

// GetBaseEvent creates a base event with common fields
func GetBaseEvent(ctx context.Context, resourceKind ResourceKind, resourceName string, reason EventReason, message string, details *EventDetails) *Event {
	var actorStr string
	if actor := ctx.Value(consts.EventActorCtxKey); actor != nil {
		actorStr = actor.(string)
	}

	var componentStr string
	if component := ctx.Value(consts.EventSourceComponentCtxKey); component != nil {
		componentStr = component.(string)
	}

	// Generate a UUID for the event name to ensure k8s compliance
	eventName := uuid.New().String()

	event := Event{
		Metadata: ObjectMeta{
			Name: lo.ToPtr(eventName),
		},
		InvolvedObject: ObjectReference{
			Kind: string(resourceKind),
			Name: resourceName,
		},
		Source: EventSource{
			Component: componentStr,
		},
		Actor: actorStr,
	}

	// Add request ID to the event for correlation
	if reqID := ctx.Value(middleware.RequestIDKey); reqID != nil {
		event.Metadata.Annotations = &map[string]string{EventAnnotationRequestID: reqID.(string)}
	}

	event.Reason = reason
	event.Message = message
	event.Type = GetEventType(reason)
	event.Details = details

	return &event
}
