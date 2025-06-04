package tasks

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/instrumentation"
	"github.com/flightctl/flightctl/internal/kvstore"
	"github.com/flightctl/flightctl/internal/service"
	"github.com/flightctl/flightctl/internal/tasks_client"
	"github.com/flightctl/flightctl/pkg/k8sclient"
	"github.com/flightctl/flightctl/pkg/queues"
	"github.com/sirupsen/logrus"
	"go.opentelemetry.io/otel/attribute"
)

func dispatchTasks(serviceHandler service.Service, callbackManager tasks_client.CallbackManager, k8sClient k8sclient.K8SClient, kvStore kvstore.KVStore) queues.ConsumeHandler {
	return func(ctx context.Context, payload []byte, log logrus.FieldLogger) error {
		var reference tasks_client.ResourceReference
		if err := json.Unmarshal(payload, &reference); err != nil {
			log.WithError(err).Error("failed to unmarshal consume payload")
			return err
		}

		ctx, span := instrumentation.StartSpan(ctx, "flightctl/tasks", reference.TaskName)
		defer span.End()

		span.SetAttributes(
			attribute.String("reference.task_name", reference.TaskName),
			attribute.String("reference.op", reference.Op),
			attribute.String("reference.org_id", reference.OrgID.String()),
			attribute.String("reference.kind", reference.Kind),
			attribute.String("reference.name", reference.Name),
			attribute.String("reference.owner", reference.Owner),
		)

		log.Infof("dispatching task %s, op %s, kind %s, orgID %s, name %s",
			reference.TaskName, reference.Op, reference.Kind, reference.OrgID, reference.Name)
		switch reference.TaskName {
		case tasks_client.FleetRolloutTask:
			return fleetRollout(ctx, &reference, serviceHandler, callbackManager, log)
		case tasks_client.FleetSelectorMatchTask:
			return fleetSelectorMatching(ctx, &reference, serviceHandler, callbackManager, log)
		case tasks_client.FleetValidateTask:
			return fleetValidate(ctx, &reference, serviceHandler, callbackManager, k8sClient, log)
		case tasks_client.DeviceRenderTask:
			return deviceRender(ctx, &reference, serviceHandler, callbackManager, k8sClient, kvStore, log)
		case tasks_client.RepositoryUpdatesTask:
			return repositoryUpdate(ctx, &reference, serviceHandler, callbackManager, log)
		default:
			return fmt.Errorf("unexpected task name %s", reference.TaskName)
		}
	}
}

func LaunchConsumers(ctx context.Context,
	queuesProvider queues.Provider,
	serviceHandler service.Service,
	callbackManager tasks_client.CallbackManager,
	k8sClient k8sclient.K8SClient,
	kvStore kvstore.KVStore,
	numConsumers, threadsPerConsumer int) error {
	for i := 0; i != numConsumers; i++ {
		consumer, err := queuesProvider.NewConsumer(consts.TaskQueue)
		if err != nil {
			return err
		}
		for j := 0; j != threadsPerConsumer; j++ {
			if err = consumer.Consume(ctx, dispatchTasks(serviceHandler, callbackManager, k8sClient, kvStore)); err != nil {
				return err
			}
		}
	}
	return nil
}
