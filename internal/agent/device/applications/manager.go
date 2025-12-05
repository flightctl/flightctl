package applications

import (
	"context"
	"fmt"

	"github.com/flightctl/flightctl/api/v1beta1"
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
	"github.com/samber/lo"
)

const (
	pullAuthPath = "/root/.config/containers/auth.json"
)

var _ Manager = (*manager)(nil)

type manager struct {
	podmanMonitor *PodmanMonitor
	podmanClient  *client.Podman
	readWriter    fileio.ReadWriter
	log           *log.PrefixLogger
	bootTime      string

	// cache of extracted nested OCI targets
	ociTargetCache *provider.OCITargetCache

	// cache of temporary extracted app data
	appDataCache map[string]*provider.AppData
}

func NewManager(
	log *log.PrefixLogger,
	readWriter fileio.ReadWriter,
	podmanClient *client.Podman,
	systemInfo systeminfo.Manager,
	systemdManager systemd.Manager,
) Manager {
	bootTime := systemInfo.BootTime()
	return &manager{
		readWriter:     readWriter,
		podmanMonitor:  NewPodmanMonitor(log, podmanClient, systemdManager, bootTime, readWriter),
		podmanClient:   podmanClient,
		log:            log,
		bootTime:       bootTime,
		ociTargetCache: provider.NewOCITargetCache(),
		appDataCache:   provider.NewAppDataCache(),
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
		return m.podmanMonitor.Ensure(NewApplication(provider))
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
		return m.podmanMonitor.Remove(NewApplication(provider))
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
		return m.podmanMonitor.Update(NewApplication(provider))
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
		m.podmanMonitor.client,
		m.readWriter,
		desired,
		provider.WithEmbedded(m.bootTime),
		provider.WithAppDataCache(m.appDataCache),
	)
	if err != nil {
		return fmt.Errorf("parsing apps: %w", err)
	}

	// the prefetch manager now handles scheduling internally via registered functions
	// we only need to verify providers once images are ready
	return m.verifyProviders(ctx, providers)
}

func (m *manager) resolvePullSecret(desired *v1beta1.DeviceSpec) (*client.PullSecret, error) {
	secret, found, err := client.ResolvePullSecret(m.log, m.readWriter, desired, pullAuthPath)
	if err != nil {
		return nil, fmt.Errorf("resolving pull secret: %w", err)
	}
	if !found {
		return nil, nil
	}
	return secret, nil
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
	// cleanup extraction cache from this sync cycle
	defer m.clearAppDataCache()

	// execute actions for applications using the podman runtime - this includes
	// compose and quadlets
	if err := m.podmanMonitor.ExecuteActions(ctx); err != nil {
		return fmt.Errorf("error executing actions: %w", err)
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
	applicationsStatus, applicationSummary, err := m.podmanMonitor.Status()
	if err != nil {
		return err
	}

	status.ApplicationsSummary.Status = applicationSummary.Status
	status.ApplicationsSummary.Info = applicationSummary.Info
	status.Applications = applicationsStatus
	return nil
}

func (m *manager) Shutdown(ctx context.Context, state shutdown.State) error {
	if state.SystemShutdown {
		m.log.Info("System shutdown detected - draining applications")
		return m.podmanMonitor.Drain(ctx)
	} else {
		m.log.Debug("Agent restart detected - stopping monitor")
		return m.podmanMonitor.Stop()
	}
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

	// resolve pull secret
	secret, err := m.resolvePullSecret(desired)
	if err != nil {
		return nil, fmt.Errorf("resolving pull secret: %w", err)
	}

	baseTargets, err := provider.CollectBaseOCITargets(ctx, m.readWriter, desired, secret)
	if err != nil {
		return nil, fmt.Errorf("collecting base OCI targets: %w", err)
	}
	m.log.Debugf("Collected %d base OCI targets", len(baseTargets))

	nestedTargets, requeue, activeNames, err := m.collectNestedTargets(ctx, desired, secret)
	if err != nil {
		return nil, fmt.Errorf("collecting nested OCI targets: %w", err)
	}
	m.log.Debugf("Collected %d nested OCI targets", len(nestedTargets))

	var allTargets []dependency.OCIPullTarget
	allTargets = append(allTargets, baseTargets...)
	allTargets = append(allTargets, nestedTargets...)

	// garbage collect stale cache entries
	m.ociTargetCache.GC(activeNames)

	return &dependency.OCICollection{Targets: allTargets, Requeue: requeue}, nil
}

// collectNestedTargets collects nested OCI targets with per-application caching.
func (m *manager) collectNestedTargets(
	ctx context.Context,
	desired *v1beta1.DeviceSpec,
	secret *client.PullSecret,
) ([]dependency.OCIPullTarget, bool, []string, error) {
	var allNestedTargets []dependency.OCIPullTarget
	var activeAppNames []string
	needsRequeue := false

	for _, appSpec := range *desired.Applications {
		appName := lo.FromPtr(appSpec.Name)
		activeAppNames = append(activeAppNames, appName)

		providerType, err := appSpec.Type()
		if err != nil {
			return nil, false, nil, fmt.Errorf("getting provider type for app %s: %w", appName, err)
		}

		// only image-based apps have nested targets extracted from parent images
		if providerType != v1beta1.ImageApplicationProviderType {
			continue
		}

		imageSpec, err := appSpec.AsImageApplicationProviderSpec()
		if err != nil {
			return nil, false, nil, fmt.Errorf("getting image spec for app %s: %w", appName, err)
		}

		imageRef := imageSpec.Image

		// Detect if reference is an artifact or image and check if it exists locally
		var digest string
		var ociType dependency.OCIType
		var exists bool

		// Check if it's an image first (most common case)
		if m.podmanClient.ImageExists(ctx, imageRef) {
			ociType = dependency.OCITypeImage
			exists = true
			digest, err = m.podmanClient.ImageDigest(ctx, imageRef)
			if err != nil {
				return nil, false, nil, fmt.Errorf("getting image digest for %s: %w", imageRef, err)
			}
		} else if m.podmanClient.ArtifactExists(ctx, imageRef) {
			ociType = dependency.OCITypeArtifact
			exists = true
			digest, err = m.podmanClient.ArtifactDigest(ctx, imageRef)
			if err != nil {
				return nil, false, nil, fmt.Errorf("getting artifact digest for %s: %w", imageRef, err)
			}
		}

		if !exists {
			m.log.Debugf("Reference %s for app %s not available yet, skipping nested extraction", imageRef, appName)
			needsRequeue = true
			continue
		}

		if cachedEntry, found := m.ociTargetCache.Get(appName); found {
			if cachedEntry.Parent.Digest == digest {
				// cache hit - parent digest matches
				m.log.Debugf("Using cached nested targets for app %s (digest: %s)", appName, digest)
				allNestedTargets = append(allNestedTargets, cachedEntry.Children...)
				continue
			}
			m.log.Debugf("Cache invalidated for app %s - digest changed from %s to %s", appName, cachedEntry.Parent.Digest, digest)
		}

		// cache miss or invalid - extract nested targets for this image
		appData, err := m.extractNestedTargetsForImage(ctx, appSpec, &imageSpec, secret)
		if err != nil {
			return nil, false, nil, fmt.Errorf("extracting nested targets for app %s: %w", appName, err)
		}

		// store app data for reuse during Verify
		m.appDataCache[appName] = appData

		// update nested targets cache
		cacheEntry := provider.CacheEntry{
			Name: appName,
			Parent: dependency.OCIPullTarget{
				Type:      ociType,
				Reference: imageRef,
				Digest:    digest,
			},
			Children: appData.Targets,
		}
		m.ociTargetCache.Set(cacheEntry)
		m.log.Debugf("Cached %d nested targets for app %s (type: %s, digest: %s)", len(appData.Targets), appName, ociType, digest)

		allNestedTargets = append(allNestedTargets, appData.Targets...)
	}

	return allNestedTargets, needsRequeue, activeAppNames, nil
}

// extractNestedTargetsForImage extracts nested OCI targets from a single image-based application.
func (m *manager) extractNestedTargetsForImage(
	ctx context.Context,
	appSpec v1beta1.ApplicationProviderSpec,
	imageSpec *v1beta1.ImageApplicationProviderSpec,
	secret *client.PullSecret,
) (*provider.AppData, error) {
	return provider.ExtractNestedTargetsFromImage(
		ctx,
		m.log,
		m.podmanMonitor.client,
		m.readWriter,
		&appSpec,
		imageSpec,
		secret,
	)
}
