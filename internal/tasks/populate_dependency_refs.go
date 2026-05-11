package tasks

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/service"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
)

func populateDependencyRefs(ctx context.Context, orgId uuid.UUID, event domain.Event, serviceHandler service.Service, log logrus.FieldLogger) error {
	logic := NewPopulateDependencyRefsLogic(log, serviceHandler, orgId)
	switch event.InvolvedObject.Kind {
	case domain.FleetKind:
		return logic.PopulateForFleet(ctx, event.InvolvedObject.Name)
	case domain.DeviceKind:
		return logic.PopulateForDevice(ctx, event.InvolvedObject.Name)
	default:
		return nil
	}
}

type PopulateDependencyRefsLogic struct {
	log            logrus.FieldLogger
	serviceHandler service.Service
	orgId          uuid.UUID
}

func NewPopulateDependencyRefsLogic(log logrus.FieldLogger, serviceHandler service.Service, orgId uuid.UUID) PopulateDependencyRefsLogic {
	return PopulateDependencyRefsLogic{log: log, serviceHandler: serviceHandler, orgId: orgId}
}

func (p PopulateDependencyRefsLogic) PopulateForFleet(ctx context.Context, fleetName string) error {
	fleet, st := p.serviceHandler.GetFleet(ctx, p.orgId, fleetName, domain.GetFleetParams{})
	if st.Code != http.StatusOK {
		return fmt.Errorf("failed getting fleet %s: %s", fleetName, st.Message)
	}

	refs := collectConfigRefs(p.log, fleet.Spec.Template.Spec.Config, &fleetName, nil)
	if st := p.serviceHandler.ReplaceDependencyRefsByFleet(ctx, p.orgId, fleetName, refs); st.Code != http.StatusOK {
		return fmt.Errorf("replacing dependency refs for fleet %s: %s", fleetName, st.Message)
	}
	return nil
}

func (p PopulateDependencyRefsLogic) PopulateForDevice(ctx context.Context, deviceName string) error {
	device, st := p.serviceHandler.GetDevice(ctx, p.orgId, deviceName)
	if st.Code != http.StatusOK {
		return fmt.Errorf("failed getting device %s: %s", deviceName, st.Message)
	}

	if isFleetOwned(device) {
		return nil
	}

	var config *[]domain.ConfigProviderSpec
	if device.Spec != nil {
		config = device.Spec.Config
	}

	emptyFleet := ""
	refs := collectConfigRefs(p.log, config, &emptyFleet, &deviceName)
	if st := p.serviceHandler.ReplaceStandaloneDeviceDependencyRefs(ctx, p.orgId, deviceName, refs); st.Code != http.StatusOK {
		return fmt.Errorf("replacing dependency refs for standalone device %s: %s", deviceName, st.Message)
	}
	return nil
}

func isFleetOwned(device *domain.Device) bool {
	if device.Metadata.Owner == nil {
		return false
	}
	ownerType, _, err := util.GetResourceOwner(device.Metadata.Owner)
	if err != nil {
		return false
	}
	return ownerType == domain.FleetKind
}

func collectConfigRefs(log logrus.FieldLogger, config *[]domain.ConfigProviderSpec, fleetName *string, deviceName *string) []model.DependencyRef {
	if config == nil {
		return nil
	}

	dn := ""
	if deviceName != nil {
		dn = *deviceName
	}

	var refs []model.DependencyRef
	for i := range *config {
		configItem := (*config)[i]
		configType, err := configItem.Type()
		if err != nil {
			log.WithError(err).Warn("skipping config item with unknown type during dependency ref collection")
			continue
		}
		switch configType {
		case domain.GitConfigProviderType:
			gitSpec, err := configItem.AsGitConfigProviderSpec()
			if err != nil {
				log.WithError(err).Warn("skipping git config item that failed to decode")
				continue
			}
			if isParameterized(gitSpec.GitRef.TargetRevision) {
				continue
			}
			refs = append(refs, model.DependencyRef{
				FleetName:      fleetName,
				DeviceName:     &dn,
				RefType:        "git",
				ResourceKey:    fmt.Sprintf("git:%s/%s", gitSpec.GitRef.Repository, gitSpec.GitRef.TargetRevision),
				RepositoryName: &gitSpec.GitRef.Repository,
				Revision:       &gitSpec.GitRef.TargetRevision,
			})
		case domain.HttpConfigProviderType:
			httpSpec, err := configItem.AsHttpConfigProviderSpec()
			if err != nil {
				log.WithError(err).Warn("skipping HTTP config item that failed to decode")
				continue
			}
			suffix := ""
			if httpSpec.HttpRef.Suffix != nil {
				suffix = *httpSpec.HttpRef.Suffix
			}
			refs = append(refs, model.DependencyRef{
				FleetName:      fleetName,
				DeviceName:     &dn,
				RefType:        "http",
				ResourceKey:    fmt.Sprintf("http:%s/%s", httpSpec.HttpRef.Repository, suffix),
				RepositoryName: &httpSpec.HttpRef.Repository,
				HTTPSuffix:     httpSpec.HttpRef.Suffix,
			})
		case domain.KubernetesSecretProviderType:
			k8sSpec, err := configItem.AsKubernetesSecretProviderSpec()
			if err != nil {
				log.WithError(err).Warn("skipping K8s secret config item that failed to decode")
				continue
			}
			refs = append(refs, model.DependencyRef{
				FleetName:       fleetName,
				DeviceName:      &dn,
				RefType:         "secret",
				ResourceKey:     fmt.Sprintf("secret:%s/%s", k8sSpec.SecretRef.Namespace, k8sSpec.SecretRef.Name),
				SecretName:      &k8sSpec.SecretRef.Name,
				SecretNamespace: &k8sSpec.SecretRef.Namespace,
			})
		}
	}
	return refs
}

func isParameterized(s string) bool {
	return strings.Contains(s, "{{") && strings.Contains(s, "}}")
}

func shouldPopulateDependencyRefs(_ context.Context, event domain.Event, log logrus.FieldLogger) bool {
	switch event.InvolvedObject.Kind {
	case domain.FleetKind:
		if event.Reason == domain.EventReasonResourceCreated {
			return true
		}
		if event.Reason == domain.EventReasonResourceUpdated {
			return hasUpdatedFields(event.Details, log, domain.SpecTemplate)
		}
	case domain.DeviceKind:
		if event.Reason == domain.EventReasonResourceCreated {
			return true
		}
		if event.Reason == domain.EventReasonResourceUpdated {
			return hasUpdatedFields(event.Details, log, domain.Spec)
		}
	}
	return false
}
