package applications

import (
	"context"
	"fmt"
	"maps"
	"sort"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	grpc_v1 "github.com/flightctl/flightctl/api/grpc/v1"
	"github.com/flightctl/flightctl/internal/agent/client"
	appconsole "github.com/flightctl/flightctl/internal/agent/device/applications/console"
	"github.com/flightctl/flightctl/internal/agent/device/applications/provider"
	"github.com/flightctl/flightctl/internal/agent/device/deltastatus"
	"github.com/flightctl/flightctl/internal/agent/device/dependency"
	"github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/agent/device/spec"
	"github.com/flightctl/flightctl/internal/agent/device/status"
	"github.com/flightctl/flightctl/internal/agent/device/systemd"
	"github.com/flightctl/flightctl/internal/agent/device/systeminfo"
	"github.com/flightctl/flightctl/internal/agent/shutdown"
	"github.com/flightctl/flightctl/internal/appspec"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/opencontainers/go-digest"
)

var _ Manager = (*manager)(nil)

type manager struct {
	deltaStatusStore   *deltastatus.Store
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

	// verified image-volume identities avoid repeating an image inspect while
	// the same application lifetime and volume incarnation remain installed.
	imageVolumeObservations map[imageVolumeObservationKey]cachedImageVolumeObservation

	// appConsole is created by WithConsole and owned by this manager.
	// executor/dialFn live on PodmanMonitor (VM/serial-console specific).
	appConsole *appconsole.Manager
}

func NewManager(
	log *log.PrefixLogger,
	rwFactory fileio.ReadWriterFactory,
	podmanFactory client.PodmanFactory,
	clients client.CLIClients,
	systemInfo systeminfo.Manager,
	systemdFactory systemd.ManagerFactory,
	pullConfigResolver dependency.PullConfigResolver,
	opts ...ManagerOption,
) *manager {
	bootTime := systemInfo.BootTime()
	kubernetesMonitor := NewKubernetesMonitor(log, clients, rwFactory, pullConfigResolver)
	manager := &manager{
		rwFactory:          rwFactory,
		kubernetesMonitor:  kubernetesMonitor,
		podmanFactory:      podmanFactory,
		clients:            clients,
		pullConfigResolver: pullConfigResolver,
		log:                log,
		bootTime:           bootTime,
		ociTargetCache:     provider.NewOCITargetCache(),
		appDataCache:       provider.NewAppDataCache(),
	}
	for _, opt := range opts {
		opt(manager)
	}
	manager.podmanMonitor = NewPodmanMonitor(log, podmanFactory, systemdFactory, bootTime, rwFactory, WithPodmanMonitorDeltaStatusStore(manager.deltaStatusStore))
	return manager
}

// ManagerOption configures optional application reporting state.
type ManagerOption func(*manager)

func WithDeltaStatusStore(store *deltastatus.Store) ManagerOption {
	return func(m *manager) { m.deltaStatusStore = store }
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

func (m *manager) newAppFromProvider(p provider.Provider) Application {
	spec := p.Spec()
	app := NewApplication(p)
	if spec.AppType == v1beta1.AppTypeHelm {
		app = NewHelmApplication(p)
	}
	// Capture expected images from the installed provider, not a future desired spec.
	app.imageCollectionComplete = spec.AppType == v1beta1.AppTypeContainer
	// Charts and Compose or Quadlet bundles are package references; their
	// workload images are tracked separately by the OCI cache.
	if spec.AppType != v1beta1.AppTypeHelm && spec.AppType != v1beta1.AppTypeCompose && spec.AppType != v1beta1.AppTypeQuadlet && spec.Image != "" {
		app.requiredImages = append(app.requiredImages, spec.Image)
	}
	if m.ociTargetCache != nil {
		if entry, found := m.ociTargetCache.Get(p.ID()); found {
			app.imageCollectionComplete = true
			for _, target := range entry.Children {
				app.requiredImages = append(app.requiredImages, target.Reference)
			}
		}
	}
	if spec.AppType == v1beta1.AppTypeCompose && spec.Image == "" {
		images, complete := inlineComposeWorkloadImages(spec.ComposeApp)
		app.requiredImages = append(app.requiredImages, images...)
		app.imageCollectionComplete = complete
	}
	if spec.AppType == v1beta1.AppTypeQuadlet && spec.Image == "" {
		images, complete := inlineQuadletImageReferences(spec.QuadletApp)
		app.requiredImages = append(app.requiredImages, images...)
		app.imageCollectionComplete = complete
	}
	return app
}

func inlineComposeWorkloadImages(composeApp *v1beta1.ComposeApplication) ([]string, bool) {
	if composeApp == nil {
		return nil, false
	}
	inline, err := composeApp.AsInlineApplicationProviderSpec()
	if err != nil {
		return nil, false
	}
	composeSpec, err := appspec.ParseComposeFromSpec(inline.Inline)
	if err != nil {
		return nil, false
	}
	images := make([]string, 0, len(composeSpec.Services))
	for _, service := range composeSpec.Services {
		if service.Image != "" {
			images = append(images, service.Image)
		}
	}
	return images, true
}

func inlineQuadletImageReferences(quadletApp *v1beta1.QuadletApplication) ([]string, bool) {
	if quadletApp == nil {
		return nil, false
	}
	inline, err := quadletApp.AsInlineApplicationProviderSpec()
	if err != nil {
		return nil, false
	}
	images, err := appspec.ParseQuadletImageReferencesFromSpec(inline.Inline)
	if err != nil {
		return nil, false
	}
	return images, true
}

func (m *manager) attachApplicationLifetime(p provider.Provider) {
	if m.deltaStatusStore == nil || p == nil || p.Spec() == nil {
		return
	}
	p.Spec().Lifetime = ""
	lifetime, err := m.deltaStatusStore.EnsureApplicationLifetime(p.Spec().Name)
	if err != nil {
		m.log.Warnf("Cannot persist application lifetime for %s; delta evidence will be withheld: %v", p.Spec().Name, err)
		return
	}
	p.Spec().Lifetime = lifetime
}

func (m *manager) Ensure(ctx context.Context, provider provider.Provider) error {
	m.attachApplicationLifetime(provider)
	if err := m.validateProviderDeps(ctx, provider); err != nil {
		return err
	}
	appType := provider.Spec().AppType
	switch appType {
	case v1beta1.AppTypeCompose, v1beta1.AppTypeQuadlet, v1beta1.AppTypeContainer:
		if m.podmanMonitor.Has(provider.Spec().ID) {
			m.podmanMonitor.QueueLifecycle(
				provider.Spec().ID,
				provider.Spec().DesiredState,
				provider.Spec().RestartGeneration,
			)
			return nil
		}
		if err := provider.Install(ctx); err != nil {
			return fmt.Errorf("%w %w: %w", errors.ErrInstallingApplication, errors.WithElement(provider.Name()), err)
		}
		return m.podmanMonitor.Ensure(ctx, m.newAppFromProvider(provider))
	case v1beta1.AppTypeHelm:
		if m.kubernetesMonitor.Has(provider.Spec().ID) {
			m.kubernetesMonitor.QueueLifecycle(
				provider.Spec().ID,
				provider.Spec().DesiredState,
				provider.Spec().RestartGeneration,
			)
			return nil
		}
		if err := provider.Install(ctx); err != nil {
			return fmt.Errorf("%w %w: %w", errors.ErrInstallingApplication, errors.WithElement(provider.Name()), err)
		}
		return m.kubernetesMonitor.Ensure(m.newAppFromProvider(provider))
	default:
		return fmt.Errorf("%w: %s", errors.ErrUnsupportedAppType, appType)
	}
}

func (m *manager) Remove(ctx context.Context, provider provider.Provider) error {
	if m.deltaStatusStore != nil {
		provider.Spec().Lifetime = m.deltaStatusStore.ApplicationLifetime(provider.Spec().Name)
	}
	if err := provider.Remove(ctx); err != nil {
		return fmt.Errorf("%w %w: %w", errors.ErrRemovingApplication, errors.WithElement(provider.Name()), err)
	}
	m.clearImageVolumeObservations(provider.Spec().ID)
	if m.deltaStatusStore != nil {
		if err := m.deltaStatusStore.RemoveApplication(provider.Spec().Name); err != nil {
			m.log.Warnf("Failed to remove application delta state for %s: %v", provider.Spec().Name, err)
		}
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
	m.attachApplicationLifetime(provider)
	if err := m.validateProviderDeps(ctx, provider); err != nil {
		return err
	}
	appType := provider.Spec().AppType
	switch appType {
	case v1beta1.AppTypeCompose, v1beta1.AppTypeQuadlet, v1beta1.AppTypeContainer:
		if err := provider.Remove(ctx); err != nil {
			return fmt.Errorf("%w: %w", errors.ErrRemovingApplication, err)
		}
		m.clearImageVolumeObservations(provider.Spec().ID)
		if err := provider.Install(ctx); err != nil {
			return fmt.Errorf("%w: %w", errors.ErrInstallingApplication, err)
		}
		return m.podmanMonitor.QueueUpdate(m.newAppFromProvider(provider))
	case v1beta1.AppTypeHelm:
		if err := provider.Remove(ctx); err != nil {
			return fmt.Errorf("%w: %w", errors.ErrRemovingApplication, err)
		}
		if err := provider.Install(ctx); err != nil {
			return fmt.Errorf("%w: %w", errors.ErrInstallingApplication, err)
		}
		return m.kubernetesMonitor.Update(m.newAppFromProvider(provider))
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

	m.addVolumeImageObservations(ctx, allResults)

	for i := range allResults {
		result := &allResults[i]
		result.Status.LastDelta = nil
		complete := result.Images.Complete
		parentReference := ""
		if (result.Status.AppType == v1beta1.AppTypeHelm || result.Status.AppType == v1beta1.AppTypeCompose || result.Status.AppType == v1beta1.AppTypeQuadlet) && m.ociTargetCache != nil {
			if target, found := m.ociTargetCache.Get(result.ID); found {
				parentReference = target.Parent.Reference
			}
		}
		if result.Status.ImageDigests != nil {
			for _, reported := range *result.Status.ImageDigests {
				found := false
				isWorkloadImage := false
				for _, image := range result.Images.Images {
					if image.Image == reported.Image {
						isWorkloadImage = true
						if image.Digest == reported.Digest {
							found = true
							break
						}
					}
				}
				// Charts and Compose or Quadlet bundles are parent references, not workloads.
				isApplicationParent := !isWorkloadImage && parentReference != "" && reported.Image == parentReference
				if !found && !isApplicationParent {
					complete = false
				}
			}
		}
		for _, image := range result.Images.Images {
			found := false
			if result.Status.ImageDigests != nil {
				for _, reported := range *result.Status.ImageDigests {
					if image.Image == reported.Image && image.Digest == reported.Digest {
						found = true
						break
					}
				}
			}
			if !found {
				complete = false
			}
		}
		if m.deltaStatusStore != nil {
			// The lifetime was captured with the monitor snapshot before volume
			// inspection. The Store rejects it if removal/recreation happened while
			// those independent observations were being gathered.
			result.Status.LastDelta = m.deltaStatusStore.ObservedApplicationResultForLifetime(result.Status.Name, result.Images.Lifetime, result.Images.Images, complete)
		}
	}
	statuses, summary := aggregateAppStatuses(allResults)
	status.ApplicationsSummary = summary
	status.Applications = statuses
	return nil
}

// addVolumeImageObservations collects only identities attached to the actual
// installed volume. It never resolves the current mutable image reference as a
// substitute for the content held by that volume.
func (m *manager) addVolumeImageObservations(ctx context.Context, results []AppStatusResult) {
	podmanClients := make(map[v1beta1.Username]*client.Podman)
	clientErrors := make(map[v1beta1.Username]error)
	for i := range results {
		result := &results[i]
		for _, expected := range result.Images.Volumes {
			if expected.ID == "" || expected.Reference == "" || expected.OCIType == "" || !expected.Available {
				result.Images.Complete = false
				continue
			}
			var artifactBinding deltastatus.ApplicationVolumeBinding
			if expected.OCIType == string(dependency.OCITypePodmanArtifact) {
				if m.deltaStatusStore == nil {
					result.Images.Complete = false
					continue
				}
				var ok bool
				artifactBinding, ok = m.deltaStatusStore.ApplicationVolumeBinding(result.Status.Name, result.Images.Lifetime, expected.ID)
				if !ok {
					result.Images.Complete = false
					continue
				}
			}
			owner := result.Status.RunAs.WithDefault(v1beta1.RootUsername)
			podman, found := podmanClients[owner]
			if !found {
				var err error
				podman, err = m.volumePodmanClient(owner)
				podmanClients[owner] = podman
				clientErrors[owner] = err
			}
			if clientErrors[owner] != nil || podman == nil {
				result.Images.Complete = false
				m.log.Debugf("could not inspect application volume %q: %v", expected.ID, clientErrors[owner])
				continue
			}
			inspection, err := podman.InspectVolume(ctx, expected.ID)
			if err != nil || inspection == nil || inspection.Name != expected.ID {
				result.Images.Complete = false
				m.log.Debugf("could not inspect application volume %q: %v", expected.ID, err)
				continue
			}

			var image deltastatus.ApplicationImage
			switch expected.OCIType {
			case string(dependency.OCITypePodmanArtifact):
				if inspection.Driver != "local" || inspection.CreatedAt != artifactBinding.CreatedAt {
					result.Images.Complete = false
					continue
				}
				var ok bool
				currentBinding, ok := m.deltaStatusStore.ApplicationVolumeBinding(result.Status.Name, result.Images.Lifetime, expected.ID)
				if !ok || currentBinding != artifactBinding {
					result.Images.Complete = false
					continue
				}
				image = artifactBinding.Image
			case string(dependency.OCITypePodmanImage):
				image, found = m.cachedImageVolumeObservation(inspection, result.Images.ID, result.Images.Lifetime, expected.ID, owner)
				if found {
					break
				}
				var ok bool
				image, ok = podmanImageVolumeObservation(ctx, podman, inspection, result.Images.ID, result.Images.Lifetime, owner)
				if !ok {
					result.Images.Complete = false
					continue
				}
				if inspection.CreatedAt != "" {
					if m.imageVolumeObservations == nil {
						m.imageVolumeObservations = make(map[imageVolumeObservationKey]cachedImageVolumeObservation)
					}
					m.imageVolumeObservations[imageVolumeObservationKey{applicationID: result.Images.ID, volumeID: expected.ID, owner: owner}] = cachedImageVolumeObservation{
						lifetime: result.Images.Lifetime, createdAt: inspection.CreatedAt,
						imageID: inspection.Options["image"], image: image,
					}
				}
			default:
				result.Images.Complete = false
				continue
			}
			if image.Image != expected.Reference || image.Owner != string(owner) || image.OCIType != expected.OCIType || image.Digest == "" {
				result.Images.Complete = false
				continue
			}
			result.Images.Images = append(result.Images.Images, image)
			addStatusImageDigest(&result.Status, image.Image, image.Digest)
		}
	}
}

func (m *manager) volumePodmanClient(owner v1beta1.Username) (*client.Podman, error) {
	if m.podmanFactory == nil {
		return nil, fmt.Errorf("podman factory is not configured")
	}
	podman, err := m.podmanFactory(owner)
	if err == nil && podman == nil {
		return nil, fmt.Errorf("podman factory returned nil client")
	}
	return podman, err
}

func (m *manager) clearImageVolumeObservations(applicationID string) {
	maps.DeleteFunc(m.imageVolumeObservations, func(key imageVolumeObservationKey, _ cachedImageVolumeObservation) bool {
		return key.applicationID == applicationID
	})
}

type imageVolumeObservationKey struct {
	applicationID string
	volumeID      string
	owner         v1beta1.Username
}

type cachedImageVolumeObservation struct {
	lifetime  string
	createdAt string
	imageID   string
	image     deltastatus.ApplicationImage
}

func (m *manager) cachedImageVolumeObservation(
	inspection *client.PodmanVolumeInspect,
	applicationID, lifetime, volumeID string,
	owner v1beta1.Username,
) (deltastatus.ApplicationImage, bool) {
	if inspection == nil || inspection.Driver != "image" || !client.IsImmutablePodmanImageID(inspection.Options["image"]) {
		return deltastatus.ApplicationImage{}, false
	}
	cached, ok := m.imageVolumeObservations[imageVolumeObservationKey{applicationID: applicationID, volumeID: volumeID, owner: owner}]
	if !ok || inspection.CreatedAt == "" || cached.lifetime != lifetime || cached.createdAt != inspection.CreatedAt || cached.imageID != inspection.Options["image"] {
		return deltastatus.ApplicationImage{}, false
	}
	labels := inspection.Labels
	if labels[client.PodmanDeltaVolumeApplicationLabel] != applicationID ||
		labels[client.PodmanDeltaVolumeLifetimeLabel] != lifetime ||
		labels[client.PodmanDeltaVolumeReferenceLabel] != cached.image.Image ||
		labels[client.PodmanDeltaVolumeOwnerLabel] != string(owner) ||
		labels[client.PodmanDeltaVolumeOCITypeLabel] != string(dependency.OCITypePodmanImage) ||
		labels[client.PodmanDeltaVolumeDigestLabel] != cached.image.Digest {
		return deltastatus.ApplicationImage{}, false
	}
	return cached.image, true
}

func podmanImageVolumeObservation(ctx context.Context, podman *client.Podman, inspection *client.PodmanVolumeInspect, applicationID, lifetime string, owner v1beta1.Username) (deltastatus.ApplicationImage, bool) {
	if inspection == nil || applicationID == "" || lifetime == "" || inspection.Driver != "image" || !client.IsImmutablePodmanImageID(inspection.Options["image"]) {
		return deltastatus.ApplicationImage{}, false
	}
	if podman == nil {
		return deltastatus.ApplicationImage{}, false
	}
	labels := inspection.Labels
	image := deltastatus.ApplicationImage{
		Image:   labels[client.PodmanDeltaVolumeReferenceLabel],
		Owner:   labels[client.PodmanDeltaVolumeOwnerLabel],
		OCIType: labels[client.PodmanDeltaVolumeOCITypeLabel],
		Digest:  labels[client.PodmanDeltaVolumeDigestLabel],
	}
	if labels[client.PodmanDeltaVolumeApplicationLabel] != applicationID || labels[client.PodmanDeltaVolumeLifetimeLabel] != lifetime || image.Image == "" || image.Owner != string(owner) || image.OCIType != string(dependency.OCITypePodmanImage) {
		return deltastatus.ApplicationImage{}, false
	}
	if _, err := digest.Parse(image.Digest); err != nil {
		return deltastatus.ApplicationImage{}, false
	}
	observedDigest, err := podman.ImageDigest(ctx, inspection.Options["image"])
	if err != nil || observedDigest != image.Digest {
		return deltastatus.ApplicationImage{}, false
	}
	return image, true
}

func addStatusImageDigest(status *v1beta1.DeviceApplicationStatus, image, imageDigest string) {
	var digests []v1beta1.ApplicationImageDigest
	if status.ImageDigests != nil {
		digests = append(digests, *status.ImageDigests...)
	}
	for _, existing := range digests {
		if existing.Image == image && existing.Digest == imageDigest {
			return
		}
	}
	digests = append(digests, v1beta1.ApplicationImageDigest{Image: image, Digest: imageDigest})
	sort.Slice(digests, func(i, j int) bool {
		if digests[i].Image != digests[j].Image {
			return digests[i].Image < digests[j].Image
		}
		return digests[i].Digest < digests[j].Digest
	})
	status.ImageDigests = &digests
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
// Caching: Parent references/digests and nested targets are cached by application ID.
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

// WithConsole injects app console dependencies after construction and creates
// the owned AppConsoleManager. Must be called before RunConsole.
func (m *manager) WithConsole(
	deviceName string,
	grpcClient grpc_v1.RouterServiceClient,
) {
	if grpcClient == nil {
		m.log.Warn("remote access gRPC client not available — app console disabled")
		return
	}
	m.appConsole = appconsole.NewManager(grpcClient, deviceName, appconsole.ResolverFunc(m.resolveConsole), m.log)
}

// RunConsole blocks on the spec watcher and syncs app console sessions on each
// device update. Call as a goroutine alongside other Run methods.
func (m *manager) RunConsole(ctx context.Context, watcher spec.Watcher) {
	if m.appConsole == nil {
		m.log.Warn("RunConsole called without WithConsole — app console disabled")
		return
	}
	m.log.Debug("Starting app console controller")
	defer func() {
		m.log.Debug("Stopping app console controller")
		m.appConsole.Wait()
	}()

	for {
		device, err := watcher.Pop()
		if err != nil {
			m.log.Warnf("failed to pop from spec watcher: %v", err)
			return
		}
		m.appConsole.Sync(ctx, device)
	}
}

// resolveConsole is the unexported Session factory. It delegates to the monitor
// that owns the named app. Wrapped via appconsole.ResolverFunc in WithConsole
// to avoid exposing an exported method on manager.
func (m *manager) resolveConsole(appName, consoleType string) (appconsole.Session, error) {
	session, err := m.podmanMonitor.resolveConsole(appName, consoleType)
	if !errors.Is(err, errConsoleAppNotFound) {
		return session, err
	}
	session, err = m.kubernetesMonitor.resolveConsole(appName, consoleType)
	if !errors.Is(err, errConsoleAppNotFound) {
		return session, err
	}
	return nil, fmt.Errorf("app %q not found in any monitor", appName)
}
