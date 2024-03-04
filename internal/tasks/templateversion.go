package tasks

import (
	"context"
	"fmt"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/reqid"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
)

func TemplateVersionCreated(taskManager TaskManager) {
	for {
		select {
		case <-taskManager.ctx.Done():
			taskManager.log.Info("Received ctx.Done(), stopping")
			return
		case resourceRef := <-taskManager.channels[ChannelTemplateVersion]:
			requestID := reqid.NextRequestID()
			ctx := context.WithValue(context.Background(), middleware.RequestIDKey, requestID)
			log := log.WithReqIDFromCtx(ctx, taskManager.log)
			err := SyncFleetTemplateToTemplateVersion(ctx, log, taskManager, resourceRef)
			if err != nil {
				log.Errorf("failed syncing template to template version: %v", err)
			}
		}
	}
}

func SyncFleetTemplateToTemplateVersion(ctx context.Context, log logrus.FieldLogger, taskManager TaskManager, resourceRef ResourceReference) error {
	log.Infof("Syncing template of %s to template version %s/%s", resourceRef.Owner, resourceRef.OrgID, resourceRef.Name)
	ownerType, fleetName, err := util.GetResourceOwner(&resourceRef.Owner)
	if err != nil {
		return err
	}
	if ownerType != model.FleetKind {
		return nil
	}

	templateVersion, err := taskManager.store.TemplateVersion().Get(ctx, resourceRef.OrgID, fleetName, resourceRef.Name)
	if err != nil {
		return fmt.Errorf("failed fetching templateVersion: %w", err)
	}

	fleet, err := taskManager.store.Fleet().Get(ctx, resourceRef.OrgID, fleetName)
	if err != nil {
		return fmt.Errorf("failed fetching fleet: %w", err)
	}

	templateVersion.Status = &api.TemplateVersionStatus{Os: &api.DeviceOSSpec{}}
	if fleet.Spec.Template.Spec.Os != nil {
		templateVersion.Status.Os.Image, err = getConcreteOsImage(fleet.Spec.Template.Spec.Os.Image)
		if err != nil {
			return fmt.Errorf("failed getting concrete image for %s: %w", fleet.Spec.Template.Spec.Os.Image, err)
		}
	}
	if fleet.Spec.Template.Spec.Config != nil {
		newConfigs := []api.TemplateVersionStatus_Config_Item{}
		for i := range *fleet.Spec.Template.Spec.Config {
			configItem := (*fleet.Spec.Template.Spec.Config)[i]
			disc, err := configItem.Discriminator()
			if err != nil {
				return fmt.Errorf("failed getting discriminator: %w", err)
			}
			var newConfig *api.TemplateVersionStatus_Config_Item
			switch disc {
			case string(api.TemplateDiscriminatorGitConfig):
				newConfig, err = handleGitConfig(taskManager, resourceRef.OrgID, &configItem)
			case string(api.TemplateDiscriminatorKubernetesSec):
				newConfig, err = handleK8sConfig(&configItem)
			case string(api.TemplateDiscriminatorInlineConfig):
				newConfig, err = handleInlineConfig(&configItem)
			default:
				return fmt.Errorf("unsupported discriminator %s", disc)
			}
			if err != nil {
				return err
			}
			newConfigs = append(newConfigs, *newConfig)
		}

		templateVersion.Status.Config = &newConfigs
	}

	return taskManager.store.TemplateVersion().UpdateStatus(taskManager.ctx, resourceRef.OrgID, templateVersion)
}

func getConcreteOsImage(image string) (string, error) {
	return image, nil
}

// Translate branch or tag into hash
func handleGitConfig(tm TaskManager, orgId uuid.UUID, configItem *api.DeviceSpec_Config_Item) (*api.TemplateVersionStatus_Config_Item, error) {
	gitSpec, err := configItem.AsGitConfigProviderSpec()
	if err != nil {
		return nil, fmt.Errorf("failed getting config item as GitConfigProviderSpec: %w", err)
	}

	repo, err := tm.store.Repository().GetInternal(tm.ctx, orgId, gitSpec.GitRef.Repository)
	if err != nil {
		return nil, fmt.Errorf("failed fetching specified Repository definition %s/%s: %w", orgId, gitSpec.GitRef.Repository, err)
	}

	_, hash, err := CloneGitRepo(repo, &gitSpec.GitRef.TargetRevision, util.IntToPtr(1))
	if err != nil {
		return nil, fmt.Errorf("failed cloning specified git repository %s/%s: %w", orgId, gitSpec.GitRef.Repository, err)
	}
	gitSpec.GitRef.TargetRevision = hash

	newConfig := &api.TemplateVersionStatus_Config_Item{}
	err = newConfig.FromGitConfigProviderSpec(gitSpec)
	if err != nil {
		return nil, fmt.Errorf("failed setting config item %s as GitConfigProviderSpec: %w", gitSpec.Name, err)
	}
	return newConfig, nil
}

// Simply copy the config
func handleInlineConfig(configItem *api.DeviceSpec_Config_Item) (*api.TemplateVersionStatus_Config_Item, error) {
	inlineSpec, err := configItem.AsInlineConfigProviderSpec()
	if err != nil {
		return nil, fmt.Errorf("failed getting config item as InlineConfigProviderSpec: %w", err)
	}
	newConfig := &api.TemplateVersionStatus_Config_Item{}
	err = newConfig.FromInlineConfigProviderSpec(inlineSpec)
	if err != nil {
		return nil, fmt.Errorf("failed setting config item %s as InlineConfigProviderSpec: %w", inlineSpec.Name, err)
	}
	return newConfig, nil
}

// Simply copy the config
func handleK8sConfig(configItem *api.DeviceSpec_Config_Item) (*api.TemplateVersionStatus_Config_Item, error) {
	return nil, fmt.Errorf("service does not yet support kubernetes config")
}
