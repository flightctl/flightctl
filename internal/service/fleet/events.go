package fleet

import (
	"context"
	"reflect"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/service/common"
	"github.com/flightctl/flightctl/internal/service/events"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
)

// EmitFleetRolloutStartedEvent emits the FleetRolloutStarted event. Exported because
// internal/service/templateversion triggers a fleet rollout as part of deploying a template
// version and needs to emit this fleet-owned event without depending on a shared events hub
// for fleet-specific decisions.
func EmitFleetRolloutStartedEvent(ctx context.Context, eventsService events.Service, orgId uuid.UUID, templateVersionName string, fleetName string, immediateRollout bool) {
	event := common.GetFleetRolloutStartedEvent(ctx, templateVersionName, fleetName, immediateRollout, false)
	if event != nil {
		eventsService.CreateEvent(ctx, orgId, event)
	}
}

// EmitFleetUpdatedEvent handles all fleet-related event emission logic for a fleet
// create/update/delete.
func EmitFleetUpdatedEvent(ctx context.Context, eventsService events.Service, log logrus.FieldLogger, resourceKind domain.ResourceKind, orgId uuid.UUID, name string, oldResource, newResource interface{}, created bool, err error) {
	var (
		oldFleet, newFleet *domain.Fleet
		ok                 bool
		event              *domain.Event
	)
	if oldFleet, newFleet, ok = common.CastResources[domain.Fleet](oldResource, newResource); !ok {
		return
	}

	if err != nil {
		status := common.StoreErrorToApiStatus(err, created, domain.FleetKind, &name)
		event = common.GetResourceCreatedOrUpdatedFailureEvent(ctx, created, domain.FleetKind, name, status, nil)
	} else {
		// Compute ResourceUpdatedDetails at service level
		var updateDetails *domain.ResourceUpdatedDetails
		if !created && oldFleet != nil && newFleet != nil {
			updateDetails = common.ComputeResourceUpdatedDetails(oldFleet.Metadata, newFleet.Metadata)
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
		event = common.GetResourceCreatedOrUpdatedSuccessEvent(ctx, created, domain.FleetKind, name, updateDetails, log, nil)
	}

	// Emit a created/updated event (if nil, no event is emitted)
	eventsService.CreateEvent(ctx, orgId, event)

	// Emit fleet validation events if applicable
	emitFleetValidEvents(ctx, eventsService, orgId, name, oldFleet, newFleet)

	// Guard against nil newFleet (e.g., in delete operations)
	if newFleet == nil {
		return
	}

	deployingTemplateVersion, exists := newFleet.GetAnnotation(domain.FleetAnnotationDeployingTemplateVersion)
	if !exists {
		return
	}

	// Emit fleet rollout events if applicable
	emitFleetRolloutNewEvent(ctx, eventsService, orgId, name, oldFleet, newFleet)
	emitFleetRolloutBatchCompletedEvent(ctx, eventsService, orgId, name, deployingTemplateVersion, oldFleet, newFleet)
	emitFleetRolloutCompletedEvent(ctx, eventsService, orgId, name, deployingTemplateVersion, oldFleet, newFleet)
	emitFleetRolloutFailedEvent(ctx, eventsService, orgId, name, deployingTemplateVersion, oldFleet, newFleet)
}

func emitFleetRolloutNewEvent(ctx context.Context, eventsService events.Service, orgId uuid.UUID, name string, oldFleet, newFleet *domain.Fleet) {
	if newFleet == nil {
		return
	}
	if !newFleet.IsRolloutNew(oldFleet) {
		return
	}
	eventsService.CreateEvent(ctx, orgId, common.GetFleetRolloutNewEvent(ctx, name))
}

func emitFleetRolloutBatchCompletedEvent(ctx context.Context, eventsService events.Service, orgId uuid.UUID, name string, deployingTemplateVersion string, oldFleet, newFleet *domain.Fleet) {
	if newFleet == nil {
		return
	}
	batchCompleted, report := newFleet.IsRolloutBatchCompleted(oldFleet)
	if !batchCompleted {
		return
	}
	eventsService.CreateEvent(ctx, orgId, common.GetFleetRolloutBatchCompletedEvent(ctx, name, deployingTemplateVersion, report))

	if report.BatchName == domain.FinalImplicitBatchName {
		eventsService.CreateEvent(ctx, orgId, common.GetFleetRolloutCompletedEvent(ctx, name, deployingTemplateVersion))
	}
}

func emitFleetValidEvents(ctx context.Context, eventsService events.Service, orgId uuid.UUID, name string, oldFleet, newFleet *domain.Fleet) {
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
	if !common.HasConditionChanged(oldCondition, newCondition) {
		return
	}

	// Emit events based on the condition status
	if newCondition.Status == domain.ConditionStatusTrue {
		eventsService.CreateEvent(ctx, orgId, common.GetFleetSpecValidEvent(ctx, name))
	} else {
		// Fleet became invalid
		message := "Unknown"
		if newCondition.Message != "" {
			message = newCondition.Message
		}
		eventsService.CreateEvent(ctx, orgId, common.GetFleetSpecInvalidEvent(ctx, name, message))
	}
}

func emitFleetRolloutCompletedEvent(ctx context.Context, eventsService events.Service, orgId uuid.UUID, name string, deployingTemplateVersion string, oldFleet, newFleet *domain.Fleet) {
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

	eventsService.CreateEvent(ctx, orgId, common.GetFleetRolloutCompletedEvent(ctx, name, deployingTemplateVersion))
}

func emitFleetRolloutFailedEvent(ctx context.Context, eventsService events.Service, orgId uuid.UUID, name string, deployingTemplateVersion string, oldFleet, newFleet *domain.Fleet) {
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

	eventsService.CreateEvent(ctx, orgId, common.GetFleetRolloutFailedEvent(ctx, name, deployingTemplateVersion, newCondition.Message))
}
