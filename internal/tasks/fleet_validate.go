package tasks

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/service"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/internal/util/validation"
	"github.com/flightctl/flightctl/pkg/k8sclient"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
)

// The fleet_validate task is triggered when a fleet is updated. It validates the
// fleet's configuration and, if valid, creates a new template version representing
// the fleet's desired state.
//
// To ensure idempotency, the template version name is derived deterministically
// from the fleet name and the resource generation (which changes when the spec
// changes). This prevents duplicate template versions from being created if the
// task is retried or processed more than once.
//
// If a template version with the computed name already exists, the task assumes
// it was previously created successfully and exits without error. This is safe
// because the template version is immutable after creation.
//
// This design avoids unnecessary object creation, ensures consistency, and allows
// safe reprocessing of the task without side effects.

func fleetValidate(ctx context.Context, orgId uuid.UUID, event domain.Event, serviceHandler service.Service, k8sClient k8sclient.K8SClient, log logrus.FieldLogger) error {
	logic := NewFleetValidateLogic(log, serviceHandler, k8sClient, orgId, event)
	switch {
	case event.InvolvedObject.Kind == domain.FleetKind:
		err := logic.CreateNewTemplateVersionIfFleetValid(ctx)
		if err != nil {
			log.Errorf("failed validating fleet %s/%s: %v", orgId, event.InvolvedObject.Name, err)
		}
	default:
		log.Errorf("FleetValidate called with unexpected kind %s and reason %s", event.InvolvedObject.Kind, event.Reason)
	}
	return nil
}

type FleetValidateLogic struct {
	log            logrus.FieldLogger
	serviceHandler service.Service
	k8sClient      k8sclient.K8SClient
	orgId          uuid.UUID
	event          domain.Event
	templateConfig *[]domain.ConfigProviderSpec
}

func NewFleetValidateLogic(log logrus.FieldLogger, serviceHandler service.Service, k8sClient k8sclient.K8SClient, orgId uuid.UUID, event domain.Event) FleetValidateLogic {
	return FleetValidateLogic{log: log, serviceHandler: serviceHandler, k8sClient: k8sClient, orgId: orgId, event: event}
}

func (t *FleetValidateLogic) CreateNewTemplateVersionIfFleetValid(ctx context.Context) error {
	fleet, status := t.serviceHandler.GetFleet(ctx, t.orgId, t.event.InvolvedObject.Name, domain.GetFleetParams{})
	if status.Code != http.StatusOK {
		return fmt.Errorf("failed getting fleet %s/%s: %s", t.orgId, t.event.InvolvedObject.Name, status.Message)
	}

	templateVersionName := generateTemplateVersionName(fleet)
	t.templateConfig = fleet.Spec.Template.Spec.Config
	referencedRepos, validationErr := t.validateConfig(ctx)

	// Set the many-to-many relationship with the repos (we do this even if the validation failed so that we will
	// validate the fleet again if the repository is updated, and then it might be fixed).
	status = t.serviceHandler.OverwriteFleetRepositoryRefs(ctx, t.orgId, *fleet.Metadata.Name, referencedRepos...)
	if status.Code != http.StatusOK {
		return fmt.Errorf("setting repository references: %s", status.Message)
	}

	if validationErr != nil {
		return t.setStatus(ctx, validationErr)
	}

	templateVersion := domain.TemplateVersion{
		Metadata: domain.ObjectMeta{
			Name:  &templateVersionName,
			Owner: util.SetResourceOwner(domain.FleetKind, *fleet.Metadata.Name),
		},
		Spec: domain.TemplateVersionSpec{Fleet: *fleet.Metadata.Name},
		Status: &domain.TemplateVersionStatus{
			Applications: fleet.Spec.Template.Spec.Applications,
			Config:       fleet.Spec.Template.Spec.Config,
			Os:           fleet.Spec.Template.Spec.Os,
			Resources:    fleet.Spec.Template.Spec.Resources,
			Systemd:      fleet.Spec.Template.Spec.Systemd,
			UpdatePolicy: fleet.Spec.Template.Spec.UpdatePolicy,
		},
	}

	immediateRollout := fleet.Spec.RolloutPolicy == nil || fleet.Spec.RolloutPolicy.DeviceSelection == nil
	tv, status := t.serviceHandler.CreateTemplateVersion(ctx, t.orgId, templateVersion, immediateRollout)
	if status.Code != http.StatusCreated {
		if status.Code == http.StatusConflict {
			t.log.Warnf("templateVersion %s already exists", templateVersionName)
			return nil
		}
		return t.setStatus(ctx, fmt.Errorf("failed creating templateVersion for valid fleet: %s", status.Message))
	}

	annotations := map[string]string{
		domain.FleetAnnotationTemplateVersion: *tv.Metadata.Name,
	}
	status = t.serviceHandler.UpdateFleetAnnotations(ctx, t.orgId, *fleet.Metadata.Name, annotations, nil)
	if status.Code != http.StatusOK {
		return t.setStatus(ctx, fmt.Errorf("failed setting fleet annotation with newly-created templateVersion: %s", status.Message))
	}

	err := t.serviceHandler.SetOutOfDate(ctx, t.orgId, util.ResourceOwner(domain.FleetKind, *fleet.Metadata.Name))
	if err != nil {
		// Warn only.  It is better to continue processing than to fail the fleet validation and stop rollour.
		t.log.Warnf("failed marking devices out-of-date after new template version created: %v", err)
	}

	return t.setStatus(ctx, nil)
}

func (t *FleetValidateLogic) setStatus(ctx context.Context, validationErr error) error {
	condition := domain.Condition{Type: domain.ConditionTypeFleetValid}

	if validationErr == nil {
		condition.Status = domain.ConditionStatusTrue
		condition.Reason = "Valid"
	} else {
		condition.Status = domain.ConditionStatusFalse
		condition.Reason = "Invalid"
		condition.Message = validationErr.Error()
	}

	status := t.serviceHandler.UpdateFleetConditions(ctx, t.orgId, t.event.InvolvedObject.Name, []domain.Condition{condition})
	if status.Code != http.StatusOK {
		t.log.Errorf("Failed setting condition for fleet %s/%s: %s", t.orgId, t.event.InvolvedObject.Name, status.Message)
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

func (t *FleetValidateLogic) validateConfigItem(ctx context.Context, configItem *domain.ConfigProviderSpec) (*string, *string, error) {
	configType, err := configItem.Type()
	if err != nil {
		return nil, nil, fmt.Errorf("%w: failed getting config type: %w", ErrUnknownConfigName, err)
	}

	switch configType {
	case domain.GitConfigProviderType:
		return t.validateGitConfig(ctx, configItem)
	case domain.KubernetesSecretProviderType:
		return t.validateK8sConfig(ctx, configItem)
	case domain.InlineConfigProviderType:
		return t.validateInlineConfig(configItem)
	case domain.HttpConfigProviderType:
		return t.validateHttpProviderConfig(ctx, configItem)
	default:
		return nil, nil, fmt.Errorf("%w: unsupported config type %q", ErrUnknownConfigName, configType)
	}
}

func (t *FleetValidateLogic) validateGitConfig(ctx context.Context, configItem *domain.ConfigProviderSpec) (*string, *string, error) {
	gitSpec, err := configItem.AsGitConfigProviderSpec()
	if err != nil {
		return nil, nil, fmt.Errorf("%w: failed getting config item as GitConfigProviderSpec: %w", ErrUnknownConfigName, err)
	}

	repo, status := t.serviceHandler.GetRepository(ctx, t.orgId, gitSpec.GitRef.Repository)
	if status.Code != http.StatusOK {
		return &gitSpec.Name, &gitSpec.GitRef.Repository, fmt.Errorf("failed fetching specified Repository definition %s/%s: %s", t.orgId, gitSpec.GitRef.Repository, status.Message)
	}
	_, err = repo.Spec.GetRepoURL()
	if err != nil {
		return &gitSpec.Name, &gitSpec.GitRef.Repository, err
	}

	return &gitSpec.Name, &gitSpec.GitRef.Repository, nil
}

func (t *FleetValidateLogic) validateK8sConfig(ctx context.Context, configItem *domain.ConfigProviderSpec) (*string, *string, error) {
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

func (t *FleetValidateLogic) validateInlineConfig(configItem *domain.ConfigProviderSpec) (*string, *string, error) {
	inlineSpec, err := configItem.AsInlineConfigProviderSpec()
	if err != nil {
		return nil, nil, fmt.Errorf("%w: failed getting config item as InlineConfigProviderSpec: %w", ErrUnknownConfigName, err)
	}

	// Everything was already validated at the API level
	return &inlineSpec.Name, nil, nil
}

func (t *FleetValidateLogic) validateHttpProviderConfig(ctx context.Context, configItem *domain.ConfigProviderSpec) (*string, *string, error) {
	httpConfigProviderSpec, err := configItem.AsHttpConfigProviderSpec()
	if err != nil {
		return nil, nil, fmt.Errorf("%w: failed getting config item as HttpConfigProviderSpec: %w", ErrUnknownConfigName, err)
	}

	repo, status := t.serviceHandler.GetRepository(ctx, t.orgId, httpConfigProviderSpec.HttpRef.Repository)
	if status.Code != http.StatusOK {
		return &httpConfigProviderSpec.Name, &httpConfigProviderSpec.HttpRef.Repository, fmt.Errorf("failed fetching specified Repository definition %s/%s: %s", t.orgId, httpConfigProviderSpec.HttpRef.Repository, status.Message)
	}
	_, err = repo.Spec.GetRepoURL()
	if err != nil {
		return &httpConfigProviderSpec.Name, &httpConfigProviderSpec.HttpRef.Repository, err
	}

	return &httpConfigProviderSpec.Name, &httpConfigProviderSpec.HttpRef.Repository, nil
}

const hashLen = 8

// generateTemplateVersionName produces a DNS-compatible TemplateVersion name from a Fleet.
// For short fleet names, it uses the simple form: {fleetName}-{generation}.
// For long fleet names where the simple form would exceed the DNS subdomain limit,
// it truncates the name and inserts a hash of the full name to preserve uniqueness:
// {truncatedName}-{hash}-{generation}.
func generateTemplateVersionName(fleet *domain.Fleet) string {
	name := *fleet.Metadata.Name
	genStr := strconv.FormatInt(*fleet.Metadata.Generation, 10)

	simple := name + "-" + genStr
	if len(simple) <= validation.DNS1123MaxLength {
		return simple
	}

	hash := sha256.Sum256([]byte(name))
	hashStr := hex.EncodeToString(hash[:])[:hashLen]

	maxPrefix := validation.DNS1123MaxLength - 1 - hashLen - 1 - len(genStr)
	prefix := name[:maxPrefix]

	return prefix + "-" + hashStr + "-" + genStr
}
