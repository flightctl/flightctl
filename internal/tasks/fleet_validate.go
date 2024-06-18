package tasks

import (
	"context"
	"errors"
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
			default:
				log.Errorf("FleetValidate called with unexpected kind %s and op %s", resourceRef.Kind, resourceRef.Op)
			}
		}
	}
}

type FleetValidateLogic struct {
	taskManager TaskManager
	log         logrus.FieldLogger
	store       store.Store
	resourceRef ResourceReference
	repoNames   []string
}

func NewFleetValidateLogic(taskManager TaskManager, log logrus.FieldLogger, store store.Store, resourceRef ResourceReference) FleetValidateLogic {
	return FleetValidateLogic{taskManager: taskManager, log: log, store: store, resourceRef: resourceRef}
}

func (t *FleetValidateLogic) CreateNewTemplateVersionIfFleetValid(ctx context.Context, fleet *api.Fleet) error {
	validationErr := t.validateFleetTemplate(ctx, fleet)

	// Set the many-to-may relationship with the repos (we do this even if the validation failed so that we will
	// validate the fleet again if the repository is updated, and then it might be fixed)
	err := t.store.Fleet().OverwriteRepositoryRefs(ctx, t.resourceRef.OrgID, *fleet.Metadata.Name, t.repoNames...)
	if err != nil {
		return fmt.Errorf("setting repository references: %w", err)
	}

	if validationErr != nil {
		return fmt.Errorf("validating fleet: %w", validationErr)
	}

	templateVersion := api.TemplateVersion{
		Metadata: api.ObjectMeta{
			Name:  util.TimeStampStringPtr(),
			Owner: util.SetResourceOwner(model.FleetKind, *fleet.Metadata.Name),
		},
		Spec: api.TemplateVersionSpec{Fleet: *fleet.Metadata.Name},
	}

	tv, err := t.store.TemplateVersion().Create(ctx, t.resourceRef.OrgID, &templateVersion, t.taskManager.TemplateVersionCreatedCallback)
	if err != nil {
		return fmt.Errorf("creating templateVersion for valid fleet: %w", err)
	}

	annotations := map[string]string{
		model.FleetAnnotationTemplateVersion: *tv.Metadata.Name,
	}
	err = t.store.Fleet().UpdateAnnotations(ctx, t.resourceRef.OrgID, *fleet.Metadata.Name, annotations, nil)
	if err != nil {
		return fmt.Errorf("setting fleet annotation with newly-created templateVersion: %w", err)
	}

	return nil
}

func (t *FleetValidateLogic) validateFleetTemplate(ctx context.Context, fleet *api.Fleet) error {
	if fleet.Spec.Template.Spec.Config == nil {
		return nil
	}

	invalidConfigs := []string{}
	for i := range *fleet.Spec.Template.Spec.Config {
		configItem := (*fleet.Spec.Template.Spec.Config)[i]
		name, err := t.validateConfigItem(ctx, &configItem)
		if err != nil {
			if errors.Is(err, ErrUnknownConfigName) {
				name = "<unknown>"
			}
			invalidConfigs = append(invalidConfigs, name)
			t.log.Errorf("failed rendering config %s for fleet %s/%s: %v", name, t.resourceRef.OrgID, *fleet.Metadata.Name, err)
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
		configurationStr := "configuration"
		if len(invalidConfigs) > 1 {
			configurationStr += "s"
		}
		retErr = fmt.Errorf("fleet has %d invalid %s: %s", len(invalidConfigs), configurationStr, strings.Join(invalidConfigs, ", "))
		condition.Message = util.StrToPtr(retErr.Error())
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

func (t *FleetValidateLogic) validateConfigItem(ctx context.Context, configItem *api.DeviceSpec_Config_Item) (string, error) {
	disc, err := configItem.Discriminator()
	if err != nil {
		return "", fmt.Errorf("%w: failed getting discriminator: %w", ErrUnknownConfigName, err)
	}

	switch disc {
	case string(api.TemplateDiscriminatorGitConfig):
		gitSpec, err := configItem.AsGitConfigProviderSpec()
		if err != nil {
			return "", fmt.Errorf("%w: failed getting config item %s as GitConfigProviderSpec: %w", ErrUnknownConfigName, gitSpec.Name, err)
		}
		t.repoNames = append(t.repoNames, gitSpec.GitRef.Repository)
		_, err = t.store.Repository().GetInternal(ctx, t.resourceRef.OrgID, gitSpec.GitRef.Repository)
		if err != nil {
			return gitSpec.Name, fmt.Errorf("failed fetching specified Repository definition %s/%s: %w", t.resourceRef.OrgID, gitSpec.GitRef.Repository, err)
		}
		return gitSpec.Name, nil

	case string(api.TemplateDiscriminatorKubernetesSec):
		k8sSpec, err := configItem.AsKubernetesSecretProviderSpec()
		if err != nil {
			return "", fmt.Errorf("%w: failed getting config item %s as AsKubernetesSecretProviderSpec: %w", ErrUnknownConfigName, k8sSpec.Name, err)
		}
		return k8sSpec.Name, fmt.Errorf("service does not yet support kubernetes config")

	case string(api.TemplateDiscriminatorInlineConfig):
		inlineSpec, err := configItem.AsInlineConfigProviderSpec()
		if err != nil {
			return "", fmt.Errorf("%w: failed getting config item %s as InlineConfigProviderSpec: %w", ErrUnknownConfigName, inlineSpec.Name, err)
		}
		return inlineSpec.Name, t.validateInlineConfig(&inlineSpec)

	default:
		return "", fmt.Errorf("%w: unsupported discriminator %s", ErrUnknownConfigName, disc)
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
