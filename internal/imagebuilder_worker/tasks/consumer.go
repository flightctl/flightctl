package tasks

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/consts"
	coredomain "github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/imagebuilder_api/domain"
	imagebuilderapi "github.com/flightctl/flightctl/internal/imagebuilder_api/service"
	imagebuilderstore "github.com/flightctl/flightctl/internal/imagebuilder_api/store"
	"github.com/flightctl/flightctl/internal/instrumentation/tracing"
	"github.com/flightctl/flightctl/internal/kvstore"
	"github.com/flightctl/flightctl/internal/service"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/worker_client"
	"github.com/flightctl/flightctl/pkg/queues"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
	"go.opentelemetry.io/otel/attribute"
)

const (
	// AckTimeout is the timeout for acknowledging messages
	AckTimeout = 5 * time.Second
)

// Consumer handles incoming jobs from the queue and routes them to appropriate handlers
type Consumer struct {
	store               imagebuilderstore.Store
	mainStore           store.Store
	kvStore             kvstore.KVStore
	serviceHandler      *service.ServiceHandler
	imageBuilderService imagebuilderapi.Service
	queueProducer       queues.QueueProducer
	cfg                 *config.Config
	log                 logrus.FieldLogger
}

// NewConsumer creates a new Consumer instance with the provided dependencies
func NewConsumer(
	store imagebuilderstore.Store,
	mainStore store.Store,
	kvStore kvstore.KVStore,
	serviceHandler *service.ServiceHandler,
	imageBuilderService imagebuilderapi.Service,
	queueProducer queues.QueueProducer,
	cfg *config.Config,
	log logrus.FieldLogger,
) *Consumer {
	return &Consumer{
		store:               store,
		mainStore:           mainStore,
		kvStore:             kvStore,
		serviceHandler:      serviceHandler,
		imageBuilderService: imageBuilderService,
		queueProducer:       queueProducer,
		cfg:                 cfg,
		log:                 log,
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
	case string(domain.ResourceKindImageBuild):
		switch event.Reason {
		case coredomain.EventReasonResourceCreated:
			processingErr = c.processImageBuild(ctx, eventWithOrgId, log)
		case coredomain.EventReasonResourceUpdated:
			// Handle ImageBuild updates - if completed, requeue related ImageExports
			processingErr = c.HandleImageBuildUpdate(ctx, eventWithOrgId, log)
		default:
			log.Debugf("ignoring ImageBuild event with reason %q (only ResourceCreated and ResourceUpdated are processed)", event.Reason)
			// Complete non-ResourceCreated/ResourceUpdated events without processing
			ackCtx, cancelAck := context.WithTimeout(context.Background(), AckTimeout)
			defer cancelAck()
			if ackErr := consumer.Complete(ackCtx, entryID, payload, nil); ackErr != nil {
				log.WithError(ackErr).Errorf("failed to complete message %s", entryID)
			}
			return nil
		}

	case string(domain.ResourceKindImageExport):
		switch event.Reason {
		case coredomain.EventReasonResourceCreated:
			processingErr = c.processImageExport(ctx, eventWithOrgId, log)
		default:
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

// enqueueEvent enqueues an event to the imagebuild queue
func (c *Consumer) enqueueEvent(ctx context.Context, orgID uuid.UUID, event *coredomain.Event, log logrus.FieldLogger) error {
	// Create EventWithOrgId structure for the queue
	eventWithOrgId := worker_client.EventWithOrgId{
		OrgId: orgID,
		Event: *event,
	}

	payload, err := json.Marshal(eventWithOrgId)
	if err != nil {
		return fmt.Errorf("failed to marshal event: %w", err)
	}

	// Use creation timestamp if available, otherwise use current time
	var timestamp int64
	if event.Metadata.CreationTimestamp != nil {
		timestamp = event.Metadata.CreationTimestamp.UnixMicro()
	} else {
		timestamp = time.Now().UnixMicro()
	}

	if err := c.queueProducer.Enqueue(ctx, payload, timestamp); err != nil {
		return fmt.Errorf("failed to enqueue event: %w", err)
	}

	log.WithField("orgId", orgID).
		WithField("kind", event.InvolvedObject.Kind).
		WithField("name", event.InvolvedObject.Name).
		Debug("Enqueued event")
	return nil
}

// LaunchConsumers starts the specified number of queue consumers for imagebuild jobs
func LaunchConsumers(
	ctx context.Context,
	queuesProvider queues.Provider,
	store imagebuilderstore.Store,
	mainStore store.Store,
	kvStore kvstore.KVStore,
	serviceHandler *service.ServiceHandler,
	imageBuilderService imagebuilderapi.Service,
	cfg *config.Config,
	log logrus.FieldLogger,
) error {
	maxConcurrentBuilds := cfg.ImageBuilderWorker.MaxConcurrentBuilds
	log.Infof("Launching %d imagebuild queue consumers", maxConcurrentBuilds)

	// Create queue producer for consumer (used for requeueing related ImageExports)
	consumerQueueProducer, err := queuesProvider.NewQueueProducer(ctx, consts.ImageBuildTaskQueue)
	if err != nil {
		return fmt.Errorf("failed to create queue producer for consumer: %w", err)
	}

	taskConsumer := NewConsumer(store, mainStore, kvStore, serviceHandler, imageBuilderService, consumerQueueProducer, cfg, log)

	for i := 0; i < maxConcurrentBuilds; i++ {
		consumer, err := queuesProvider.NewQueueConsumer(ctx, consts.ImageBuildTaskQueue)
		if err != nil {
			return fmt.Errorf("failed to create queue consumer %d: %w", i, err)
		}
		if err = consumer.Consume(ctx, taskConsumer.Consume); err != nil {
			return fmt.Errorf("failed to start consumer %d: %w", i, err)
		}
	}

	// Run requeue task once on startup
	go taskConsumer.RunRequeueOnStartup(ctx)

	// Start periodic timeout check task
	go taskConsumer.runPeriodicTimeoutCheck(ctx)

	log.Info("All imagebuild queue consumers started")
	return nil
}

// runPeriodicTimeoutCheck runs the periodic timeout check task loop
func (c *Consumer) runPeriodicTimeoutCheck(ctx context.Context) {
	// Get interval from config (defaults are set when config is created)
	interval := time.Duration(c.cfg.ImageBuilderWorker.TimeoutCheckTaskInterval)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Run immediately on startup
	c.executeTimeoutCheck(ctx)

	for {
		select {
		case <-ctx.Done():
			c.log.Info("Periodic timeout check task stopped")
			return
		case <-ticker.C:
			c.executeTimeoutCheck(ctx)
		}
	}
}

// executeTimeoutCheck performs the timeout check for all organizations
func (c *Consumer) executeTimeoutCheck(ctx context.Context) {
	log := c.log.WithField("task", "timeout-check")
	log.Debug("Starting periodic timeout check task")

	// Get timeout duration from config
	if c.cfg == nil || c.cfg.ImageBuilderWorker == nil {
		log.Error("Config or ImageBuilderWorker config is nil, skipping timeout check")
		return
	}
	timeoutDuration := time.Duration(c.cfg.ImageBuilderWorker.ImageBuilderTimeout)
	if timeoutDuration <= 0 {
		log.WithField("timeoutDuration", timeoutDuration).Error("Invalid timeout duration, skipping timeout check")
		return
	}

	// List all organizations
	orgs, err := c.mainStore.Organization().List(ctx, store.ListParams{})
	if err != nil {
		log.WithError(err).Error("Failed to list organizations")
		return
	}

	totalFailed := 0
	for _, org := range orgs {
		failedCount, err := c.CheckAndMarkTimeoutsForOrg(ctx, org.ID, timeoutDuration, log)
		if err != nil {
			log.WithError(err).WithField("orgId", org.ID).Error("Failed to check timeouts for organization")
			continue
		}
		totalFailed += failedCount
	}

	if totalFailed > 0 {
		log.WithField("movedToFail", totalFailed).Info("Periodic timeout check task completed")
	} else {
		log.Debug("Periodic timeout check task completed - no resources moved to fail")
	}
}
