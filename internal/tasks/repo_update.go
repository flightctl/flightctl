package tasks

import (
	"context"
	"fmt"
	"net/http"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/service"
	"github.com/flightctl/flightctl/internal/tasks_client"
	"github.com/sirupsen/logrus"
)

// The repositoryUpdate task is triggered when a repository is updated or deleted.
// It notifies all fleets and devices that reference the repository so they can
// re-validate or re-process their configurations.
//
// For update events, it looks up all associated fleets and devices and triggers
// FleetSourceUpdated or DeviceSourceUpdated callbacks.
//
// This task is idempotent because it performs only read operations followed by
// conditional notifications. Re-executing the task results in the same callbacks
// being sent again, which is safe and intended. No persistent state is modified,
// and the callbacks themselves are assumed to be idempotent or safely repeatable.

func repositoryUpdate(ctx context.Context, resourceRef *tasks_client.ResourceReference, serviceHandler service.Service, callbackManager tasks_client.CallbackManager, log logrus.FieldLogger) error {
	logic := NewRepositoryUpdateLogic(callbackManager, log, serviceHandler, *resourceRef)

	switch {
	case resourceRef.Op == tasks_client.RepositoryUpdateOpUpdate && resourceRef.Kind == api.RepositoryKind:
		err := logic.HandleRepositoryUpdate(ctx)
		if err != nil {
			log.Errorf("failed to notify associated resources of update to repository %s/%s: %v", resourceRef.OrgID, resourceRef.Name, err)
		}
	default:
		log.Errorf("RepositoryUpdate called with unexpected kind %s and op %s", resourceRef.Kind, resourceRef.Op)
	}
	return nil
}

type RepositoryUpdateLogic struct {
	callbackManager tasks_client.CallbackManager
	log             logrus.FieldLogger
	serviceHandler  service.Service
	resourceRef     tasks_client.ResourceReference
}

func NewRepositoryUpdateLogic(callbackManager tasks_client.CallbackManager, log logrus.FieldLogger, serviceHandler service.Service, resourceRef tasks_client.ResourceReference) RepositoryUpdateLogic {
	return RepositoryUpdateLogic{callbackManager: callbackManager, log: log, serviceHandler: serviceHandler, resourceRef: resourceRef}
}

func (t *RepositoryUpdateLogic) HandleRepositoryUpdate(ctx context.Context) error {
	fleets, status := t.serviceHandler.GetRepositoryFleetReferences(ctx, t.resourceRef.Name)
	if status.Code != http.StatusOK {
		return fmt.Errorf("fetching fleets: %s", status.Message)
	}

	for _, fleet := range fleets.Items {
		t.callbackManager.FleetSourceUpdated(ctx, t.resourceRef.OrgID, *fleet.Metadata.Name)
	}

	devices, status := t.serviceHandler.GetRepositoryDeviceReferences(ctx, t.resourceRef.Name)
	if status.Code != http.StatusOK {
		return fmt.Errorf("fetching devices: %s", status.Message)
	}

	for _, device := range devices.Items {
		t.callbackManager.DeviceSourceUpdated(ctx, t.resourceRef.OrgID, *device.Metadata.Name)
	}

	return nil
}
