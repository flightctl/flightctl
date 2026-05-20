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

	if event.Reason == domain.EventReasonResourceDeleted {
		return logic.HandleDeletion(ctx, event)
	}

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

	refs := collectConfigRefs(p.log, fleet.Spec.Template.Spec.Config, fleetName, "")
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
		// Clean up any leftover standalone refs from before the device was
		// adopted by a fleet. Fleet-owned devices get their refs from
		// fleet validation (non-parameterized) or fleet rollout (parameterized).
		if st := p.serviceHandler.ReplaceStandaloneDeviceDependencyRefs(ctx, p.orgId, deviceName, nil); st.Code != http.StatusOK {
			return fmt.Errorf("cleaning standalone refs for fleet-owned device %s: %s", deviceName, st.Message)
		}
		return nil
	}

	var config *[]domain.ConfigProviderSpec
	if device.Spec != nil {
		config = device.Spec.Config
	}

	refs := collectConfigRefs(p.log, config, "", deviceName)
	if st := p.serviceHandler.ReplaceStandaloneDeviceDependencyRefs(ctx, p.orgId, deviceName, refs); st.Code != http.StatusOK {
		return fmt.Errorf("replacing dependency refs for standalone device %s: %s", deviceName, st.Message)
	}
	return nil
}

// HandleDeletion cleans up all dependency_refs for a deleted fleet or device.
func (p PopulateDependencyRefsLogic) HandleDeletion(ctx context.Context, event domain.Event) error {
	name := event.InvolvedObject.Name
	switch event.InvolvedObject.Kind {
	case domain.FleetKind:
		if st := p.serviceHandler.DeleteDependencyRefsByFleet(ctx, p.orgId, name); st.Code != http.StatusOK {
			return fmt.Errorf("deleting dependency refs for fleet %s: %s", name, st.Message)
		}
	case domain.DeviceKind:
		// DeleteByDevice removes ALL refs for this device — both standalone
		// (fleet_name='') and fleet-rollout refs (fleet_name='some-fleet').
		if st := p.serviceHandler.DeleteDependencyRefsByDevice(ctx, p.orgId, name); st.Code != http.StatusOK {
			return fmt.Errorf("deleting dependency refs for device %s: %s", name, st.Message)
		}
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

func collectConfigRefs(log logrus.FieldLogger, config *[]domain.ConfigProviderSpec, fleetName string, deviceName string) []model.DependencyRef {
	if config == nil {
		return nil
	}

	var refs []model.DependencyRef
	seenNames := make(map[string]struct{})
	warnedNames := make(map[string]struct{})
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
			warnDuplicateConfigName(log, seenNames, warnedNames, gitSpec.Name, i, fleetName, deviceName)
			fn := fleetName
			dn := deviceName
			refs = append(refs, model.DependencyRef{
				FleetName:          &fn,
				DeviceName:         &dn,
				RefType:            "git",
				ResourceKey:        fmt.Sprintf("git:%s/%s", gitSpec.GitRef.Repository, gitSpec.GitRef.TargetRevision),
				RepositoryName:     &gitSpec.GitRef.Repository,
				Revision:           &gitSpec.GitRef.TargetRevision,
				ConfigProviderName: gitSpec.Name,
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
			if isParameterized(suffix) {
				continue
			}
			warnDuplicateConfigName(log, seenNames, warnedNames, httpSpec.Name, i, fleetName, deviceName)
			fn := fleetName
			dn := deviceName
			refs = append(refs, model.DependencyRef{
				FleetName:          &fn,
				DeviceName:         &dn,
				RefType:            "http",
				ResourceKey:        httpResourceKey(httpSpec.HttpRef.Repository, suffix),
				RepositoryName:     &httpSpec.HttpRef.Repository,
				HTTPSuffix:         httpSpec.HttpRef.Suffix,
				ConfigProviderName: httpSpec.Name,
			})
		case domain.KubernetesSecretProviderType:
			k8sSpec, err := configItem.AsKubernetesSecretProviderSpec()
			if err != nil {
				log.WithError(err).Warn("skipping K8s secret config item that failed to decode")
				continue
			}
			if isParameterized(k8sSpec.SecretRef.Namespace) || isParameterized(k8sSpec.SecretRef.Name) {
				continue
			}
			warnDuplicateConfigName(log, seenNames, warnedNames, k8sSpec.Name, i, fleetName, deviceName)
			fn := fleetName
			dn := deviceName
			refs = append(refs, model.DependencyRef{
				FleetName:          &fn,
				DeviceName:         &dn,
				RefType:            "secret",
				ResourceKey:        fmt.Sprintf("secret:%s/%s", k8sSpec.SecretRef.Namespace, k8sSpec.SecretRef.Name),
				SecretName:         &k8sSpec.SecretRef.Name,
				SecretNamespace:    &k8sSpec.SecretRef.Namespace,
				ConfigProviderName: k8sSpec.Name,
			})
		}
	}
	return refs
}

// warnDuplicateConfigName logs a single warning per unique duplicate name to
// surface pre-existing specs that have not yet been corrected. The warnedNames
// map ensures each name is warned about at most once per call to collectConfigRefs,
// preventing log spam on repeated invocations for the same spec.
func warnDuplicateConfigName(log logrus.FieldLogger, seenNames, warnedNames map[string]struct{}, name string, idx int, fleetName, deviceName string) {
	if _, seen := seenNames[name]; seen {
		if _, alreadyWarned := warnedNames[name]; !alreadyWarned {
			log.WithField("duplicate_config_name", name).Warnf(
				"config[%d] has duplicate name %q (fleet=%q device=%q); dependency sync may be unreliable until the spec is corrected",
				idx, name, fleetName, deviceName)
			warnedNames[name] = struct{}{}
		}
	}
	seenNames[name] = struct{}{}
}

func isParameterized(s string) bool {
	return strings.Contains(s, "{{") && strings.Contains(s, "}}")
}

func shouldPopulateDependencyRefs(_ context.Context, event domain.Event, log logrus.FieldLogger) bool {
	switch event.InvolvedObject.Kind {
	case domain.FleetKind:
		switch event.Reason {
		case domain.EventReasonResourceCreated:
			return true
		case domain.EventReasonResourceUpdated:
			return hasUpdatedFields(event.Details, log, domain.SpecTemplate)
		case domain.EventReasonResourceDeleted:
			return true
		}
	case domain.DeviceKind:
		switch event.Reason {
		case domain.EventReasonResourceCreated:
			return true
		case domain.EventReasonResourceUpdated:
			return hasUpdatedFields(event.Details, log, domain.Spec)
		case domain.EventReasonResourceDeleted:
			return true
		}
	}
	return false
}
