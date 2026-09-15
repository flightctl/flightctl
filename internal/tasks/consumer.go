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
	catalogservice "github.com/flightctl/flightctl/internal/service/catalog"
	dependencyrefservice "github.com/flightctl/flightctl/internal/service/dependencyref"
	deviceservice "github.com/flightctl/flightctl/internal/service/device"
	eventservice "github.com/flightctl/flightctl/internal/service/event"
	fleetservice "github.com/flightctl/flightctl/internal/service/fleet"
	repositoryservice "github.com/flightctl/flightctl/internal/service/repository"
	templateversionservice "github.com/flightctl/flightctl/internal/service/templateversion"
	"github.com/flightctl/flightctl/internal/worker_client"
	"github.com/flightctl/flightctl/pkg/k8sclient"
	"github.com/flightctl/flightctl/pkg/queues"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"go.opentelemetry.io/otel/attribute"
)

type TaskConsumer struct {
	FleetSvc           fleetservice.Service
	TemplateversionSvc templateversionservice.Service
	DeviceSvc          deviceservice.Service
	DependencyrefSvc   dependencyrefservice.Service
	RepositorySvc      repositoryservice.Service
	CatalogSvc         catalogservice.Service
	EventSvc           eventservice.Service
	K8sClient          k8sclient.K8SClient
	KVStore            kvstore.KVStore
	Cfg                *config.Config
	WorkerMetrics      *worker.WorkerCollector
	EncryptionMigrator *EncryptionMigrator
	QueuePublisher     queues.QueueProducer
	WorkerClient       worker_client.WorkerClient
	DeltaStore         generationLookup
	Preparing          preparingClearer
}

func (d TaskConsumer) dispatch() queues.ConsumeHandler {
	return func(ctx context.Context, payload []byte, entryID string, consumer queues.QueueConsumer, log logrus.FieldLogger) error {
		startTime := time.Now()

		// Increment in-progress counter
		if d.WorkerMetrics != nil {
			d.WorkerMetrics.IncMessagesInProgress()
			defer d.WorkerMetrics.DecMessagesInProgress()
		}

		// Add timeout for the entire event processing
		ctx, cancel := context.WithTimeout(ctx, EventProcessingTimeout)
		defer cancel()

		var eventWithOrgId worker_client.EventWithOrgId
		if err := json.Unmarshal(payload, &eventWithOrgId); err != nil {
			log.WithError(err).Error("failed to unmarshal consume payload")
			// Record unmarshal error as a permanent failure (parsing errors are not retryable)
			if d.WorkerMetrics != nil {
				d.WorkerMetrics.IncPermanentFailures()
				d.WorkerMetrics.IncMessagesProcessed("permanent_failure")
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
			err = runTaskWithMetrics(taskName, d.WorkerMetrics, func() error {
				return fleetRollout(ctx, eventWithOrgId.OrgId, eventWithOrgId.Event, d.FleetSvc, d.TemplateversionSvc, d.DeviceSvc, d.DependencyrefSvc, log)
			})
			errorMessages = appendErrorMessage(errorMessages, taskName, err)
		}
		if shouldReconcileDeviceOwnership(ctx, eventWithOrgId.Event, log) {
			taskName = "fleetSelectorMatching"
			err = runTaskWithMetrics(taskName, d.WorkerMetrics, func() error {
				return fleetSelectorMatching(ctx, eventWithOrgId.OrgId, eventWithOrgId.Event, d.DeviceSvc, d.FleetSvc, log)
			})
			errorMessages = appendErrorMessage(errorMessages, taskName, err)
		}
		if shouldValidateFleet(ctx, eventWithOrgId.Event, log) {
			taskName = "fleetValidation"
			err = runTaskWithMetrics(taskName, d.WorkerMetrics, func() error {
				if eventWithOrgId.Event.InvolvedObject.Kind != domain.FleetKind {
					log.Errorf("FleetValidate called with unexpected kind %s and reason %s", eventWithOrgId.Event.InvolvedObject.Kind, eventWithOrgId.Event.Reason)
					return nil
				}
				logic := NewFleetValidateLogic(log, d.FleetSvc, d.TemplateversionSvc, d.DeviceSvc, d.RepositorySvc, d.K8sClient, eventWithOrgId.OrgId, eventWithOrgId.Event)
				logic.WorkerClient = d.WorkerClient
				if err := logic.CreateNewTemplateVersionIfFleetValid(ctx); err != nil {
					log.Errorf("failed validating fleet %s/%s: %v", eventWithOrgId.OrgId, eventWithOrgId.Event.InvolvedObject.Name, err)
				}
				return nil
			})
			errorMessages = appendErrorMessage(errorMessages, taskName, err)
		}
		if shouldPopulateDependencyRefs(ctx, eventWithOrgId.Event, log) {
			taskName = "populateDependencyRefs"
			err = runTaskWithMetrics(taskName, d.WorkerMetrics, func() error {
				return populateDependencyRefs(ctx, eventWithOrgId.OrgId, eventWithOrgId.Event, d.FleetSvc, d.DeviceSvc, d.DependencyrefSvc, log)
			})
			errorMessages = appendErrorMessage(errorMessages, taskName, err)
		}
		if shouldRenderDevice(ctx, eventWithOrgId.Event, log) {
			taskName = "deviceRender"
			err = runTaskWithMetrics(taskName, d.WorkerMetrics, func() error {
				return deviceRender(ctx, eventWithOrgId.OrgId, eventWithOrgId.Event, d.DeviceSvc, d.RepositorySvc, d.CatalogSvc, d.K8sClient, d.KVStore, d.DeltaStore, d.Preparing, d.Cfg, log)
			})
			errorMessages = appendErrorMessage(errorMessages, taskName, err)
		}
		if shouldUpdateRepositoryReferers(ctx, eventWithOrgId.Event, log) {
			taskName = "repositoryUpdate"
			err = runTaskWithMetrics(taskName, d.WorkerMetrics, func() error {
				return repositoryUpdate(ctx, eventWithOrgId.OrgId, eventWithOrgId.Event, d.RepositorySvc, d.EventSvc, log)
			})
			errorMessages = appendErrorMessage(errorMessages, taskName, err)
		}
		if shouldReconcileFleetApplicationLifecycle(ctx, eventWithOrgId.Event, log) {
			taskName = "fleetApplicationLifecycle"
			err = runTaskWithMetrics(taskName, d.WorkerMetrics, func() error {
				return fleetApplicationLifecycle(ctx, eventWithOrgId.OrgId, eventWithOrgId.Event, d.FleetSvc, d.DeviceSvc, d.EventSvc, log)
			})
			errorMessages = appendErrorMessage(errorMessages, taskName, err)
		}
		if shouldRunEncryptionMigration(eventWithOrgId.Event) {
			taskName = "encryptionMigration"
			err = runTaskWithMetrics(taskName, d.WorkerMetrics, func() error {
				return runEncryptionMigrationBatch(ctx, eventWithOrgId.OrgId, eventWithOrgId.Event, d.EncryptionMigrator, d.QueuePublisher, log)
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
			EmitInternalTaskFailedEvent(emitCtx, eventWithOrgId.OrgId, errorMessage, eventWithOrgId.Event, d.EventSvc)
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
		if d.WorkerMetrics != nil {
			d.WorkerMetrics.ObserveProcessingDuration(time.Since(startTime))

			if len(errorMessages) > 0 {
				d.WorkerMetrics.IncMessagesProcessed("queued_for_retry")
			} else {
				d.WorkerMetrics.IncMessagesProcessed("success")
				d.WorkerMetrics.UpdateLastSuccessfulTask()
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

	// If a dependency change was detected for a fleet, return true
	if event.Reason == domain.EventReasonDependencyChangeDetected && event.InvolvedObject.Kind == domain.FleetKind {
		return true
	}

	return false
}

func shouldRenderDevice(ctx context.Context, event domain.Event, log logrus.FieldLogger) bool {
	if event.InvolvedObject.Kind != domain.DeviceKind {
		return false
	}

	if lo.Contains([]domain.EventReason{domain.EventReasonReferencedRepositoryUpdated,
		domain.EventReasonDependencyChangeDetected,
		domain.EventReasonResourceCreated,
		domain.EventReasonFleetRolloutDeviceSelected, domain.EventReasonDeviceConflictResolved,
		domain.EventReasonDeviceDecommissioned, domain.EventReasonApplicationLifecycleChanged,
		domain.EventReasonDeltaGenerationCompleted}, event.Reason) {
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

// shouldReconcileFleetApplicationLifecycle matches the event emitted by the fleet-scoped
// stop/start APIs (see StopFleetApplication/StartFleetApplication) whenever the fleet-level
// application lifecycle default changes, so it can be propagated to every member device.
func shouldReconcileFleetApplicationLifecycle(ctx context.Context, event domain.Event, log logrus.FieldLogger) bool {
	return event.InvolvedObject.Kind == domain.FleetKind && event.Reason == domain.EventReasonApplicationLifecycleChanged
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

func shouldRunEncryptionMigration(event domain.Event) bool {
	return event.Reason == EventReasonEncryptionMigrationBatch
}

func runEncryptionMigrationBatch(ctx context.Context, orgID uuid.UUID, event domain.Event, migrator *EncryptionMigrator, publisher queues.QueueProducer, log logrus.FieldLogger) error {
	if migrator == nil {
		return fmt.Errorf("encryption migration: migrator is nil")
	}
	kind := event.InvolvedObject.Kind
	if kind == "" {
		return fmt.Errorf("encryption migration: event involvedObject.kind is required")
	}
	key := leaseKey(kind, orgID)
	unlock, acquired, err := migrator.locker.TryLock(ctx, key)
	if err != nil {
		return err
	}
	if !acquired {
		// Do not drop the chain: the holder may have raced with unlock, or keys may collide.
		// Delay briefly to avoid a hot re-enqueue loop while another worker holds the lease.
		log.Infof("encryption migration: %s org %s lease held by another worker; re-enqueueing after delay", kind, orgID)
		enqueueEncryptionMigrationAfter(migrator.lifecycleCtx, publisher, kind, orgID, encryptionMigrationLeaseBusyDelay, log)
		return nil
	}
	leaseHeld := true
	defer func() {
		if !leaseHeld {
			return
		}
		if unlockErr := unlock(); unlockErr != nil {
			log.WithError(unlockErr).Errorf("encryption migration: failed to release lease for %s org %s", kind, orgID)
		}
	}()

	report, err := migrator.RunBatch(ctx, kind, orgID)
	if err != nil {
		return err
	}
	if report.Complete {
		log.Infof("encryption migration: %s org %s idle (complete for active key %q)", kind, orgID, report.ActiveKeyID)
		return nil
	}
	if report.RetryAfter > 0 {
		log.Infof("encryption migration: scheduling %s org %s retry after %s", kind, orgID, report.RetryAfter.Round(time.Second))
		enqueueEncryptionMigrationAfter(migrator.lifecycleCtx, publisher, kind, orgID, report.RetryAfter, log)
		return nil
	}
	// Release before re-enqueue so another replica can acquire immediately.
	if unlockErr := unlock(); unlockErr != nil {
		return fmt.Errorf("encryption migration: release lease before re-enqueue for %s org %s: %w", kind, orgID, unlockErr)
	}
	leaseHeld = false
	if err := EnqueueEncryptionMigration(ctx, publisher, kind, orgID); err != nil {
		return err
	}
	log.Infof("encryption migration: re-enqueued %s org %s after batch scanned=%d updated=%d errors=%d",
		kind, orgID, report.Scanned, report.Updated, report.Errors)
	return nil
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

func LaunchConsumers(ctx context.Context, queuesProvider queues.Provider, d TaskConsumer, numConsumers, threadsPerConsumer int) error {
	totalConsumers := numConsumers * threadsPerConsumer

	if d.WorkerMetrics != nil {
		d.WorkerMetrics.SetConsumersActive(float64(totalConsumers))
		go func() {
			<-ctx.Done()
			d.WorkerMetrics.SetConsumersActive(0)
		}()
	}

	for i := 0; i != numConsumers; i++ {
		consumer, err := queuesProvider.NewQueueConsumer(ctx, consts.TaskQueue)
		if err != nil {
			return err
		}
		for j := 0; j != threadsPerConsumer; j++ {
			if err = consumer.Consume(ctx, d.dispatch()); err != nil {
				return err
			}
		}
	}
	return nil
}
