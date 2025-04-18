package applications

import (
	"context"
	"fmt"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/applications/provider"
	"github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/agent/device/systeminfo"
	"github.com/flightctl/flightctl/pkg/log"
)

var _ Manager = (*manager)(nil)

type manager struct {
	podmanMonitor *PodmanMonitor
	readWriter    fileio.ReadWriter
	log           *log.PrefixLogger
}

func NewManager(
	log *log.PrefixLogger,
	readWriter fileio.ReadWriter,
	podmanClient *client.Podman,
	systemInfo systeminfo.Manager,
) Manager {
	bootTime := systemInfo.BootTime()
	return &manager{
		readWriter:    readWriter,
		podmanMonitor: NewPodmanMonitor(log, podmanClient, bootTime, readWriter),
		log:           log,
	}
}

// Add an application to be managed
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

// Remove by name
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

// Update an application
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

// BeforeUpdate prepares the manager for reconciliation.
func (m *manager) BeforeUpdate(ctx context.Context, desired *v1alpha1.DeviceSpec) error {
	if desired.Applications == nil || len(*desired.Applications) == 0 {
		m.log.Debug("No applications to pre-check")
		return nil
	}
	m.log.Debug("Pre-checking application dependencies")
	defer m.log.Info("Finished pre-checking application dependencies")

	providers, err := provider.FromDeviceSpec(ctx, m.log, m.podmanMonitor.client, m.readWriter, desired, provider.WithEmbedded())
	if err != nil {
		return fmt.Errorf("parsing apps: %w", err)
	}

	for _, provider := range providers {
		// verify the application content is valid and dependencies are met.
		if err := provider.Verify(ctx); err != nil {
			return fmt.Errorf("initializing application: %w", err)
		}
	}

	return nil
}

// AfterUpdate executes actions generated by the manager during reconciliation.
func (m *manager) AfterUpdate(ctx context.Context) error {
	// execute actions for applications using the podman runtime this includes
	// compose and quadlets.
	if err := m.podmanMonitor.ExecuteActions(ctx); err != nil {
		return fmt.Errorf("error executing actions: %w", err)
	}
	return nil
}

func (m *manager) Status(ctx context.Context, status *v1alpha1.DeviceStatus) error {
	applicationsStatus, applicationSummary, err := m.podmanMonitor.Status()
	if err != nil {
		return err
	}

	status.ApplicationsSummary.Status = applicationSummary.Status
	status.ApplicationsSummary.Info = applicationSummary.Info
	status.Applications = applicationsStatus
	return nil
}

func (m *manager) Stop(ctx context.Context) error {
	return m.podmanMonitor.Stop(ctx)
}
