package delta_worker

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/instrumentation/metrics/worker"
	"github.com/flightctl/flightctl/internal/oci"
	deltastore "github.com/flightctl/flightctl/internal/store/delta"
	"github.com/flightctl/flightctl/internal/worker_client"
	"github.com/flightctl/flightctl/pkg/queues"
	"github.com/sirupsen/logrus"
)

const ackTimeout = 5 * time.Second

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

	handler := jobHandler(newPipeline(cfg, store, log), workerMetrics)
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

func newPipeline(cfg *config.Config, store deltastore.Store, log logrus.FieldLogger) *pipeline {
	timeout := 30 * time.Minute
	if cfg != nil && cfg.DeltaGeneration != nil {
		timeout = cfg.DeltaGeneration.EffectiveTimeout()
	}
	return &pipeline{
		store:   store,
		timeout: timeout,
		check: func(ctx context.Context, imageRepository, sourceDigest, targetDigest string) (existenceResult, error) {
			existCfg, err := existenceConfigFromSpec(ctx, writeSpecFromConfig(cfg), imageRepository)
			if err != nil {
				return existenceResult{}, err
			}
			return checkExistingDelta(ctx, imageRepository, sourceDigest, targetDigest, existCfg)
		},
		generate: func(ctx context.Context, sourceRef, targetRef, pushPath string) (string, int64, error) {
			g := generator{run: execRunner{}, writeSpec: writeSpecFromConfig(cfg), log: log}
			return g.createAndPushDelta(ctx, sourceRef, targetRef, pushPath)
		},
		pushPath: pushPathFromConfig(cfg),
	}
}

func writeSpecFromConfig(cfg *config.Config) *domain.OciRepoSpec {
	if cfg == nil || cfg.DeltaGeneration == nil || cfg.DeltaGeneration.DefaultRepository == nil {
		return nil
	}
	return oci.SelectWriteTarget(nil, cfg.DeltaGeneration.DefaultRepository.OciRepoSpec())
}

func pushPathFromConfig(cfg *config.Config) func(string) (string, error) {
	return func(imageRepository string) (string, error) {
		spec := writeSpecFromConfig(cfg)
		if spec == nil {
			return "", fmt.Errorf("deltaGeneration.defaultRepository is required to push")
		}
		return oci.ResolveDeltaPushPath(spec, imageRepository)
	}
}

func jobHandler(p *pipeline, workerMetrics *worker.WorkerCollector) queues.ConsumeHandler {
	return func(ctx context.Context, payload []byte, entryID string, consumer queues.QueueConsumer, log logrus.FieldLogger) error {
		start := time.Now()
		if workerMetrics != nil {
			workerMetrics.IncMessagesInProgress()
			defer workerMetrics.DecMessagesInProgress()
		}

		taskType := taskTypeFromPayload(payload, log)
		log.Infof("received %s", taskType)
		if workerMetrics != nil {
			workerMetrics.IncTasksByType(taskType)
			workerMetrics.ObserveTaskExecutionDuration(taskType, time.Since(start))
			workerMetrics.IncMessagesProcessed("success")
			workerMetrics.UpdateLastSuccessfulTask()
		}

		var event worker_client.EventWithOrgId
		if err := json.Unmarshal(payload, &event); err == nil {
			if procErr := p.process(ctx, event, log); procErr != nil {
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
}

func taskTypeFromPayload(payload []byte, log logrus.FieldLogger) string {
	var event worker_client.EventWithOrgId
	if err := json.Unmarshal(payload, &event); err != nil {
		log.WithError(err).Error("failed to unmarshal event payload")
		return "unknown"
	}
	if event.Event.Reason == "" {
		return "unknown"
	}
	return string(event.Event.Reason)
}
