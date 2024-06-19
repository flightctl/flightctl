package tasks

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/pkg/queues"
	"github.com/sirupsen/logrus"
)

const TaskQueue = "task-queue"

func DispatchCallbacks(store store.Store, callbackManager CallbackManager) queues.ConsumeHandler {
	return func(ctx context.Context, payload []byte, log logrus.FieldLogger) error {
		var reference ResourceReference
		if err := json.Unmarshal(payload, &reference); err != nil {
			log.WithError(err).Error("failed to unmarshal consume payload")
			return err
		}
		switch reference.TaskName {
		case FleetRolloutTask:
			return fleetRollout(ctx, &reference, store, callbackManager, log)
		case FleetSelectorMatchTask:
			return fleetSelectorMatching(ctx, &reference, store, callbackManager, log)
		case TemplateVersionPopulateTask:
			return templateVersionPopulate(ctx, &reference, store, callbackManager, log)
		case FleetValidateTask:
			return fleetValidate(ctx, &reference, store, callbackManager, log)
		case DeviceRenderTask:
			return deviceRender(ctx, &reference, store, callbackManager, log)
		case RepositoryUpdatesTask:
			return repositoryUpdate(ctx, &reference, store, callbackManager, log)
		default:
			return fmt.Errorf("unexpected task name %s", reference.TaskName)
		}
	}
}

func LaunchConsumers(ctx context.Context,
	provider queues.Provider,
	store store.Store,
	callbackManager CallbackManager,
	numConsumers, threadsPerConsumer int) error {
	for i := 0; i != numConsumers; i++ {
		consumer, err := provider.NewConsumer(TaskQueue)
		if err != nil {
			return err
		}
		for j := 0; j != threadsPerConsumer; j++ {
			if err = consumer.Consume(ctx, DispatchCallbacks(store, callbackManager)); err != nil {
				return err
			}
		}
	}
	return nil
}
