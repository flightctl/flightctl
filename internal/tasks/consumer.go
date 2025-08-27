package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/instrumentation/tracing"
	"github.com/flightctl/flightctl/internal/kvstore"
	"github.com/flightctl/flightctl/internal/service"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/internal/worker_client"
	"github.com/flightctl/flightctl/pkg/k8sclient"
	"github.com/flightctl/flightctl/pkg/queues"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"go.opentelemetry.io/otel/attribute"
)

func dispatchTasks(serviceHandler service.Service, k8sClient k8sclient.K8SClient, kvStore kvstore.KVStore) queues.ConsumeHandler {
	return func(ctx context.Context, payload []byte, log logrus.FieldLogger) error {
		var eventWithOrgId worker_client.EventWithOrgId
		if err := json.Unmarshal(payload, &eventWithOrgId); err != nil {
			log.WithError(err).Error("failed to unmarshal consume payload")
			return err
		}

		ctx, span := tracing.StartSpan(ctx, "flightctl/worker", fmt.Sprintf("%s-%s", eventWithOrgId.Event.InvolvedObject.Kind, eventWithOrgId.Event.Reason))
		defer span.End()

		span.SetAttributes(
			attribute.String("event.kind", eventWithOrgId.Event.InvolvedObject.Kind),
			attribute.String("event.name", eventWithOrgId.Event.InvolvedObject.Name),
			attribute.String("event.reason", string(eventWithOrgId.Event.Reason)),
		)

		log.Infof("reconciling: %s, %s, %s/%s", eventWithOrgId.Event.InvolvedObject.Kind, eventWithOrgId.Event.Reason, eventWithOrgId.OrgId, eventWithOrgId.Event.InvolvedObject.Name)

		var err error
		var taskName string
		errorMessages := []string{}

		ctx = util.WithOrganizationID(ctx, eventWithOrgId.OrgId)

		if shouldRolloutFleet(ctx, eventWithOrgId.Event, log) {
			taskName = "fleetRollout"
			err = fleetRollout(ctx, eventWithOrgId.OrgId, eventWithOrgId.Event, serviceHandler, log)
			errorMessages = appendErrorMessage(errorMessages, taskName, err)
		}
		if shouldReconcileDeviceOwnership(ctx, eventWithOrgId.Event, log) {
			taskName = "fleetSelectorMatching"
			err = fleetSelectorMatching(ctx, eventWithOrgId.OrgId, eventWithOrgId.Event, serviceHandler, log)
			errorMessages = appendErrorMessage(errorMessages, taskName, err)
		}
		if shouldValidateFleet(ctx, eventWithOrgId.Event, log) {
			taskName = "fleetValidation"
			err = fleetValidate(ctx, eventWithOrgId.OrgId, eventWithOrgId.Event, serviceHandler, k8sClient, log)
			errorMessages = appendErrorMessage(errorMessages, taskName, err)
		}
		if shouldRenderDevice(ctx, eventWithOrgId.Event, log) {
			taskName = "deviceRender"
			err = deviceRender(ctx, eventWithOrgId.OrgId, eventWithOrgId.Event, serviceHandler, k8sClient, kvStore, log)
			errorMessages = appendErrorMessage(errorMessages, taskName, err)
		}
		if shouldUpdateRepositoryReferers(ctx, eventWithOrgId.Event, log) {
			taskName = "repositoryUpdate"
			err = repositoryUpdate(ctx, eventWithOrgId.OrgId, eventWithOrgId.Event, serviceHandler, log)
			errorMessages = appendErrorMessage(errorMessages, taskName, err)
		}

		// Emit InternalTaskFailedEvent for any unhandled task failures
		// This serves as a safety net while preserving specific error handling within tasks
		if len(errorMessages) > 0 {
			errorMessage := fmt.Sprintf("%d tasks failed during reconciliation: %s", len(errorMessages), strings.Join(errorMessages, ", "))
			log.WithError(errors.New(errorMessage)).Error("tasks failed during reconciliation")
			emitInternalTaskFailedEvent(ctx, errorMessage, eventWithOrgId.Event, serviceHandler)
		}
		return nil
	}
}

func appendErrorMessage(errorMessages []string, taskName string, err error) []string {
	if err != nil {
		errorMessages = append(errorMessages, fmt.Sprintf("%s: %s", taskName, err.Error()))
	}
	return errorMessages
}

func emitInternalTaskFailedEvent(ctx context.Context, errorMessage string, originalEvent api.Event, serviceHandler service.Service) {
	resourceKind := api.ResourceKind(originalEvent.InvolvedObject.Kind)
	resourceName := originalEvent.InvolvedObject.Name
	reason := originalEvent.Reason
	message := fmt.Sprintf("%s internal task failed: %s - %s.", resourceKind, reason, errorMessage)
	event := api.GetBaseEvent(ctx,
		resourceKind,
		resourceName,
		api.EventReasonInternalTaskFailed,
		message,
		nil)

	details := api.EventDetails{}
	if detailsErr := details.FromInternalTaskFailedDetails(api.InternalTaskFailedDetails{
		ErrorMessage:  errorMessage,
		RetryCount:    nil,
		OriginalEvent: originalEvent,
	}); detailsErr == nil {
		event.Details = &details
	}

	// Emit the event
	serviceHandler.CreateEvent(ctx, event)
}

func shouldRolloutFleet(ctx context.Context, event api.Event, log logrus.FieldLogger) bool {
	// If a devices's owner or labels were updated return true
	if event.Reason == api.EventReasonResourceUpdated && event.InvolvedObject.Kind == api.DeviceKind {
		return hasUpdatedFields(event.Details, log, api.Owner, api.Labels)
	}

	// If a device was created, return true
	if event.Reason == api.EventReasonResourceCreated && event.InvolvedObject.Kind == api.DeviceKind {
		return true
	}

	// If we got a rollout started event and it's immediate, return true
	if event.Reason == api.EventReasonFleetRolloutStarted && event.Details != nil {
		details, err := event.Details.AsFleetRolloutStartedDetails()
		if err != nil {
			log.WithError(err).Error("failed to convert event details to fleet rollout started details")
			return false
		}
		return details.RolloutStrategy == api.None
	}

	return false
}

func shouldReconcileDeviceOwnership(ctx context.Context, event api.Event, log logrus.FieldLogger) bool {
	// If a fleet's label selector was updated, return true
	if event.Reason == api.EventReasonResourceUpdated && event.InvolvedObject.Kind == api.FleetKind {
		return hasUpdatedFields(event.Details, log, api.SpecSelector)
	}

	// If a fleet was created, return true
	if event.Reason == api.EventReasonResourceCreated && event.InvolvedObject.Kind == api.FleetKind {
		return true
	}

	// If a fleet was deleted, return true
	if event.Reason == api.EventReasonResourceDeleted && event.InvolvedObject.Kind == api.FleetKind {
		return true
	}

	// If a device was created, return true
	if event.Reason == api.EventReasonResourceCreated && event.InvolvedObject.Kind == api.DeviceKind {
		return true
	}

	// If a device's labels were updated, return true
	if event.Reason == api.EventReasonResourceUpdated && event.InvolvedObject.Kind == api.DeviceKind {
		return hasUpdatedFields(event.Details, log, api.Labels)
	}

	return false
}

func shouldValidateFleet(ctx context.Context, event api.Event, log logrus.FieldLogger) bool {
	// If a fleet's template was updated, return true
	if event.Reason == api.EventReasonResourceUpdated && event.InvolvedObject.Kind == api.FleetKind {
		return hasUpdatedFields(event.Details, log, api.SpecTemplate)
	}

	// If a fleet was created, return true
	if event.Reason == api.EventReasonResourceCreated && event.InvolvedObject.Kind == api.FleetKind {
		return true
	}

	// If a repository that the fleet is associated with was updated, return true
	if event.Reason == api.EventReasonReferencedRepositoryUpdated && event.InvolvedObject.Kind == api.FleetKind {
		return true
	}

	return false
}

func shouldRenderDevice(ctx context.Context, event api.Event, log logrus.FieldLogger) bool {
	// If a repository that the device is associated with was updated, return true
	if event.Reason == api.EventReasonReferencedRepositoryUpdated && event.InvolvedObject.Kind == api.DeviceKind {
		return true
	}

	// If a device spec was updated and it doesn't have the delayDeviceRender annotation equal to "true", return true
	if event.Reason == api.EventReasonResourceUpdated && event.InvolvedObject.Kind == api.DeviceKind {
		if !hasUpdatedFields(event.Details, log, api.Spec) {
			return false
		}
		if event.Metadata.Annotations == nil {
			return true
		}
		if val, ok := (*event.Metadata.Annotations)[api.EventAnnotationDelayDeviceRender]; ok {
			if val == "true" {
				return false
			}
		}
		return true
	}

	// If a device was created, return true
	if event.Reason == api.EventReasonResourceCreated && event.InvolvedObject.Kind == api.DeviceKind {
		return true
	}

	return false
}

func shouldUpdateRepositoryReferers(ctx context.Context, event api.Event, log logrus.FieldLogger) bool {
	// If a repository was updated, return true
	if event.Reason == api.EventReasonResourceUpdated && event.InvolvedObject.Kind == api.RepositoryKind {
		return hasUpdatedFields(event.Details, log, api.Spec)
	}

	// If a repository was created, return true
	if event.Reason == api.EventReasonResourceCreated && event.InvolvedObject.Kind == api.RepositoryKind {
		return true
	}

	return false
}

func hasUpdatedFields(details *api.EventDetails, log logrus.FieldLogger, fields ...api.ResourceUpdatedDetailsUpdatedFields) bool {
	if details == nil {
		return false
	}

	updateDetails, err := details.AsResourceUpdatedDetails()
	if err != nil {
		log.WithError(err).Error("failed to convert event details to resource updated details")
		return false
	}

	updatedFields := updateDetails.UpdatedFields
	for _, field := range updatedFields {
		if lo.Contains(fields, field) {
			return true
		}
	}
	return false
}

func LaunchConsumers(ctx context.Context,
	queuesProvider queues.Provider,
	serviceHandler service.Service,
	k8sClient k8sclient.K8SClient,
	kvStore kvstore.KVStore,
	numConsumers, threadsPerConsumer int) error {
	for i := 0; i != numConsumers; i++ {
		consumer, err := queuesProvider.NewConsumer(consts.TaskQueue)
		if err != nil {
			return err
		}
		for j := 0; j != threadsPerConsumer; j++ {
			if err = consumer.Consume(ctx, dispatchTasks(serviceHandler, k8sClient, kvStore)); err != nil {
				return err
			}
		}
	}
	return nil
}
