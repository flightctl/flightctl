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
	DefaultNumConsumers = 5
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
	numConsumers   int
	consumers      []queues.Consumer
	wg             sync.WaitGroup
	ctx            context.Context
	cancel         context.CancelFunc
}

func NewPeriodicTaskConsumer(queuesProvider queues.Provider, log logrus.FieldLogger, executors map[PeriodicTaskType]PeriodicTaskExecutor) *PeriodicTaskConsumer {
	return &PeriodicTaskConsumer{
		queuesProvider: queuesProvider,
		log:            log,
		executors:      executors,
		numConsumers:   DefaultNumConsumers,
		consumers:      make([]queues.Consumer, 0),
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
	consumers := make([]queues.Consumer, 0, c.numConsumers)
	for i := 0; i < c.numConsumers; i++ {
		consumer, err := c.queuesProvider.NewConsumer(consts.PeriodicTaskQueue)
		if err != nil {
			// Clean up already created consumers
			for _, c := range consumers {
				c.Close()
			}
			return err
		}
		consumers = append(consumers, consumer)
	}

	c.consumers = consumers

	// Start all consumers
	for i, consumer := range c.consumers {
		c.wg.Add(1)
		go c.runConsumer(i, consumer)
	}

	c.log.Infof("Started %d periodic task consumers", c.numConsumers)
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
