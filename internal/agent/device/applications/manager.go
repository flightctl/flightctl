package applications

import (
	"context"
	"fmt"
	"sync"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/applications/provider"
	"github.com/flightctl/flightctl/internal/agent/device/dependency"
	"github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/agent/device/status"
	"github.com/flightctl/flightctl/internal/agent/device/systeminfo"
	"github.com/flightctl/flightctl/internal/agent/shutdown"
	"github.com/flightctl/flightctl/pkg/log"
)

const (
	pullAuthPath = "/root/.config/containers/auth.json"
)

var _ Manager = (*manager)(nil)

type manager struct {
	podmanMonitor *PodmanMonitor
	podmanClient  *client.Podman
	systemdClient *client.Systemd
	readWriter    fileio.ReadWriter
	log           *log.PrefixLogger
	once          sync.Once
}

func NewManager(
	log *log.PrefixLogger,
	readWriter fileio.ReadWriter,
	podmanClient *client.Podman,
	systemdClient *client.Systemd,
	systemInfo systeminfo.Manager,
) Manager {
	bootTime := systemInfo.BootTime()
	return &manager{
		readWriter:    readWriter,
		podmanMonitor: NewPodmanMonitor(log, podmanClient, bootTime, readWriter),
		podmanClient:  podmanClient,
		systemdClient: systemdClient,
		log:           log,
	}
}

func (m *manager) Ensure(ctx context.Context, provider provider.Provider) error {
	appType := provider.Spec().AppType
	switch appType {
	case v1alpha1.AppTypeCompose:
		if m.podmanMonitor.Has(provider.Spec().ID) {
			return nil
		}
		if err := provider.Install(ctx); err != nil {
			return fmt.Errorf("installing application: %w", err)
		}
		return m.podmanMonitor.Ensure(NewApplication(provider))
	default:
		return fmt.Errorf("%w: %s", errors.ErrUnsupportedAppType, appType)
	}
}

func (m *manager) Remove(ctx context.Context, provider provider.Provider) error {
	appType := provider.Spec().AppType
	switch appType {
	case v1alpha1.AppTypeCompose:
		if err := provider.Remove(ctx); err != nil {
			return fmt.Errorf("removing application: %w", err)
		}
		return m.podmanMonitor.Remove(NewApplication(provider))
	default:
		return fmt.Errorf("%w: %s", errors.ErrUnsupportedAppType, appType)
	}
}

func (m *manager) Update(ctx context.Context, provider provider.Provider) error {
	appType := provider.Spec().AppType
	switch appType {
	case v1alpha1.AppTypeCompose:
		if err := provider.Remove(ctx); err != nil {
			return fmt.Errorf("removing application: %w", err)
		}
		if err := provider.Install(ctx); err != nil {
			return fmt.Errorf("installing application: %w", err)
		}
		return m.podmanMonitor.Update(NewApplication(provider))
	default:
		return fmt.Errorf("%w: %s", errors.ErrUnsupportedAppType, appType)
	}
}

func (m *manager) BeforeUpdate(ctx context.Context, desired *v1alpha1.DeviceSpec) error {
	if desired.Applications == nil || len(*desired.Applications) == 0 {
		m.log.Debug("No applications to pre-check")
		return nil
	}
	m.log.Debug("Pre-checking application dependencies")
	defer m.log.Debug("Finished pre-checking application dependencies")

	providers, err := provider.FromDeviceSpec(ctx, m.log, m.podmanMonitor.client, m.readWriter, desired, provider.WithEmbedded())
	if err != nil {
		return fmt.Errorf("parsing apps: %w", err)
	}

	// the prefetch manager now handles scheduling internally via registered functions
	// we only need to verify providers once images are ready
	return m.verifyProviders(ctx, providers)
}

func (m *manager) resolvePullSecret(desired *v1alpha1.DeviceSpec) (*client.PullSecret, error) {
	secret, found, err := client.ResolvePullSecret(m.log, m.readWriter, desired, pullAuthPath)
	if err != nil {
		return nil, fmt.Errorf("resolving pull secret: %w", err)
	}
	if !found {
		return nil, nil
	}
	return secret, nil
}

func (m *manager) collectOCITargets(providers []provider.Provider, secret *client.PullSecret) ([]dependency.OCIPullTarget, error) {
	var targets []dependency.OCIPullTarget
	for _, provider := range providers {
		providerTargets, err := provider.OCITargets(secret)
		if err != nil {
			return nil, fmt.Errorf("provider oci targets: %w", err)
		}
		targets = append(targets, providerTargets...)
	}
	return targets, nil
}

func (m *manager) verifyProviders(ctx context.Context, providers []provider.Provider) error {
	for _, provider := range providers {
		if err := provider.Verify(ctx); err != nil {
			return fmt.Errorf("verify app provider: %w", err)
		}
	}
	return nil
}

func (m *manager) AfterUpdate(ctx context.Context) error {
	// execute actions for applications using the podman runtime - this includes
	// compose and quadlets
	if err := m.podmanMonitor.ExecuteActions(ctx); err != nil {
		return fmt.Errorf("error executing actions: %w", err)
	}
	return nil
}

func (m *manager) Status(ctx context.Context, status *v1alpha1.DeviceStatus, opts ...status.CollectorOpt) error {
	applicationsStatus, applicationSummary, err := m.podmanMonitor.Status()
	if err != nil {
		return err
	}

	status.ApplicationsSummary.Status = applicationSummary.Status
	status.ApplicationsSummary.Info = applicationSummary.Info
	status.Applications = applicationsStatus
	return nil
}

func (m *manager) Drain(ctx context.Context) (err error) {
	if !shutdown.IsSystemShutdown(ctx, m.systemdClient, m.readWriter, m.log) {
		m.log.Debug("System shutdown not detected, skipping drain")
		return nil
	}

	m.once.Do(func() {
		err = m.podmanMonitor.Drain(ctx)
	})
	if err != nil {
		return fmt.Errorf("drain workloads: %w", err)
	}
	return nil
}

// CollectOCITargets returns a function that collects OCI targets from applications
func (m *manager) CollectOCITargets(ctx context.Context, current, desired *v1alpha1.DeviceSpec) ([]dependency.OCIPullTarget, error) {
	if desired.Applications == nil || len(*desired.Applications) == 0 {
		m.log.Debug("No applications to collect OCI targets from")
		return nil, nil
	}

	m.log.Debug("Collecting OCI targets from applications")

	// parse applications and create providers
	providers, err := provider.FromDeviceSpec(ctx, m.log, m.podmanMonitor.client, m.readWriter, desired, provider.WithEmbedded())
	if err != nil {
		return nil, fmt.Errorf("parsing applications: %w", err)
	}

	// resolve pull secret
	secret, err := m.resolvePullSecret(desired)
	if err != nil {
		return nil, fmt.Errorf("resolving pull secret: %w", err)
	}

	// collect OCI targets from all providers
	targets, err := m.collectOCITargets(providers, secret)
	if err != nil {
		return nil, fmt.Errorf("collecting OCI targets: %w", err)
	}

	m.log.Debugf("Collected %d OCI targets from applications", len(targets))
	return targets, nil
}
