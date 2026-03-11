package applications

import (
	"context"
	"fmt"
	"sort"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/applications/provider"
	"github.com/flightctl/flightctl/internal/agent/device/dependency"
	"github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/agent/device/status"
	"github.com/flightctl/flightctl/internal/agent/device/systemd"
	"github.com/flightctl/flightctl/internal/agent/device/systeminfo"
	"github.com/flightctl/flightctl/internal/agent/shutdown"
	"github.com/flightctl/flightctl/pkg/log"
)

var _ Manager = (*manager)(nil)

type manager struct {
	podmanMonitor      *PodmanMonitor
	kubernetesMonitor  *KubernetesMonitor
	clients            client.CLIClients
	podmanFactory      client.PodmanFactory
	rwFactory          fileio.ReadWriterFactory
	pullConfigResolver dependency.PullConfigResolver
	log                *log.PrefixLogger
	bootTime           string

	// osUpdatePending is cached from BeforeUpdate for use during syncDevice
	osUpdatePending bool

	// cache of extracted nested OCI targets
	ociTargetCache *provider.OCITargetCache

	// cache of temporary extracted app data
	appDataCache map[string]*provider.AppData
}

func NewManager(
	log *log.PrefixLogger,
	rwFactory fileio.ReadWriterFactory,
	podmanFactory client.PodmanFactory,
	clients client.CLIClients,
	systemInfo systeminfo.Manager,
	systemdFactory systemd.ManagerFactory,
	pullConfigResolver dependency.PullConfigResolver,
) Manager {
	bootTime := systemInfo.BootTime()
	return &manager{
		rwFactory:          rwFactory,
		podmanMonitor:      NewPodmanMonitor(log, podmanFactory, systemdFactory, bootTime, rwFactory),
		kubernetesMonitor:  NewKubernetesMonitor(log, clients, rwFactory),
		podmanFactory:      podmanFactory,
		clients:            clients,
		pullConfigResolver: pullConfigResolver,
		log:                log,
		bootTime:           bootTime,
		ociTargetCache:     provider.NewOCITargetCache(),
		appDataCache:       provider.NewAppDataCache(),
	}
}

func isDeferrableAppError(osUpdatePending bool, err error) bool {
	return osUpdatePending && errors.Is(err, errors.ErrAppDependency)
}

func (m *manager) validateProviderDeps(ctx context.Context, p provider.Provider) error {
	if err := p.EnsureDependencies(ctx); err != nil {
		if !isDeferrableAppError(m.osUpdatePending, err) {
			return err
		}
		m.log.Infof("%s is missing app dependencies. Deferring application modification until after OS update: %v", p.Name(), err)
	}
	return nil
}

func (m *manager) Ensure(ctx context.Context, provider provider.Provider) error {
	if err := m.validateProviderDeps(ctx, provider); err != nil {
		return err
	}
	appType := provider.Spec().AppType
	switch appType {
	case v1beta1.AppTypeCompose, v1beta1.AppTypeQuadlet, v1beta1.AppTypeContainer:
		if m.podmanMonitor.Has(provider.Spec().ID) {
			return nil
		}
		if err := provider.Install(ctx); err != nil {
			return fmt.Errorf("%w: %w", errors.ErrInstallingApplication, err)
		}
		return m.podmanMonitor.Ensure(ctx, NewApplication(provider))
	case v1beta1.AppTypeHelm:
		if m.kubernetesMonitor.Has(provider.Spec().ID) {
			return nil
		}
		if err := provider.Install(ctx); err != nil {
			return fmt.Errorf("%w: %w", errors.ErrInstallingApplication, err)
		}
		return m.kubernetesMonitor.Ensure(NewHelmApplication(provider))
	default:
		return fmt.Errorf("%w: %s", errors.ErrUnsupportedAppType, appType)
	}
}

func (m *manager) Remove(ctx context.Context, provider provider.Provider) error {
	if err := provider.Remove(ctx); err != nil {
		return fmt.Errorf("%w: %w", errors.ErrRemovingApplication, err)
	}

	// If dependencies are missing (e.g., kubernetes unavailable for helm apps),
	// we can't queue the monitor action. The important cleanup already happened
	// via provider.Remove() above, so we log and continue for idempotent removal.
	if err := m.validateProviderDeps(ctx, provider); err != nil {
		m.log.Warnf("Skipping monitor removal action for %s: %v", provider.Name(), err)
		return nil
	}

	appType := provider.Spec().AppType
	switch appType {
	case v1beta1.AppTypeCompose, v1beta1.AppTypeQuadlet, v1beta1.AppTypeContainer:
		return m.podmanMonitor.QueueRemove(NewApplication(provider))
	case v1beta1.AppTypeHelm:
		return m.kubernetesMonitor.Remove(NewHelmApplication(provider))
	default:
		return fmt.Errorf("%w: %s", errors.ErrUnsupportedAppType, appType)
	}
}

func (m *manager) Update(ctx context.Context, provider provider.Provider) error {
	if err := m.validateProviderDeps(ctx, provider); err != nil {
		return err
	}
	appType := provider.Spec().AppType
	switch appType {
	case v1beta1.AppTypeCompose, v1beta1.AppTypeQuadlet, v1beta1.AppTypeContainer:
		if err := provider.Remove(ctx); err != nil {
			return fmt.Errorf("%w: %w", errors.ErrRemovingApplication, err)
		}
		if err := provider.Install(ctx); err != nil {
			return fmt.Errorf("%w: %w", errors.ErrInstallingApplication, err)
		}
		return m.podmanMonitor.QueueUpdate(NewApplication(provider))
	case v1beta1.AppTypeHelm:
		if err := provider.Remove(ctx); err != nil {
			return fmt.Errorf("%w: %w", errors.ErrRemovingApplication, err)
		}
		if err := provider.Install(ctx); err != nil {
			return fmt.Errorf("%w: %w", errors.ErrInstallingApplication, err)
		}
		return m.kubernetesMonitor.Update(NewHelmApplication(provider))
	default:
		return fmt.Errorf("%w: %s", errors.ErrUnsupportedAppType, appType)
	}
}

func (m *manager) BeforeUpdate(ctx context.Context, desired *v1beta1.DeviceSpec, opts ...UpdateOpt) error {
	o := applyUpdateOpts(opts...)
	m.osUpdatePending = o.osUpdatePending

	if desired.Applications == nil || len(*desired.Applications) == 0 {
		m.log.Debug("No applications to pre-check")
		return nil
	}
	m.log.Debug("Pre-checking application dependencies")
	defer m.log.Debug("Finished pre-checking application dependencies")

	providers, err := provider.FromDeviceSpec(
		ctx,
		m.log,
		m.podmanFactory,
		m.clients,
		m.rwFactory,
		desired,
		provider.WithEmbedded(m.bootTime),
		provider.WithAppDataCache(m.appDataCache),
	)
	if err != nil {
		return fmt.Errorf("parsing apps: %w", err)
	}

	return m.verifyProviders(ctx, providers)
}

func (m *manager) verifyProviders(ctx context.Context, providers []provider.Provider) error {
	for _, p := range providers {
		if err := p.Verify(ctx); err != nil {
			if !isDeferrableAppError(m.osUpdatePending, err) {
				return fmt.Errorf("verify app provider: %w: %w", errors.WithElement(p.Name()), err)
			}
			m.log.Infof("%s is missing app dependencies. Deferring application validation until after OS update: %v", p.Name(), err)
		}
	}
	return nil
}

func (m *manager) AfterUpdate(ctx context.Context) error {
	defer m.clearAppDataCache()

	if err := m.podmanMonitor.ExecuteActions(ctx); err != nil {
		return fmt.Errorf("error executing podman actions: %w", err)
	}

	if err := m.kubernetesMonitor.ExecuteActions(ctx); err != nil {
		return fmt.Errorf("error executing kubernetes actions: %w", err)
	}

	return nil
}

func (m *manager) clearAppDataCache() {
	for name, cachedData := range m.appDataCache {
		if err := cachedData.Cleanup(); err != nil {
			m.log.Warnf("Failed to cleanup extraction for app %s: %v", name, err)
		}
	}
	m.appDataCache = provider.NewAppDataCache()
}

func (m *manager) Status(ctx context.Context, status *v1beta1.DeviceStatus, opts ...status.CollectorOpt) error {
	var allResults []AppStatusResult

	podmanResults, err := m.podmanMonitor.Status()
	if err != nil {
		return err
	}
	allResults = append(allResults, podmanResults...)

	k8sResults, err := m.kubernetesMonitor.Status()
	if err != nil {
		return err
	}
	allResults = append(allResults, k8sResults...)

	statuses, summary := aggregateAppStatuses(allResults)
	status.ApplicationsSummary = summary
	status.Applications = statuses
	return nil
}

func aggregateAppStatuses(results []AppStatusResult) ([]v1beta1.DeviceApplicationStatus, v1beta1.DeviceApplicationsSummaryStatus) {
	if len(results) == 0 {
		return []v1beta1.DeviceApplicationStatus{}, v1beta1.DeviceApplicationsSummaryStatus{
			Status: v1beta1.ApplicationsSummaryStatusNoApplications,
		}
	}

	// Sort by app name so status.Applications and summary.Info are deterministic; results order comes from map iteration.
	sort.Slice(results, func(i, j int) bool { return results[i].Status.Name < results[j].Status.Name })

	statuses := make([]v1beta1.DeviceApplicationStatus, 0, len(results))
	var overallStatus v1beta1.ApplicationsSummaryStatusType
	var erroredApps []string
	var degradedApps []string

	for _, result := range results {
		statuses = append(statuses, result.Status)

		switch result.Summary.Status {
		case v1beta1.ApplicationsSummaryStatusError:
			erroredApps = append(erroredApps, fmt.Sprintf("%s is in status %s", result.Status.Name, result.Summary.Status))
			overallStatus = v1beta1.ApplicationsSummaryStatusError
		case v1beta1.ApplicationsSummaryStatusDegraded:
			degradedApps = append(degradedApps, fmt.Sprintf("%s is in status %s", result.Status.Name, result.Summary.Status))
			if overallStatus != v1beta1.ApplicationsSummaryStatusError {
				overallStatus = v1beta1.ApplicationsSummaryStatusDegraded
			}
		case v1beta1.ApplicationsSummaryStatusUnknown:
			degradedApps = append(degradedApps, fmt.Sprintf("Not started: %s", result.Status.Name))
			if overallStatus != v1beta1.ApplicationsSummaryStatusError {
				overallStatus = v1beta1.ApplicationsSummaryStatusDegraded
			}
		case v1beta1.ApplicationsSummaryStatusHealthy:
			if overallStatus != v1beta1.ApplicationsSummaryStatusError &&
				overallStatus != v1beta1.ApplicationsSummaryStatusDegraded {
				overallStatus = v1beta1.ApplicationsSummaryStatusHealthy
			}
		}
	}

	summary := v1beta1.DeviceApplicationsSummaryStatus{Status: overallStatus}
	if len(erroredApps) > 0 || len(degradedApps) > 0 {
		summary.Info = buildAppSummaryInfo(erroredApps, degradedApps, maxAppSummaryInfoLength)
	}
	return statuses, summary
}

func (m *manager) Shutdown(ctx context.Context, state shutdown.State) error {
	var errs []error

	if state.SystemShutdown {
		m.log.Info("System shutdown detected - draining applications")
		if err := m.podmanMonitor.Drain(ctx); err != nil {
			errs = append(errs, err)
		}
		// For kubernetes/helm apps, just stop monitoring. Unlike podman apps,
		// helm uninstall would delete the release and all its resources (including
		// PVCs), causing data loss. The cluster manages its own state across reboots.
		if err := m.kubernetesMonitor.Stop(); err != nil {
			errs = append(errs, err)
		}
	} else {
		m.log.Debug("Agent restart detected - stopping monitors")
		if err := m.podmanMonitor.Stop(); err != nil {
			errs = append(errs, err)
		}
		if err := m.kubernetesMonitor.Stop(); err != nil {
			errs = append(errs, err)
		}
	}

	return errors.Join(errs...)
}

// CollectOCITargets implements two-phase collection:
// Phase 1: Collect base images and volumes (before images are available locally)
// Phase 2: Extract nested images from base images (after base images are fetched)
// The dependency manager calls this iteratively, fetching targets between calls.
//
// Caching: Nested targets are cached by application name. Cache entries store the parent
// image digest (for image-based apps) or children list (for inline apps) for invalidation.
func (m *manager) CollectOCITargets(ctx context.Context, current, desired *v1beta1.DeviceSpec, opts ...dependency.OCICollectOpt) (*dependency.OCICollection, error) {
	o := dependency.ApplyOCICollectOpts(opts...)
	osUpdatePending := o.OSUpdatePending()

	collection, err := provider.CollectOCITargets(
		ctx,
		m.log,
		m.podmanFactory,
		m.clients,
		m.rwFactory,
		desired,
		provider.WithPullConfigResolver(m.pullConfigResolver),
		provider.WithOCICache(m.ociTargetCache),
		provider.WithAppData(m.appDataCache),
	)
	if err != nil {
		if !isDeferrableAppError(osUpdatePending, err) {
			return nil, fmt.Errorf("%w: %w", errors.ErrExtractingOCI, err)
		}
		m.log.Infof("Missing app dependencies, deferring OCI target collection until after OS update: %v", err)
	}

	return collection, nil
}
