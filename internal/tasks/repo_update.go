package tasks

import (
	"context"
	"fmt"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/tasks_client"
	"github.com/sirupsen/logrus"
)

func repositoryUpdate(ctx context.Context, resourceRef *tasks_client.ResourceReference, store store.Store, callbackManager tasks_client.CallbackManager, log logrus.FieldLogger) error {
	logic := NewRepositoryUpdateLogic(callbackManager, log, store, *resourceRef)

	switch {
	case resourceRef.Op == tasks_client.RepositoryUpdateOpUpdate && resourceRef.Kind == api.RepositoryKind:
		err := logic.HandleRepositoryUpdate(ctx)
		if err != nil {
			log.Errorf("failed to notify associated resources of update to repository %s/%s: %v", resourceRef.OrgID, resourceRef.Name, err)
		}
	case resourceRef.Op == tasks_client.RepositoryUpdateOpDeleteAll && resourceRef.Kind == api.RepositoryKind:
		err := logic.HandleAllRepositoriesDeleted(ctx, log)
		if err != nil {
			log.Errorf("failed to notify associated resources deletion of all repositories in org %s: %v", resourceRef.OrgID, err)
		}
	default:
		log.Errorf("RepositoryUpdate called with unexpected kind %s and op %s", resourceRef.Kind, resourceRef.Op)
	}
	return nil
}

type RepositoryUpdateLogic struct {
	callbackManager tasks_client.CallbackManager
	log             logrus.FieldLogger
	store           store.Store
	resourceRef     tasks_client.ResourceReference
}

func NewRepositoryUpdateLogic(callbackManager tasks_client.CallbackManager, log logrus.FieldLogger, store store.Store, resourceRef tasks_client.ResourceReference) RepositoryUpdateLogic {
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

func (t *RepositoryUpdateLogic) doesConfigReferenceAnyRepo(configItems []api.ConfigProviderSpec) (bool, error) {
	for _, configItem := range configItems {
		configType, err := configItem.Type()
		if err != nil {
			return false, fmt.Errorf("failed getting config type: %w", err)
		}

		if configType != api.GitConfigProviderType {
			continue
		}

		return true, nil
	}

	return false, nil
}
