package tasks

import (
	"context"
	"fmt"
	"net/http"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/service"
	servicecommon "github.com/flightctl/flightctl/internal/service/common"
	"github.com/google/uuid"
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

func repositoryUpdate(ctx context.Context, orgId uuid.UUID, event api.Event, serviceHandler service.Service, log logrus.FieldLogger) error {
	logic := NewRepositoryUpdateLogic(log, serviceHandler, orgId, event)

	if err := logic.HandleRepositoryUpdate(ctx); err != nil {
		log.Errorf("failed to notify associated resources of update to repository %s/%s: %v", orgId, event.InvolvedObject.Name, err)
		return err
	}

	return nil
}

type RepositoryUpdateLogic struct {
	log            logrus.FieldLogger
	serviceHandler service.Service
	orgId          uuid.UUID
	event          api.Event
}

func NewRepositoryUpdateLogic(log logrus.FieldLogger, serviceHandler service.Service, orgId uuid.UUID, event api.Event) RepositoryUpdateLogic {
	return RepositoryUpdateLogic{log: log, serviceHandler: serviceHandler, orgId: orgId, event: event}
}

func (t *RepositoryUpdateLogic) HandleRepositoryUpdate(ctx context.Context) error {
	fleets, status := t.serviceHandler.GetRepositoryFleetReferences(ctx, t.orgId, t.event.InvolvedObject.Name)
	if status.Code != http.StatusOK {
		return fmt.Errorf("fetching fleets: %s", status.Message)
	}

	for _, fleet := range fleets.Items {
		t.serviceHandler.CreateEvent(ctx, t.orgId, servicecommon.GetReferencedRepositoryUpdatedEvent(ctx, api.FleetKind, *fleet.Metadata.Name, t.event.InvolvedObject.Name))
	}

	devices, status := t.serviceHandler.GetRepositoryDeviceReferences(ctx, t.orgId, t.event.InvolvedObject.Name)
	if status.Code != http.StatusOK {
		return fmt.Errorf("fetching devices: %s", status.Message)
	}

	for _, device := range devices.Items {
		t.serviceHandler.CreateEvent(ctx, t.orgId, servicecommon.GetReferencedRepositoryUpdatedEvent(ctx, api.DeviceKind, *device.Metadata.Name, t.event.InvolvedObject.Name))
	}

	return nil
}
