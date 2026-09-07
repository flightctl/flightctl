package tasks

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/instrumentation/metrics/worker"
	deltastore "github.com/flightctl/flightctl/internal/store/delta"
	"github.com/flightctl/flightctl/internal/worker_client"
	"github.com/flightctl/flightctl/pkg/queues"
	"github.com/sirupsen/logrus"
)

const ackTimeout = 5 * time.Second

// Consumer handles incoming jobs from the delta-generation task queue.
type Consumer struct {
	cfg           *config.Config
	store         deltastore.Store
	workerMetrics *worker.WorkerCollector
	log           logrus.FieldLogger

	jobTimeout     time.Duration
	existenceCheck func(ctx context.Context, imageRepository, sourceDigest, targetDigest string) (existenceResult, error)
	generateDelta  func(ctx context.Context, sourceRef, targetRef, pushPath string) (deltaRef string, sizeBytes int64, err error)
	pushPath       func(imageRepository string) (string, error)
	resume         func(ctx context.Context, key deltastore.GenerationKey) error
}

// NewConsumer creates a new Consumer instance.
func NewConsumer(cfg *config.Config, store deltastore.Store, workerMetrics *worker.WorkerCollector, log logrus.FieldLogger) *Consumer {
	return &Consumer{
		cfg:           cfg,
		store:         store,
		workerMetrics: workerMetrics,
		log:           log,
	}
}

// Consume handles a single queue message.
func (c *Consumer) Consume(ctx context.Context, payload []byte, entryID string, consumer queues.QueueConsumer, log logrus.FieldLogger) error {
	start := time.Now()
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
	if c.workerMetrics != nil {
		c.workerMetrics.IncTasksByType(taskType)
		c.workerMetrics.ObserveTaskExecutionDuration(taskType, time.Since(start))
		c.workerMetrics.IncMessagesProcessed("success")
		c.workerMetrics.UpdateLastSuccessfulTask()
	}

	switch event.Event.Reason {
	case domain.EventReasonGenerateDelta:
		if procErr := c.handleGenerateDelta(ctx, event, log); procErr != nil {
			log.WithError(procErr).Error("delta generation job failed")
		}
	}

	ackCtx, cancel := context.WithTimeout(context.Background(), ackTimeout)
	defer cancel()
	if err := consumer.Complete(ackCtx, entryID, payload, nil); err != nil {
		log.WithError(err).Errorf("failed to complete message %s", entryID)
		return err
	}
	return nil
}

// LaunchConsumers starts Redis consumers on the delta-generation task queue.
func LaunchConsumers(ctx context.Context, queuesProvider queues.Provider, cfg *config.Config, store deltastore.Store, workerMetrics *worker.WorkerCollector, log logrus.FieldLogger) error {
	n := cfg.DeltaGeneration.EffectiveMaxConcurrentDeltaGenerations()
	if workerMetrics != nil {
		workerMetrics.SetConsumersActive(float64(n))
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
