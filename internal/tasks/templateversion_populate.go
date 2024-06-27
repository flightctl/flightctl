package tasks

import (
	"context"
	"fmt"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/reqid"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/sirupsen/logrus"
)

func templateVersionPopulate(ctx context.Context, resourceRef *ResourceReference, store store.Store, callbackManager CallbackManager, log logrus.FieldLogger) error {
	logic := NewTemplateVersionPopulateLogic(callbackManager, log, store, *resourceRef)
	if resourceRef.Op == TemplateVersionPopulateOpCreated {
		err := logic.SyncFleetTemplateToTemplateVersion(ctx)
		if err != nil {
			log.Errorf("failed populating template version %s/%s: %v", resourceRef.OrgID, resourceRef.Name, err)
		}
	} else {
		log.Errorf("TemplateVersionPopulate called with unexpected kind %s and op %s", resourceRef.Kind, resourceRef.Op)
	}
	return nil
}

func TemplateVersionPopulate(taskManager TaskManager) {
	for {
		select {
		case <-taskManager.ctx.Done():
			taskManager.log.Info("Received ctx.Done(), stopping")
			return
		case resourceRef := <-taskManager.channels[ChannelTemplateVersionPopulate]:
			requestID := reqid.NextRequestID()
			ctx := context.WithValue(context.Background(), middleware.RequestIDKey, requestID)
			log := log.WithReqIDFromCtx(ctx, taskManager.log)
			logic := NewTemplateVersionPopulateLogic(taskManager, log, taskManager.store, resourceRef)
			if resourceRef.Op == TemplateVersionPopulateOpCreated {
				err := logic.SyncFleetTemplateToTemplateVersion(ctx)
				if err != nil {
					log.Errorf("failed populating template version %s/%s: %v", resourceRef.OrgID, resourceRef.Name, err)
				}
			} else {
				log.Errorf("TemplateVersionPopulate called with unexpected kind %s and op %s", resourceRef.Kind, resourceRef.Op)
			}
		}
	}
}

type TemplateVersionPopulateLogic struct {
	callbackManager CallbackManager
	log             logrus.FieldLogger
	store           store.Store
	resourceRef     ResourceReference
	templateVersion *api.TemplateVersion
	fleet           *api.Fleet
	frozenConfig    []api.TemplateVersionStatus_Config_Item
}

func NewTemplateVersionPopulateLogic(callbackManager CallbackManager, log logrus.FieldLogger, store store.Store, resourceRef ResourceReference) TemplateVersionPopulateLogic {
	return TemplateVersionPopulateLogic{callbackManager: callbackManager, log: log, store: store, resourceRef: resourceRef}
}

func (t *TemplateVersionPopulateLogic) SyncFleetTemplateToTemplateVersion(ctx context.Context) error {
	t.log.Infof("Syncing template of %s to template version %s/%s", t.resourceRef.Owner, t.resourceRef.OrgID, t.resourceRef.Name)
	err := t.getFleetAndTemplateVersion(ctx)
	if t.templateVersion == nil {
		if err != nil {
			return err
		}
		// non-fleet owner
		return nil
	}
	if err != nil {
		return t.setStatus(ctx, err)
	}

	if t.fleet.Spec.Template.Spec.Config != nil {
		t.frozenConfig = []api.TemplateVersionStatus_Config_Item{}

		for i := range *t.fleet.Spec.Template.Spec.Config {
			configItem := (*t.fleet.Spec.Template.Spec.Config)[i]
			err := t.handleConfigItem(ctx, &configItem)
			if err != nil {
				return t.setStatus(ctx, err)
			}
		}
	}
	return t.setStatus(ctx, nil)
}

func (t *TemplateVersionPopulateLogic) getFleetAndTemplateVersion(ctx context.Context) error {
	ownerType, fleetName, err := util.GetResourceOwner(&t.resourceRef.Owner)
	if err != nil {
		return err
	}
	if ownerType != model.FleetKind {
		return nil
	}

	templateVersion, err := t.store.TemplateVersion().Get(ctx, t.resourceRef.OrgID, fleetName, t.resourceRef.Name)
	if err != nil {
		return fmt.Errorf("failed fetching templateVersion: %w", err)
	}
	t.templateVersion = templateVersion

	fleet, err := t.store.Fleet().Get(ctx, t.resourceRef.OrgID, fleetName)
	if err != nil {
		return fmt.Errorf("failed fetching fleet: %w", err)
	}
	t.fleet = fleet

	return nil
}

func (t *TemplateVersionPopulateLogic) handleConfigItem(ctx context.Context, configItem *api.DeviceSpec_Config_Item) error {
	disc, err := configItem.Discriminator()
	if err != nil {
		return fmt.Errorf("failed getting discriminator: %w", err)
	}

	switch disc {
	case string(api.TemplateDiscriminatorGitConfig):
		return t.handleGitConfig(ctx, configItem)
	case string(api.TemplateDiscriminatorKubernetesSec):
		return t.handleK8sConfig(configItem)
	case string(api.TemplateDiscriminatorInlineConfig):
		return t.handleInlineConfig(configItem)
	default:
		return fmt.Errorf("unsupported discriminator %s", disc)
	}
}

// Translate branch or tag into hash
func (t *TemplateVersionPopulateLogic) handleGitConfig(ctx context.Context, configItem *api.DeviceSpec_Config_Item) error {
	gitSpec, err := configItem.AsGitConfigProviderSpec()
	if err != nil {
		return fmt.Errorf("failed getting config item as GitConfigProviderSpec: %w", err)
	}

	repo, err := t.store.Repository().GetInternal(ctx, t.resourceRef.OrgID, gitSpec.GitRef.Repository)
	if err != nil {
		return fmt.Errorf("failed fetching specified Repository definition %s/%s: %w", t.resourceRef.OrgID, gitSpec.GitRef.Repository, err)
	}

	if repo.Spec == nil {
		return fmt.Errorf("empty Repository definition %s/%s: %w", t.resourceRef.OrgID, gitSpec.GitRef.Repository, err)
	}

	if ContainsParameter([]byte(gitSpec.GitRef.TargetRevision)) {
		return fmt.Errorf("parameters in TargetRevision are not currently supported")
	}

	// TODO: Use local cache
	_, hash, err := CloneGitRepo(repo, &gitSpec.GitRef.TargetRevision, util.IntToPtr(1))
	if err != nil {
		return fmt.Errorf("failed cloning specified git repository %s/%s: %w", t.resourceRef.OrgID, gitSpec.GitRef.Repository, err)
	}

	// Add this git hash into the frozen config
	gitSpec.GitRef.TargetRevision = hash
	newConfig := &api.TemplateVersionStatus_Config_Item{}
	err = newConfig.FromGitConfigProviderSpec(gitSpec)
	if err != nil {
		return fmt.Errorf("failed creating git config from item %s: %w", gitSpec.Name, err)
	}
	t.frozenConfig = append(t.frozenConfig, *newConfig)

	return nil
}

// TODO: implement
func (t *TemplateVersionPopulateLogic) handleK8sConfig(configItem *api.DeviceSpec_Config_Item) error {
	return fmt.Errorf("service does not yet support kubernetes config")
}

func (t *TemplateVersionPopulateLogic) handleInlineConfig(configItem *api.DeviceSpec_Config_Item) error {
	inlineSpec, err := configItem.AsInlineConfigProviderSpec()
	if err != nil {
		return fmt.Errorf("failed getting config item as InlineConfigProviderSpec: %w", err)
	}

	// Just add the inline config as-is
	newConfig := &api.TemplateVersionStatus_Config_Item{}
	err = newConfig.FromInlineConfigProviderSpec(inlineSpec)
	if err != nil {
		return fmt.Errorf("failed creating inline config from item %s: %w", inlineSpec.Name, err)
	}

	t.frozenConfig = append(t.frozenConfig, *newConfig)
	return nil
}

func (t *TemplateVersionPopulateLogic) setStatus(ctx context.Context, validationErr error) error {
	t.templateVersion.Status = &api.TemplateVersionStatus{}
	if validationErr != nil {
		t.log.Errorf("failed syncing template to template version: %v", validationErr)
	} else {
		t.templateVersion.Status.Os = t.fleet.Spec.Template.Spec.Os
		t.templateVersion.Status.Containers = t.fleet.Spec.Template.Spec.Containers
		t.templateVersion.Status.Systemd = t.fleet.Spec.Template.Spec.Systemd
		t.templateVersion.Status.Config = &t.frozenConfig
	}

	t.templateVersion.Status.Conditions = []api.Condition{}
	api.SetStatusConditionByError(&t.templateVersion.Status.Conditions, api.TemplateVersionValid, "Valid", "Invalid", validationErr)

	err := t.store.TemplateVersion().UpdateStatus(ctx, t.resourceRef.OrgID, t.templateVersion, util.BoolToPtr(validationErr == nil), t.callbackManager.TemplateVersionValidatedCallback)
	if err != nil {
		return fmt.Errorf("failed setting TemplateVersion status: %w", err)
	}
	return validationErr
}
