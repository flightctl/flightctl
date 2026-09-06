package delta_worker

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/instrumentation/metrics/worker"
	"github.com/flightctl/flightctl/internal/worker_client"
	"github.com/flightctl/flightctl/pkg/queues"
	"github.com/sirupsen/logrus"
)

const ackTimeout = 5 * time.Second

// LaunchConsumers starts Redis consumers on the delta-generation task queue.
func LaunchConsumers(ctx context.Context, queuesProvider queues.Provider, cfg *config.Config, workerMetrics *worker.WorkerCollector, log logrus.FieldLogger) error {
	n := cfg.DeltaGeneration.EffectiveMaxConcurrentDeltaGenerations()
	if workerMetrics != nil {
		workerMetrics.SetConsumersActive(float64(n))
		go func() {
			<-ctx.Done()
			workerMetrics.SetConsumersActive(0)
		}()
	}

	handler := idleHandler(workerMetrics)
	for i := 0; i < n; i++ {
		consumer, err := queuesProvider.NewQueueConsumer(ctx, consts.DeltaGenerationTaskQueue)
		if err != nil {
			return fmt.Errorf("failed to create delta-generation consumer %d: %w", i, err)
		}
		if err = consumer.Consume(ctx, handler); err != nil {
			return fmt.Errorf("failed to start delta-generation consumer %d: %w", i, err)
		}
	}
	return nil
}

func idleHandler(workerMetrics *worker.WorkerCollector) queues.ConsumeHandler {
	return func(ctx context.Context, payload []byte, entryID string, consumer queues.QueueConsumer, log logrus.FieldLogger) error {
		start := time.Now()
		if workerMetrics != nil {
			workerMetrics.IncMessagesInProgress()
			defer workerMetrics.DecMessagesInProgress()
		}

		var event worker_client.EventWithOrgId
		if err := json.Unmarshal(payload, &event); err != nil {
			log.WithError(err).Error("failed to unmarshal event payload")
			return completePoisonMessage(consumer, workerMetrics, log, entryID, payload)
		}

		if !worker_client.IsDeltaGenerationQueueEvent(event.Event.Reason) {
			log.WithField("reason", event.Event.Reason).Warn("dropping mis-routed event on delta-generation queue")
			return completePoisonMessage(consumer, workerMetrics, log, entryID, payload)
		}

		taskType := string(event.Event.Reason)
		if workerMetrics != nil {
			workerMetrics.IncTasksByType(taskType)
			workerMetrics.ObserveTaskExecutionDuration(taskType, time.Since(start))
			workerMetrics.IncMessagesProcessed("success")
			workerMetrics.UpdateLastSuccessfulTask()
		}

		ackCtx, cancel := context.WithTimeout(context.Background(), ackTimeout)
		defer cancel()
		if err := consumer.Complete(ackCtx, entryID, payload, nil); err != nil {
			log.WithError(err).Errorf("failed to complete message %s", entryID)
			return err
		}
		return nil
	}
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
