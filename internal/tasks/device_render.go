package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	config_latest "github.com/coreos/ignition/v2/config/v3_4"
	config_latest_types "github.com/coreos/ignition/v2/config/v3_4/types"
	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/pkg/ignition"
	"github.com/flightctl/flightctl/pkg/k8sclient"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
)

func deviceRender(ctx context.Context, resourceRef *ResourceReference, store store.Store, callbackManager CallbackManager, k8sClient k8sclient.K8SClient, log logrus.FieldLogger) error {
	logic := NewDeviceRenderLogic(callbackManager, log, store, k8sClient, *resourceRef)
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
	resourceRef     ResourceReference
}

func NewDeviceRenderLogic(callbackManager CallbackManager, log logrus.FieldLogger, store store.Store, k8sClient k8sclient.K8SClient, resourceRef ResourceReference) DeviceRenderLogic {
	return DeviceRenderLogic{callbackManager: callbackManager, log: log, store: store, k8sClient: k8sClient, resourceRef: resourceRef}
}

func (t *DeviceRenderLogic) RenderDevice(ctx context.Context) error {
	t.log.Infof("Rendering device %s/%s", t.resourceRef.OrgID, t.resourceRef.Name)

	device, err := t.store.Device().Get(ctx, t.resourceRef.OrgID, t.resourceRef.Name)
	if err != nil {
		return fmt.Errorf("failed getting device %s/%s: %w", t.resourceRef.OrgID, t.resourceRef.Name, err)
	}

	// If device.Spec or device.Spec.Config are nil, we still want to render an empty ignition config
	var config *[]api.ConfigProviderSpec
	if device.Spec != nil {
		config = device.Spec.Config
	}

	renderedConfig, repoNames, renderErr := renderConfig(ctx, t.resourceRef.OrgID, t.store, t.k8sClient, config, !util.IsEmptyString(device.Metadata.Owner), false)

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

	renderedApplications, err := renderApplications(ctx, t.store, t.resourceRef.OrgID, device.Spec.Applications, !util.IsEmptyString(device.Metadata.Owner), false)
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

type renderConfigArgs struct {
	orgId                uuid.UUID
	store                store.Store
	k8sClient            k8sclient.K8SClient
	ignitionConfig       *config_latest_types.Config
	repoNames            []string
	validateOnly         bool
	deviceBelongsToFleet bool
}

type renderApplicationArgs struct {
	orgId                uuid.UUID
	store                store.Store
	applications         []api.RenderedApplicationSpec
	validateOnly         bool
	deviceBelongsToFleet bool
}

func renderApplications(ctx context.Context, store store.Store, orgId uuid.UUID, applications *[]api.ApplicationSpec, deviceBelongsToFleet bool, validateOnly bool) (renderedApplications []byte, err error) {
	if applications == nil {
		return nil, nil
	}

	args := renderApplicationArgs{
		orgId:                orgId,
		store:                store,
		deviceBelongsToFleet: deviceBelongsToFleet,
		validateOnly:         validateOnly,
	}

	var invalidApplications []string
	var firstError error

	for i := range *applications {
		application := (*applications)[i]
		var applicationName string
		name, renderErr := renderApplication(ctx, &application, &args)
		if errors.Is(renderErr, ErrUnknownApplicationType) {
			applicationName = "<unknown>"
		} else {
			applicationName = name
		}

		if paramErr := validateParameters(&application, args.validateOnly, args.deviceBelongsToFleet); paramErr != nil {
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

	renderedApplications, err = json.Marshal(&args.applications)
	if err != nil {
		return nil, fmt.Errorf("failed marshalling applications: %w", err)
	}

	return renderedApplications, nil
}

func renderConfig(ctx context.Context, orgId uuid.UUID, store store.Store, k8sClient k8sclient.K8SClient, config *[]api.ConfigProviderSpec, deviceBelongsToFleet bool, validateOnly bool) (renderedConfig []byte, repoNames []string, err error) {
	args := renderConfigArgs{}
	emptyIgnitionConfig := config_latest_types.Config{
		Ignition: config_latest_types.Ignition{
			Version: config_latest_types.MaxVersion.String(),
		},
	}
	args.ignitionConfig = &emptyIgnitionConfig
	args.validateOnly = validateOnly
	args.orgId = orgId
	args.store = store
	args.k8sClient = k8sClient
	args.deviceBelongsToFleet = deviceBelongsToFleet

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

func renderConfigItems(ctx context.Context, config *[]api.ConfigProviderSpec, args *renderConfigArgs) error {
	if config == nil {
		return nil
	}

	invalidConfigs := []string{}
	var firstError error
	for i := range *config {
		configItem := (*config)[i]
		name, err := renderConfigItem(ctx, &configItem, args)
		paramErr := validateParameters(&configItem, args.validateOnly, args.deviceBelongsToFleet)

		if err != nil && errors.Is(err, ErrUnknownConfigName) {
			name = "<unknown>"
		}

		// An error message regarding invalid parameters should take precedence
		// because it may be the cause of the render error
		if paramErr != nil {
			err = paramErr
		}

		if err != nil {
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

type RenderItem interface {
	MarshalJSON() ([]byte, error)
}

func validateParameters(item RenderItem, validateOnly, deviceBelongsToFleet bool) error {
	cfgJson, err := item.MarshalJSON()
	if err != nil {
		return fmt.Errorf("failed converting configuration to json: %w", err)
	}
	if validateOnly {
		// Make sure all parameters are in the proper format
		return ValidateParameterFormat(cfgJson)
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

func renderConfigItem(ctx context.Context, configItem *api.ConfigProviderSpec, args *renderConfigArgs) (string, error) {
	configType, err := configItem.Type()
	if err != nil {
		return "", fmt.Errorf("%w: failed getting config type: %w", ErrUnknownConfigName, err)
	}

	switch configType {
	case api.GitConfigProviderType:
		return renderGitConfig(ctx, configItem, args)
	case api.KubernetesSecretProviderType:
		return renderK8sConfig(configItem, args)
	case api.InlineConfigProviderType:
		return renderInlineConfig(configItem, args)
	case api.HttpConfigProviderType:
		return renderHttpProviderConfig(ctx, configItem, args)
	default:
		return "", fmt.Errorf("%w: unsupported config type %q", ErrUnknownConfigName, configType)
	}
}

func renderApplication(_ context.Context, app *api.ApplicationSpec, args *renderApplicationArgs) (string, error) {
	appType, err := app.Type()
	if err != nil {
		return "", fmt.Errorf("failed getting application type: %w", err)
	}
	switch appType {
	case api.ImageApplicationProviderType:
		return renderImageApplicationProvider(app, args)
	default:
		return "", fmt.Errorf("%w: unsupported application type %q", ErrUnknownApplicationType, appType)
	}
}

func renderImageApplicationProvider(app *api.ApplicationSpec, args *renderApplicationArgs) (string, error) {
	imageProvider, err := app.AsImageApplicationProvider()
	if err != nil {
		return "", fmt.Errorf("%w: failed getting application as ImageApplicationProvider: %w", ErrUnknownApplicationType, err)
	}

	appName := util.FromPtr(app.Name)
	if args.validateOnly {
		return appName, nil
	}

	renderedApp := api.RenderedApplicationSpec{
		Name:    app.Name,
		EnvVars: app.EnvVars,
	}
	if err := renderedApp.FromImageApplicationProvider(imageProvider); err != nil {
		return appName, fmt.Errorf("failed rendering application %s: %w", appName, err)
	}

	args.applications = append(args.applications, renderedApp)
	return appName, nil
}

func renderGitConfig(ctx context.Context, configItem *api.ConfigProviderSpec, args *renderConfigArgs) (string, error) {
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
	ignitionConfig, err := ConvertFileSystemToIgnition(mfs, gitSpec.GitRef.Path, lo.FromPtr(gitSpec.GitRef.MountPath))
	if err != nil {
		return gitSpec.Name, fmt.Errorf("failed parsing git config item %s: %w", gitSpec.Name, err)
	}
	mergedConfig := config_latest.Merge(*args.ignitionConfig, *ignitionConfig)
	args.ignitionConfig = &mergedConfig

	return gitSpec.Name, nil
}

func renderK8sConfig(configItem *api.ConfigProviderSpec, args *renderConfigArgs) (string, error) {
	k8sSpec, err := configItem.AsKubernetesSecretProviderSpec()
	if err != nil {
		return "", fmt.Errorf("%w: failed getting config item as KubernetesSecretProviderSpec: %w", ErrUnknownConfigName, err)
	}
	if args.k8sClient == nil {
		return k8sSpec.Name, errors.New("kubernetes API is not available")
	}
	secret, err := args.k8sClient.GetSecret(k8sSpec.SecretRef.Namespace, k8sSpec.SecretRef.Name)
	if err != nil {
		return k8sSpec.Name, fmt.Errorf("failed getting secret %s/%s: %w", k8sSpec.SecretRef.Namespace, k8sSpec.SecretRef.Name, err)
	}
	ignitionWrapper, err := ignition.NewWrapper()
	if err != nil {
		return k8sSpec.Name, fmt.Errorf("failed to create ignition wrapper: %w", err)
	}
	splits := filepath.SplitList(k8sSpec.SecretRef.MountPath)
	for name, contents := range secret.Data {
		ignitionWrapper.SetFile(filepath.Join(append(splits, name)...), contents, 0o644, false, nil, nil)
	}
	if !args.validateOnly {
		args.ignitionConfig = lo.ToPtr(ignitionWrapper.Merge(*args.ignitionConfig))
	}
	return k8sSpec.Name, nil
}

func renderInlineConfig(configItem *api.ConfigProviderSpec, args *renderConfigArgs) (string, error) {
	inlineSpec, err := configItem.AsInlineConfigProviderSpec()
	if err != nil {
		return "", fmt.Errorf("%w: failed getting config item as InlineConfigProviderSpec", ErrUnknownConfigName)
	}

	ignitionWrapper, err := ignition.NewWrapper()
	if err != nil {
		return inlineSpec.Name, fmt.Errorf("failed to create ignition wrapper: %w", err)
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

	if !args.validateOnly {
		args.ignitionConfig = lo.ToPtr(ignitionWrapper.Merge(*args.ignitionConfig))
	}

	return inlineSpec.Name, nil
}

func renderHttpProviderConfig(ctx context.Context, configItem *api.ConfigProviderSpec, args *renderConfigArgs) (string, error) {
	httpConfigProviderSpec, err := configItem.AsHttpConfigProviderSpec()
	if err != nil {
		return "", fmt.Errorf("%w: failed getting config item as HttpConfigProviderSpec: %w", ErrUnknownConfigName, err)
	}
	args.repoNames = append(args.repoNames, httpConfigProviderSpec.HttpRef.Repository)
	repo, err := args.store.Repository().GetInternal(ctx, args.orgId, httpConfigProviderSpec.HttpRef.Repository)
	if err != nil {
		return httpConfigProviderSpec.Name, fmt.Errorf("failed fetching specified Repository definition %s/%s: %w", args.orgId, httpConfigProviderSpec.HttpRef.Repository, err)
	}
	if repo.Spec == nil {
		return httpConfigProviderSpec.Name, fmt.Errorf("empty Repository definition %s/%s: %w", args.orgId, httpConfigProviderSpec.HttpRef.Repository, err)
	}
	repoURL, err := repo.Spec.Data.GetRepoURL()
	if err != nil {
		return "", err
	}

	// Append the suffix only if exists (as it's optional)
	if httpConfigProviderSpec.HttpRef.Suffix != nil {
		repoURL = repoURL + *httpConfigProviderSpec.HttpRef.Suffix
	}
	if args.validateOnly {
		return httpConfigProviderSpec.Name, nil
	}
	repoSpec := repo.Spec.Data
	body, err := sendHTTPrequest(repoSpec, repoURL)
	if err != nil {
		return "", fmt.Errorf("sending HTTP Request")
	}

	// Convert body to ignition config
	ignitionWrapper, err := ignition.NewWrapper()
	if err != nil {
		return httpConfigProviderSpec.Name, fmt.Errorf("failed to create ignition wrapper: %w", err)
	}

	ignitionWrapper.SetFile(httpConfigProviderSpec.HttpRef.FilePath, body, 0o644, false, nil, nil)
	if !args.validateOnly {
		args.ignitionConfig = lo.ToPtr(ignitionWrapper.Merge(*args.ignitionConfig))
	}

	return httpConfigProviderSpec.Name, nil
}
