package tasks

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	config_latest "github.com/coreos/ignition/v2/config/v3_4"
	config_latest_types "github.com/coreos/ignition/v2/config/v3_4/types"
	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/pkg/ignition"
	"github.com/flightctl/flightctl/pkg/k8sclient"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
)

func deviceRender(ctx context.Context, resourceRef *ResourceReference, store store.Store, callbackManager CallbackManager, k8sClient k8sclient.K8SClient, configStorage ConfigStorage, log logrus.FieldLogger) error {
	logic := NewDeviceRenderLogic(callbackManager, log, store, k8sClient, configStorage, *resourceRef)
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

type DeviceRenderLogic struct {
	callbackManager CallbackManager
	log             logrus.FieldLogger
	store           store.Store
	k8sClient       k8sclient.K8SClient
	configStorage   ConfigStorage
	resourceRef     ResourceReference
	ownerFleet      *string
	templateVersion *string
	deviceConfig    *[]api.ConfigProviderSpec
	applications    *[]api.ApplicationSpec
}

func NewDeviceRenderLogic(callbackManager CallbackManager, log logrus.FieldLogger, store store.Store, k8sClient k8sclient.K8SClient, configStorage ConfigStorage, resourceRef ResourceReference) DeviceRenderLogic {
	return DeviceRenderLogic{callbackManager: callbackManager, log: log, store: store, k8sClient: k8sClient, configStorage: configStorage, resourceRef: resourceRef}
}

func (t *DeviceRenderLogic) RenderDevice(ctx context.Context) error {
	t.log.Infof("Rendering device %s/%s", t.resourceRef.OrgID, t.resourceRef.Name)

	device, err := t.store.Device().Get(ctx, t.resourceRef.OrgID, t.resourceRef.Name)
	if err != nil {
		return fmt.Errorf("failed getting device %s/%s: %w", t.resourceRef.OrgID, t.resourceRef.Name, err)
	}

	// If device.Spec or device.Spec.Config are nil, we still want to render an empty ignition config
	if device.Spec != nil {
		t.deviceConfig = device.Spec.Config
		t.applications = device.Spec.Applications
	}

	if device.Metadata.Owner != nil {
		_, owner, err := util.GetResourceOwner(device.Metadata.Owner)
		if err != nil {
			return fmt.Errorf("failed getting device owner %s/%s: %w", t.resourceRef.OrgID, t.resourceRef.Name, err)
		}
		t.ownerFleet = &owner

		if device.Metadata.Annotations != nil {
			tvString := (*device.Metadata.Annotations)[model.DeviceAnnotationTemplateVersion]
			t.templateVersion = &tvString
		}
	}

	ignitionConfig, referencedRepos, renderErr := t.renderConfig(ctx)
	renderedConfig, err := json.Marshal(ignitionConfig)
	if err != nil {
		return fmt.Errorf("failed marshalling configuration: %w", err)
	}

	// Set the many-to-many relationship with the repos (we do this even if the render failed so that we will
	// render the device again if the repository is updated, and then it might be fixed).
	// This only applies to devices that don't belong to a fleet, because otherwise the fleet will be
	// notified about changes to the repository.
	if device.Metadata.Owner == nil || *device.Metadata.Owner == "" {
		err = t.store.Device().OverwriteRepositoryRefs(ctx, t.resourceRef.OrgID, *device.Metadata.Name, referencedRepos...)
		if err != nil {
			return t.setStatus(ctx, fmt.Errorf("setting repository references: %w", err))
		}
	}

	if renderErr != nil {
		return t.setStatus(ctx, renderErr)
	}

	renderedApplications, err := t.renderApplications(ctx)
	if err != nil {
		return t.setStatus(ctx, err)
	}

	err = t.store.Device().UpdateRendered(ctx, t.resourceRef.OrgID, t.resourceRef.Name, string(renderedConfig), string(renderedApplications))
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

func (t *DeviceRenderLogic) renderApplications(ctx context.Context) ([]byte, error) {
	if t.applications == nil {
		return nil, nil
	}

	var invalidApplications []string
	var renderedApplications []api.RenderedApplicationSpec
	var firstError error

	for i := range *t.applications {
		application := (*t.applications)[i]
		name, renderedApplication, renderErr := renderApplication(ctx, &application)
		applicationName := util.DefaultIfNil(name, "<unknown>")

		if paramErr := validateNoParametersInConfig(&application, t.ownerFleet != nil); paramErr != nil {
			// An error message regarding invalid parameters should take precedence
			// because it may be the cause of the render error
			renderErr = paramErr
		}

		// Append invalid configs only if there's an error
		if renderErr != nil {
			invalidApplications = append(invalidApplications, applicationName)
			if firstError == nil {
				firstError = renderErr
			}
		} else {
			if renderedApplication != nil {
				renderedApplications = append(renderedApplications, *renderedApplication)
			}
		}
	}

	if len(invalidApplications) > 0 {
		pluralSuffix := ""
		errorPrefix := "Error"
		if len(invalidApplications) > 1 {
			pluralSuffix = "s"
			errorPrefix = "First error"
		}
		return nil, fmt.Errorf("%d invalid application%s: %s. %s: %v", len(invalidApplications), pluralSuffix, strings.Join(invalidApplications, ", "), errorPrefix, firstError)
	}

	renderedApplicationBytes, err := json.Marshal(renderedApplications)
	if err != nil {
		return nil, fmt.Errorf("failed marshalling applications: %w", err)
	}

	return renderedApplicationBytes, nil
}

func (t *DeviceRenderLogic) renderConfig(ctx context.Context) (*config_latest_types.Config, []string, error) {
	ignitionConfig := &config_latest_types.Config{
		Ignition: config_latest_types.Ignition{
			Version: config_latest_types.MaxVersion.String(),
		},
	}

	if t.deviceConfig == nil {
		return ignitionConfig, nil, nil
	}

	invalidConfigs := []string{}
	referencedRepos := []string{}
	var firstError error
	for i := range *t.deviceConfig {
		configItem := (*t.deviceConfig)[i]
		name, repoName, err := t.renderConfigItem(ctx, &configItem, &ignitionConfig)
		paramErr := validateNoParametersInConfig(&configItem, t.ownerFleet != nil)

		if repoName != nil {
			referencedRepos = append(referencedRepos, *repoName)
		}

		// An error message regarding invalid parameters should take precedence
		// because it may be the cause of the render error
		if paramErr != nil {
			err = paramErr
		}

		if err != nil {
			invalidConfigs = append(invalidConfigs, util.DefaultIfNil(name, "<unknown>"))
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
		return nil, referencedRepos, fmt.Errorf("%d invalid %s: %s. %s: %v", len(invalidConfigs), configurationStr, strings.Join(invalidConfigs, ", "), errorStr, firstError)
	}

	return ignitionConfig, referencedRepos, nil
}

type RenderItem interface {
	MarshalJSON() ([]byte, error)
}

func validateNoParametersInConfig(item RenderItem, deviceBelongsToFleet bool) error {
	cfgJson, err := item.MarshalJSON()
	if err != nil {
		return fmt.Errorf("failed converting configuration to json: %w", err)
	}

	// If we're rendering the device config and it still has parameters, something went wrong
	if ContainsParameter(cfgJson) {
		if deviceBelongsToFleet {
			return fmt.Errorf("configuration contains parameter, perhaps due to a missing device label")
		} else {
			return fmt.Errorf("configuration contains parameter, but parameters can only be used in fleet templates")
		}
	}

	return nil
}

func (t *DeviceRenderLogic) renderConfigItem(ctx context.Context, configItem *api.ConfigProviderSpec, ignitionConfig **config_latest_types.Config) (*string, *string, error) {
	configType, err := configItem.Type()
	if err != nil {
		return nil, nil, fmt.Errorf("%w: failed getting config type: %w", ErrUnknownConfigName, err)
	}

	switch configType {
	case api.GitConfigProviderType:
		return t.renderGitConfig(ctx, configItem, ignitionConfig)
	case api.KubernetesSecretProviderType:
		return t.renderK8sConfig(configItem, ignitionConfig)
	case api.InlineConfigProviderType:
		return t.renderInlineConfig(configItem, ignitionConfig)
	case api.HttpConfigProviderType:
		return t.renderHttpProviderConfig(ctx, configItem, ignitionConfig)
	default:
		return nil, nil, fmt.Errorf("%w: unsupported config type %q", ErrUnknownConfigName, configType)
	}
}

func renderApplication(_ context.Context, app *api.ApplicationSpec) (*string, *api.RenderedApplicationSpec, error) {
	appType, err := app.Type()
	if err != nil {
		return nil, nil, fmt.Errorf("failed getting application type: %w", err)
	}
	switch appType {
	case api.ImageApplicationProviderType:
		return renderImageApplicationProvider(app)
	default:
		return nil, nil, fmt.Errorf("%w: unsupported application type: %q", ErrUnknownApplicationType, appType)
	}
}

func renderImageApplicationProvider(app *api.ApplicationSpec) (*string, *api.RenderedApplicationSpec, error) {
	imageProvider, err := app.AsImageApplicationProvider()
	if err != nil {
		return nil, nil, fmt.Errorf("%w: failed getting application as ImageApplicationProvider: %w", ErrUnknownApplicationType, err)
	}

	appName := util.FromPtr(app.Name)
	renderedApp := api.RenderedApplicationSpec{
		Name:    app.Name,
		EnvVars: app.EnvVars,
	}
	if err := renderedApp.FromImageApplicationProvider(imageProvider); err != nil {
		return &appName, nil, fmt.Errorf("failed rendering application %s: %w", appName, err)
	}

	return &appName, &renderedApp, nil
}

func (t *DeviceRenderLogic) renderGitConfig(ctx context.Context, configItem *api.ConfigProviderSpec, ignitionConfig **config_latest_types.Config) (*string, *string, error) {
	gitSpec, err := configItem.AsGitConfigProviderSpec()
	if err != nil {
		return nil, nil, fmt.Errorf("%w: failed getting config item as GitConfigProviderSpec: %w", ErrUnknownConfigName, err)
	}

	repo, err := t.store.Repository().GetInternal(ctx, t.resourceRef.OrgID, gitSpec.GitRef.Repository)
	if err != nil {
		return &gitSpec.Name, &gitSpec.GitRef.Repository, fmt.Errorf("failed fetching specified Repository definition %s/%s: %w", t.resourceRef.OrgID, gitSpec.GitRef.Repository, err)
	}

	if repo.Spec == nil {
		return &gitSpec.Name, &gitSpec.GitRef.Repository, fmt.Errorf("empty Repository definition %s/%s: %w", t.resourceRef.OrgID, gitSpec.GitRef.Repository, err)
	}

	repoURL, err := repo.Spec.Data.GetRepoURL()
	if err != nil {
		return &gitSpec.Name, &gitSpec.GitRef.Repository, fmt.Errorf("failed fetching git repository URL %s/%s: %w", t.resourceRef.OrgID, gitSpec.GitRef.Repository, err)
	}

	var gitHash string

	// If the device is part of a fleet, we need to make sure the repo and targetRevision are frozen
	if t.ownerFleet != nil && t.templateVersion != nil {
		repoKey := ConfigStorageRepositoryUrlKey{
			OrgID:           t.resourceRef.OrgID,
			Fleet:           *t.ownerFleet,
			TemplateVersion: *t.templateVersion,
			Repository:      gitSpec.GitRef.Repository,
		}
		origRepoURL, err := t.configStorage.StoreIfNotExistsAndFetch(ctx, repoKey.ComposeKey(), []byte(repoURL))
		if err != nil {
			return &gitSpec.Name, &gitSpec.GitRef.Repository, fmt.Errorf("failed getting repository url for %s/%s: %w", t.resourceRef.OrgID, gitSpec.GitRef.Repository, err)
		}
		if repoURL != string(origRepoURL) {
			t.log.Warnf("repository URL updated from %s to %s for %s/%s", origRepoURL, repoURL, t.resourceRef.OrgID, gitSpec.GitRef.Repository)
			err = repo.Spec.Data.MergeGenericRepoSpec(api.GenericRepoSpec{Url: string(origRepoURL)})
			if err != nil {
				return &gitSpec.Name, &gitSpec.GitRef.Repository, fmt.Errorf("failed updating changed repository url for %s/%s: %w", t.resourceRef.OrgID, gitSpec.GitRef.Repository, err)
			}
		}

		gitRevisionKey := ConfigStorageGitRevisionKey{
			OrgID:           t.resourceRef.OrgID,
			Fleet:           *t.ownerFleet,
			TemplateVersion: *t.templateVersion,
			Repository:      gitSpec.GitRef.Repository,
			TargetRevision:  gitSpec.GitRef.TargetRevision,
		}
		hashBytes, err := t.configStorage.Fetch(ctx, gitRevisionKey.ComposeKey())
		if err != nil {
			return &gitSpec.Name, &gitSpec.GitRef.Repository, fmt.Errorf("failed fetching frozen git revision %s/%s: %w", t.resourceRef.OrgID, gitSpec.GitRef.Repository, err)
		}
		gitHash = string(hashBytes)
	}

	gitRevision := gitSpec.GitRef.TargetRevision
	if gitHash != "" {
		gitRevision = gitHash
	}

	mfs, clonedHash, err := CloneGitRepo(repo, &gitRevision, nil)
	if err != nil {
		return &gitSpec.Name, &gitSpec.GitRef.Repository, fmt.Errorf("failed cloning specified git repository %s/%s: %w", t.resourceRef.OrgID, gitSpec.GitRef.Repository, err)
	}

	if gitHash == "" && t.ownerFleet != nil && t.templateVersion != nil {
		gitRevisionKey := ConfigStorageGitRevisionKey{
			OrgID:           t.resourceRef.OrgID,
			Fleet:           *t.ownerFleet,
			TemplateVersion: *t.templateVersion,
			Repository:      gitSpec.GitRef.Repository,
			TargetRevision:  gitSpec.GitRef.TargetRevision,
		}
		err := t.configStorage.StoreIfNotExists(ctx, gitRevisionKey.ComposeKey(), []byte(clonedHash))
		if err != nil {
			return &gitSpec.Name, &gitSpec.GitRef.Repository, fmt.Errorf("failed storing frozen git revision %s/%s: %w", t.resourceRef.OrgID, gitSpec.GitRef.Repository, err)
		}
	}

	// Create an ignition from the git subtree and merge it into the rendered config
	ignition, err := ConvertFileSystemToIgnition(mfs, gitSpec.GitRef.Path, lo.FromPtr(gitSpec.GitRef.MountPath))
	if err != nil {
		return &gitSpec.Name, &gitSpec.GitRef.Repository, fmt.Errorf("failed parsing git config item %s: %w", gitSpec.Name, err)
	}
	mergedConfig := config_latest.Merge(**ignitionConfig, *ignition)
	*ignitionConfig = &mergedConfig

	return &gitSpec.Name, &gitSpec.GitRef.Repository, nil
}

func (t *DeviceRenderLogic) renderK8sConfig(configItem *api.ConfigProviderSpec, ignitionConfig **config_latest_types.Config) (*string, *string, error) {
	k8sSpec, err := configItem.AsKubernetesSecretProviderSpec()
	if err != nil {
		return nil, nil, fmt.Errorf("%w: failed getting config item as KubernetesSecretProviderSpec: %w", ErrUnknownConfigName, err)
	}
	if t.k8sClient == nil {
		return &k8sSpec.Name, nil, fmt.Errorf("kubernetes API is not available")
	}
	secret, err := t.k8sClient.GetSecret(k8sSpec.SecretRef.Namespace, k8sSpec.SecretRef.Name)
	if err != nil {
		return &k8sSpec.Name, nil, fmt.Errorf("failed getting secret %s/%s: %w", k8sSpec.SecretRef.Namespace, k8sSpec.SecretRef.Name, err)
	}
	ignitionWrapper, err := ignition.NewWrapper()
	if err != nil {
		return &k8sSpec.Name, nil, fmt.Errorf("failed to create ignition wrapper: %w", err)
	}
	splits := filepath.SplitList(k8sSpec.SecretRef.MountPath)
	for name, contents := range secret.Data {
		ignitionWrapper.SetFile(filepath.Join(append(splits, name)...), contents, 0o644, false, nil, nil)
	}

	*ignitionConfig = lo.ToPtr(ignitionWrapper.Merge(**ignitionConfig))
	return &k8sSpec.Name, nil, nil
}

func (t *DeviceRenderLogic) renderInlineConfig(configItem *api.ConfigProviderSpec, ignitionConfig **config_latest_types.Config) (*string, *string, error) {
	inlineSpec, err := configItem.AsInlineConfigProviderSpec()
	if err != nil {
		return nil, nil, fmt.Errorf("%w: failed getting config item as InlineConfigProviderSpec: %w", ErrUnknownConfigName, err)
	}

	ignitionWrapper, err := ignition.NewWrapper()
	if err != nil {
		return &inlineSpec.Name, nil, fmt.Errorf("failed to create ignition wrapper: %w", err)
	}

	for _, file := range inlineSpec.Inline {
		mode := 0o644
		if file.Mode != nil {
			mode = *file.Mode
		}

		isBase64 := false
		if file.ContentEncoding != nil && *file.ContentEncoding == api.Base64 {
			isBase64 = true
		}
		ignitionWrapper.SetFile(file.Path, []byte(file.Content), mode, isBase64, file.User, file.Group)
	}

	*ignitionConfig = lo.ToPtr(ignitionWrapper.Merge(**ignitionConfig))
	return &inlineSpec.Name, nil, nil
}

func (t *DeviceRenderLogic) renderHttpProviderConfig(ctx context.Context, configItem *api.ConfigProviderSpec, ignitionConfig **config_latest_types.Config) (*string, *string, error) {
	httpConfigProviderSpec, err := configItem.AsHttpConfigProviderSpec()
	if err != nil {
		return nil, nil, fmt.Errorf("%w: failed getting config item as HttpConfigProviderSpec: %w", ErrUnknownConfigName, err)
	}
	repo, err := t.store.Repository().GetInternal(ctx, t.resourceRef.OrgID, httpConfigProviderSpec.HttpRef.Repository)
	if err != nil {
		return &httpConfigProviderSpec.Name, &httpConfigProviderSpec.HttpRef.Repository, fmt.Errorf("failed fetching specified Repository definition %s/%s: %w", t.resourceRef.OrgID, httpConfigProviderSpec.HttpRef.Repository, err)
	}
	if repo.Spec == nil {
		return &httpConfigProviderSpec.Name, &httpConfigProviderSpec.HttpRef.Repository, fmt.Errorf("empty Repository definition %s/%s: %w", t.resourceRef.OrgID, httpConfigProviderSpec.HttpRef.Repository, err)
	}
	repoURL, err := repo.Spec.Data.GetRepoURL()
	if err != nil {
		return &httpConfigProviderSpec.Name, &httpConfigProviderSpec.HttpRef.Repository, err
	}

	// Append the suffix only if exists (as it's optional)
	if httpConfigProviderSpec.HttpRef.Suffix != nil {
		repoURL = repoURL + *httpConfigProviderSpec.HttpRef.Suffix
	}

	body, err := sendHTTPrequest(repo.Spec.Data, repoURL)
	if err != nil {
		return &httpConfigProviderSpec.Name, &httpConfigProviderSpec.HttpRef.Repository, fmt.Errorf("sending HTTP Request")
	}

	// Convert body to ignition config
	ignitionWrapper, err := ignition.NewWrapper()
	if err != nil {
		return &httpConfigProviderSpec.Name, &httpConfigProviderSpec.HttpRef.Repository, fmt.Errorf("failed to create ignition wrapper: %w", err)
	}

	ignitionWrapper.SetFile(httpConfigProviderSpec.HttpRef.FilePath, body, 0o644, false, nil, nil)
	*ignitionConfig = lo.ToPtr(ignitionWrapper.Merge(**ignitionConfig))

	return &httpConfigProviderSpec.Name, &httpConfigProviderSpec.HttpRef.Repository, nil
}
