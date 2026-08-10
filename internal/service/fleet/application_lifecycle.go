package fleet

import (
	"context"
	"errors"
	"net/http"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/service/common"
	fleetstore "github.com/flightctl/flightctl/internal/store/fleet"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
)

var errApplicationNotInSpec = errors.New("application not in fleet template")

func ensureFleetLifecycleMutable(fleet *domain.Fleet, appName string) error {
	if fleet == nil || !domain.ApplicationsContainName(fleet.Spec.Template.Spec.Applications, appName) {
		return errApplicationNotInSpec
	}
	return nil
}

func lifecycleActionStatus(err error, kind string, name, appName string) domain.Status {
	if errors.Is(err, errApplicationNotInSpec) {
		return domain.StatusResourceNotFound("Application", appName)
	}
	return common.StoreErrorToApiStatus(err, false, kind, &name)
}

// pruneFleetLifecycleOnCurrent drops fleet lifecycle defaults for apps no longer in the template.
// Decode/prune errors are ignored so a corrupt annotation cannot block the spec write.
func pruneFleetLifecycleOnCurrent(log logrus.FieldLogger, fleet *domain.Fleet) error {
	if fleet == nil {
		return nil
	}
	pruned, changed, err := domain.PruneApplicationLifecycleAnnotationMap(
		lo.FromPtr(fleet.Metadata.Annotations),
		fleet.Spec.Template.Spec.Applications,
		domain.FleetAnnotationApplicationLifecycle,
	)
	if err != nil {
		if log != nil {
			log.WithError(err).Warnf("skipping application lifecycle prune for fleet %s", lo.FromPtr(fleet.Metadata.Name))
		}
		return nil
	}
	if !changed {
		return nil
	}
	fleet.Metadata.Annotations = &pruned
	return nil
}

// StopFleetApplication sets a fleet-level default so that the named application's desiredState
// is "stopped" on every device currently owned by this fleet, independent of the application's
// declarative spec. The default is stamped with a fresh version so it wins over an earlier
// device-level override, but a device-level action issued afterwards would in turn win over it.
func (h *ServiceHandler) StopFleetApplication(ctx context.Context, orgId uuid.UUID, name string, appName string) (*domain.Fleet, domain.Status) {
	if status := h.validateFleetForLifecycleAction(ctx, orgId, name, appName); status.Code != http.StatusOK {
		return nil, status
	}

	_, _, _, err := h.store.Mutate(ctx, orgId, name, nil, func(m *fleetstore.FleetMutation) error {
		if err := m.RequireExisting(); err != nil {
			return err
		}
		if err := ensureFleetLifecycleMutable(m.Fleet, appName); err != nil {
			return err
		}
		ann, err := domain.MergeApplicationLifecycleOverrides(
			lo.FromPtr(m.Fleet.Metadata.Annotations),
			domain.FleetAnnotationApplicationLifecycle,
			map[string]domain.ApplicationLifecycleOverride{
				appName: domain.NewDesiredStateOverride(domain.ApplicationDesiredStateStopped, domain.NewLifecycleVersion()),
			},
		)
		if err != nil {
			return err
		}
		m.Fleet.Metadata.Annotations = &ann
		return nil
	})
	if err != nil {
		return nil, lifecycleActionStatus(err, domain.FleetKind, name, appName)
	}

	h.events.CreateEvent(ctx, orgId, common.GetFleetApplicationLifecycleChangedEvent(ctx, name, appName, domain.ApplicationLifecycleActionStop))
	return h.GetFleet(ctx, orgId, name, domain.GetFleetParams{})
}

// StartFleetApplication sets a fleet-level default so that the named application's desiredState
// is "running" on every device currently owned by this fleet, independent of the application's
// declarative spec. Same recency-based arbitration against device-level overrides as
// StopFleetApplication.
func (h *ServiceHandler) StartFleetApplication(ctx context.Context, orgId uuid.UUID, name string, appName string) (*domain.Fleet, domain.Status) {
	if status := h.validateFleetForLifecycleAction(ctx, orgId, name, appName); status.Code != http.StatusOK {
		return nil, status
	}

	_, _, _, err := h.store.Mutate(ctx, orgId, name, nil, func(m *fleetstore.FleetMutation) error {
		if err := m.RequireExisting(); err != nil {
			return err
		}
		if err := ensureFleetLifecycleMutable(m.Fleet, appName); err != nil {
			return err
		}
		ann, err := domain.MergeApplicationLifecycleOverrides(
			lo.FromPtr(m.Fleet.Metadata.Annotations),
			domain.FleetAnnotationApplicationLifecycle,
			map[string]domain.ApplicationLifecycleOverride{
				appName: domain.NewDesiredStateOverride(domain.ApplicationDesiredStateRunning, domain.NewLifecycleVersion()),
			},
		)
		if err != nil {
			return err
		}
		m.Fleet.Metadata.Annotations = &ann
		return nil
	})
	if err != nil {
		return nil, lifecycleActionStatus(err, domain.FleetKind, name, appName)
	}

	h.events.CreateEvent(ctx, orgId, common.GetFleetApplicationLifecycleChangedEvent(ctx, name, appName, domain.ApplicationLifecycleActionStart))
	return h.GetFleet(ctx, orgId, name, domain.GetFleetParams{})
}

// validateFleetForLifecycleAction fetches the fleet and validates it exists and has an
// application named appName in its device template.
func (h *ServiceHandler) validateFleetForLifecycleAction(ctx context.Context, orgId uuid.UUID, name string, appName string) domain.Status {
	fleet, status := h.GetFleet(ctx, orgId, name, domain.GetFleetParams{})
	if status.Code != http.StatusOK {
		return status
	}
	if !domain.ApplicationsContainName(fleet.Spec.Template.Spec.Applications, appName) {
		return domain.StatusResourceNotFound("Application", appName)
	}
	return domain.StatusOK()
}
