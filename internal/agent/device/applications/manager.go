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
) Manager {
	bootTime := systemInfo.BootTime()
	return &manager{
		rwFactory:         rwFactory,
		podmanMonitor:     NewPodmanMonitor(log, podmanFactory, systemdFactory, bootTime, rwFactory),
		kubernetesMonitor: NewKubernetesMonitor(log, clients, rwFactory),
		podmanFactory:     podmanFactory,
		clients:           clients,
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
			return fmt.Errorf("installing application: %w", err)
		}
		return m.podmanMonitor.Ensure(ctx, NewApplication(provider))
	case v1beta1.AppTypeHelm:
		if !m.kubernetesMonitor.IsEnabled() {
			return errors.ErrKubernetesAppsDisabled
		}
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
			return fmt.Errorf("removing application: %w", err)
		}
		return m.podmanMonitor.QueueRemove(NewApplication(provider))
	case v1beta1.AppTypeHelm:
		if !m.kubernetesMonitor.IsEnabled() {
			return errors.ErrKubernetesAppsDisabled
		}
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
			return fmt.Errorf("removing application: %w", err)
		}
		if err := provider.Install(ctx); err != nil {
			return fmt.Errorf("installing application: %w", err)
		}
		return m.podmanMonitor.QueueUpdate(NewApplication(provider))
	case v1beta1.AppTypeHelm:
		if !m.kubernetesMonitor.IsEnabled() {
			return errors.ErrKubernetesAppsDisabled
		}
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
	for _, provider := range providers {
		if err := provider.Verify(ctx); err != nil {
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

// CollectOCITargets implements two-phase collection:
// Phase 1: Collect base images and volumes (before images are available locally)
// Phase 2: Extract nested images from base images (after base images are fetched)
// The dependency manager calls this iteratively, fetching targets between calls.
//
// Caching: Nested targets are cached by application name. Cache entries store the parent
// image digest (for image-based apps) or children list (for inline apps) for invalidation.
func (m *manager) CollectOCITargets(ctx context.Context, current, desired *v1beta1.DeviceSpec) (*dependency.OCICollection, error) {
	if desired.Applications == nil || len(*desired.Applications) == 0 {
		m.log.Debug("No applications to collect OCI targets from")
		m.ociTargetCache.Clear()
		return &dependency.OCICollection{}, nil
	}

	configProvider, err := m.resolvePullConfigs(desired)
	if err != nil {
		return nil, fmt.Errorf("resolving pull secrets: %w", err)
	}

	baseTargets, err := provider.CollectBaseOCITargets(ctx, m.rwFactory, desired, configProvider)
	if err != nil {
		return nil, fmt.Errorf("collecting base OCI targets: %w", err)
	}
	m.log.Debugf("Collected %d base OCI targets", len(baseTargets))

	nestedTargets, requeue, activeNames, err := m.collectNestedTargets(ctx, desired, configProvider)
	if err != nil {
		return nil, fmt.Errorf("collecting nested OCI targets: %w", err)
	}
	m.log.Debugf("Collected %d nested OCI targets", len(nestedTargets))

	var allTargets dependency.OCIPullTargetsByUser
	allTargets = allTargets.MergeWith(baseTargets)
	allTargets = allTargets.MergeWith(nestedTargets)

	// garbage collect stale cache entries
	m.ociTargetCache.GC(activeNames)

	return &dependency.OCICollection{Targets: allTargets, Requeue: requeue}, nil
}

// collectNestedTargets collects nested OCI targets with per-application caching.
func (m *manager) collectNestedTargets(
	ctx context.Context,
	desired *v1beta1.DeviceSpec,
	configProvider client.PullConfigProvider,
) (dependency.OCIPullTargetsByUser, bool, []string, error) {
	var allNestedTargets dependency.OCIPullTargetsByUser
	var activeAppNames []string
	needsRequeue := false

	for _, appSpec := range *desired.Applications {
		appName, err := provider.ResolveImageAppName(&appSpec)
		if err != nil {
			return nil, false, nil, fmt.Errorf("resolving app name: %w", err)
		}
		activeAppNames = append(activeAppNames, appName)
		needsExtraction, err := provider.AppNeedsNestedExtraction(&appSpec)
		if err != nil {
			return nil, false, nil, fmt.Errorf("checking nested extraction for app %s: %w", appName, err)
		}
		if !needsExtraction {
			continue
		}

		targetUser, err := provider.ResolveUser(&appSpec)
		if err != nil {
			return nil, false, nil, fmt.Errorf("resolving user %q: %w", appSpec, err)
		}

		targets, requeue, err := m.collectNestedTargetsForApp(ctx, appSpec, configProvider)
		if err != nil {
			return nil, false, nil, fmt.Errorf("collecting nested targets for %s: %w", appName, err)
		}

		if requeue {
			needsRequeue = true
		}
		allNestedTargets = allNestedTargets.Add(targetUser, targets...)
	}

	return allNestedTargets, needsRequeue, activeAppNames, nil
}

// collectNestedTargetsForApp extracts nested OCI targets from a single image-based application.
func (m *manager) collectNestedTargetsForApp(
	ctx context.Context,
	appSpec v1beta1.ApplicationProviderSpec,
	configProvider client.PullConfigProvider,
) ([]dependency.OCIPullTarget, bool, error) {
	appName, err := provider.ResolveImageAppName(&appSpec)
	if err != nil {
		return nil, false, fmt.Errorf("resolving app name: %w", err)
	}

	ref, err := provider.ResolveImageRef(&appSpec)
	if err != nil {
		return nil, false, fmt.Errorf("resolving image ref: %w", err)
	}

	appType, err := appSpec.GetAppType()
	if err != nil {
		return nil, false, fmt.Errorf("getting app type: %w", err)
	}

	user, err := provider.ResolveUser(&appSpec)
	if err != nil {
		return nil, false, fmt.Errorf("resolving user %q: %w", appSpec, err)
	}

	available, ociType, digest, err := m.isParentAvailable(ctx, appType, ref, user)
	if err != nil {
		return nil, false, fmt.Errorf("checking parent availability: %w", err)
	}
	if !available {
		m.log.Debugf("Reference %s for app %s not available yet, skipping nested extraction", ref, appName)
		return nil, true, nil
	}

	if cachedEntry, found := m.ociTargetCache.Get(appName); found {
		if m.isCacheValid(cachedEntry, ref, digest) {
			m.log.Debugf("Using cached nested targets for app %s", appName)
			return cachedEntry.Children, false, nil
		}
		m.log.Debugf("Cache invalidated for app %s", appName)
	}

	appData, err := m.extractNestedTargetsForImage(ctx, appSpec, configProvider)
	if err != nil {
		return nil, false, fmt.Errorf("extracting nested targets for app %s: %w", appName, err)
	}

	m.appDataCache[appName] = appData

	m.ociTargetCache.Set(provider.CacheEntry{
		Name: appName,
		Parent: dependency.OCIPullTarget{
			Type:      ociType,
			Reference: ref,
			Digest:    digest,
		},
		Children: appData.Targets,
	})
	m.log.Debugf("Cached %d nested targets for app %s (type: %s)", len(appData.Targets), appName, ociType)

	return appData.Targets, false, nil
}

// isParentAvailable checks if the parent OCI target is available locally.
func (m *manager) isParentAvailable(ctx context.Context, appType v1beta1.AppType, ref string, user v1beta1.Username) (bool, dependency.OCIType, string, error) {
	switch appType {
	case v1beta1.AppTypeHelm:
		resolved, err := m.clients.Helm().IsResolved(ref)
		if err != nil {
			return false, "", "", fmt.Errorf("check chart resolved: %w", err)
		}
		return resolved, dependency.OCITypeHelmChart, "", nil
	default:
		podman, err := m.podmanFactory(user)
		if err != nil {
			return false, "", "", fmt.Errorf("creating podman client: %w", err)
		}

		if podman.ImageExists(ctx, ref) {
			digest, err := podman.ImageDigest(ctx, ref)
			if err != nil {
				return false, "", "", fmt.Errorf("getting image digest: %w", err)
			}
			return true, dependency.OCITypePodmanImage, digest, nil
		}
		if podman.ArtifactExists(ctx, ref) {
			digest, err := podman.ArtifactDigest(ctx, ref)
			if err != nil {
				return false, "", "", fmt.Errorf("getting artifact digest: %w", err)
			}
			return true, dependency.OCITypePodmanArtifact, digest, nil
		}
		return false, "", "", nil
	}
}

// isCacheValid checks if a cache entry is still valid for the given reference and digest.
func (m *manager) isCacheValid(entry provider.CacheEntry, ref, digest string) bool {
	if entry.Parent.Reference != ref {
		return false
	}
	if digest != "" && entry.Parent.Digest != digest {
		return false
	}
	return true
}

// extractNestedTargetsForImage extracts nested OCI targets from a single image-based application.
func (m *manager) extractNestedTargetsForImage(
	ctx context.Context,
	appSpec v1beta1.ApplicationProviderSpec,
	configProvider client.PullConfigProvider,
) (*provider.AppData, error) {
	return provider.ExtractNestedTargetsFromImage(
		ctx,
		m.log,
		m.podmanFactory,
		m.clients,
		m.rwFactory,
		&appSpec,
		configProvider,
	)
}
