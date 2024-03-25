package tasks

import (
	"context"
	"fmt"
	"strings"

	config_latest "github.com/coreos/ignition/v2/config/v3_4"
	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/store"
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

			if resourceRef.Op == FleetValidateOpUpdate {
				err := logic.CreateNewTemplateVersionIfFleetValid(ctx)
				if err != nil {
					log.Errorf("failed validating fleet %s/%s: %v", resourceRef.OrgID, resourceRef.Name, err)
				}
			} else {
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

func (t *FleetValidateLogic) CreateNewTemplateVersionIfFleetValid(ctx context.Context) error {
	fleet, err := t.store.Fleet().Get(ctx, t.resourceRef.OrgID, t.resourceRef.Name)
	if err != nil {
		return fmt.Errorf("fetching fleet: %w", err)
	}

	err = t.validateFleetTemplate(ctx, fleet)
	if err != nil {
		return fmt.Errorf("validating fleet: %w", err)
	}

	templateVersion := api.TemplateVersion{
		Metadata: api.ObjectMeta{Name: util.TimeStampStringPtr()},
		Spec:     api.TemplateVersionSpec{Fleet: t.resourceRef.Name},
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
		t.log.Debugf("Validated config %s from fleet %s/%s: %v", name, t.resourceRef.OrgID, t.resourceRef.Name, err)
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
	api.SetStatusCondition(fleet.Status.Conditions, condition)
	err := t.store.Fleet().UpdateConditions(ctx, t.resourceRef.OrgID, t.resourceRef.Name, *fleet.Status.Conditions)
	if err != nil {
		t.log.Warnf("failed setting condition on fleet %s/%s: %v", t.resourceRef.OrgID, t.resourceRef.Name, err)
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
