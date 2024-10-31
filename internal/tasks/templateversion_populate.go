package tasks

import (
	"context"
	"errors"
	"fmt"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/pkg/k8sclient"
	"github.com/sirupsen/logrus"
)

func templateVersionPopulate(ctx context.Context, resourceRef *ResourceReference, store store.Store, callbackManager CallbackManager, k8sClient k8sclient.K8SClient, log logrus.FieldLogger) error {
	logic := NewTemplateVersionPopulateLogic(callbackManager, log, store, k8sClient, *resourceRef)
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

type TemplateVersionPopulateLogic struct {
	callbackManager    CallbackManager
	log                logrus.FieldLogger
	store              store.Store
	k8sClient          k8sclient.K8SClient
	resourceRef        ResourceReference
	templateVersion    *api.TemplateVersion
	fleet              *api.Fleet
	frozenConfig       []api.ConfigProviderSpec
	frozenApplications []api.ApplicationSpec
}

func NewTemplateVersionPopulateLogic(callbackManager CallbackManager, log logrus.FieldLogger, store store.Store, k8sClient k8sclient.K8SClient, resourceRef ResourceReference) TemplateVersionPopulateLogic {
	return TemplateVersionPopulateLogic{callbackManager: callbackManager, log: log, store: store, resourceRef: resourceRef, k8sClient: k8sClient}
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

	// freeze the config source
	if t.fleet.Spec.Template.Spec.Config != nil {
		t.frozenConfig = []api.ConfigProviderSpec{}

		for i := range *t.fleet.Spec.Template.Spec.Config {
			configItem := (*t.fleet.Spec.Template.Spec.Config)[i]
			err := t.handleConfigItem(ctx, &configItem)
			if err != nil {
				return t.setStatus(ctx, err)
			}
		}
	}

	// freeze applications source
	if t.fleet.Spec.Template.Spec.Applications != nil {
		t.frozenApplications = []api.ApplicationSpec{}
		for i := range *t.fleet.Spec.Template.Spec.Applications {
			app := (*t.fleet.Spec.Template.Spec.Applications)[i]
			err := t.handleApplication(app)
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

func (t *TemplateVersionPopulateLogic) handleConfigItem(ctx context.Context, configItem *api.ConfigProviderSpec) error {
	configType, err := configItem.Type()
	if err != nil {
		return fmt.Errorf("failed getting config type: %w", err)
	}

	switch configType {
	case api.GitConfigProviderType:
		return t.handleGitConfig(configItem)
	case api.KubernetesSecretProviderType:
		return t.handleK8sConfig(configItem)
	case api.InlineConfigProviderType:
		return t.handleInlineConfig(configItem)
	case api.HttpConfigProviderType:
		return t.handleHttpConfig(configItem)
	default:
		return fmt.Errorf("unsupported config type %q", configType)
	}
}

func (t *TemplateVersionPopulateLogic) handleApplication(app api.ApplicationSpec) error {
	providerType, err := app.Type()
	if err != nil {
		return fmt.Errorf("failed getting application type: %w", err)
	}

	switch providerType {
	case api.ImageApplicationProviderType:
		return t.handleImageApplicationProvider(app)
	// Add other application providers here
	default:
		return fmt.Errorf("unsupported application provider type %s", providerType)
	}
}

func (t *TemplateVersionPopulateLogic) handleImageApplicationProvider(app api.ApplicationSpec) error {
	// Add the image-based application as is to maintain the frozen pattern,
	// since the service won't handle image payloads directly.
	t.frozenApplications = append(t.frozenApplications, app)
	return nil
}

// Translate branch or tag into hash
func (t *TemplateVersionPopulateLogic) handleGitConfig(configItem *api.ConfigProviderSpec) error {
	gitSpec, err := configItem.AsGitConfigProviderSpec()
	if err != nil {
		return fmt.Errorf("failed getting config item as GitConfigProviderSpec: %w", err)
	}

	newConfig := &api.ConfigProviderSpec{}
	err = newConfig.FromGitConfigProviderSpec(gitSpec)
	if err != nil {
		return fmt.Errorf("failed creating git config from item %s: %w", gitSpec.Name, err)
	}
	t.frozenConfig = append(t.frozenConfig, *newConfig)

	return nil
}

func (t *TemplateVersionPopulateLogic) handleK8sConfig(configItem *api.ConfigProviderSpec) error {
	if t.k8sClient == nil {
		return errors.New("k8s client is not available: skipping handling kubernetes secret configuration")
	}
	k8sSpec, err := configItem.AsKubernetesSecretProviderSpec()
	if err != nil {
		return fmt.Errorf("failed getting config item as KubernetesSecretProviderSpec: %w", err)
	}

	newConfig := &api.ConfigProviderSpec{}
	err = newConfig.FromKubernetesSecretProviderSpec(k8sSpec)
	if err != nil {
		return fmt.Errorf("failed creating k8s secret config from item %s: %w", k8sSpec.Name, err)
	}
	t.frozenConfig = append(t.frozenConfig, *newConfig)
	return nil
}

func (t *TemplateVersionPopulateLogic) handleInlineConfig(configItem *api.ConfigProviderSpec) error {
	inlineSpec, err := configItem.AsInlineConfigProviderSpec()
	if err != nil {
		return fmt.Errorf("failed getting config item as InlineConfigProviderSpec: %w", ErrUnknownConfigName)
	}

	for _, file := range inlineSpec.Inline {
		if file.User != nil && ContainsParameter([]byte(*file.User)) {
			return fmt.Errorf("parameters in user field of inline configuration are not supported")
		}

		if file.Group != nil && ContainsParameter([]byte(*file.Group)) {
			return fmt.Errorf("parameters in group field of inline configuration are not supported")
		}
	}

	// Just add the inline config as-is
	newConfig := &api.ConfigProviderSpec{}
	err = newConfig.FromInlineConfigProviderSpec(inlineSpec)
	if err != nil {
		return fmt.Errorf("failed creating inline config from item %s: %w", inlineSpec.Name, err)
	}

	t.frozenConfig = append(t.frozenConfig, *newConfig)
	return nil
}

func (t *TemplateVersionPopulateLogic) handleHttpConfig(configItem *api.ConfigProviderSpec) error {
	httpSpec, err := configItem.AsHttpConfigProviderSpec()
	if err != nil {
		return fmt.Errorf("failed getting config item as HttpConfigProviderSpec: %w", err)
	}

	// Just add the HTTP config as-is
	// TODO(MGMT-18498): Freeze the config
	newConfig := &api.ConfigProviderSpec{}
	err = newConfig.FromHttpConfigProviderSpec(httpSpec)
	if err != nil {
		return fmt.Errorf("failed creating HTTP config from item %s: %w", httpSpec.Name, err)
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
		t.templateVersion.Status.Systemd = t.fleet.Spec.Template.Spec.Systemd
		t.templateVersion.Status.Config = &t.frozenConfig
		t.templateVersion.Status.Hooks = t.fleet.Spec.Template.Spec.Hooks
		t.templateVersion.Status.Resources = t.fleet.Spec.Template.Spec.Resources
		t.templateVersion.Status.Applications = &t.frozenApplications
	}
	api.SetStatusConditionByError(&t.templateVersion.Status.Conditions, api.TemplateVersionValid, "Valid", "Invalid", validationErr)

	err := t.store.TemplateVersion().UpdateStatus(ctx, t.resourceRef.OrgID, t.templateVersion, util.BoolToPtr(validationErr == nil), t.callbackManager.TemplateVersionValidatedCallback)
	if err != nil {
		return fmt.Errorf("failed setting TemplateVersion status: %w", err)
	}
	return validationErr
}
