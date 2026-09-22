package tasks

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/flightctl/flightctl/internal/consts"
	deltaconfig "github.com/flightctl/flightctl/internal/delta_worker/config"
	generateTask "github.com/flightctl/flightctl/internal/delta_worker/tasks/generate"
	generationcomplete "github.com/flightctl/flightctl/internal/delta_worker/tasks/generationcomplete"
	preparecomplete "github.com/flightctl/flightctl/internal/delta_worker/tasks/preparecomplete"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/instrumentation/metrics/worker"
	"github.com/flightctl/flightctl/internal/worker_client"
	"github.com/flightctl/flightctl/pkg/queues"
	"github.com/sirupsen/logrus"
)

const ackTimeout = 5 * time.Second

// Consumer handles incoming jobs from the delta-generation task queue.
type Consumer struct {
	cfg             *deltaconfig.DeltaGenerationConfig
	workerMetrics   *worker.WorkerCollector
	log             logrus.FieldLogger
	preparer        PrepareDeltasHandler
	generator       GenerateDeltaHandler
	completion      GenerationCompleteHandler
	prepareComplete PrepareCompleteHandler
}

type PrepareDeltasHandler interface {
	Prepare(ctx context.Context, ev worker_client.EventWithOrgId) error
}

type GenerateDeltaHandler interface {
	Handle(ctx context.Context, ev worker_client.EventWithOrgId, log logrus.FieldLogger) error
}

type GenerationCompleteHandler interface {
	Handle(context.Context, worker_client.EventWithOrgId) error
}

type PrepareCompleteHandler interface {
	Handle(context.Context, worker_client.EventWithOrgId) error
}

// ConsumerWiring configures the task handlers used by the generic consumer.
type ConsumerWiring struct {
	Preparer        PrepareDeltasHandler
	Generator       GenerateDeltaHandler
	Completion      GenerationCompleteHandler
	PrepareComplete PrepareCompleteHandler
}

// NewConsumer creates a new Consumer instance.
func NewConsumer(cfg *deltaconfig.DeltaGenerationConfig, workerMetrics *worker.WorkerCollector, log logrus.FieldLogger, wiring *ConsumerWiring) *Consumer {
	c := &Consumer{
		cfg:           cfg,
		workerMetrics: workerMetrics,
		log:           log,
	}
	if wiring != nil {
		c.preparer = wiring.Preparer
		c.generator = wiring.Generator
		c.completion = wiring.Completion
		c.prepareComplete = wiring.PrepareComplete
	}
	return c
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
	case domain.EventReasonPrepareDeltas:
		if c.workerMetrics != nil {
			c.workerMetrics.IncTasksByType(taskType)
		}
		procErr = c.handlePrepareDeltas(ctx, event, log)
	case domain.EventReasonGenerateDelta:
		if parseErr := generateTask.ValidateGenerationJob(event); parseErr != nil {
			log.WithError(parseErr).Error("invalid GenerateDelta payload")
			return completePoisonMessage(consumer, c.workerMetrics, log, entryID, payload)
		} else {
			if c.workerMetrics != nil {
				c.workerMetrics.IncTasksByType(taskType)
			}
			taskStart := time.Now()
			if c.generator != nil {
				procErr = c.generator.Handle(ctx, event, log)
			}
			if c.workerMetrics != nil {
				c.workerMetrics.ObserveTaskExecutionDuration(taskType, time.Since(taskStart))
			}
		}
	case domain.EventReasonDeltaGenerationComplete:
		if c.workerMetrics != nil {
			c.workerMetrics.IncTasksByType(taskType)
		}
		if c.completion != nil {
			procErr = c.completion.Handle(ctx, event)
			if generationcomplete.IsInvalidPayload(procErr) {
				log.WithError(procErr).Error("invalid DeltaGenerationComplete payload")
				return completePoisonMessage(consumer, c.workerMetrics, log, entryID, payload)
			}
		}
	case domain.EventReasonDeltaPrepareComplete:
		if c.workerMetrics != nil {
			c.workerMetrics.IncTasksByType(taskType)
		}
		if c.prepareComplete != nil {
			procErr = c.prepareComplete.Handle(ctx, event)
			if preparecomplete.IsInvalidPayload(procErr) {
				log.WithError(procErr).Error("invalid DeltaPrepareComplete payload")
				return completePoisonMessage(consumer, c.workerMetrics, log, entryID, payload)
			}
		}
	default:
		log.Debugf("unhandled delta-generation event reason %q; acknowledging", event.Event.Reason)
	}
	if procErr != nil {
		log.WithError(procErr).Error("delta generation job failed")
		if event.Event.Reason == domain.EventReasonPrepareDeltas {
			return procErr
		}
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

func (c *Consumer) handlePrepareDeltas(ctx context.Context, ev worker_client.EventWithOrgId, log logrus.FieldLogger) error {
	if c.preparer == nil {
		return nil
	}
	timeout := 30 * time.Minute
	if c.cfg != nil {
		timeout = c.cfg.EffectiveTimeout()
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	log.Infof("preparing deltas for %s/%s", ev.Event.InvolvedObject.Kind, ev.Event.InvolvedObject.Name)
	return c.preparer.Prepare(ctx, ev)
}

// LaunchConsumers starts Redis consumers on the delta-generation task queue.
func LaunchConsumers(ctx context.Context, queuesProvider queues.Provider, cfg *deltaconfig.DeltaGenerationConfig, workerMetrics *worker.WorkerCollector, log logrus.FieldLogger, wiring *ConsumerWiring) error {
	n := cfg.EffectiveMaxConcurrentDeltaGenerations()
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

	taskConsumer := NewConsumer(cfg, workerMetrics, log, wiring)
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
