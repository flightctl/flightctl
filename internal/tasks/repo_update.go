package tasks

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/flightctl/flightctl/internal/domain"
	servicecommon "github.com/flightctl/flightctl/internal/service/common"
	eventservice "github.com/flightctl/flightctl/internal/service/event"
	repositoryservice "github.com/flightctl/flightctl/internal/service/repository"
	inspectionstore "github.com/flightctl/flightctl/internal/store/appinspection"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/google/uuid"
	"github.com/samber/lo"
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

func repositoryUpdate(ctx context.Context, orgId uuid.UUID, event domain.Event, repositorySvc repositoryservice.Service, eventSvc eventservice.Service, log logrus.FieldLogger) error {
	logic := NewRepositoryUpdateLogic(log, repositorySvc, eventSvc, orgId, event)

	if err := logic.HandleRepositoryUpdate(ctx); err != nil {
		log.Errorf("failed to notify associated resources of update to repository %s/%s: %v", orgId, event.InvolvedObject.Name, err)
		return err
	}

	return nil
}

// retryRepositoryInspections targets only unfinished acquisition of packages in
// this OCI scope. Complete content facts remain reusable across access changes.
func (t *DeviceRenderLogic) retryRepositoryInspections(ctx context.Context, orgID uuid.UUID, event domain.Event, eventSvc eventservice.Service) error {
	if t == nil || t.inspection == nil {
		return nil
	}
	repo, status := t.repositorySvc.GetRepository(ctx, orgID, event.InvolvedObject.Name)
	if status.Code == http.StatusNotFound {
		return nil
	}
	if status.Code != http.StatusOK {
		return fmt.Errorf("look up inspection repository: %s", status.Message)
	}
	spec, err := repo.Spec.AsOciRepoSpec()
	if err != nil || spec.Type != domain.OciRepoSpecTypeOci {
		return nil
	}
	scope, err := inspectionRepositoryScope(spec)
	if err != nil {
		return err
	}
	eventID, err := uuid.Parse(lo.FromPtr(event.Metadata.Name))
	if err != nil {
		return err
	}
	// Live notifications can omit the timestamp. Use the persisted time so
	// redelivery cannot fence claims acquired after the Repository changed.
	eventTime := event.Metadata.CreationTimestamp
	if eventTime == nil {
		list, status := eventSvc.ListEvents(ctx, orgID, domain.ListEventsParams{
			FieldSelector: lo.ToPtr("metadata.name=" + eventID.String()), Limit: lo.ToPtr(int32(1)),
		})
		if status.Code != http.StatusOK {
			return fmt.Errorf("look up inspection repair event: %s", status.Message)
		}
		if len(list.Items) != 0 {
			eventTime = list.Items[0].Metadata.CreationTimestamp
		}
	}
	if eventTime != nil {
		if err := t.inspection.store.FenceRepositoryClaims(ctx, orgID, scope, *eventTime); err != nil {
			return err
		}
	}
	return t.emitInspectionRetries(ctx, orgID, scope, eventID, event.InvolvedObject.Name, eventSvc)
}

func inspectionRepositoryScope(spec domain.OciRepoSpec) (inspectionstore.RepositoryScope, error) {
	repository := strings.Trim(lo.FromPtr(spec.Repository), "/")
	path := repository
	if path == "" {
		path = "placeholder"
	}
	ref, err := inspectionstore.NormalizeReference(inspectionstore.ReferenceInput{
		Reference: spec.Registry + "/" + path, Role: model.InspectionRolePackage, Mechanism: model.InspectionPullContainer,
	})
	if err != nil {
		return inspectionstore.RepositoryScope{}, err
	}
	scope := inspectionstore.RepositoryScope{Registry: ref.Registry, Port: ref.Port}
	if repository != "" {
		scope.Path, scope.Exact = ref.Path, true
	} else if namespace := strings.Trim(lo.FromPtr(spec.Namespace), "/"); namespace != "" {
		scope.Path = strings.Split(namespace, "/")
	}
	return scope, nil
}

func (t *DeviceRenderLogic) emitInspectionRetries(ctx context.Context, orgID uuid.UUID, scope inspectionstore.RepositoryScope, eventID uuid.UUID, repository string, eventSvc eventservice.Service) error {
	for after := ""; ; {
		names, err := t.inspection.store.ListDevicesAwaitingRepository(ctx, orgID, scope, after, inspectionWakeupBatch)
		if err != nil {
			return err
		}
		for _, name := range names {
			event := servicecommon.GetReferencedRepositoryUpdatedEvent(ctx, domain.DeviceKind, name, repository)
			event.Metadata.Name = lo.ToPtr(uuid.NewSHA1(eventID, []byte(name)).String())
			if err := eventSvc.CreateEventOnce(ctx, orgID, event); err != nil {
				return fmt.Errorf("emit inspection retry for device %s: %w", name, err)
			}
		}
		if len(names) < inspectionWakeupBatch {
			return nil
		}
		after = names[len(names)-1]
	}
}

type RepositoryUpdateLogic struct {
	log           logrus.FieldLogger
	repositorySvc repositoryservice.Service
	eventSvc      eventservice.Service
	orgId         uuid.UUID
	event         domain.Event
}

func NewRepositoryUpdateLogic(log logrus.FieldLogger, repositorySvc repositoryservice.Service, eventSvc eventservice.Service, orgId uuid.UUID, event domain.Event) RepositoryUpdateLogic {
	return RepositoryUpdateLogic{log: log, repositorySvc: repositorySvc, eventSvc: eventSvc, orgId: orgId, event: event}
}

func (t *RepositoryUpdateLogic) HandleRepositoryUpdate(ctx context.Context) error {
	fleets, status := t.repositorySvc.GetRepositoryFleetReferences(ctx, t.orgId, t.event.InvolvedObject.Name)
	if status.Code != http.StatusOK {
		return fmt.Errorf("fetching fleets: %s", status.Message)
	}

	for _, fleet := range fleets.Items {
		t.eventSvc.CreateEvent(ctx, t.orgId, servicecommon.GetReferencedRepositoryUpdatedEvent(ctx, domain.FleetKind, *fleet.Metadata.Name, t.event.InvolvedObject.Name))
	}

	devices, status := t.repositorySvc.GetRepositoryDeviceReferences(ctx, t.orgId, t.event.InvolvedObject.Name)
	if status.Code != http.StatusOK {
		return fmt.Errorf("fetching devices: %s", status.Message)
	}

	for _, device := range devices.Items {
		t.eventSvc.CreateEvent(ctx, t.orgId, servicecommon.GetReferencedRepositoryUpdatedEvent(ctx, domain.DeviceKind, *device.Metadata.Name, t.event.InvolvedObject.Name))
	}

	return nil
}
