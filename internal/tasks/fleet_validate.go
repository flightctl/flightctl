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
	deviceservice "github.com/flightctl/flightctl/internal/service/device"
	fleetservice "github.com/flightctl/flightctl/internal/service/fleet"
	repositoryservice "github.com/flightctl/flightctl/internal/service/repository"
	templateversionservice "github.com/flightctl/flightctl/internal/service/templateversion"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/internal/worker_client"
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
// If a template version with the computed name already exists, the task
// verifies it and re-emits the idempotent PrepareDeltas event. This recovers
// the window where creation succeeded but the process stopped before event
// publication.
//
// This design avoids unnecessary object creation, ensures consistency, and allows
// safe reprocessing of the task without side effects.

type FleetValidateLogic struct {
	log                logrus.FieldLogger
	fleetSvc           fleetservice.Service
	templateversionSvc templateversionservice.Service
	deviceSvc          deviceservice.Service
	repositorySvc      repositoryservice.Service
	k8sClient          k8sclient.K8SClient
	orgId              uuid.UUID
	event              domain.Event
	templateConfig     *[]domain.ConfigProviderSpec
	WorkerClient       worker_client.WorkerClient
}

func NewFleetValidateLogic(log logrus.FieldLogger, fleetSvc fleetservice.Service, templateversionSvc templateversionservice.Service, deviceSvc deviceservice.Service, repositorySvc repositoryservice.Service, k8sClient k8sclient.K8SClient, orgId uuid.UUID, event domain.Event) FleetValidateLogic {
	return FleetValidateLogic{
		log:                log,
		fleetSvc:           fleetSvc,
		templateversionSvc: templateversionSvc,
		deviceSvc:          deviceSvc,
		repositorySvc:      repositorySvc,
		k8sClient:          k8sClient,
		orgId:              orgId,
		event:              event,
	}
}

func (t *FleetValidateLogic) CreateNewTemplateVersionIfFleetValid(ctx context.Context) error {
	fleet, status := t.fleetSvc.GetFleet(ctx, t.orgId, t.event.InvolvedObject.Name, domain.GetFleetParams{})
	if status.Code != http.StatusOK {
		return fmt.Errorf("failed getting fleet %s/%s: %s", t.orgId, t.event.InvolvedObject.Name, status.Message)
	}

	fingerprint := t.getFingerprint()
	templateVersionName := generateTemplateVersionName(fleet, fingerprint)
	t.templateConfig = fleet.Spec.Template.Spec.Config
	referencedRepos, validationErr := t.validateConfig(ctx)

	// Set the many-to-many relationship with the repos (we do this even if the validation failed so that we will
	// validate the fleet again if the repository is updated, and then it might be fixed).
	status = t.fleetSvc.OverwriteFleetRepositoryRefs(ctx, t.orgId, *fleet.Metadata.Name, referencedRepos...)
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
	tv, status := t.templateversionSvc.CreateTemplateVersion(ctx, t.orgId, templateVersion, immediateRollout)
	if status.Code != http.StatusCreated {
		if status.Code == http.StatusConflict {
			t.log.Warnf("templateVersion %s already exists; recovering PrepareDeltas emission", templateVersionName)
			existing, getStatus := t.templateversionSvc.GetTemplateVersion(ctx, t.orgId, *fleet.Metadata.Name, templateVersionName)
			if getStatus.Code != http.StatusOK || existing == nil {
				return t.setStatus(ctx, fmt.Errorf("failed recovering existing templateVersion %s: %s", templateVersionName, getStatus.Message))
			}
			emittedName := templateVersionName
			if existing.Metadata.Name != nil {
				emittedName = *existing.Metadata.Name
			}
			if err := t.prepareFleetRollout(ctx, fleet, emittedName); err != nil {
				return t.setStatus(ctx, err)
			}
			return t.setStatus(ctx, nil)
		}
		return t.setStatus(ctx, fmt.Errorf("failed creating templateVersion for valid fleet: %s", status.Message))
	}

	if tv == nil || tv.Metadata.Name == nil {
		return t.setStatus(ctx, fmt.Errorf("created templateVersion has no name"))
	}
	if err := t.prepareFleetRollout(ctx, fleet, *tv.Metadata.Name); err != nil {
		return t.setStatus(ctx, err)
	}

	return t.setStatus(ctx, nil)
}

// prepareFleetRollout makes the newly-created template version current on the
// fleet and marks its devices out of date before queueing delta preparation.
// Keeping these state changes before PrepareDeltas ensures the completion CAS
// can match the same template-version identity and that a queued event always
// observes the desired resource state.
func (t *FleetValidateLogic) prepareFleetRollout(ctx context.Context, fleet *domain.Fleet, templateVersionName string) error {
	fleetName := *fleet.Metadata.Name
	status := t.fleetSvc.UpdateFleetAnnotations(ctx, t.orgId, fleetName, map[string]string{
		domain.FleetAnnotationTemplateVersion: templateVersionName,
	}, nil)
	if status.Code != http.StatusOK {
		return fmt.Errorf("failed setting fleet annotation with templateVersion: %s", status.Message)
	}

	if err := t.deviceSvc.SetOutOfDate(ctx, t.orgId, util.ResourceOwner(domain.FleetKind, fleetName)); err != nil {
		// Warn only. It is better to continue processing than to fail fleet
		// validation and leave the PrepareDeltas event unpublished.
		t.log.Warnf("failed marking devices out-of-date after new template version created: %v", err)
	}

	return t.emitPrepareDeltas(ctx, fleetName, templateVersionName, fleet.Metadata.ResourceVersion)
}

func (t *FleetValidateLogic) emitPrepareDeltas(ctx context.Context, fleetName, tvName string, resourceVersion *string) error {
	if t.WorkerClient == nil {
		return fmt.Errorf("worker client is required to emit PrepareDeltas")
	}
	details := domain.PrepareDeltasDetails{
		DetailType:      domain.PrepareDeltasDetailsDetailType("PrepareDeltas"),
		TemplateVersion: &tvName,
		ResourceVersion: resourceVersion,
	}
	var eventDetails domain.EventDetails
	if err := eventDetails.FromPrepareDeltasDetails(details); err != nil {
		return err
	}
	t.WorkerClient.EmitEvent(ctx, t.orgId, domain.GetBaseEvent(ctx, domain.FleetKind, fleetName, domain.EventReasonPrepareDeltas, "Preparing OS image deltas", &eventDetails))
	return nil
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

	status := t.fleetSvc.UpdateFleetConditions(ctx, t.orgId, t.event.InvolvedObject.Name, []domain.Condition{condition})
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

	repo, status := t.repositorySvc.GetRepository(ctx, t.orgId, gitSpec.GitRef.Repository)
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

	repo, status := t.repositorySvc.GetRepository(ctx, t.orgId, httpConfigProviderSpec.HttpRef.Repository)
	if status.Code != http.StatusOK {
		return &httpConfigProviderSpec.Name, &httpConfigProviderSpec.HttpRef.Repository, fmt.Errorf("failed fetching specified Repository definition %s/%s: %s", t.orgId, httpConfigProviderSpec.HttpRef.Repository, status.Message)
	}
	_, err = repo.Spec.GetRepoURL()
	if err != nil {
		return &httpConfigProviderSpec.Name, &httpConfigProviderSpec.HttpRef.Repository, err
	}

	return &httpConfigProviderSpec.Name, &httpConfigProviderSpec.HttpRef.Repository, nil
}

func (t *FleetValidateLogic) getFingerprint() string {
	if t.event.Reason != domain.EventReasonDependencyChangeDetected || t.event.Details == nil {
		return ""
	}
	details, err := t.event.Details.AsDependencyChangeDetectedDetails()
	if err != nil {
		t.log.WithError(err).Warn("failed extracting fingerprint from DependencyChangeDetected event")
		return ""
	}
	return details.Fingerprint
}

func generateTemplateVersionName(fleet *domain.Fleet, fingerprint string) string {
	base := "v" + strconv.FormatInt(*fleet.Metadata.Generation, 10)
	if fingerprint == "" {
		return base
	}
	// Hash the fingerprint to guarantee RFC 1123 compliance — raw HTTP ETags
	// contain quotes and Last-Modified headers contain spaces/colons.
	h := sha256.Sum256([]byte(fingerprint))
	return base + "-" + hex.EncodeToString(h[:4])
}
