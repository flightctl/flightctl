package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/instrumentation/metrics/worker"
	"github.com/flightctl/flightctl/internal/instrumentation/tracing"
	"github.com/flightctl/flightctl/internal/kvstore"
	"github.com/flightctl/flightctl/internal/service"
	"github.com/flightctl/flightctl/internal/worker_client"
	"github.com/flightctl/flightctl/pkg/k8sclient"
	"github.com/flightctl/flightctl/pkg/queues"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"go.opentelemetry.io/otel/attribute"
)

func dispatchTasks(serviceHandler service.Service, k8sClient k8sclient.K8SClient, kvStore kvstore.KVStore, cfg *config.Config, workerMetrics *worker.WorkerCollector) queues.ConsumeHandler {
	return func(ctx context.Context, payload []byte, entryID string, consumer queues.QueueConsumer, log logrus.FieldLogger) error {
		startTime := time.Now()

		// Increment in-progress counter
		if workerMetrics != nil {
			workerMetrics.IncMessagesInProgress()
			defer workerMetrics.DecMessagesInProgress()
		}

		// Add timeout for the entire event processing
		ctx, cancel := context.WithTimeout(ctx, EventProcessingTimeout)
		defer cancel()

		var eventWithOrgId worker_client.EventWithOrgId
		if err := json.Unmarshal(payload, &eventWithOrgId); err != nil {
			log.WithError(err).Error("failed to unmarshal consume payload")
			// Record unmarshal error as a permanent failure (parsing errors are not retryable)
			if workerMetrics != nil {
				workerMetrics.IncPermanentFailures()
				workerMetrics.IncMessagesProcessed("permanent_failure")
			}
			// Complete the message successfully to remove it from queue (parsing errors are not retryable)
			ackCtx, cancelAck := context.WithTimeout(context.Background(), AckTimeout)
			defer cancelAck()
			if ackErr := consumer.Complete(ackCtx, entryID, payload, nil); ackErr != nil {
				log.WithError(ackErr).Errorf("failed to complete message %s after unmarshal error", entryID)
			}
			return nil // Don't return error to avoid retries
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

		if shouldRolloutFleet(ctx, eventWithOrgId.Event, log) {
			taskName = "fleetRollout"
			err = runTaskWithMetrics(taskName, workerMetrics, func() error {
				return fleetRollout(ctx, eventWithOrgId.OrgId, eventWithOrgId.Event, serviceHandler, log)
			})
			errorMessages = appendErrorMessage(errorMessages, taskName, err)
		}
		if shouldReconcileDeviceOwnership(ctx, eventWithOrgId.Event, log) {
			taskName = "fleetSelectorMatching"
			err = runTaskWithMetrics(taskName, workerMetrics, func() error {
				return fleetSelectorMatching(ctx, eventWithOrgId.OrgId, eventWithOrgId.Event, serviceHandler, log)
			})
			errorMessages = appendErrorMessage(errorMessages, taskName, err)
		}
		if shouldValidateFleet(ctx, eventWithOrgId.Event, log) {
			taskName = "fleetValidation"
			err = runTaskWithMetrics(taskName, workerMetrics, func() error {
				return fleetValidate(ctx, eventWithOrgId.OrgId, eventWithOrgId.Event, serviceHandler, k8sClient, log)
			})
			errorMessages = appendErrorMessage(errorMessages, taskName, err)
		}
		if shouldRenderDevice(ctx, eventWithOrgId.Event, log) {
			taskName = "deviceRender"
			err = runTaskWithMetrics(taskName, workerMetrics, func() error {
				return deviceRender(ctx, eventWithOrgId.OrgId, eventWithOrgId.Event, serviceHandler, k8sClient, kvStore, cfg, log)
			})
			errorMessages = appendErrorMessage(errorMessages, taskName, err)
		}
		if shouldUpdateRepositoryReferers(ctx, eventWithOrgId.Event, log) {
			taskName = "repositoryUpdate"
			err = runTaskWithMetrics(taskName, workerMetrics, func() error {
				return repositoryUpdate(ctx, eventWithOrgId.OrgId, eventWithOrgId.Event, serviceHandler, log)
			})
			errorMessages = appendErrorMessage(errorMessages, taskName, err)
		}

		// Emit InternalTaskFailedEvent for any unhandled task failures
		// This serves as a safety net while preserving specific error handling within tasks
		var returnErr error
		if len(errorMessages) > 0 {
			errorMessage := fmt.Sprintf("%d tasks failed during reconciliation: %s", len(errorMessages), strings.Join(errorMessages, ", "))
			log.WithError(errors.New(errorMessage)).Error("tasks failed during reconciliation")
			// ensure emission even if processing ctx timed out
			emitCtx, cancelEmit := context.WithTimeout(context.Background(), AckTimeout)
			defer cancelEmit()
			EmitInternalTaskFailedEvent(emitCtx, eventWithOrgId.OrgId, errorMessage, eventWithOrgId.Event, serviceHandler)
			returnErr = errors.New(errorMessage)
		}

		// Complete the message processing (either successfully or after emitting failure event)
		ackCtx, cancelAck := context.WithTimeout(context.Background(), AckTimeout)
		defer cancelAck()
		if err := consumer.Complete(ackCtx, entryID, payload, returnErr); err != nil {
			log.WithError(err).Errorf("failed to complete message %s", entryID)
			return err
		}

		// Record metrics only after successful completion
		if workerMetrics != nil {
			// Record processing duration
			workerMetrics.ObserveProcessingDuration(time.Since(startTime))

			if len(errorMessages) > 0 {
				// Record message queued for retry (actual retry/permanent failure determination happens in queue maintenance)
				workerMetrics.IncMessagesProcessed("queued_for_retry")
			} else {
				// Record successful processing
				workerMetrics.IncMessagesProcessed("success")
				workerMetrics.UpdateLastSuccessfulTask()
			}
		}

		return returnErr
	}
}

func appendErrorMessage(errorMessages []string, taskName string, err error) []string {
	if err != nil {
		errorMessages = append(errorMessages, fmt.Sprintf("%s: %s", taskName, err.Error()))
	}
	return errorMessages
}

// runTaskWithMetrics wraps task execution with metrics collection
func runTaskWithMetrics(name string, workerMetrics *worker.WorkerCollector, fn func() error) error {
	start := time.Now()
	err := fn()
	if workerMetrics != nil {
		workerMetrics.IncTasksByType(name)
		workerMetrics.ObserveTaskExecutionDuration(name, time.Since(start))
	}
	return err
}

func shouldRolloutFleet(ctx context.Context, event domain.Event, log logrus.FieldLogger) bool {
	// If a devices's owner or labels were updated return true
	if event.Reason == domain.EventReasonResourceUpdated && event.InvolvedObject.Kind == domain.DeviceKind {
		return hasUpdatedFields(event.Details, log, domain.Owner, domain.Labels)
	}

	if event.Reason == domain.EventReasonFleetRolloutBatchDispatched && event.InvolvedObject.Kind == domain.FleetKind {
		return true
	}

	// If a device was created, return true
	if event.Reason == domain.EventReasonResourceCreated && event.InvolvedObject.Kind == domain.DeviceKind {
		return true
	}

	// If we got a rollout started event and it's immediate, return true
	if event.Reason == domain.EventReasonFleetRolloutStarted && event.Details != nil {
		details, err := event.Details.AsFleetRolloutStartedDetails()
		if err != nil {
			log.WithError(err).Error("failed to convert event details to fleet rollout started details")
			return false
		}
		return details.RolloutStrategy == domain.None
	}

	return false
}

func shouldReconcileDeviceOwnership(ctx context.Context, event domain.Event, log logrus.FieldLogger) bool {
	// If a fleet's label selector was updated, return true
	if event.Reason == domain.EventReasonResourceUpdated && event.InvolvedObject.Kind == domain.FleetKind {
		return hasUpdatedFields(event.Details, log, domain.SpecSelector)
	}

	// If a fleet was created, return true
	if event.Reason == domain.EventReasonResourceCreated && event.InvolvedObject.Kind == domain.FleetKind {
		return true
	}

	// If a fleet was deleted, return true
	if event.Reason == domain.EventReasonResourceDeleted && event.InvolvedObject.Kind == domain.FleetKind {
		return true
	}

	// If a device was created, return true
	if event.Reason == domain.EventReasonResourceCreated && event.InvolvedObject.Kind == domain.DeviceKind {
		return true
	}

	// If a device's labels were updated, return true
	if event.Reason == domain.EventReasonResourceUpdated && event.InvolvedObject.Kind == domain.DeviceKind {
		return hasUpdatedFields(event.Details, log, domain.Labels)
	}

	return false
}

func shouldValidateFleet(ctx context.Context, event domain.Event, log logrus.FieldLogger) bool {
	// If a fleet's template was updated, return true
	if event.Reason == domain.EventReasonResourceUpdated && event.InvolvedObject.Kind == domain.FleetKind {
		return hasUpdatedFields(event.Details, log, domain.SpecTemplate)
	}

	// If a fleet was created, return true
	if event.Reason == domain.EventReasonResourceCreated && event.InvolvedObject.Kind == domain.FleetKind {
		return true
	}

	// If a repository that the fleet is associated with was updated, return true
	if event.Reason == domain.EventReasonReferencedRepositoryUpdated && event.InvolvedObject.Kind == domain.FleetKind {
		return true
	}

	return false
}

func shouldRenderDevice(ctx context.Context, event domain.Event, log logrus.FieldLogger) bool {
	if event.InvolvedObject.Kind != domain.DeviceKind {
		return false
	}

	if lo.Contains([]domain.EventReason{domain.EventReasonReferencedRepositoryUpdated,
		domain.EventReasonResourceCreated,
		domain.EventReasonFleetRolloutDeviceSelected, domain.EventReasonDeviceConflictResolved,
		domain.EventReasonDeviceDecommissioned}, event.Reason) {
		return true
	}

	// If a device spec was updated and it doesn't have the delayDeviceRender annotation equal to "true", return true
	if event.Reason == domain.EventReasonResourceUpdated {
		if !hasUpdatedFields(event.Details, log, domain.Spec) {
			return false
		}
		if event.Metadata.Annotations == nil {
			return true
		}
		if val, ok := (*event.Metadata.Annotations)[domain.EventAnnotationDelayDeviceRender]; ok {
			if val == "true" {
				return false
			}
		}
		return true
	}

	return false
}

func shouldUpdateRepositoryReferers(ctx context.Context, event domain.Event, log logrus.FieldLogger) bool {
	// If a repository was updated, return true
	if event.Reason == domain.EventReasonResourceUpdated && event.InvolvedObject.Kind == domain.RepositoryKind {
		return hasUpdatedFields(event.Details, log, domain.Spec)
	}

	// If a repository was created, return true
	if event.Reason == domain.EventReasonResourceCreated && event.InvolvedObject.Kind == domain.RepositoryKind {
		return true
	}

	return false
}

func hasUpdatedFields(details *domain.EventDetails, log logrus.FieldLogger, fields ...domain.ResourceUpdatedDetailsUpdatedFields) bool {
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
	cfg *config.Config,
	numConsumers, threadsPerConsumer int,
	workerMetrics *worker.WorkerCollector) error {
	totalConsumers := numConsumers * threadsPerConsumer

	// Set active consumers metric
	if workerMetrics != nil {
		workerMetrics.SetConsumersActive(float64(totalConsumers))
		go func() {
			<-ctx.Done()
			workerMetrics.SetConsumersActive(0)
		}()
	}

	for i := 0; i != numConsumers; i++ {
		consumer, err := queuesProvider.NewQueueConsumer(ctx, consts.TaskQueue)
		if err != nil {
			return err
		}
		for j := 0; j != threadsPerConsumer; j++ {
			if err = consumer.Consume(ctx, dispatchTasks(serviceHandler, k8sClient, kvStore, cfg, workerMetrics)); err != nil {
				return err
			}
		}
	}
	return nil
}
