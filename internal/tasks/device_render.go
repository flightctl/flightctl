package tasks

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	config_latest "github.com/coreos/ignition/v2/config/v3_4"
	config_latest_types "github.com/coreos/ignition/v2/config/v3_4/types"
	api "github.com/flightctl/flightctl/api/v1beta1"
	"github.com/flightctl/flightctl/internal/kvstore"
	"github.com/flightctl/flightctl/internal/service"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/internal/util/validation"
	"github.com/flightctl/flightctl/pkg/ignition"
	"github.com/flightctl/flightctl/pkg/k8sclient"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
)

// The deviceRender task is triggered when a device is updated or its template version changes.
// It renders the device’s configuration and applications into a final form that can be consumed
// by the edge device, and stores the rendered output.
//
// To ensure idempotency:
// - If the device spec hasn't changed (as determined by UpdateRenderedDevice), the rendered
//   version is not bumped.
// - The rendering process is deterministic, based on the device spec, configuration sources,
//   and application specs.
// - External inputs (e.g., Git repositories, HTTP endpoints, Kubernetes secrets) are frozen per
//   fleet/template version using a KV store. Writes to the store use SetNX to prevent changes
//   after freezing, and to detect inconsistencies.
// - The rendered output and device condition status are safely overwritten or retried without
//   side effects.
//
// This design ensures the task can be retried safely, detects mid-write inconsistencies,
// and avoids unnecessary reprocessing when the output is already up to date.

func deviceRender(ctx context.Context, orgId uuid.UUID, event api.Event, serviceHandler service.Service, k8sClient k8sclient.K8SClient, kvStore kvstore.KVStore, log logrus.FieldLogger) error {
	logic := NewDeviceRenderLogic(log, serviceHandler, k8sClient, kvStore, orgId, event)
	if event.InvolvedObject.Kind == api.DeviceKind {
		err := logic.RenderDevice(ctx)
		if err != nil {
			log.Errorf("failed rendering device %s/%s: %v", orgId, event.InvolvedObject.Name, err)
		} else {
			log.Infof("completed rendering device %s/%s", orgId, event.InvolvedObject.Name)
		}
	} else {
		log.Errorf("DeviceRender called with unexpected kind %s and op %s", event.InvolvedObject.Kind, event.Reason)
	}
	return nil
}

type DeviceRenderLogic struct {
	log             logrus.FieldLogger
	serviceHandler  service.Service
	k8sClient       k8sclient.K8SClient
	kvStore         kvstore.KVStore
	orgId           uuid.UUID
	event           api.Event
	ownerFleet      *string
	templateVersion *string
	deviceConfig    *[]api.ConfigProviderSpec
	applications    *[]api.ApplicationProviderSpec
}

func NewDeviceRenderLogic(log logrus.FieldLogger, serviceHandler service.Service, k8sClient k8sclient.K8SClient, kvStore kvstore.KVStore, orgId uuid.UUID, event api.Event) DeviceRenderLogic {
	return DeviceRenderLogic{log: log, serviceHandler: serviceHandler, k8sClient: k8sClient, kvStore: kvStore, orgId: orgId, event: event}
}

func (t *DeviceRenderLogic) RenderDevice(ctx context.Context) error {
	device, status := t.serviceHandler.GetDevice(ctx, t.orgId, t.event.InvolvedObject.Name)
	if status.Code != http.StatusOK {
		return fmt.Errorf("failed getting device %s/%s: %s", t.orgId, t.event.InvolvedObject.Name, status.Message)
	}

	// Calculate hash including device spec to detect changes
	specHash := hashRenderedWithSpec(device.Spec)

	// If device.Spec or device.Spec.Config are nil, we still want to render an empty ignition config
	if device.Spec != nil {
		t.deviceConfig = device.Spec.Config
		t.applications = device.Spec.Applications
	}

	if device.Metadata.Annotations != nil {
		annotations := lo.FromPtr(device.Metadata.Annotations)

		// Don't render if the device is awaiting reconnection or paused due to conflicts after restore
		if val, ok := annotations[api.DeviceAnnotationAwaitingReconnect]; ok {
			if val == "true" {
				t.log.Infof("Device %s is awaiting reconnection after restore", t.event.InvolvedObject.Name)
				return nil
			}
		}
		if val, ok := annotations[api.DeviceAnnotationConflictPaused]; ok {
			if val == "true" {
				t.log.Infof("Device %s is paused due to conflicts", t.event.InvolvedObject.Name)
				return nil
			}
		}

		// Don't render if the device spec hash hasn't changed since the last render
		if val, ok := annotations[api.DeviceAnnotationRenderedSpecHash]; ok {
			if val == specHash {
				t.log.Infof("Device %s spec hash hasn't changed since the last render", t.event.InvolvedObject.Name)
				return nil
			}
		}
	}

	if device.Metadata.Owner != nil {
		_, owner, err := util.GetResourceOwner(device.Metadata.Owner)
		if err != nil {
			return fmt.Errorf("failed getting device owner %s/%s: %w", t.orgId, t.event.InvolvedObject.Name, err)
		}
		t.ownerFleet = &owner

		annotations := lo.FromPtr(device.Metadata.Annotations)
		tvString, exists := util.GetFromMap(annotations, api.DeviceAnnotationTemplateVersion)
		if !exists {
			return fmt.Errorf("device has no templateversion annotation")
		}
		t.templateVersion = &tvString
	}

	// TODO: remove ignition
	ignitionConfig, referencedRepos, renderErr := t.renderConfig(ctx)
	renderedConfig, err := ignitionConfigToRenderedConfig(ignitionConfig)
	if err != nil {
		return fmt.Errorf("failed converting ignition config to rendered config: %w", err)
	}

	// Set the many-to-many relationship with the repos (we do this even if the render failed so that we will
	// render the device again if the repository is updated, and then it might be fixed).
	// This only applies to devices that don't belong to a fleet, because otherwise the fleet will be
	// notified about changes to the repository.
	if device.Metadata.Owner == nil || *device.Metadata.Owner == "" {
		status = t.serviceHandler.OverwriteDeviceRepositoryRefs(ctx, t.orgId, *device.Metadata.Name, referencedRepos...)
		if status.Code != http.StatusOK {
			return t.setStatus(ctx, fmt.Errorf("setting repository references: %s", status.Message))
		}
	}

	if renderErr != nil {
		return t.setStatus(ctx, renderErr)
	}

	renderedApplications, err := t.renderApplications(ctx)
	if err != nil {
		return t.setStatus(ctx, err)
	}

	status = t.serviceHandler.UpdateRenderedDevice(ctx, t.orgId, t.event.InvolvedObject.Name, string(renderedConfig), string(renderedApplications), specHash)
	return t.setStatus(ctx, service.ApiStatusToErr(status))
}

func (t *DeviceRenderLogic) setStatus(ctx context.Context, renderErr error) error {
	condition := api.Condition{Type: api.ConditionTypeDeviceSpecValid}

	if renderErr == nil {
		condition.Status = api.ConditionStatusTrue
		condition.Reason = "Valid"
	} else {
		condition.Status = api.ConditionStatusFalse
		condition.Reason = "Invalid"
		condition.Message = renderErr.Error()
	}

	status := t.serviceHandler.SetDeviceServiceConditions(ctx, t.orgId, t.event.InvolvedObject.Name, []api.Condition{condition})
	if status.Code != http.StatusOK {
		t.log.Errorf("Failed setting condition for device %s/%s: %s", t.orgId, t.event.InvolvedObject.Name, status.Message)
	}
	return renderErr
}

func (t *DeviceRenderLogic) renderApplications(ctx context.Context) ([]byte, error) {
	if t.applications == nil {
		return nil, nil
	}

	var invalidApplications []string
	var renderedApplications []api.ApplicationProviderSpec
	var firstError error

	for i := range *t.applications {
		application := (*t.applications)[i]
		name, renderedApplication, renderErr := renderApplication(ctx, &application)
		applicationName := util.DefaultIfNil(name, "<unknown>")

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
		return nil, fmt.Errorf("%d invalid application%s: %s. %s: %w", len(invalidApplications), pluralSuffix, strings.Join(invalidApplications, ", "), errorPrefix, firstError)
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

		if repoName != nil {
			referencedRepos = append(referencedRepos, *repoName)
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
		return nil, referencedRepos, fmt.Errorf("%d invalid %s: %s. %s: %w", len(invalidConfigs), configurationStr, strings.Join(invalidConfigs, ", "), errorStr, firstError)
	}

	return ignitionConfig, referencedRepos, nil
}

type RenderItem interface {
	MarshalJSON() ([]byte, error)
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
		return t.renderK8sConfig(ctx, configItem, ignitionConfig)
	case api.InlineConfigProviderType:
		return t.renderInlineConfig(configItem, ignitionConfig)
	case api.HttpConfigProviderType:
		return t.renderHttpProviderConfig(ctx, configItem, ignitionConfig)
	default:
		return nil, nil, fmt.Errorf("%w: unsupported config type %q", ErrUnknownConfigName, configType)
	}
}

func renderApplication(_ context.Context, app *api.ApplicationProviderSpec) (*string, *api.ApplicationProviderSpec, error) {
	appType, err := app.Type()
	if err != nil {
		return nil, nil, fmt.Errorf("failed getting application type: %w", err)
	}
	switch appType {
	case api.ImageApplicationProviderType:
		return app.Name, app, nil
	case api.InlineApplicationProviderType:
		return app.Name, app, nil
	default:
		return nil, nil, fmt.Errorf("%w: unsupported application type: %q", ErrUnknownApplicationType, appType)
	}
}

func (t *DeviceRenderLogic) renderGitConfig(ctx context.Context, configItem *api.ConfigProviderSpec, ignitionConfig **config_latest_types.Config) (*string, *string, error) {
	gitSpec, err := configItem.AsGitConfigProviderSpec()
	if err != nil {
		return nil, nil, fmt.Errorf("%w: failed getting config item as GitConfigProviderSpec: %w", ErrUnknownConfigName, err)
	}

	repo, status := t.serviceHandler.GetRepository(ctx, t.orgId, gitSpec.GitRef.Repository)
	if status.Code != http.StatusOK {
		return &gitSpec.Name, &gitSpec.GitRef.Repository, fmt.Errorf("failed fetching specified Repository definition %s/%s: %s", t.orgId, gitSpec.GitRef.Repository, status.Message)
	}

	var ignition *config_latest_types.Config

	// If the device is not part of a fleet, just clone from git into ignition
	if t.ownerFleet == nil {
		ignition, _, err = CloneGitRepoToIgnition(repo, gitSpec.GitRef.TargetRevision, gitSpec.GitRef.Path)
		if err != nil {
			return &gitSpec.Name, &gitSpec.GitRef.Repository, fmt.Errorf("failed cloning specified git repository %s/%s: %w", t.orgId, gitSpec.GitRef.Repository, err)
		}
	} else {
		ignition, err = t.cloneCachedGitRepoToIgnition(ctx, repo, gitSpec.GitRef.TargetRevision, gitSpec.GitRef.Path)
		if err != nil {
			return &gitSpec.Name, &gitSpec.GitRef.Repository, fmt.Errorf("failed fetching specified git repository %s/%s: %w", t.orgId, gitSpec.GitRef.Repository, err)
		}
	}

	// Validate all file paths produced by the git config before merging
	for _, f := range ignition.Storage.Files {
		if err := validation.DenyForbiddenDevicePath(f.Path); err != nil {
			return &gitSpec.Name, &gitSpec.GitRef.Repository, fmt.Errorf("invalid path from git config: %w", err)
		}
	}
	for _, dir := range ignition.Storage.Directories {
		if err := validation.DenyForbiddenDevicePath(dir.Path); err != nil {
			return &gitSpec.Name, &gitSpec.GitRef.Repository, fmt.Errorf("invalid path from git config: %w", err)
		}
	}
	for _, link := range ignition.Storage.Links {
		if err := validation.DenyForbiddenDevicePath(link.Path); err != nil {
			return &gitSpec.Name, &gitSpec.GitRef.Repository, fmt.Errorf("invalid path from git config: %w", err)
		}
		// Validate symlink target to prevent bypassing forbidden path restrictions
		if link.Target != nil {
			var resolvedTarget string
			if filepath.IsAbs(*link.Target) {
				resolvedTarget = filepath.Clean(*link.Target)
			} else {
				// Relative target: resolve relative to link's directory
				resolvedTarget = filepath.Clean(filepath.Join(filepath.Dir(link.Path), *link.Target))
			}
			if err := validation.DenyForbiddenDevicePath(resolvedTarget); err != nil {
				return &gitSpec.Name, &gitSpec.GitRef.Repository, fmt.Errorf("invalid symlink target from git config: %w", err)
			}
		}
	}

	mergedConfig := config_latest.Merge(**ignitionConfig, *ignition)
	*ignitionConfig = &mergedConfig

	return &gitSpec.Name, &gitSpec.GitRef.Repository, nil
}

func (t *DeviceRenderLogic) renderK8sConfig(ctx context.Context, configItem *api.ConfigProviderSpec, ignitionConfig **config_latest_types.Config) (*string, *string, error) {
	k8sSpec, err := configItem.AsKubernetesSecretProviderSpec()
	if err != nil {
		return nil, nil, fmt.Errorf("%w: failed getting config item as KubernetesSecretProviderSpec: %w", ErrUnknownConfigName, err)
	}
	if t.k8sClient == nil {
		return &k8sSpec.Name, nil, fmt.Errorf("kubernetes API is not available")
	}

	var secretData map[string][]byte
	var key kvstore.K8sSecretKey
	needToStoreData := false

	if t.ownerFleet != nil {
		key = kvstore.K8sSecretKey{
			OrgID:           t.orgId,
			Fleet:           *t.ownerFleet,
			TemplateVersion: *t.templateVersion,
			Namespace:       k8sSpec.SecretRef.Namespace,
			Name:            k8sSpec.SecretRef.Name,
		}
		data, err := t.kvStore.Get(ctx, key.ComposeKey())
		if err != nil {
			return &k8sSpec.Name, nil, fmt.Errorf("failed fetching cached secret data: %w", err)
		}
		if data != nil {
			err = json.Unmarshal(data, &secretData)
			if err != nil {
				return &k8sSpec.Name, nil, fmt.Errorf("failed parsing cached secret data: %w", err)
			}
		} else {
			needToStoreData = true
		}
	}

	if secretData == nil {
		secret, err := t.k8sClient.GetSecret(ctx, k8sSpec.SecretRef.Namespace, k8sSpec.SecretRef.Name)
		if err != nil {
			return &k8sSpec.Name, nil, fmt.Errorf("failed getting secret %s/%s: %w", k8sSpec.SecretRef.Namespace, k8sSpec.SecretRef.Name, err)
		}
		secretData = secret.Data
	}

	if needToStoreData {
		secretDataToStore, err := json.Marshal(secretData)
		if err != nil {
			return &k8sSpec.Name, nil, fmt.Errorf("failed marshalling secret data %s/%s: %w", k8sSpec.SecretRef.Namespace, k8sSpec.SecretRef.Name, err)
		}
		updated, err := t.kvStore.SetNX(ctx, key.ComposeKey(), secretDataToStore)
		if err != nil {
			return &k8sSpec.Name, nil, fmt.Errorf("failed storing secret %s/%s: %w", k8sSpec.SecretRef.Namespace, k8sSpec.SecretRef.Name, err)
		}
		if !updated {
			return &k8sSpec.Name, nil, fmt.Errorf("failed freezing secret %s/%s: unexpectedly changed", k8sSpec.SecretRef.Namespace, k8sSpec.SecretRef.Name)
		}
	}

	ignitionWrapper, err := ignition.NewWrapper()
	if err != nil {
		return &k8sSpec.Name, nil, fmt.Errorf("failed to create ignition wrapper: %w", err)
	}
	base := filepath.Clean(k8sSpec.SecretRef.MountPath)
	for name, contents := range secretData {
		// enforce filename (no path segments)
		if name == "." || name == ".." || strings.ContainsRune(name, '/') {
			return &k8sSpec.Name, nil, fmt.Errorf("invalid secret key %q: must be a single file name", name)
		}
		dest := filepath.Join(base, name)
		if err := validation.DenyForbiddenDevicePath(dest); err != nil {
			return &k8sSpec.Name, nil, fmt.Errorf("invalid secret-derived path %q: %w", dest, err)
		}
		ignitionWrapper.SetFile(dest, contents, 0o644, false, nil, nil)
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
		if file.ContentEncoding != nil && *file.ContentEncoding == api.EncodingBase64 {
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
	repo, status := t.serviceHandler.GetRepository(ctx, t.orgId, httpConfigProviderSpec.HttpRef.Repository)
	if status.Code != http.StatusOK {
		return &httpConfigProviderSpec.Name, &httpConfigProviderSpec.HttpRef.Repository, fmt.Errorf("failed fetching specified Repository definition %s/%s: %s", t.orgId, httpConfigProviderSpec.HttpRef.Repository, status.Message)
	}

	if t.ownerFleet != nil {
		err = t.getFrozenRepositoryURL(ctx, repo)
		if err != nil {
			return &httpConfigProviderSpec.Name, &httpConfigProviderSpec.HttpRef.Repository, err
		}
	}
	repoURL, err := repo.Spec.GetRepoURL()
	if err != nil {
		return &httpConfigProviderSpec.Name, &httpConfigProviderSpec.HttpRef.Repository, err
	}

	// Append the suffix only if exists (as it's optional)
	if httpConfigProviderSpec.HttpRef.Suffix != nil {
		repoURL = repoURL + *httpConfigProviderSpec.HttpRef.Suffix
	}

	var httpData []byte
	var httpKey kvstore.HttpKey
	needToStoreData := false

	if t.ownerFleet != nil {
		httpKey = kvstore.HttpKey{
			OrgID:           t.orgId,
			Fleet:           *t.ownerFleet,
			TemplateVersion: *t.templateVersion,
			URL:             repoURL,
		}
		data, err := t.kvStore.Get(ctx, httpKey.ComposeKey())
		if err != nil {
			return &httpConfigProviderSpec.Name, nil, fmt.Errorf("failed fetching cached data: %w", err)
		}
		if data != nil {
			httpData = data
		} else {
			needToStoreData = true
		}
	}

	if httpData == nil {
		httpData, err = sendHTTPrequest(repo.Spec, repoURL)
		if err != nil {
			return &httpConfigProviderSpec.Name, nil, fmt.Errorf("failed fetching data: %w", err)
		}
	}

	if needToStoreData {
		updated, err := t.kvStore.SetNX(ctx, httpKey.ComposeKey(), httpData)
		if err != nil {
			return &httpConfigProviderSpec.Name, nil, fmt.Errorf("failed storing data: %w", err)
		}
		if !updated {
			return &httpConfigProviderSpec.Name, nil, fmt.Errorf("failed storing data: unexpectedly changed")
		}
	}

	// Convert body to ignition config
	ignitionWrapper, err := ignition.NewWrapper()
	if err != nil {
		return &httpConfigProviderSpec.Name, &httpConfigProviderSpec.HttpRef.Repository, fmt.Errorf("failed to create ignition wrapper: %w", err)
	}

	ignitionWrapper.SetFile(httpConfigProviderSpec.HttpRef.FilePath, httpData, 0o644, false, nil, nil)
	*ignitionConfig = lo.ToPtr(ignitionWrapper.Merge(**ignitionConfig))

	return &httpConfigProviderSpec.Name, &httpConfigProviderSpec.HttpRef.Repository, nil
}

func (t *DeviceRenderLogic) getFrozenRepositoryURL(ctx context.Context, repo *api.Repository) error {
	repoURL, err := repo.Spec.GetRepoURL()
	if err != nil {
		return fmt.Errorf("failed fetching git repository URL %s/%s: %w", t.orgId, *repo.Metadata.Name, err)
	}

	repoKey := kvstore.RepositoryUrlKey{
		OrgID:           t.orgId,
		Fleet:           *t.ownerFleet,
		TemplateVersion: *t.templateVersion,
		Repository:      *repo.Metadata.Name,
	}
	origRepoURL, err := t.kvStore.GetOrSetNX(ctx, repoKey.ComposeKey(), []byte(repoURL))
	if err != nil {
		return fmt.Errorf("failed storing repository url for %s/%s: %w", t.orgId, *repo.Metadata.Name, err)
	}
	if repoURL != string(origRepoURL) {
		t.log.Warnf("repository URL updated from %s to %s for %s/%s", origRepoURL, repoURL, t.orgId, *repo.Metadata.Name)
		err = repo.Spec.MergeGenericRepoSpec(api.GenericRepoSpec{Url: string(origRepoURL)})
		if err != nil {
			return fmt.Errorf("failed updating changed repository url for %s/%s: %w", t.orgId, *repo.Metadata.Name, err)
		}
	}

	return nil
}

func (t *DeviceRenderLogic) cloneCachedGitRepoToIgnition(ctx context.Context, repo *api.Repository, targetRevision string, path string) (*config_latest_types.Config, error) {
	// 1. Get the frozen repository URL
	err := t.getFrozenRepositoryURL(ctx, repo)
	if err != nil {
		return nil, err
	}

	// 2. Do we have the mapping of targetRevision -> frozenHash cached?
	gitRevisionKey := kvstore.GitRevisionKey{
		OrgID:           t.orgId,
		Fleet:           *t.ownerFleet,
		TemplateVersion: *t.templateVersion,
		Repository:      *repo.Metadata.Name,
		TargetRevision:  targetRevision,
	}
	frozenHashBytes, err := t.kvStore.Get(ctx, gitRevisionKey.ComposeKey())
	if err != nil {
		return nil, fmt.Errorf("failed fetching frozen git revision: %w", err)
	}

	// 3. If we have the frozen hash, try to get the git data from the cache
	gitContentsKey := kvstore.GitContentsKey{
		OrgID:           t.orgId,
		Fleet:           *t.ownerFleet,
		TemplateVersion: *t.templateVersion,
		Repository:      *repo.Metadata.Name,
		TargetRevision:  targetRevision,
		Path:            path,
	}

	if frozenHashBytes != nil {
		cachedGitData, err := t.kvStore.Get(ctx, gitContentsKey.ComposeKey())
		if err != nil {
			return nil, fmt.Errorf("failed fetching cached git data: %w", err)
		}

		// If we got the git data from cache, change the mount path and return
		if cachedGitData != nil {
			wrapper, err := ignition.NewWrapperFromJson(cachedGitData)
			if err != nil {
				return nil, fmt.Errorf("fetched invalid json-encoded ignition from kvstore: %w", err)
			}

			ign := wrapper.AsIgnitionConfig()
			return &ign, nil
		}
	}

	// 4. We didn't get the data from the cache, so we need to clone from git
	revisionToClone := targetRevision
	if frozenHashBytes != nil {
		revisionToClone = string(frozenHashBytes)
	}

	// We clone from git and get ignition with no mount path prefix (i.e., set to "/")
	ign, hash, err := CloneGitRepoToIgnition(repo, revisionToClone, path)
	if err != nil {
		return nil, fmt.Errorf("failed cloning git: %w", err)
	}

	// 5. If we didn't freeze the hash yet, do it now
	if frozenHashBytes == nil {
		_, err = t.kvStore.SetNX(ctx, gitRevisionKey.ComposeKey(), []byte(hash))
		if err != nil {
			return nil, fmt.Errorf("failed freezing git hash: %w", err)
		}
	}

	// 6. Cache the git data
	wrapper := ignition.NewWrapperFromIgnition(*ign)
	jsonData, err := wrapper.AsJson()
	if err != nil {
		return nil, fmt.Errorf("failed converting git ignition to json: %w", err)
	}
	_, err = t.kvStore.SetNX(ctx, gitContentsKey.ComposeKey(), jsonData)
	if err != nil {
		return nil, fmt.Errorf("failed caching git data: %w", err)
	}

	ignToReturn := wrapper.AsIgnitionConfig()
	return &ignToReturn, nil
}

// TODO: this is temporary, ignition will be removed in the future
// ignitionConfigToRenderedConfig converts an ignition config to rendered config bytes
func ignitionConfigToRenderedConfig(ignition *config_latest_types.Config) ([]byte, error) {
	emptyConfig := []byte("[]")

	if ignition == nil || len(ignition.Storage.Files) == 0 {
		return emptyConfig, nil
	}

	var files []api.FileSpec
	for _, file := range ignition.Storage.Files {
		content := lo.FromPtr(file.Contents.Source)
		encoding := api.EncodingPlain

		// parse encoding
		if strings.HasPrefix(content, "data:") {
			encoding = api.EncodingBase64
			if commaIndex := strings.Index(content, ","); commaIndex != -1 {
				content = content[commaIndex+1:]
			}
		}

		group := lo.FromPtr(file.Group.Name)
		if file.Group.ID != nil {
			group = strconv.Itoa(*file.Group.ID)
		}

		user := lo.FromPtr(file.User.Name)
		if file.User.ID != nil {
			user = strconv.Itoa(*file.User.ID)
		}

		fileSpec := api.FileSpec{
			Content:         content,
			ContentEncoding: &encoding,
			Path:            file.Path,
			User:            &user,
			Group:           &group,
			Mode:            file.Mode,
		}

		files = append(files, fileSpec)
	}

	// convert all files to a single inline config provider
	provider := api.ConfigProviderSpec{}
	err := provider.FromInlineConfigProviderSpec(api.InlineConfigProviderSpec{
		Inline: files,
	})
	if err != nil {
		return nil, fmt.Errorf("converting files to inline config provider: %w", err)
	}
	providers := &[]api.ConfigProviderSpec{provider}
	renderedConfig, err := json.Marshal(providers)
	if err != nil {
		return nil, fmt.Errorf("marshalling rendered config: %w", err)
	}

	return renderedConfig, nil
}

// hashRenderedWithSpec creates a hash of the device spec to detect changes
func hashRenderedWithSpec(deviceSpec *api.DeviceSpec) string {
	if deviceSpec == nil {
		return ""
	}
	specBytes, _ := json.Marshal(deviceSpec)
	hash := sha256.Sum256(specBytes)
	return hex.EncodeToString(hash[:])
}
