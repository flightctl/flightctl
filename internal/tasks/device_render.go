package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	config_latest "github.com/coreos/ignition/v2/config/v3_4"
	config_latest_types "github.com/coreos/ignition/v2/config/v3_4/types"
	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/reqid"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/sirupsen/logrus"
	"sigs.k8s.io/yaml"
)

func DeviceRender(taskManager TaskManager) {
	for {
		select {
		case <-taskManager.ctx.Done():
			taskManager.log.Info("Received ctx.Done(), stopping")
			return
		case resourceRef := <-taskManager.channels[ChannelDeviceRender]:
			requestID := reqid.NextRequestID()
			ctx := context.WithValue(context.Background(), middleware.RequestIDKey, requestID)
			log := log.WithReqIDFromCtx(ctx, taskManager.log)
			logic := NewDeviceRenderLogic(taskManager, log, taskManager.store, resourceRef)
			if resourceRef.Op == DeviceRenderOpUpdate {
				err := logic.RenderDevice(ctx)
				if err != nil {
					log.Errorf("failed rendering device %s/%s: %v", resourceRef.OrgID, resourceRef.Name, err)
				} else {
					log.Infof("completed rendering device %s/%s", resourceRef.OrgID, resourceRef.Name)
				}
			} else {
				log.Errorf("DeviceRender called with unexpected kind %s and op %s", resourceRef.Kind, resourceRef.Op)
			}
		}
	}
}

type DeviceRenderLogic struct {
	taskManager    TaskManager
	log            logrus.FieldLogger
	store          store.Store
	resourceRef    ResourceReference
	device         *api.Device
	renderedConfig *config_latest_types.Config
	repoNames      []string
}

func NewDeviceRenderLogic(taskManager TaskManager, log logrus.FieldLogger, store store.Store, resourceRef ResourceReference) DeviceRenderLogic {
	return DeviceRenderLogic{taskManager: taskManager, log: log, store: store, resourceRef: resourceRef}
}

func (t *DeviceRenderLogic) RenderDevice(ctx context.Context) error {
	t.log.Infof("Rendering device %s/%s", t.resourceRef.OrgID, t.resourceRef.Name)

	device, err := t.store.Device().Get(ctx, t.resourceRef.OrgID, t.resourceRef.Name)
	if err != nil {
		return err
	}
	t.device = device
	emptyIgnitionConfig, _, _ := config_latest.ParseCompatibleVersion([]byte("{\"ignition\": {\"version\": \"3.4.0\"}"))
	t.renderedConfig = &emptyIgnitionConfig

	invalidConfigs := []string{}
	if device.Spec != nil && device.Spec.Config != nil {
		for i := range *device.Spec.Config {
			configItem := (*device.Spec.Config)[i]
			name, err := t.handleConfigItem(ctx, &configItem)
			if err != nil {
				var unknownErr ErrUnknownConfigName
				if errors.As(err, &unknownErr) {
					name = "<unknown>"
				}
				invalidConfigs = append(invalidConfigs, name)
			}
			t.log.Errorf("failed rendering config %s for device %s/%s: %v", name, t.resourceRef.OrgID, t.resourceRef.Name, err)
		}
	}

	if device.Metadata.Owner == nil || *device.Metadata.Owner == "" {
		// Set the many-to-may relationship with the repos (we do this even if the validation failed so that we will
		// validate the fleet again if the repository is updated, and then it might be fixed)
		err = t.store.Device().OverwriteRepositoryRefs(ctx, t.resourceRef.OrgID, *device.Metadata.Name, t.repoNames...)
		if err != nil {
			return t.setStatus(ctx, fmt.Errorf("setting repository references: %w", err))
		}
	}

	if len(invalidConfigs) != 0 {
		configurationStr := "configuration"
		if len(invalidConfigs) > 1 {
			configurationStr += "s"
		}
		err = fmt.Errorf("device has %d invalid %s: %s", len(invalidConfigs), configurationStr, strings.Join(invalidConfigs, ", "))
		return t.setStatus(ctx, err)
	}

	configjson, err := json.Marshal(t.renderedConfig)
	if err != nil {
		return t.setStatus(ctx, err)
	}

	err = t.store.Device().UpdateRendered(ctx, t.resourceRef.OrgID, t.resourceRef.Name, string(configjson))
	return t.setStatus(ctx, err)
}

func (t *DeviceRenderLogic) handleConfigItem(ctx context.Context, configItem *api.DeviceSpec_Config_Item) (string, error) {

	disc, err := configItem.Discriminator()
	if err != nil {
		return "", NewUnknownConfigNameError(fmt.Errorf("failed getting discriminator: %w", err))
	}

	switch disc {
	case string(api.TemplateDiscriminatorGitConfig):
		return t.handleGitConfig(ctx, configItem)
	case string(api.TemplateDiscriminatorKubernetesSec):
		return t.handleK8sConfig(configItem)
	case string(api.TemplateDiscriminatorInlineConfig):
		return t.handleInlineConfig(configItem)
	default:
		return "", NewUnknownConfigNameError(fmt.Errorf("unsupported discriminator %s", disc))
	}
}

func (t *DeviceRenderLogic) handleGitConfig(ctx context.Context, configItem *api.DeviceSpec_Config_Item) (string, error) {
	gitSpec, err := configItem.AsGitConfigProviderSpec()
	if err != nil {
		return "", NewUnknownConfigNameError(fmt.Errorf("failed getting config item as GitConfigProviderSpec: %w", err))
	}

	t.repoNames = append(t.repoNames, gitSpec.GitRef.Repository)
	repo, err := t.store.Repository().GetInternal(ctx, t.resourceRef.OrgID, gitSpec.GitRef.Repository)
	if err != nil {
		return gitSpec.Name, fmt.Errorf("failed fetching specified Repository definition %s/%s: %w", t.resourceRef.OrgID, gitSpec.GitRef.Repository, err)
	}

	if repo.Spec == nil {
		return gitSpec.Name, fmt.Errorf("failed fetching specified Repository definition %s/%s: %w", t.resourceRef.OrgID, gitSpec.GitRef.Repository, err)
	}

	// TODO: Use local cache
	mfs, _, err := CloneGitRepo(repo, &gitSpec.GitRef.TargetRevision, nil)
	if err != nil {
		return gitSpec.Name, fmt.Errorf("failed cloning specified git repository %s/%s: %w", t.resourceRef.OrgID, gitSpec.GitRef.Repository, err)
	}

	// Create an ignition from the git subtree and merge it into the rendered config
	ignitionConfig, err := ConvertFileSystemToIgnition(mfs, gitSpec.GitRef.Path)
	if err != nil {
		return gitSpec.Name, fmt.Errorf("failed parsing git config item %s: %w", gitSpec.Name, err)
	}
	renderedConfig := config_latest.Merge(*t.renderedConfig, *ignitionConfig)
	t.renderedConfig = &renderedConfig

	return gitSpec.Name, nil
}

// TODO: implement
func (t *DeviceRenderLogic) handleK8sConfig(configItem *api.DeviceSpec_Config_Item) (string, error) {
	k8sSpec, err := configItem.AsKubernetesSecretProviderSpec()
	if err != nil {
		return "", NewUnknownConfigNameError(fmt.Errorf("failed getting config item as KubernetesSecretProviderSpec: %w", err))
	}

	return k8sSpec.Name, fmt.Errorf("service does not yet support kubernetes config")
}

func (t *DeviceRenderLogic) handleInlineConfig(configItem *api.DeviceSpec_Config_Item) (string, error) {
	inlineSpec, err := configItem.AsInlineConfigProviderSpec()
	if err != nil {
		return "", NewUnknownConfigNameError(fmt.Errorf("failed getting config item as InlineConfigProviderSpec: %w", err))
	}

	// Add this inline config into the unrendered config
	newConfig := &api.TemplateVersionStatus_Config_Item{}
	err = newConfig.FromInlineConfigProviderSpec(inlineSpec)
	if err != nil {
		return inlineSpec.Name, fmt.Errorf("failed creating inline config from item %s: %w", inlineSpec.Name, err)
	}

	// Convert yaml to json
	yamlBytes, err := yaml.Marshal(inlineSpec.Inline)
	if err != nil {
		return inlineSpec.Name, fmt.Errorf("invalid yaml in inline config item %s: %w", inlineSpec.Name, err)
	}
	jsonBytes, err := yaml.YAMLToJSON(yamlBytes)
	if err != nil {
		return inlineSpec.Name, fmt.Errorf("failed converting yaml to json in inline config item %s: %w", inlineSpec.Name, err)
	}

	// Convert to ignition and merge into the rendered config
	ignitionConfig, _, err := config_latest.ParseCompatibleVersion(jsonBytes)
	if err != nil {
		return inlineSpec.Name, fmt.Errorf("failed parsing inline config item %s: %w", inlineSpec.Name, err)
	}

	renderedConfig := config_latest.Merge(*t.renderedConfig, ignitionConfig)
	t.renderedConfig = &renderedConfig
	return inlineSpec.Name, nil
}

func (t *DeviceRenderLogic) setStatus(ctx context.Context, renderErr error) error {
	condition := api.Condition{Type: api.DeviceSpecValid}

	if renderErr == nil {
		condition.Status = api.ConditionStatusTrue
		condition.Reason = util.StrToPtr("Valid")
	} else {
		condition.Status = api.ConditionStatusFalse
		condition.Reason = util.StrToPtr("Invalid")
		condition.Message = util.StrToPtr(fmt.Sprintf("Device configuration is invalid: %v", renderErr))
	}

	err := t.store.Device().SetServiceConditions(ctx, t.resourceRef.OrgID, t.resourceRef.Name, []api.Condition{condition})
	if err != nil {
		t.log.Errorf("Failed setting condition for device %s/%s: %v", t.resourceRef.OrgID, t.resourceRef.Name, err)
	}
	return renderErr
}
