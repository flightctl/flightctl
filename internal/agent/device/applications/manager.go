package applications

import (
	"context"
	"fmt"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/applications/provider"
	"github.com/flightctl/flightctl/internal/agent/device/dependency"
	"github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/agent/device/spec"
	"github.com/flightctl/flightctl/internal/agent/device/status"
	"github.com/flightctl/flightctl/internal/agent/device/systemd"
	"github.com/flightctl/flightctl/internal/agent/device/systeminfo"
	"github.com/flightctl/flightctl/internal/agent/shutdown"
	"github.com/flightctl/flightctl/pkg/log"
)

const (
	pullAuthPath       = "/root/.config/containers/auth.json"
	helmRegistryConfig = "/root/.config/helm/registry/config.json"
	helmRepoConfig     = "/root/.config/helm/repositories.yaml"
	criConfigPath      = "/etc/crictl.yaml"
)

var _ Manager = (*manager)(nil)

type manager struct {
	podmanMonitor     *PodmanMonitor
	kubernetesMonitor *KubernetesMonitor
	clients           client.CLIClients
	podmanFactory     client.PodmanFactory
	rwFactory         fileio.ReadWriterFactory
	specManager       spec.Manager
	log               *log.PrefixLogger
	bootTime          string

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
	specManager spec.Manager,
) Manager {
	bootTime := systemInfo.BootTime()
	return &manager{
		rwFactory:         rwFactory,
		podmanMonitor:     NewPodmanMonitor(log, podmanFactory, systemdFactory, bootTime, rwFactory),
		kubernetesMonitor: NewKubernetesMonitor(log, clients, rwFactory),
		podmanFactory:     podmanFactory,
		clients:           clients,
		specManager:       specManager,
		log:               log,
		bootTime:          bootTime,
		ociTargetCache:    provider.NewOCITargetCache(),
		appDataCache:      provider.NewAppDataCache(),
	}
}

func (m *manager) Ensure(ctx context.Context, provider provider.Provider) error {
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
			return fmt.Errorf("installing application: %w", err)
		}
		return m.kubernetesMonitor.Ensure(NewHelmApplication(provider))
	default:
		return fmt.Errorf("%w: %s", errors.ErrUnsupportedAppType, appType)
	}
}

func (m *manager) Remove(ctx context.Context, provider provider.Provider) error {
	appType := provider.Spec().AppType
	switch appType {
	case v1beta1.AppTypeCompose, v1beta1.AppTypeQuadlet, v1beta1.AppTypeContainer:
		if err := provider.Remove(ctx); err != nil {
			return fmt.Errorf("%w: %w", errors.ErrRemovingApplication, err)
		}
		return m.podmanMonitor.QueueRemove(NewApplication(provider))
	case v1beta1.AppTypeHelm:
		if err := provider.Remove(ctx); err != nil {
			return fmt.Errorf("removing application: %w", err)
		}
		return m.kubernetesMonitor.Remove(NewHelmApplication(provider))
	default:
		return fmt.Errorf("%w: %s", errors.ErrUnsupportedAppType, appType)
	}
}

func (m *manager) Update(ctx context.Context, provider provider.Provider) error {
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
			return fmt.Errorf("removing application: %w", err)
		}
		if err := provider.Install(ctx); err != nil {
			return fmt.Errorf("installing application: %w", err)
		}
		return m.kubernetesMonitor.Update(NewHelmApplication(provider))
	default:
		return fmt.Errorf("%w: %s", errors.ErrUnsupportedAppType, appType)
	}
}

func (m *manager) BeforeUpdate(ctx context.Context, desired *v1beta1.DeviceSpec) error {
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

func (m *manager) resolvePullConfigs(desired *v1beta1.DeviceSpec) (client.PullConfigProvider, error) {
	rootRW, err := m.rwFactory("")
	if err != nil {
		return nil, err
	}
	configs := make(map[client.ConfigType]*client.PullConfig)

	containerConfig, found, err := client.ResolvePullConfig(m.log, rootRW, desired, pullAuthPath)
	if err != nil {
		return nil, fmt.Errorf("resolving container auth config: %w", err)
	}
	if found {
		configs[client.ConfigTypeContainerSecret] = containerConfig
	}

	helmRegistryCfg, found, err := client.ResolvePullConfig(m.log, rootRW, desired, helmRegistryConfig)
	if err != nil {
		return nil, fmt.Errorf("resolving helm registry config: %w", err)
	}
	if found {
		configs[client.ConfigTypeHelmRegistrySecret] = helmRegistryCfg
	} else if containerConfig != nil {
		// Avoid double cleaning
		configs[client.ConfigTypeHelmRegistrySecret] = &client.PullConfig{
			Path:    containerConfig.Path,
			Cleanup: nil,
		}
	}

	helmRepoCfg, found, err := client.ResolvePullConfig(m.log, rootRW, desired, helmRepoConfig)
	if err != nil {
		return nil, fmt.Errorf("resolving helm repository config: %w", err)
	}
	if found {
		configs[client.ConfigTypeHelmRepoConfig] = helmRepoCfg
	}

	criConfig, found, err := client.ResolvePullConfig(m.log, rootRW, desired, criConfigPath)
	if err != nil {
		return nil, fmt.Errorf("resolving CRI config: %w", err)
	}
	if found {
		configs[client.ConfigTypeCRIConfig] = criConfig
	}

	return client.NewPullConfigProvider(configs), nil
}

func (m *manager) verifyProviders(ctx context.Context, providers []provider.Provider) error {
	osUpdatePending, err := m.specManager.IsOSUpdatePending(ctx)
	if err != nil {
		return fmt.Errorf("checking OS update status: %w", err)
	}

	for _, p := range providers {
		if err := p.Verify(ctx); err != nil {
			if errors.Is(err, errors.ErrAppDependency) && osUpdatePending {
				m.log.Infof("Deferring app %s until after OS update: %v", p.Name(), err)
				continue
			}
			return fmt.Errorf("verify app provider: %w", err)
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
		if err := m.kubernetesMonitor.Drain(ctx); err != nil {
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

	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

func isDeferredError(osUpdatePending bool, err error) bool {
	// if an OS Update is pending, and we receive and app dependencies are not yet resolved error, defer
	// full validation until the update occurs.
	return osUpdatePending && errors.Is(err, errors.ErrAppDependency)
}

// CollectOCITargets collects all OCI targets for the desired spec.
// It delegates to provider.CollectOCITargets which handles dependency checking,
// base image collection, nested extraction, and caching.
func (m *manager) CollectOCITargets(ctx context.Context, current, desired *v1beta1.DeviceSpec) (*dependency.OCICollection, error) {
	configProvider, err := m.resolvePullConfigs(desired)
	if err != nil {
		return nil, fmt.Errorf("resolving pull secrets: %w", err)
	}

	osUpdatePending, err := m.specManager.IsOSUpdatePending(ctx)
	if err != nil {
		configProvider.Cleanup()
		return nil, fmt.Errorf("checking if OS update is pending: %w", err)
	}

	collection, err := provider.CollectOCITargets(
		ctx,
		m.log,
		m.podmanFactory,
		m.clients,
		m.rwFactory,
		desired,
		provider.WithPullConfigProvider(configProvider),
		provider.WithOCICache(m.ociTargetCache),
		provider.WithAppData(m.appDataCache),
	)
	if err != nil {
		if !isDeferredError(osUpdatePending, err) {
			configProvider.Cleanup()
			return nil, fmt.Errorf("collecting OCI targets: %w", err)
		}
		m.log.Infof("Deferred dependency error during OCI collection: %v", err)
	}

	return collection, nil
}
