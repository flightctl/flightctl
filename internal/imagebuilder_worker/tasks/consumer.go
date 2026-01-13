package tasks

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	apiimagebuilder "github.com/flightctl/flightctl/api/imagebuilder/v1beta1"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/consts"
	imagebuilderstore "github.com/flightctl/flightctl/internal/imagebuilder_api/store"
	"github.com/flightctl/flightctl/internal/instrumentation/tracing"
	"github.com/flightctl/flightctl/internal/kvstore"
	"github.com/flightctl/flightctl/internal/service"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/worker_client"
	"github.com/flightctl/flightctl/pkg/queues"
	"github.com/sirupsen/logrus"
	"go.opentelemetry.io/otel/attribute"
)

const (
	// AckTimeout is the timeout for acknowledging messages
	AckTimeout = 5 * time.Second
)

// Consumer handles incoming jobs from the queue and routes them to appropriate handlers
type Consumer struct {
	store          imagebuilderstore.Store
	mainStore      store.Store
	kvStore        kvstore.KVStore
	serviceHandler *service.ServiceHandler
	cfg            *config.Config
	log            logrus.FieldLogger
}

// NewConsumer creates a new Consumer instance with the provided dependencies
func NewConsumer(
	store imagebuilderstore.Store,
	mainStore store.Store,
	kvStore kvstore.KVStore,
	serviceHandler *service.ServiceHandler,
	cfg *config.Config,
	log logrus.FieldLogger,
) *Consumer {
	return &Consumer{
		store:          store,
		mainStore:      mainStore,
		kvStore:        kvStore,
		serviceHandler: serviceHandler,
		cfg:            cfg,
		log:            log,
	}
}

// Consume handles incoming events from the queue and routes them to appropriate handlers
func (c *Consumer) Consume(ctx context.Context, payload []byte, entryID string, consumer queues.QueueConsumer, log logrus.FieldLogger) error {
	startTime := time.Now()

	var eventWithOrgId worker_client.EventWithOrgId
	if err := json.Unmarshal(payload, &eventWithOrgId); err != nil {
		log.WithError(err).Error("failed to unmarshal event payload")
		// Complete the message successfully to remove it from queue (parsing errors are not retryable)
		ackCtx, cancelAck := context.WithTimeout(context.Background(), AckTimeout)
		defer cancelAck()
		if ackErr := consumer.Complete(ackCtx, entryID, payload, nil); ackErr != nil {
			log.WithError(ackErr).Errorf("failed to complete message %s after unmarshal error", entryID)
		}
		return nil // Don't return error to avoid retries
	}

	event := eventWithOrgId.Event
	orgID := eventWithOrgId.OrgId

	ctx, span := tracing.StartSpan(ctx, "flightctl/imagebuilder-worker", fmt.Sprintf("%s-%s", event.InvolvedObject.Kind, event.InvolvedObject.Name))
	defer span.End()

	span.SetAttributes(
		attribute.String("event.kind", event.InvolvedObject.Kind),
		attribute.String("event.name", event.InvolvedObject.Name),
		attribute.String("event.reason", string(event.Reason)),
		attribute.String("event.orgId", orgID.String()),
	)

	log.Infof("processing event: kind=%s, name=%s, reason=%s, orgId=%s", event.InvolvedObject.Kind, event.InvolvedObject.Name, event.Reason, orgID)

	// Route to appropriate handler based on involved object kind and reason
	var processingErr error
	switch event.InvolvedObject.Kind {
	case string(apiimagebuilder.ResourceKindImageBuild):
		if event.Reason == api.EventReasonResourceCreated {
			processingErr = c.processImageBuild(ctx, eventWithOrgId, log)
		} else {
			log.Debugf("ignoring ImageBuild event with reason %q (only ResourceCreated is processed)", event.Reason)
			// Complete non-ResourceCreated events without processing
			ackCtx, cancelAck := context.WithTimeout(context.Background(), AckTimeout)
			defer cancelAck()
			if ackErr := consumer.Complete(ackCtx, entryID, payload, nil); ackErr != nil {
				log.WithError(ackErr).Errorf("failed to complete message %s", entryID)
			}
			return nil
		}

	case string(apiimagebuilder.ResourceKindImageExport):
		if event.Reason == api.EventReasonResourceCreated {
			processingErr = c.processImageExport(ctx, eventWithOrgId, log)
		} else {
			log.Debugf("ignoring ImageExport event with reason %q (only ResourceCreated is processed)", event.Reason)
			// Complete non-ResourceCreated events without processing
			ackCtx, cancelAck := context.WithTimeout(context.Background(), AckTimeout)
			defer cancelAck()
			if ackErr := consumer.Complete(ackCtx, entryID, payload, nil); ackErr != nil {
				log.WithError(ackErr).Errorf("failed to complete message %s", entryID)
			}
			return nil
		}

	default:
		log.Warnf("unknown resource kind %q, acknowledging without processing", event.InvolvedObject.Kind)
		// Complete unknown resource kinds to prevent queue blocking
		ackCtx, cancelAck := context.WithTimeout(context.Background(), AckTimeout)
		defer cancelAck()
		if ackErr := consumer.Complete(ackCtx, entryID, payload, nil); ackErr != nil {
			log.WithError(ackErr).Errorf("failed to complete message %s", entryID)
		}
		return nil
	}

	if processingErr != nil {
		log.WithError(processingErr).Errorf("failed to process %s event", event.InvolvedObject.Kind)
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
		log.WithError(processingErr).Infof("event processing failed after %v", duration)
	} else {
		log.Infof("event processing completed successfully in %v", duration)
	}

	return processingErr
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

	taskConsumer := NewConsumer(store, mainStore, kvStore, serviceHandler, cfg, log)

	for i := 0; i < maxConcurrentBuilds; i++ {
		consumer, err := queuesProvider.NewQueueConsumer(ctx, consts.ImageBuildTaskQueue)
		if err != nil {
			return fmt.Errorf("failed to create queue consumer %d: %w", i, err)
		}
		if err = consumer.Consume(ctx, taskConsumer.Consume); err != nil {
			return fmt.Errorf("failed to start consumer %d: %w", i, err)
		}
	}

	log.Info("All imagebuild queue consumers started")
	return nil
}
