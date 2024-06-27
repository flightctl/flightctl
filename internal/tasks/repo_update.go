package tasks

import (
	"context"
	"fmt"

	"github.com/flightctl/flightctl/api/v1alpha1"
	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/reqid"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/sirupsen/logrus"
)

func repositoryUpdate(ctx context.Context, resourceRef *ResourceReference, store store.Store, callbackManager CallbackManager, log logrus.FieldLogger) error {
	logic := NewRepositoryUpdateLogic(callbackManager, log, store, *resourceRef)

	switch {
	case resourceRef.Op == RepositoryUpdateOpUpdate && resourceRef.Kind == model.RepositoryKind:
		err := logic.HandleRepositoryUpdate(ctx)
		if err != nil {
			log.Errorf("failed to notify associated resources of update to repository %s/%s: %v", resourceRef.OrgID, resourceRef.Name, err)
		}
	case resourceRef.Op == RepositoryUpdateOpDeleteAll && resourceRef.Kind == model.RepositoryKind:
		err := logic.HandleAllRepositoriesDeleted(ctx, log)
		if err != nil {
			log.Errorf("failed to notify associated resources deletion of all repositories in org %s: %v", resourceRef.OrgID, err)
		}
	default:
		log.Errorf("RepositoryUpdate called with unexpected kind %s and op %s", resourceRef.Kind, resourceRef.Op)
	}
	return nil
}

func RepositoryUpdate(taskManager TaskManager) {
	for {
		select {
		case <-taskManager.ctx.Done():
			taskManager.log.Info("Received ctx.Done(), stopping")
			return
		case resourceRef := <-taskManager.channels[ChannelRepositoryUpdates]:
			requestID := reqid.NextRequestID()
			ctx := context.WithValue(context.Background(), middleware.RequestIDKey, requestID)
			log := log.WithReqIDFromCtx(ctx, taskManager.log)
			logic := NewRepositoryUpdateLogic(taskManager, log, taskManager.store, resourceRef)

			switch {
			case resourceRef.Op == RepositoryUpdateOpUpdate && resourceRef.Kind == model.RepositoryKind:
				err := logic.HandleRepositoryUpdate(ctx)
				if err != nil {
					log.Errorf("failed to notify associated resources of update to repository %s/%s: %v", resourceRef.OrgID, resourceRef.Name, err)
				}
			case resourceRef.Op == RepositoryUpdateOpDeleteAll && resourceRef.Kind == model.RepositoryKind:
				err := logic.HandleAllRepositoriesDeleted(ctx, log)
				if err != nil {
					log.Errorf("failed to notify associated resources deletion of all repositories in org %s: %v", resourceRef.OrgID, err)
				}
			default:
				log.Errorf("RepositoryUpdate called with unexpected kind %s and op %s", resourceRef.Kind, resourceRef.Op)
			}
		}
	}
}

type RepositoryUpdateLogic struct {
	callbackManager CallbackManager
	log             logrus.FieldLogger
	store           store.Store
	resourceRef     ResourceReference
}

func NewRepositoryUpdateLogic(callbackManager CallbackManager, log logrus.FieldLogger, store store.Store, resourceRef ResourceReference) RepositoryUpdateLogic {
	return RepositoryUpdateLogic{callbackManager: callbackManager, log: log, store: store, resourceRef: resourceRef}
}

func (t *RepositoryUpdateLogic) HandleRepositoryUpdate(ctx context.Context) error {
	fleets, err := t.store.Repository().GetFleetRefs(ctx, t.resourceRef.OrgID, t.resourceRef.Name)
	if err != nil {
		return fmt.Errorf("fetching fleets: %w", err)
	}

	for _, fleet := range fleets.Items {
		t.callbackManager.FleetSourceUpdated(t.resourceRef.OrgID, *fleet.Metadata.Name)
	}

	devices, err := t.store.Repository().GetDeviceRefs(ctx, t.resourceRef.OrgID, t.resourceRef.Name)
	if err != nil {
		return fmt.Errorf("fetching devices: %w", err)
	}

	for _, device := range devices.Items {
		t.callbackManager.DeviceSourceUpdated(t.resourceRef.OrgID, *device.Metadata.Name)
	}

	return nil
}

func (t *RepositoryUpdateLogic) HandleAllRepositoriesDeleted(ctx context.Context, log logrus.FieldLogger) error {
	fleets, err := t.store.Fleet().List(ctx, t.resourceRef.OrgID, store.ListParams{})
	if err != nil {
		return fmt.Errorf("fetching fleets: %w", err)
	}

	for _, fleet := range fleets.Items {
		hasReference, err := t.doesConfigReferenceAnyRepo(*fleet.Spec.Template.Spec.Config)
		if err != nil {
			log.Errorf("failed checking if fleet %s/%s references any repo: %v", t.resourceRef.OrgID, *fleet.Metadata.Name, err)
			continue
		}

		if hasReference {
			t.callbackManager.FleetSourceUpdated(t.resourceRef.OrgID, *fleet.Metadata.Name)
		}
	}

	devices, err := t.store.Device().List(ctx, t.resourceRef.OrgID, store.ListParams{})
	if err != nil {
		return fmt.Errorf("fetching devices: %w", err)
	}

	for _, device := range devices.Items {
		hasReference, err := t.doesConfigReferenceAnyRepo(*device.Spec.Config)
		if err != nil {
			log.Errorf("failed checking if device %s/%s references any repo: %v", t.resourceRef.OrgID, *device.Metadata.Name, err)
			continue
		}

		if hasReference {
			t.callbackManager.DeviceSourceUpdated(t.resourceRef.OrgID, *device.Metadata.Name)
		}
	}

	return nil
}

func (t *RepositoryUpdateLogic) doesConfigReferenceAnyRepo(configItems []v1alpha1.DeviceSpec_Config_Item) (bool, error) {
	for _, configItem := range configItems {
		disc, err := configItem.Discriminator()
		if err != nil {
			return false, fmt.Errorf("failed getting discriminator: %w", err)
		}

		if disc != string(api.TemplateDiscriminatorGitConfig) {
			continue
		}

		return true, nil
	}

	return false, nil
}
