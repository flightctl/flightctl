package tasks

import (
	"context"
	"fmt"
	"strings"

	config_latest "github.com/coreos/ignition/v2/config/v3_4"
	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/reqid"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/sirupsen/logrus"
	"sigs.k8s.io/yaml"
)

func FleetValidate(taskManager TaskManager) {
	for {
		select {
		case <-taskManager.ctx.Done():
			taskManager.log.Info("Received ctx.Done(), stopping")
			return
		case resourceRef := <-taskManager.channels[ChannelFleetValidate]:
			requestID := reqid.NextRequestID()
			ctx := context.WithValue(context.Background(), middleware.RequestIDKey, requestID)
			log := log.WithReqIDFromCtx(ctx, taskManager.log)
			logic := NewFleetValidateLogic(taskManager, log, taskManager.store, resourceRef)

			switch {
			case resourceRef.Op == FleetValidateOpUpdate && resourceRef.Kind == model.FleetKind:
				fleet, err := taskManager.store.Fleet().Get(ctx, resourceRef.OrgID, resourceRef.Name)
				if err != nil {
					log.Errorf("fetching fleet while validating: %v", err)
					continue
				}
				err = logic.CreateNewTemplateVersionIfFleetValid(ctx, fleet)
				if err != nil {
					log.Errorf("failed validating fleet %s/%s: %v", resourceRef.OrgID, resourceRef.Name, err)
				}
			case resourceRef.Op == FleetValidateOpUpdate && resourceRef.Kind == model.RepositoryKind:
				logic.ValidateFleetsReferencingRepository(ctx)
			case resourceRef.Op == FleetValidateOpDeleteAll && resourceRef.Kind == model.RepositoryKind:
				logic.ValidateFleetsReferencingAnyRepository(ctx)
			default:
				log.Errorf("TemplateVersionPopulate called with unexpected kind %s and op %s", resourceRef.Kind, resourceRef.Op)
			}
		}
	}
}

type FleetValidateLogic struct {
	taskManager TaskManager
	log         logrus.FieldLogger
	store       store.Store
	resourceRef ResourceReference
}

func NewFleetValidateLogic(taskManager TaskManager, log logrus.FieldLogger, store store.Store, resourceRef ResourceReference) FleetValidateLogic {
	return FleetValidateLogic{taskManager: taskManager, log: log, store: store, resourceRef: resourceRef}
}

func (t *FleetValidateLogic) CreateNewTemplateVersionIfFleetValid(ctx context.Context, fleet *api.Fleet) error {
	err := t.validateFleetTemplate(ctx, fleet)
	if err != nil {
		return fmt.Errorf("validating fleet: %w", err)
	}

	templateVersion := api.TemplateVersion{
		Metadata: api.ObjectMeta{Name: util.TimeStampStringPtr()},
		Spec:     api.TemplateVersionSpec{Fleet: *fleet.Metadata.Name},
	}

	_, err = t.store.TemplateVersion().Create(ctx, t.resourceRef.OrgID, &templateVersion, t.taskManager.TemplateVersionCreatedCallback)
	return err
}

func (t *FleetValidateLogic) validateFleetTemplate(ctx context.Context, fleet *api.Fleet) error {
	if fleet.Spec.Template.Spec.Config == nil {
		return nil
	}

	invalidConfigs := []string{}
	for i := range *fleet.Spec.Template.Spec.Config {
		configItem := (*fleet.Spec.Template.Spec.Config)[i]
		name, err := t.validateConfigItem(ctx, &configItem)
		t.log.Debugf("Validated config %s from fleet %s/%s: %v", name, t.resourceRef.OrgID, *fleet.Metadata.Name, err)
		if err != nil {
			invalidConfigs = append(invalidConfigs, name)
		}
	}

	condition := api.Condition{Type: api.FleetValid}
	var retErr error
	if len(invalidConfigs) == 0 {
		condition.Status = api.ConditionStatusTrue
		condition.Reason = util.StrToPtr("Valid")
		retErr = nil
	} else {
		condition.Status = api.ConditionStatusFalse
		condition.Reason = util.StrToPtr("Invalid")
		condition.Message = util.StrToPtr(fmt.Sprintf("Fleet has %d invalid configurations: %s", len(invalidConfigs), strings.Join(invalidConfigs, ", ")))
		retErr = fmt.Errorf("found %d invalid configurations: %s", len(invalidConfigs), strings.Join(invalidConfigs, ", "))
	}

	if fleet.Status.Conditions == nil {
		fleet.Status.Conditions = &[]api.Condition{}
	}
	err := t.store.Fleet().UpdateConditions(ctx, t.resourceRef.OrgID, *fleet.Metadata.Name, []api.Condition{condition})
	if err != nil {
		t.log.Warnf("failed setting condition on fleet %s/%s: %v", t.resourceRef.OrgID, *fleet.Metadata.Name, err)
	}

	return retErr
}

func (t *FleetValidateLogic) validateConfigItem(ctx context.Context, configItem *api.DeviceSpecification_Config_Item) (string, error) {
	unknownName := "<unknown>"

	disc, err := configItem.Discriminator()
	if err != nil {
		return unknownName, fmt.Errorf("failed getting discriminator: %w", err)
	}

	switch disc {
	case string(api.TemplateDiscriminatorGitConfig):
		gitSpec, err := configItem.AsGitConfigProviderSpec()
		if err != nil {
			return unknownName, fmt.Errorf("failed getting config item as GitConfigProviderSpec: %w", err)
		}
		_, err = t.store.Repository().GetInternal(ctx, t.resourceRef.OrgID, gitSpec.GitRef.Repository)
		if err != nil {
			return gitSpec.Name, fmt.Errorf("failed fetching specified Repository definition %s/%s: %w", t.resourceRef.OrgID, gitSpec.GitRef.Repository, err)
		}
		return gitSpec.Name, nil

	case string(api.TemplateDiscriminatorKubernetesSec):
		k8sSpec, err := configItem.AsKubernetesSecretProviderSpec()
		if err != nil {
			return unknownName, fmt.Errorf("failed getting config item as AsKubernetesSecretProviderSpec: %w", err)
		}
		return k8sSpec.Name, fmt.Errorf("service does not yet support kubernetes config")

	case string(api.TemplateDiscriminatorInlineConfig):
		inlineSpec, err := configItem.AsInlineConfigProviderSpec()
		if err != nil {
			return unknownName, fmt.Errorf("failed getting config item %s as InlineConfigProviderSpec: %w", inlineSpec.Name, err)
		}
		return inlineSpec.Name, t.validateInlineConfig(&inlineSpec)

	default:
		return unknownName, fmt.Errorf("unsupported discriminator %s", disc)
	}
}

func (t *FleetValidateLogic) validateInlineConfig(inlineSpec *api.InlineConfigProviderSpec) error {
	// Add this inline config into the unrendered config
	newConfig := &api.TemplateVersionStatus_Config_Item{}
	err := newConfig.FromInlineConfigProviderSpec(*inlineSpec)
	if err != nil {
		return fmt.Errorf("failed creating inline config from item %s: %w", inlineSpec.Name, err)
	}

	// Convert yaml to json
	yamlBytes, err := yaml.Marshal(inlineSpec.Inline)
	if err != nil {
		return fmt.Errorf("invalid yaml in inline config item %s: %w", inlineSpec.Name, err)
	}
	jsonBytes, err := yaml.YAMLToJSON(yamlBytes)
	if err != nil {
		return fmt.Errorf("failed converting yaml to json in inline config item %s: %w", inlineSpec.Name, err)
	}

	// Convert to ignition
	_, _, err = config_latest.ParseCompatibleVersion(jsonBytes)
	if err != nil {
		return fmt.Errorf("failed parsing inline config item %s: %w", inlineSpec.Name, err)
	}

	return nil
}

func (t *FleetValidateLogic) ValidateFleetsReferencingRepository(ctx context.Context) {
	fleets, err := t.store.Fleet().List(ctx, t.resourceRef.OrgID, store.ListParams{})
	if err != nil {
		t.log.Errorf("fetching fleets: %v", err)
		return
	}

	for i := range fleets.Items {
		fleet := fleets.Items[i]
		hasReference, err := t.doesFleetReferenceRepo(&fleet, t.resourceRef.Name)
		if err != nil {
			t.log.Errorf("failed checking if fleet %s/%s references repo %s: %v", t.resourceRef.OrgID, *fleet.Metadata.Name, t.resourceRef.Name, err)
			continue
		}

		if hasReference {
			err = t.CreateNewTemplateVersionIfFleetValid(ctx, &fleet)
			if err != nil {
				t.log.Errorf("failed validating fleet %s/%s that references repo %s: %v", t.resourceRef.OrgID, *fleet.Metadata.Name, t.resourceRef.Name, err)
				continue
			}
		}
	}
}

func (t *FleetValidateLogic) doesFleetReferenceRepo(fleet *api.Fleet, repoName string) (bool, error) {
	for _, configItem := range *fleet.Spec.Template.Spec.Config {
		disc, err := configItem.Discriminator()
		if err != nil {
			return false, fmt.Errorf("failed getting discriminator: %w", err)
		}

		if disc != string(api.TemplateDiscriminatorGitConfig) {
			continue
		}

		gitSpec, err := configItem.AsGitConfigProviderSpec()
		if err != nil {
			return false, fmt.Errorf("failed getting config item as GitConfigProviderSpec: %w", err)
		}

		if gitSpec.GitRef.Repository == repoName {
			return true, nil
		}
	}

	return false, nil
}

func (t *FleetValidateLogic) ValidateFleetsReferencingAnyRepository(ctx context.Context) {
	fleets, err := t.store.Fleet().List(ctx, t.resourceRef.OrgID, store.ListParams{})
	if err != nil {
		t.log.Errorf("fetching fleets: %v", err)
		return
	}

	for i := range fleets.Items {
		fleet := fleets.Items[i]
		hasReference, err := t.doesFleetReferenceAnyRepo(&fleet)
		if err != nil {
			t.log.Errorf("failed checking if fleet %s/%s references repo %s: %v", t.resourceRef.OrgID, *fleet.Metadata.Name, t.resourceRef.Name, err)
			continue
		}

		if hasReference {
			err = t.CreateNewTemplateVersionIfFleetValid(ctx, &fleet)
			if err != nil {
				t.log.Errorf("failed validating fleet %s/%s that references repo %s: %v", t.resourceRef.OrgID, *fleet.Metadata.Name, t.resourceRef.Name, err)
				continue
			}
		}
	}
}

func (t *FleetValidateLogic) doesFleetReferenceAnyRepo(fleet *api.Fleet) (bool, error) {
	for _, configItem := range *fleet.Spec.Template.Spec.Config {
		disc, err := configItem.Discriminator()
		if err != nil {
			return false, fmt.Errorf("failed getting discriminator: %w", err)
		}

		if disc != string(api.TemplateDiscriminatorGitConfig) {
			continue
		}

		return true, nil
	}

	return false, nil
}
