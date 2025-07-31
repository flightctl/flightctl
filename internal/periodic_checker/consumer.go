package periodic

import (
	"context"
	"encoding/json"
	"runtime/debug"
	"sync"

	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/pkg/queues"
	"github.com/sirupsen/logrus"
)

const (
	DefaultConsumerCount = 5
)

func executeWithRecover(executor PeriodicTaskExecutor, ctx context.Context, log logrus.FieldLogger, taskType PeriodicTaskType, orgID string) {
	defer func() {
		if r := recover(); r != nil {
			log.WithFields(logrus.Fields{
				"panic":     r,
				"task_type": taskType,
				"org_id":    orgID,
				"stack":     string(debug.Stack()),
			}).Error("task execution panic")
		}
	}()
	executor.Execute(ctx, log)
}

func consumeTasks(executors map[PeriodicTaskType]PeriodicTaskExecutor) queues.ConsumeHandler {
	return func(ctx context.Context, payload []byte, log logrus.FieldLogger) error {
		var reference PeriodicTaskReference
		if err := json.Unmarshal(payload, &reference); err != nil {
			log.WithError(err).Error("failed to unmarshal consume payload")
			return err
		}

		log.Infof("consuming %s task for organization %s", reference.Type, reference.OrgID)

		executor, exists := executors[reference.Type]
		if !exists {
			log.Errorf("no executor found for task type %s", reference.Type)
			return nil
		}

		// Add the orgID to the context
		ctx = util.WithOrganizationID(ctx, reference.OrgID)

		executeWithRecover(executor, ctx, log, reference.Type, reference.OrgID.String())

		return nil
	}
}

type PeriodicTaskConsumer struct {
	queuesProvider queues.Provider
	log            logrus.FieldLogger
	executors      map[PeriodicTaskType]PeriodicTaskExecutor
	consumers      []queues.Consumer
	wg             sync.WaitGroup
	ctx            context.Context
	cancel         context.CancelFunc
}

type PeriodicTaskConsumerConfig struct {
	QueuesProvider queues.Provider
	Log            logrus.FieldLogger
	Executors      map[PeriodicTaskType]PeriodicTaskExecutor
	ConsumerCount  int
}

func NewPeriodicTaskConsumer(config PeriodicTaskConsumerConfig) *PeriodicTaskConsumer {
	if config.ConsumerCount == 0 {
		config.ConsumerCount = DefaultConsumerCount
	}

	return &PeriodicTaskConsumer{
		queuesProvider: config.QueuesProvider,
		log:            config.Log,
		executors:      config.Executors,
		consumers:      make([]queues.Consumer, config.ConsumerCount),
	}
}

// runConsumer runs a single consumer goroutine
func (c *PeriodicTaskConsumer) runConsumer(consumerID int, consumer queues.Consumer) {
	defer c.wg.Done()

	c.log.Infof("Starting periodic task consumer %d", consumerID)

	if err := consumer.Consume(c.ctx, consumeTasks(c.executors)); err != nil {
		c.log.WithError(err).Errorf("Consumer %d failed to start", consumerID)
		return
	}

	<-c.ctx.Done()
	c.log.Infof("Consumer %d stopped", consumerID)
}

func (c *PeriodicTaskConsumer) Start(ctx context.Context) error {
	c.ctx, c.cancel = context.WithCancel(ctx)

	// Create all consumers first
	for i := 0; i < len(c.consumers); i++ {
		consumer, err := c.queuesProvider.NewConsumer(consts.PeriodicTaskQueue)
		if err != nil {
			// Clean up already created consumers
			for _, c := range c.consumers {
				if c != nil {
					c.Close()
				}
			}
			return err
		}
		c.consumers[i] = consumer
	}

	// Start all consumers
	for i, consumer := range c.consumers {
		c.wg.Add(1)
		go c.runConsumer(i, consumer)
	}

	c.log.Infof("Started %d periodic task consumers", len(c.consumers))
	return nil
}

// Stop gracefully stops all consumers
func (c *PeriodicTaskConsumer) Stop() {
	c.log.Info("Stopping periodic task consumers...")

	// Cancel the context to signal all consumers to stop
	if c.cancel != nil {
		c.cancel()
	}

	// Close all consumer instances
	for i, consumer := range c.consumers {
		if consumer != nil {
			c.log.Infof("Closing consumer %d", i)
			consumer.Close()
		}
	}

	// Wait for all goroutines to finish
	c.wg.Wait()
	c.log.Info("All periodic task consumers stopped")
}

// Wait blocks until all consumers have stopped
func (c *PeriodicTaskConsumer) Wait() {
	c.wg.Wait()
}
