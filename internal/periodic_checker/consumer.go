package periodic

import (
	"context"
	"runtime/debug"
	"sync"

	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
)

const (
	DefaultConsumerCount = 5
)

func executeWithRecover(executor PeriodicTaskExecutor, ctx context.Context, log logrus.FieldLogger, taskType PeriodicTaskType, orgID uuid.UUID) {
	defer func() {
		if r := recover(); r != nil {
			log.WithFields(logrus.Fields{
				"panic":     r,
				"task_type": taskType,
				"org_id":    orgID.String(),
				"stack":     string(debug.Stack()),
			}).Error("task execution panic")
		}
	}()
	executor.Execute(ctx, log, orgID)
}

func (c *PeriodicTaskConsumer) processTask(ctx context.Context, reference PeriodicTaskReference) {
	c.log.WithFields(logrus.Fields{
		"task_type": reference.Type,
		"org_id":    reference.OrgID,
	}).Info("Consuming task")

	executor, exists := c.executors[reference.Type]
	if !exists {
		c.log.Errorf("no executor found for task type %s", reference.Type)
		return
	}

	executeWithRecover(executor, ctx, c.log, reference.Type, reference.OrgID)
}

type PeriodicTaskConsumer struct {
	channelManager *ChannelManager
	log            logrus.FieldLogger
	executors      map[PeriodicTaskType]PeriodicTaskExecutor
	consumerCount  int
	wg             sync.WaitGroup
	ctx            context.Context
	cancel         context.CancelFunc
}

type PeriodicTaskConsumerConfig struct {
	ChannelManager *ChannelManager
	Log            logrus.FieldLogger
	Executors      map[PeriodicTaskType]PeriodicTaskExecutor
	ConsumerCount  int
}

func NewPeriodicTaskConsumer(config PeriodicTaskConsumerConfig) *PeriodicTaskConsumer {
	if config.ConsumerCount == 0 {
		config.ConsumerCount = DefaultConsumerCount
	}

	return &PeriodicTaskConsumer{
		channelManager: config.ChannelManager,
		log:            config.Log,
		executors:      config.Executors,
		consumerCount:  config.ConsumerCount,
	}
}

// runConsumer runs a single consumer goroutine
func (c *PeriodicTaskConsumer) runConsumer(consumerID int) {
	defer c.wg.Done()

	c.log.Infof("Starting periodic task consumer %d", consumerID)

	for {
		select {
		case <-c.ctx.Done():
			c.log.Infof("Consumer %d stopped", consumerID)
			return
		default:
			taskRef, ok := c.channelManager.ConsumeTask(c.ctx)
			if !ok {
				// Channel closed or context cancelled
				c.log.Infof("Consumer %d stopped - channel closed", consumerID)
				return
			}
			c.processTask(c.ctx, taskRef)
		}
	}
}

func (c *PeriodicTaskConsumer) Start(ctx context.Context) error {
	c.ctx, c.cancel = context.WithCancel(ctx)

	// Start all consumer goroutines
	for i := 0; i < c.consumerCount; i++ {
		c.wg.Add(1)
		go c.runConsumer(i)
	}

	c.log.Infof("Started %d periodic task consumers", c.consumerCount)
	return nil
}

// Stop gracefully stops all consumers
func (c *PeriodicTaskConsumer) Stop() {
	c.log.Info("Stopping periodic task consumers...")

	// Cancel the context to signal all consumers to stop
	if c.cancel != nil {
		c.cancel()
	}

	// Wait for all goroutines to finish
	c.wg.Wait()
	c.log.Info("All periodic task consumers stopped")
}
