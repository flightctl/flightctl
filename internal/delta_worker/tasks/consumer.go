package tasks

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/consts"
	deltastore "github.com/flightctl/flightctl/internal/delta_worker/store"
	generateTask "github.com/flightctl/flightctl/internal/delta_worker/tasks/generate"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/instrumentation/metrics/worker"
	"github.com/flightctl/flightctl/internal/worker_client"
	"github.com/flightctl/flightctl/pkg/queues"
	"github.com/sirupsen/logrus"
)

const ackTimeout = 5 * time.Second

// Consumer handles incoming jobs from the delta-generation task queue.
type Consumer struct {
	cfg           *config.Config
	workerMetrics *worker.WorkerCollector
	log           logrus.FieldLogger
	generator     *generateTask.Handler
}

// NewConsumer creates a new Consumer instance.
func NewConsumer(cfg *config.Config, store deltastore.Store, workerMetrics *worker.WorkerCollector, log logrus.FieldLogger) *Consumer {
	return &Consumer{
		cfg:           cfg,
		workerMetrics: workerMetrics,
		log:           log,
		generator:     generateTask.NewHandler(cfg, store, log),
	}
}

// Consume handles a single queue message.
func (c *Consumer) Consume(ctx context.Context, payload []byte, entryID string, consumer queues.QueueConsumer, log logrus.FieldLogger) error {
	if c.workerMetrics != nil {
		c.workerMetrics.IncMessagesInProgress()
		defer c.workerMetrics.DecMessagesInProgress()
	}

	var event worker_client.EventWithOrgId
	if err := json.Unmarshal(payload, &event); err != nil {
		log.WithError(err).Error("failed to unmarshal event payload")
		return completePoisonMessage(consumer, c.workerMetrics, log, entryID, payload)
	}

	if !worker_client.IsDeltaGenerationQueueEvent(event.Event.Reason) {
		log.WithField("reason", event.Event.Reason).Warn("dropping mis-routed event on delta-generation queue")
		return completePoisonMessage(consumer, c.workerMetrics, log, entryID, payload)
	}

	taskType := string(event.Event.Reason)
	log.Infof("received %s", taskType)

	var procErr error
	switch event.Event.Reason {
	case domain.EventReasonGenerateDelta:
		if err := generateTask.ValidateGenerationJob(event); err != nil {
			log.WithField("orgId", event.OrgId).Warnf("invalid GenerateDelta payload message=%q", event.Event.Message)
			return completePoisonMessage(consumer, c.workerMetrics, log, entryID, payload)
		}
		if c.workerMetrics != nil {
			c.workerMetrics.IncTasksByType(taskType)
		}
		taskStart := time.Now()
		procErr = c.generator.Handle(ctx, event, log)
		if c.workerMetrics != nil {
			c.workerMetrics.ObserveTaskExecutionDuration(taskType, time.Since(taskStart))
		}
		if procErr != nil {
			log.WithError(procErr).Error("delta generation job failed")
		}
	case domain.EventReasonPrepareDeltas:
		if c.workerMetrics != nil {
			c.workerMetrics.IncTasksByType(taskType)
		}
		log.Debug("PrepareDeltas is not handled by the delta worker; acknowledging")
	default:
		log.Debugf("unhandled delta-generation event reason %q; acknowledging", event.Event.Reason)
	}

	ackCtx, cancel := context.WithTimeout(context.Background(), ackTimeout)
	defer cancel()
	if err := consumer.Complete(ackCtx, entryID, payload, procErr); err != nil {
		log.WithError(err).Errorf("failed to complete message %s", entryID)
		return err
	}

	if c.workerMetrics != nil {
		if procErr != nil {
			c.workerMetrics.IncMessagesProcessed("queued_for_retry")
		} else {
			c.workerMetrics.IncMessagesProcessed("success")
			c.workerMetrics.UpdateLastSuccessfulTask()
		}
	}

	return procErr
}

// LaunchConsumers starts Redis consumers on the delta-generation task queue.
func LaunchConsumers(ctx context.Context, queuesProvider queues.Provider, cfg *config.Config, store deltastore.Store, workerMetrics *worker.WorkerCollector, log logrus.FieldLogger) error {
	n := cfg.DeltaGeneration.EffectiveMaxConcurrentDeltaGenerations()
	if workerMetrics != nil {
		workerMetrics.SetConsumersActive(float64(n))
		// Queue depth is populated by the queue integration when available; zero is
		// the initial value until the first depth update is observed.
		workerMetrics.SetQueueDepth(consts.DeltaGenerationTaskQueue, 0)
		go func() {
			<-ctx.Done()
			workerMetrics.SetConsumersActive(0)
		}()
	}

	taskConsumer := NewConsumer(cfg, store, workerMetrics, log)
	for i := 0; i < n; i++ {
		consumer, err := queuesProvider.NewQueueConsumer(ctx, consts.DeltaGenerationTaskQueue)
		if err != nil {
			return fmt.Errorf("failed to create delta-generation consumer %d: %w", i, err)
		}
		if err = consumer.Consume(ctx, taskConsumer.Consume); err != nil {
			return fmt.Errorf("failed to start delta-generation consumer %d: %w", i, err)
		}
	}
	return nil
}

func completePoisonMessage(consumer queues.QueueConsumer, workerMetrics *worker.WorkerCollector, log logrus.FieldLogger, entryID string, payload []byte) error {
	if workerMetrics != nil {
		workerMetrics.IncPermanentFailures()
		workerMetrics.IncMessagesProcessed("permanent_failure")
	}
	ackCtx, cancel := context.WithTimeout(context.Background(), ackTimeout)
	defer cancel()
	if ackErr := consumer.Complete(ackCtx, entryID, payload, nil); ackErr != nil {
		log.WithError(ackErr).Errorf("failed to complete message %s after poison handling", entryID)
	}
	return nil
}
