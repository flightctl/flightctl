package periodic

import (
	"context"
	"encoding/json"

	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/pkg/queues"
	"github.com/sirupsen/logrus"
)

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

		executor.Execute(ctx, log)

		return nil
	}
}

type PeriodicTaskConsumer struct {
	queuesProvider queues.Provider
	log            logrus.FieldLogger
	executors      map[PeriodicTaskType]PeriodicTaskExecutor
}

func NewPeriodicTaskConsumer(queuesProvider queues.Provider, log logrus.FieldLogger, executors map[PeriodicTaskType]PeriodicTaskExecutor) *PeriodicTaskConsumer {
	return &PeriodicTaskConsumer{
		queuesProvider: queuesProvider,
		log:            log,
		executors:      executors,
	}
}

func (c *PeriodicTaskConsumer) Start(ctx context.Context) error {
	consumer, err := c.queuesProvider.NewConsumer(consts.PeriodicTaskQueue)
	if err != nil {
		return err
	}

	if err = consumer.Consume(ctx, consumeTasks(c.executors)); err != nil {
		return err
	}

	return nil
}
