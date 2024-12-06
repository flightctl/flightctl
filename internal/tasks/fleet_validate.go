package tasks

import (
	"context"
	"fmt"
	"strings"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/pkg/k8sclient"
	"github.com/sirupsen/logrus"
)

func ConditionTypeValidate(ctx context.Context, resourceRef *ResourceReference, store store.Store, callbackManager CallbackManager, k8sClient k8sclient.K8SClient, log logrus.FieldLogger) error {
	logic := NewConditionTypeValidateLogic(callbackManager, log, store, k8sClient, *resourceRef)
	switch {
	case resourceRef.Op == ConditionTypeValidateOpUpdate && resourceRef.Kind == api.FleetKind:
		err := logic.CreateNewTemplateVersionIfConditionTypeValid(ctx)
		if err != nil {
			log.Errorf("failed validating fleet %s/%s: %v", resourceRef.OrgID, resourceRef.Name, err)
		}
	default:
		log.Errorf("ConditionTypeValidate called with unexpected kind %s and op %s", resourceRef.Kind, resourceRef.Op)
	}
	return nil
}

type ConditionTypeValidateLogic struct {
	callbackManager CallbackManager
	log             logrus.FieldLogger
	store           store.Store
	k8sClient       k8sclient.K8SClient
	resourceRef     ResourceReference
	templateConfig  *[]api.ConfigProviderSpec
}

func NewConditionTypeValidateLogic(callbackManager CallbackManager, log logrus.FieldLogger, store store.Store, k8sClient k8sclient.K8SClient, resourceRef ResourceReference) ConditionTypeValidateLogic {
	return ConditionTypeValidateLogic{callbackManager: callbackManager, log: log, store: store, k8sClient: k8sClient, resourceRef: resourceRef}
}

func (t *ConditionTypeValidateLogic) CreateNewTemplateVersionIfConditionTypeValid(ctx context.Context) error {
	fleet, err := t.store.Fleet().Get(ctx, t.resourceRef.OrgID, t.resourceRef.Name)
	if err != nil {
		return fmt.Errorf("failed getting fleet %s/%s: %w", t.resourceRef.OrgID, t.resourceRef.Name, err)
	}

	t.templateConfig = fleet.Spec.Template.Spec.Config
	referencedRepos, validationErr := t.validateConfig(ctx)

	// Set the many-to-many relationship with the repos (we do this even if the validation failed so that we will
	// validate the fleet again if the repository is updated, and then it might be fixed).
	err = t.store.Fleet().OverwriteRepositoryRefs(ctx, t.resourceRef.OrgID, *fleet.Metadata.Name, referencedRepos...)
	if err != nil {
		return fmt.Errorf("setting repository references: %w", err)
	}

	if validationErr != nil {
		return t.setStatus(ctx, validationErr)
	}

	templateVersion := api.TemplateVersion{
		Metadata: api.ObjectMeta{
			Name:  util.TimeStampStringPtr(),
			Owner: util.SetResourceOwner(api.FleetKind, *fleet.Metadata.Name),
		},
		Spec: api.TemplateVersionSpec{Fleet: *fleet.Metadata.Name},
		Status: &api.TemplateVersionStatus{
			Applications: fleet.Spec.Template.Spec.Applications,
			Config:       fleet.Spec.Template.Spec.Config,
			Os:           fleet.Spec.Template.Spec.Os,
			Resources:    fleet.Spec.Template.Spec.Resources,
			Systemd:      fleet.Spec.Template.Spec.Systemd,
		},
	}

	tv, err := t.store.TemplateVersion().Create(ctx, t.resourceRef.OrgID, &templateVersion, t.callbackManager.TemplateVersionCreatedCallback)
	if err != nil {
		return t.setStatus(ctx, fmt.Errorf("creating templateVersion for valid fleet: %w", err))
	}

	annotations := map[string]string{
		api.FleetAnnotationTemplateVersion: *tv.Metadata.Name,
	}
	err = t.store.Fleet().UpdateAnnotations(ctx, t.resourceRef.OrgID, *fleet.Metadata.Name, annotations, nil)
	if err != nil {
		return t.setStatus(ctx, fmt.Errorf("setting fleet annotation with newly-created templateVersion: %w", err))
	}

	return t.setStatus(ctx, nil)
}

func (t *ConditionTypeValidateLogic) setStatus(ctx context.Context, validationErr error) error {
	condition := api.Condition{Type: api.ConditionTypeValid}

	if validationErr == nil {
		condition.Status = api.ConditionStatusTrue
		condition.Reason = "Valid"
	} else {
		condition.Status = api.ConditionStatusFalse
		condition.Reason = "Invalid"
		condition.Message = validationErr.Error()
	}

	err := t.store.Fleet().UpdateConditions(ctx, t.resourceRef.OrgID, t.resourceRef.Name, []api.Condition{condition})
	if err != nil {
		t.log.Errorf("Failed setting condition for fleet %s/%s: %v", t.resourceRef.OrgID, t.resourceRef.Name, err)
	}
	return validationErr
}

func (t *ConditionTypeValidateLogic) validateConfig(ctx context.Context) ([]string, error) {
	if t.templateConfig == nil {
		return nil, nil
	}

	invalidConfigs := []string{}
	referencedRepos := []string{}
	var firstError error
	for i := range *t.templateConfig {
		configItem := (*t.templateConfig)[i]
		name, repoName, err := t.validateConfigItem(ctx, &configItem)
		paramErr := validateParameterFormatInConfig(&configItem)

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
		return referencedRepos, fmt.Errorf("%d invalid %s: %s. %s: %v", len(invalidConfigs), configurationStr, strings.Join(invalidConfigs, ", "), errorStr, firstError)
	}

	return referencedRepos, nil
}

func validateParameterFormatInConfig(item RenderItem) error {
	cfgJson, err := item.MarshalJSON()
	if err != nil {
		return fmt.Errorf("failed converting configuration to json: %w", err)
	}

	return ValidateParameterFormat(cfgJson)
}

func (t *ConditionTypeValidateLogic) validateConfigItem(ctx context.Context, configItem *api.ConfigProviderSpec) (*string, *string, error) {
	configType, err := configItem.Type()
	if err != nil {
		return nil, nil, fmt.Errorf("%w: failed getting config type: %w", ErrUnknownConfigName, err)
	}

	switch configType {
	case api.GitConfigProviderType:
		return t.validateGitConfig(ctx, configItem)
	case api.KubernetesSecretProviderType:
		return t.validateK8sConfig(configItem)
	case api.InlineConfigProviderType:
		return t.validateInlineConfig(configItem)
	case api.HttpConfigProviderType:
		return t.validateHttpProviderConfig(ctx, configItem)
	default:
		return nil, nil, fmt.Errorf("%w: unsupported config type %q", ErrUnknownConfigName, configType)
	}
}

func (t *ConditionTypeValidateLogic) validateGitConfig(ctx context.Context, configItem *api.ConfigProviderSpec) (*string, *string, error) {
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

	return &gitSpec.Name, &gitSpec.GitRef.Repository, nil
}

func (t *ConditionTypeValidateLogic) validateK8sConfig(configItem *api.ConfigProviderSpec) (*string, *string, error) {
	k8sSpec, err := configItem.AsKubernetesSecretProviderSpec()
	if err != nil {
		return nil, nil, fmt.Errorf("%w: failed getting config item as KubernetesSecretProviderSpec: %w", ErrUnknownConfigName, err)
	}
	if t.k8sClient == nil {
		return &k8sSpec.Name, nil, fmt.Errorf("kubernetes API is not available")
	}
	_, err = t.k8sClient.GetSecret(k8sSpec.SecretRef.Namespace, k8sSpec.SecretRef.Name)
	if err != nil {
		return &k8sSpec.Name, nil, fmt.Errorf("failed getting secret %s/%s: %w", k8sSpec.SecretRef.Namespace, k8sSpec.SecretRef.Name, err)
	}

	return &k8sSpec.Name, nil, nil
}

func (t *ConditionTypeValidateLogic) validateInlineConfig(configItem *api.ConfigProviderSpec) (*string, *string, error) {
	inlineSpec, err := configItem.AsInlineConfigProviderSpec()
	if err != nil {
		return nil, nil, fmt.Errorf("%w: failed getting config item as InlineConfigProviderSpec: %w", ErrUnknownConfigName, err)
	}

	// Everything was already validated at the API level
	return &inlineSpec.Name, nil, nil
}

func (t *ConditionTypeValidateLogic) validateHttpProviderConfig(ctx context.Context, configItem *api.ConfigProviderSpec) (*string, *string, error) {
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
	_, err = repo.Spec.Data.GetRepoURL()
	if err != nil {
		return &httpConfigProviderSpec.Name, &httpConfigProviderSpec.HttpRef.Repository, err
	}

	return &httpConfigProviderSpec.Name, &httpConfigProviderSpec.HttpRef.Repository, nil
}
