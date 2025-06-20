package tasks

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/service"
	"github.com/flightctl/flightctl/internal/tasks_client"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/pkg/k8sclient"
	"github.com/sirupsen/logrus"
)

// The fleet_validate task is triggered when a fleet is updated. It validates the
// fleet's configuration and, if valid, creates a new template version representing
// the fleet's desired state.
//
// To ensure idempotency, the template version name is derived deterministically
// from the fleet name and a hash of the Template.Spec. This prevents duplicate
// template versions from being created if the task is retried or processed more
// than once.
//
// If a template version with the computed name already exists, the task assumes
// it was previously created successfully and exits without error.
//
// This design avoids unnecessary object creation, ensures consistency, and allows
// safe reprocessing of the task without side effects.

func fleetValidate(ctx context.Context, resourceRef *tasks_client.ResourceReference, serviceHandler service.Service, callbackManager tasks_client.CallbackManager, k8sClient k8sclient.K8SClient, log logrus.FieldLogger) error {
	logic := NewFleetValidateLogic(callbackManager, log, serviceHandler, k8sClient, *resourceRef)
	switch {
	case resourceRef.Op == tasks_client.FleetValidateOpUpdate && resourceRef.Kind == api.FleetKind:
		err := logic.CreateNewTemplateVersionIfFleetValid(ctx)
		if err != nil {
			log.Errorf("failed validating fleet %s/%s: %v", resourceRef.OrgID, resourceRef.Name, err)
		}
	default:
		log.Errorf("FleetValidate called with unexpected kind %s and op %s", resourceRef.Kind, resourceRef.Op)
	}
	return nil
}

type FleetValidateLogic struct {
	callbackManager tasks_client.CallbackManager
	log             logrus.FieldLogger
	serviceHandler  service.Service
	k8sClient       k8sclient.K8SClient
	resourceRef     tasks_client.ResourceReference
	templateConfig  *[]api.ConfigProviderSpec
}

func NewFleetValidateLogic(callbackManager tasks_client.CallbackManager, log logrus.FieldLogger, serviceHandler service.Service, k8sClient k8sclient.K8SClient, resourceRef tasks_client.ResourceReference) FleetValidateLogic {
	return FleetValidateLogic{callbackManager: callbackManager, log: log, serviceHandler: serviceHandler, k8sClient: k8sClient, resourceRef: resourceRef}
}

func (t *FleetValidateLogic) CreateNewTemplateVersionIfFleetValid(ctx context.Context) error {
	fleet, status := t.serviceHandler.GetFleet(ctx, t.resourceRef.Name, api.GetFleetParams{})
	if status.Code != http.StatusOK {
		return fmt.Errorf("failed getting fleet %s/%s: %s", t.resourceRef.OrgID, t.resourceRef.Name, status.Message)
	}

	templateVersionName, err := generateTemplateVersionName(t.resourceRef.Name, fleet.Spec.Template.Spec)
	if err != nil {
		return fmt.Errorf("failed generating template version name: %w", err)
	}

	t.templateConfig = fleet.Spec.Template.Spec.Config
	referencedRepos, validationErr := t.validateConfig(ctx)

	// Set the many-to-many relationship with the repos (we do this even if the validation failed so that we will
	// validate the fleet again if the repository is updated, and then it might be fixed).
	status = t.serviceHandler.OverwriteFleetRepositoryRefs(ctx, *fleet.Metadata.Name, referencedRepos...)
	if status.Code != http.StatusOK {
		return fmt.Errorf("setting repository references: %s", status.Message)
	}

	if validationErr != nil {
		return t.setStatus(ctx, validationErr)
	}

	templateVersion := api.TemplateVersion{
		Metadata: api.ObjectMeta{
			Name:  &templateVersionName,
			Owner: util.SetResourceOwner(api.FleetKind, *fleet.Metadata.Name),
		},
		Spec: api.TemplateVersionSpec{Fleet: *fleet.Metadata.Name},
		Status: &api.TemplateVersionStatus{
			Applications: fleet.Spec.Template.Spec.Applications,
			Config:       fleet.Spec.Template.Spec.Config,
			Os:           fleet.Spec.Template.Spec.Os,
			Resources:    fleet.Spec.Template.Spec.Resources,
			Systemd:      fleet.Spec.Template.Spec.Systemd,
			UpdatePolicy: fleet.Spec.Template.Spec.UpdatePolicy,
		},
	}

	immediateRollout := fleet.Spec.RolloutPolicy == nil || fleet.Spec.RolloutPolicy.DeviceSelection == nil
	tv, status := t.serviceHandler.CreateTemplateVersion(ctx, templateVersion, immediateRollout)
	if status.Code != http.StatusCreated {
		if status.Code == http.StatusConflict {
			t.log.Warnf("templateVersion %s already exists", templateVersionName)
			return nil
		}
		return t.setStatus(ctx, fmt.Errorf("failed creating templateVersion for valid fleet: %s", status.Message))
	}

	annotations := map[string]string{
		api.FleetAnnotationTemplateVersion: *tv.Metadata.Name,
	}
	status = t.serviceHandler.UpdateFleetAnnotations(ctx, *fleet.Metadata.Name, annotations, nil)
	if status.Code != http.StatusOK {
		return t.setStatus(ctx, fmt.Errorf("failed setting fleet annotation with newly-created templateVersion: %s", status.Message))
	}

	return t.setStatus(ctx, nil)
}

func (t *FleetValidateLogic) setStatus(ctx context.Context, validationErr error) error {
	condition := api.Condition{Type: api.FleetValid}

	if validationErr == nil {
		condition.Status = api.ConditionStatusTrue
		condition.Reason = "Valid"
	} else {
		condition.Status = api.ConditionStatusFalse
		condition.Reason = "Invalid"
		condition.Message = validationErr.Error()
	}

	status := t.serviceHandler.UpdateFleetConditions(ctx, t.resourceRef.Name, []api.Condition{condition})
	if status.Code != http.StatusOK {
		t.log.Errorf("Failed setting condition for fleet %s/%s: %s", t.resourceRef.OrgID, t.resourceRef.Name, status.Message)
	}
	return validationErr
}

func (t *FleetValidateLogic) validateConfig(ctx context.Context) ([]string, error) {
	if t.templateConfig == nil {
		return nil, nil
	}

	invalidConfigs := []string{}
	referencedRepos := []string{}
	var firstError error
	for i := range *t.templateConfig {
		configItem := (*t.templateConfig)[i]
		name, repoName, err := t.validateConfigItem(ctx, &configItem)

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
		return referencedRepos, fmt.Errorf("%d invalid %s: %s. %s: %v", len(invalidConfigs), configurationStr, strings.Join(invalidConfigs, ", "), errorStr, firstError)
	}

	return referencedRepos, nil
}

func (t *FleetValidateLogic) validateConfigItem(ctx context.Context, configItem *api.ConfigProviderSpec) (*string, *string, error) {
	configType, err := configItem.Type()
	if err != nil {
		return nil, nil, fmt.Errorf("%w: failed getting config type: %w", ErrUnknownConfigName, err)
	}

	switch configType {
	case api.GitConfigProviderType:
		return t.validateGitConfig(ctx, configItem)
	case api.KubernetesSecretProviderType:
		return t.validateK8sConfig(ctx, configItem)
	case api.InlineConfigProviderType:
		return t.validateInlineConfig(configItem)
	case api.HttpConfigProviderType:
		return t.validateHttpProviderConfig(ctx, configItem)
	default:
		return nil, nil, fmt.Errorf("%w: unsupported config type %q", ErrUnknownConfigName, configType)
	}
}

func (t *FleetValidateLogic) validateGitConfig(ctx context.Context, configItem *api.ConfigProviderSpec) (*string, *string, error) {
	gitSpec, err := configItem.AsGitConfigProviderSpec()
	if err != nil {
		return nil, nil, fmt.Errorf("%w: failed getting config item as GitConfigProviderSpec: %w", ErrUnknownConfigName, err)
	}

	repo, status := t.serviceHandler.GetRepository(ctx, gitSpec.GitRef.Repository)
	if status.Code != http.StatusOK {
		return &gitSpec.Name, &gitSpec.GitRef.Repository, fmt.Errorf("failed fetching specified Repository definition %s/%s: %s", t.resourceRef.OrgID, gitSpec.GitRef.Repository, status.Message)
	}
	_, err = repo.Spec.GetRepoURL()
	if err != nil {
		return &gitSpec.Name, &gitSpec.GitRef.Repository, err
	}

	return &gitSpec.Name, &gitSpec.GitRef.Repository, nil
}

func (t *FleetValidateLogic) validateK8sConfig(ctx context.Context, configItem *api.ConfigProviderSpec) (*string, *string, error) {
	k8sSpec, err := configItem.AsKubernetesSecretProviderSpec()
	if err != nil {
		return nil, nil, fmt.Errorf("%w: failed getting config item as KubernetesSecretProviderSpec: %w", ErrUnknownConfigName, err)
	}
	if t.k8sClient == nil {
		return &k8sSpec.Name, nil, fmt.Errorf("kubernetes API is not available")
	}
	_, err = t.k8sClient.GetSecret(ctx, k8sSpec.SecretRef.Namespace, k8sSpec.SecretRef.Name)
	if err != nil {
		return &k8sSpec.Name, nil, fmt.Errorf("failed getting secret %s/%s: %w", k8sSpec.SecretRef.Namespace, k8sSpec.SecretRef.Name, err)
	}

	return &k8sSpec.Name, nil, nil
}

func (t *FleetValidateLogic) validateInlineConfig(configItem *api.ConfigProviderSpec) (*string, *string, error) {
	inlineSpec, err := configItem.AsInlineConfigProviderSpec()
	if err != nil {
		return nil, nil, fmt.Errorf("%w: failed getting config item as InlineConfigProviderSpec: %w", ErrUnknownConfigName, err)
	}

	// Everything was already validated at the API level
	return &inlineSpec.Name, nil, nil
}

func (t *FleetValidateLogic) validateHttpProviderConfig(ctx context.Context, configItem *api.ConfigProviderSpec) (*string, *string, error) {
	httpConfigProviderSpec, err := configItem.AsHttpConfigProviderSpec()
	if err != nil {
		return nil, nil, fmt.Errorf("%w: failed getting config item as HttpConfigProviderSpec: %w", ErrUnknownConfigName, err)
	}

	repo, status := t.serviceHandler.GetRepository(ctx, httpConfigProviderSpec.HttpRef.Repository)
	if status.Code != http.StatusOK {
		return &httpConfigProviderSpec.Name, &httpConfigProviderSpec.HttpRef.Repository, fmt.Errorf("failed fetching specified Repository definition %s/%s: %s", t.resourceRef.OrgID, httpConfigProviderSpec.HttpRef.Repository, status.Message)
	}
	_, err = repo.Spec.GetRepoURL()
	if err != nil {
		return &httpConfigProviderSpec.Name, &httpConfigProviderSpec.HttpRef.Repository, err
	}

	return &httpConfigProviderSpec.Name, &httpConfigProviderSpec.HttpRef.Repository, nil
}

// generateTemplateVersionName creates a deterministic name from the fleet name and Template.Spec hash.
func generateTemplateVersionName(fleetName string, spec api.DeviceSpec) (string, error) {
	specBytes, err := json.Marshal(spec)
	if err != nil {
		return "", fmt.Errorf("failed to marshal template spec for hashing: %w", err)
	}

	hash := sha256.Sum256(specBytes)
	shortHash := hex.EncodeToString(hash[:])[:8]

	return fmt.Sprintf("%s-%s", fleetName, shortHash), nil
}
