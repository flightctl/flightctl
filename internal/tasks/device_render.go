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
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/reqid"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
	"sigs.k8s.io/yaml"
)

func deviceRender(ctx context.Context, resourceRef *ResourceReference, store store.Store, callbackManager CallbackManager, log logrus.FieldLogger) error {
	logic := NewDeviceRenderLogic(callbackManager, log, store, *resourceRef)
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
	return nil
}

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
	callbackManager CallbackManager
	log             logrus.FieldLogger
	store           store.Store
	resourceRef     ResourceReference
}

func NewDeviceRenderLogic(callbackManager CallbackManager, log logrus.FieldLogger, store store.Store, resourceRef ResourceReference) DeviceRenderLogic {
	return DeviceRenderLogic{callbackManager: callbackManager, log: log, store: store, resourceRef: resourceRef}
}

func (t *DeviceRenderLogic) RenderDevice(ctx context.Context) error {
	t.log.Infof("Rendering device %s/%s", t.resourceRef.OrgID, t.resourceRef.Name)

	device, err := t.store.Device().Get(ctx, t.resourceRef.OrgID, t.resourceRef.Name)
	if err != nil {
		return fmt.Errorf("failed getting device %s/%s: %w", t.resourceRef.OrgID, t.resourceRef.Name, err)
	}

	// If device.Spec or device.Spec.Config are nil, we still want to render an empty ignition config
	var config *[]api.DeviceSpec_Config_Item
	if device.Spec != nil {
		config = device.Spec.Config
	}

	renderedConfig, repoNames, renderErr := renderConfig(ctx, t.resourceRef.OrgID, t.store, config, false)

	// Set the many-to-many relationship with the repos (we do this even if the render failed so that we will
	// render the device again if the repository is updated, and then it might be fixed).
	// This only applies to devices that don't belong to a fleet, because otherwise the fleet will be
	// notified about changes to the repository.
	if device.Metadata.Owner == nil || *device.Metadata.Owner == "" {
		err = t.store.Device().OverwriteRepositoryRefs(ctx, t.resourceRef.OrgID, *device.Metadata.Name, repoNames...)
		if err != nil {
			return t.setStatus(ctx, fmt.Errorf("setting repository references: %w", err))
		}
	}

	if renderErr != nil {
		return t.setStatus(ctx, renderErr)
	}

	err = t.store.Device().UpdateRendered(ctx, t.resourceRef.OrgID, t.resourceRef.Name, string(renderedConfig))
	return t.setStatus(ctx, err)
}

func (t *DeviceRenderLogic) setStatus(ctx context.Context, renderErr error) error {
	condition := api.Condition{Type: api.DeviceSpecValid}

	if renderErr == nil {
		condition.Status = api.ConditionStatusTrue
		condition.Reason = "Valid"
	} else {
		condition.Status = api.ConditionStatusFalse
		condition.Reason = "Invalid"
		condition.Message = renderErr.Error()
	}

	err := t.store.Device().SetServiceConditions(ctx, t.resourceRef.OrgID, t.resourceRef.Name, []api.Condition{condition})
	if err != nil {
		t.log.Errorf("Failed setting condition for device %s/%s: %v", t.resourceRef.OrgID, t.resourceRef.Name, err)
	}
	return renderErr
}

type renderConfigArgs struct {
	orgId          uuid.UUID
	store          store.Store
	ignitionConfig *config_latest_types.Config
	repoNames      []string
	validateOnly   bool
}

func renderConfig(ctx context.Context, orgId uuid.UUID, store store.Store, config *[]api.DeviceSpec_Config_Item, validateOnly bool) (renderedConfig []byte, repoNames []string, err error) {
	args := renderConfigArgs{}
	emptyIgnitionConfig, _, _ := config_latest.ParseCompatibleVersion([]byte("{\"ignition\": {\"version\": \"3.4.0\"}"))
	args.ignitionConfig = &emptyIgnitionConfig
	args.validateOnly = validateOnly
	args.orgId = orgId
	args.store = store

	err = renderConfigItems(ctx, config, &args)
	if err != nil {
		return nil, args.repoNames, err
	}

	if validateOnly {
		return nil, args.repoNames, nil
	}

	renderedConfig, err = json.Marshal(args.ignitionConfig)
	if err != nil {
		return nil, args.repoNames, fmt.Errorf("failed marshalling configuration: %w", err)
	}

	return renderedConfig, args.repoNames, nil
}

func renderConfigItems(ctx context.Context, config *[]api.DeviceSpec_Config_Item, args *renderConfigArgs) error {
	if config == nil {
		return nil
	}

	invalidConfigs := []string{}
	var firstError error
	for i := range *config {
		configItem := (*config)[i]
		name, err := renderConfigItem(ctx, &configItem, args)
		if err != nil {
			if errors.Is(err, ErrUnknownConfigName) {
				name = "<unknown>"
			}
			invalidConfigs = append(invalidConfigs, name)
			if len(invalidConfigs) == 1 {
				firstError = err
			}
		}
	}

	if len(invalidConfigs) != 0 {
		configurationStr := "configuration"
		errorStr := "Error"
		if len(invalidConfigs) > 1 {
			configurationStr += "s"
			errorStr = "First error"
		}
		return fmt.Errorf("%d invalid %s: %s. %s: %v", len(invalidConfigs), configurationStr, strings.Join(invalidConfigs, ", "), errorStr, firstError)
	}

	return nil
}

func renderConfigItem(ctx context.Context, configItem *api.DeviceSpec_Config_Item, args *renderConfigArgs) (string, error) {
	disc, err := configItem.Discriminator()
	if err != nil {
		return "", fmt.Errorf("%w: failed getting discriminator: %w", ErrUnknownConfigName, err)
	}

	switch disc {
	case string(api.TemplateDiscriminatorGitConfig):
		return renderGitConfig(ctx, configItem, args)
	case string(api.TemplateDiscriminatorKubernetesSec):
		return renderK8sConfig(configItem)
	case string(api.TemplateDiscriminatorInlineConfig):
		return renderInlineConfig(configItem, args)
	default:
		return "", fmt.Errorf("%w: unsupported discriminator: %s", ErrUnknownConfigName, disc)
	}
}

func renderGitConfig(ctx context.Context, configItem *api.DeviceSpec_Config_Item, args *renderConfigArgs) (string, error) {
	gitSpec, err := configItem.AsGitConfigProviderSpec()
	if err != nil {
		return "", fmt.Errorf("%w: failed getting config item as GitConfigProviderSpec: %w", ErrUnknownConfigName, err)
	}

	args.repoNames = append(args.repoNames, gitSpec.GitRef.Repository)
	repo, err := args.store.Repository().GetInternal(ctx, args.orgId, gitSpec.GitRef.Repository)
	if err != nil {
		return gitSpec.Name, fmt.Errorf("failed fetching specified Repository definition %s/%s: %w", args.orgId, gitSpec.GitRef.Repository, err)
	}

	if repo.Spec == nil {
		return gitSpec.Name, fmt.Errorf("empty Repository definition %s/%s: %w", args.orgId, gitSpec.GitRef.Repository, err)
	}

	if args.validateOnly {
		return gitSpec.Name, nil
	}

	// TODO: Use local cache
	mfs, _, err := CloneGitRepo(repo, &gitSpec.GitRef.TargetRevision, nil)
	if err != nil {
		return gitSpec.Name, fmt.Errorf("failed cloning specified git repository %s/%s: %w", args.orgId, gitSpec.GitRef.Repository, err)
	}

	// Create an ignition from the git subtree and merge it into the rendered config
	ignitionConfig, err := ConvertFileSystemToIgnition(mfs, gitSpec.GitRef.Path)
	if err != nil {
		return gitSpec.Name, fmt.Errorf("failed parsing git config item %s: %w", gitSpec.Name, err)
	}
	mergedConfig := config_latest.Merge(*args.ignitionConfig, *ignitionConfig)
	args.ignitionConfig = &mergedConfig

	return gitSpec.Name, nil
}

// TODO: implement
func renderK8sConfig(configItem *api.DeviceSpec_Config_Item) (string, error) {
	k8sSpec, err := configItem.AsKubernetesSecretProviderSpec()
	if err != nil {
		return "", fmt.Errorf("%w: failed getting config item as KubernetesSecretProviderSpec: %w", ErrUnknownConfigName, err)
	}

	return k8sSpec.Name, fmt.Errorf("kubernetes config type not yet supported")
}

func renderInlineConfig(configItem *api.DeviceSpec_Config_Item, args *renderConfigArgs) (string, error) {
	inlineSpec, err := configItem.AsInlineConfigProviderSpec()
	if err != nil {
		return "", fmt.Errorf("%w: failed getting config item as InlineConfigProviderSpec: %w", ErrUnknownConfigName, err)
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

	// If we are validating and parameters are present, the ignition conversion will fail.
	if args.validateOnly {
		if !ContainsParameter(jsonBytes) {
			_, _, err = config_latest.ParseCompatibleVersion(jsonBytes)
			if err != nil {
				return inlineSpec.Name, fmt.Errorf("failed parsing inline config item %s: %w", inlineSpec.Name, err)
			}
		}
		return inlineSpec.Name, nil
	}

	if !args.validateOnly {
		// Merge the ignition into the rendered config
		ignitionConfig, _, err := config_latest.ParseCompatibleVersion(jsonBytes)
		if err != nil {
			return inlineSpec.Name, fmt.Errorf("failed parsing inline config item %s: %w", inlineSpec.Name, err)
		}
		mergedConfig := config_latest.Merge(*args.ignitionConfig, ignitionConfig)
		args.ignitionConfig = &mergedConfig
	}
	return inlineSpec.Name, nil
}
