package tasks

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/consts"
	imagebuilderstore "github.com/flightctl/flightctl/internal/imagebuilder_api/store"
	"github.com/flightctl/flightctl/internal/instrumentation/tracing"
	"github.com/flightctl/flightctl/internal/kvstore"
	"github.com/flightctl/flightctl/internal/service"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/pkg/queues"
	"github.com/sirupsen/logrus"
	"go.opentelemetry.io/otel/attribute"
)

// TaskType represents the type of task to be processed
type TaskType string

// Task types supported by the imagebuilder worker
const (
	// TaskTypeImageBuild processes container image builds
	TaskTypeImageBuild TaskType = "imageBuild"
	// TaskTypeImageExport processes image exports to different formats (vmdk, qcow2, iso)
	TaskTypeImageExport TaskType = "imageExport"
)

const (
	// JobProcessingTimeout is the maximum time allowed for processing a single job
	JobProcessingTimeout = 30 * time.Minute
	// AckTimeout is the timeout for acknowledging messages
	AckTimeout = 5 * time.Second
)

// Job represents a job from the imagebuild-queue
type Job struct {
	Type      TaskType `json:"type"`
	OrgID     string   `json:"orgId"`
	Name      string   `json:"name"`
	Timestamp int64    `json:"timestamp"`
}

// dispatchTasks handles incoming jobs from the queue and routes them to appropriate handlers
func dispatchTasks(store imagebuilderstore.Store, mainStore store.Store, kvStore kvstore.KVStore, serviceHandler *service.ServiceHandler, cfg *config.Config, log logrus.FieldLogger) queues.ConsumeHandler {
	return func(ctx context.Context, payload []byte, entryID string, consumer queues.QueueConsumer, log logrus.FieldLogger) error {
		startTime := time.Now()

		// Add timeout for the entire job processing
		ctx, cancel := context.WithTimeout(ctx, JobProcessingTimeout)
		defer cancel()

		var job Job
		if err := json.Unmarshal(payload, &job); err != nil {
			log.WithError(err).Error("failed to unmarshal job payload")
			// Complete the message successfully to remove it from queue (parsing errors are not retryable)
			ackCtx, cancelAck := context.WithTimeout(context.Background(), AckTimeout)
			defer cancelAck()
			if ackErr := consumer.Complete(ackCtx, entryID, payload, nil); ackErr != nil {
				log.WithError(ackErr).Errorf("failed to complete message %s after unmarshal error", entryID)
			}
			return nil // Don't return error to avoid retries
		}

		ctx, span := tracing.StartSpan(ctx, "flightctl/imagebuilder-worker", fmt.Sprintf("%s-%s", job.Type, job.Name))
		defer span.End()

		span.SetAttributes(
			attribute.String("job.type", string(job.Type)),
			attribute.String("job.name", job.Name),
			attribute.String("job.orgId", job.OrgID),
		)

		log.Infof("processing job: type=%s, name=%s, orgId=%s", job.Type, job.Name, job.OrgID)

		// Route to appropriate handler based on task type
		var processingErr error
		switch job.Type {
		case TaskTypeImageBuild:
			processingErr = processImageBuild(ctx, store, mainStore, kvStore, serviceHandler, cfg, job, log)

		case TaskTypeImageExport:
			processingErr = processImageExport(ctx, store, kvStore, job, log)

		default:
			log.Warnf("unknown task type %q, acknowledging without processing", job.Type)
			// Complete unknown task types to prevent queue blocking
			ackCtx, cancelAck := context.WithTimeout(context.Background(), AckTimeout)
			defer cancelAck()
			if ackErr := consumer.Complete(ackCtx, entryID, payload, nil); ackErr != nil {
				log.WithError(ackErr).Errorf("failed to complete message %s", entryID)
			}
			return nil
		}

		if processingErr != nil {
			log.WithError(processingErr).Errorf("failed to process %s job", job.Type)
		}

		// Complete the message processing
		ackCtx, cancelAck := context.WithTimeout(context.Background(), AckTimeout)
		defer cancelAck()
		if err := consumer.Complete(ackCtx, entryID, payload, processingErr); err != nil {
			log.WithError(err).Errorf("failed to complete message %s", entryID)
			return err
		}

		duration := time.Since(startTime)
		if processingErr != nil {
			log.WithError(processingErr).Infof("job processing failed after %v", duration)
		} else {
			log.Infof("job processing completed successfully in %v", duration)
		}

		return processingErr
	}
}

// LaunchConsumers starts the specified number of queue consumers for imagebuild jobs
func LaunchConsumers(
	ctx context.Context,
	queuesProvider queues.Provider,
	store imagebuilderstore.Store,
	mainStore store.Store,
	kvStore kvstore.KVStore,
	serviceHandler *service.ServiceHandler,
	cfg *config.Config,
	log logrus.FieldLogger,
) error {
	maxConcurrentBuilds := cfg.ImageBuilderWorker.MaxConcurrentBuilds
	log.Infof("Launching %d imagebuild queue consumers", maxConcurrentBuilds)

	for i := 0; i < maxConcurrentBuilds; i++ {
		consumer, err := queuesProvider.NewQueueConsumer(ctx, consts.ImageBuildTaskQueue)
		if err != nil {
			return fmt.Errorf("failed to create queue consumer %d: %w", i, err)
		}
		if err = consumer.Consume(ctx, dispatchTasks(store, mainStore, kvStore, serviceHandler, cfg, log)); err != nil {
			return fmt.Errorf("failed to start consumer %d: %w", i, err)
		}
	}

	log.Info("All imagebuild queue consumers started")
	return nil
}
