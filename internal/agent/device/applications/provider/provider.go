package provider

import (
	"context"
	"fmt"
	"maps"
	"os"
	"os/user"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/applications/helm"
	"github.com/flightctl/flightctl/internal/agent/device/applications/lifecycle"
	"github.com/flightctl/flightctl/internal/agent/device/dependency"
	"github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/api/common"
	"github.com/flightctl/flightctl/internal/quadlet"
	"github.com/flightctl/flightctl/internal/util/validation"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/samber/lo"
)

// Provider defines the interface for supplying and managing an application's spec
// and lifecycle operations for installation to disk.
type Provider interface {
	// Verify the application content is valid and dependencies are met.
	Verify(ctx context.Context) error
	// Install the application content to the device.
	Install(ctx context.Context) error
	// Remove the application content from the device.
	Remove(ctx context.Context) error
	// Name returns the name of the application.
	Name() string
	// ID returns the unique identifier for the application.
	ID() string
	// Spec returns the application spec.
	Spec() *ApplicationSpec
	// EnsureDependencies checks that the required binaries and their versions
	// for this application type are available.
	EnsureDependencies(ctx context.Context) error
}

type appProvider interface {
	Provider

	collectOCITargets(ctx context.Context, configProvider dependency.PullConfigResolver) (dependency.OCIPullTargetsByUser, error)
	// extractNestedTargets extracts nested OCI targets from this application.
	// The caller should check parentIsAvailable first to ensure the parent artifact
	// is available locally before calling this method.
	extractNestedTargets(ctx context.Context, configProvider dependency.PullConfigResolver) (*AppData, error)
	// parentIsAvailable checks if the parent artifact is available locally.
	// Returns the reference, digest, and availability status.
	// If available is false, the caller should requeue and try again later.
	parentIsAvailable(ctx context.Context) (ref string, digest string, available bool, err error)
}

type ApplicationSpec struct {
	// Name of the application
	Name string
	// ID of the application
	ID string
	// Type of the application
	AppType v1beta1.AppType
	// User that the app should be run under
	User v1beta1.Username
	// Path to the application
	Path string
	// EnvVars are the environment variables to be passed to the application
	EnvVars map[string]string
	// Embedded is true if the application is embedded in the device
	Embedded bool
	// bootTime is used for embedded app comparison (unexported, works with reflect.DeepEqual)
	bootTime string
	// Volume manager.
	Volume VolumeManager

	// App-type-specific specs (only one will be set based on AppType)
	ContainerApp *v1beta1.ContainerApplication
	HelmApp      *v1beta1.HelmApplication
	ComposeApp   *v1beta1.ComposeApplication
	QuadletApp   *v1beta1.QuadletApplication
}

func pullAuthPathForUser(username v1beta1.Username) string {
	u, err := user.Lookup(username.WithDefault(v1beta1.RootUsername).String())
	// If we have an error it is because the user doesn't exist or the homedir isn't set, in which
	// case there is no pull auth file that could be present so just ignore it.
	if err == nil && len(u.HomeDir) > 0 {
		return filepath.Join(u.HomeDir, ".config/containers/auth.json")
	}
	return ""
}

// containerPullOptions returns a lazy function for resolving container pull options.
// Resolver may be nil when extracting metadata from already-pulled images (e.g., image pruning).
func containerPullOptions(resolver dependency.PullConfigResolver, username v1beta1.Username) dependency.ClientOptsFn {
	if resolver == nil {
		return nil
	}
	authPath := pullAuthPathForUser(username)
	if len(authPath) > 0 {
		return resolver.Options(dependency.PullConfigSpec{
			Paths:    []string{authPath},
			OptionFn: client.WithPullSecret,
		})
	}
	return resolver.Options()
}

// CollectOpt is a functional option for CollectOCITargets.
type CollectOpt func(*collectConfig)

type collectConfig struct {
	configProvider dependency.PullConfigResolver
	ociCache       *OCITargetCache
	appDataCache   map[string]*AppData
}

// WithPullConfigResolver sets the pull configuration provider for OCI operations.
func WithPullConfigResolver(p dependency.PullConfigResolver) CollectOpt {
	return func(c *collectConfig) {
		c.configProvider = p
	}
}

// WithOCICache sets the cache for storing extracted nested OCI targets.
func WithOCICache(cache *OCITargetCache) CollectOpt {
	return func(c *collectConfig) {
		c.ociCache = cache
	}
}

// WithAppData sets the cache for storing extracted application data.
func WithAppData(cache map[string]*AppData) CollectOpt {
	return func(c *collectConfig) {
		c.appDataCache = cache
	}
}

// CollectOCITargets collects all OCI targets from the device spec, including:
// - Base images and volumes from each application
// - Nested images extracted from image-based applications (when parent is available)
// It handles dependency checking, caching, and deferred extraction.
func CollectOCITargets(
	ctx context.Context,
	log *log.PrefixLogger,
	podmanFactory client.PodmanFactory,
	clients client.CLIClients,
	rwFactory fileio.ReadWriterFactory,
	spec *v1beta1.DeviceSpec,
	opts ...CollectOpt,
) (*dependency.OCICollection, error) {
	cfg := collectConfig{
		ociCache:     NewOCITargetCache(),
		appDataCache: NewAppDataCache(),
	}
	for _, opt := range opts {
		opt(&cfg)
	}

	var appProviders []appProvider
	for _, providerSpec := range lo.FromPtr(spec.Applications) {
		p, err := createProviderForAppType(ctx, log, podmanFactory, clients, rwFactory, &providerSpec, &parseConfig{appDataCache: cfg.appDataCache})
		if err != nil {
			return nil, fmt.Errorf("%w: %w", errors.ErrAppProviders, err)
		}
		appProviders = append(appProviders, p)
	}

	rootPodman, err := podmanFactory(v1beta1.CurrentProcessUsername)
	if err != nil {
		return nil, fmt.Errorf("creating root podman client: %w", err)
	}
	rootReadWriter, err := rwFactory(v1beta1.CurrentProcessUsername)
	if err != nil {
		return nil, fmt.Errorf("creating root read/writer: %w", err)
	}

	embeddedProviders, err := discoverEmbeddedProviders(ctx, log, rootPodman, rootReadWriter)
	if err != nil {
		return nil, fmt.Errorf("%w: discovering embedded apps: %w", errors.ErrCollectingEmbedded, err)
	}

	providers := append(embeddedProviders, appProviders...)
	return collectProviderTargets(ctx, log, providers, cfg.configProvider, cfg.ociCache, cfg.appDataCache)
}

func collectProviderTargets(
	ctx context.Context,
	log *log.PrefixLogger,
	providers []appProvider,
	configProvider dependency.PullConfigResolver,
	ociCache *OCITargetCache,
	appDataCache map[string]*AppData,
) (*dependency.OCICollection, error) {
	var targets dependency.OCIPullTargetsByUser
	var activeNames []string
	var depsErr error
	needsRequeue := false

	for _, p := range providers {
		activeNames = append(activeNames, p.Name())

		if err := p.EnsureDependencies(ctx); err != nil {
			// gather as many dependencies as possible, allowing the caller to determine if this is a blocker
			if errors.Is(err, errors.ErrAppDependency) {
				depsErr = errors.Join(depsErr, fmt.Errorf("%w: %w", errors.WithElement(p.Name()), err))
				continue
			}
			return nil, fmt.Errorf("%w: %w", errors.ErrAppProviders, err)
		}

		baseTargets, err := p.collectOCITargets(ctx, configProvider)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", errors.ErrGettingProviderSpec, err)
		}
		targets = targets.MergeWith(baseTargets)

		nestedTargets, requeue, err := collectNestedForProvider(ctx, log, p, configProvider, ociCache, appDataCache)
		if err != nil {
			return nil, fmt.Errorf("%w: %w: %w", errors.ErrExtractingNestedTargets, errors.WithElement(p.Name()), err)
		}
		if requeue {
			needsRequeue = true
		}
		targets = targets.Add(p.Spec().User, nestedTargets...)
	}

	ociCache.GC(activeNames)

	return &dependency.OCICollection{
		Targets: targets,
		Requeue: needsRequeue,
	}, depsErr
}

func collectNestedForProvider(
	ctx context.Context,
	log *log.PrefixLogger,
	p appProvider,
	configProvider dependency.PullConfigResolver,
	ociCache *OCITargetCache,
	appDataCache map[string]*AppData,
) ([]dependency.OCIPullTarget, bool, error) {
	ref, digest, available, err := p.parentIsAvailable(ctx)
	if err != nil {
		return nil, false, err
	}
	if !available {
		return nil, true, nil
	}

	if cachedEntry, found := ociCache.Get(p.Name()); found {
		if cachedEntry.IsValid(ref, digest) {
			log.Debugf("Using cached nested targets for app %s", p.Name())
			return cachedEntry.Children, false, nil
		}
		log.Debugf("Cache invalidated for app %s: reference or digest changed", p.Name())
	}

	appData, err := p.extractNestedTargets(ctx, configProvider)
	if err != nil {
		if errors.IsRetryable(err) {
			log.Infof("Retrying error when extracting nested targets for app %s: %v", p.Name(), err)
			return nil, true, nil
		}
		return nil, false, err
	}

	if appData == nil {
		return nil, false, nil
	}

	if len(appData.Targets) > 0 {
		appDataCache[p.Name()] = appData
		ociCache.Set(CacheEntry{
			Name: p.Name(),
			Parent: dependency.OCIPullTarget{
				Reference: ref,
				Digest:    digest,
			},
			Children: appData.Targets,
		})
		log.Debugf("Cached %d nested targets for app %s", len(appData.Targets), p.Name())
	} else {
		if err := appData.Cleanup(); err != nil {
			log.Warnf("Failed to cleanup extraction for app %s with no targets: %v", p.Name(), err)
		}
	}

	return appData.Targets, false, nil
}

// discoverEmbeddedProviders discovers embedded compose and quadlet applications
// and returns providers for each. This is used for OCI target collection where
// we don't need bootTime tracking.
func discoverEmbeddedProviders(ctx context.Context, log *log.PrefixLogger, podman *client.Podman, readWriter fileio.ReadWriter) ([]appProvider, error) {
	var providers []appProvider

	composePatterns := []string{"*.yml", "*.yaml"}
	if err := discoverEmbeddedApplications(ctx, log, podman, readWriter, &providers, lifecycle.EmbeddedComposeAppPath, v1beta1.AppTypeCompose, composePatterns, ""); err != nil {
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("discovering embedded compose: %w", err)
		}
	}

	var quadletPatterns []string
	for ext := range common.SupportedQuadletExtensions {
		quadletPatterns = append(quadletPatterns, fmt.Sprintf("*%s", ext))
	}
	if err := discoverEmbeddedApplications(ctx, log, podman, readWriter, &providers, lifecycle.EmbeddedQuadletAppPath, v1beta1.AppTypeQuadlet, quadletPatterns, ""); err != nil {
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("discovering embedded quadlet: %w", err)
		}
	}

	return providers, nil
}

// ResolveImageAppName resolves the canonical application name from an ApplicationProviderSpec.
// If the spec has an explicit name, it uses that; otherwise, it falls back to the image reference.
func ResolveImageAppName(appSpec *v1beta1.ApplicationProviderSpec) (string, error) {
	appName, err := (*appSpec).GetName()
	if err != nil {
		return "", fmt.Errorf("getting app name: %w", err)
	}
	if appName != nil && *appName != "" {
		return *appName, nil
	}

	appType, err := (*appSpec).GetAppType()
	if err != nil {
		return "", fmt.Errorf("getting app type: %w", err)
	}

	switch appType {
	case v1beta1.AppTypeContainer:
		app, err := (*appSpec).AsContainerApplication()
		if err != nil {
			return "", err
		}
		return app.Image, nil
	case v1beta1.AppTypeCompose:
		app, err := (*appSpec).AsComposeApplication()
		if err != nil {
			return "", err
		}
		providerType, err := app.Type()
		if err != nil {
			return "", err
		}
		if providerType == v1beta1.ImageApplicationProviderType {
			imageSpec, err := app.AsImageApplicationProviderSpec()
			if err != nil {
				return "", err
			}
			return imageSpec.Image, nil
		}
		return "", fmt.Errorf("inline compose app must have explicit name")
	case v1beta1.AppTypeQuadlet:
		app, err := (*appSpec).AsQuadletApplication()
		if err != nil {
			return "", err
		}
		providerType, err := app.Type()
		if err != nil {
			return "", err
		}
		if providerType == v1beta1.ImageApplicationProviderType {
			imageSpec, err := app.AsImageApplicationProviderSpec()
			if err != nil {
				return "", err
			}
			return imageSpec.Image, nil
		}
		return "", fmt.Errorf("inline quadlet app must have explicit name")
	case v1beta1.AppTypeHelm:
		app, err := (*appSpec).AsHelmApplication()
		if err != nil {
			return "", err
		}
		return helm.SanitizeReleaseName(app.Image)
	default:
		return "", fmt.Errorf("%w: %s", errors.ErrUnsupportedAppType, appType)
	}
}

// AppNeedsNestedExtraction determines if an app needs nested OCI target extraction.
// Container apps don't need extraction (simple image pull).
// Helm apps need extraction for chart images.
// Compose/Quadlet apps need extraction only if image-based (not inline).
func AppNeedsNestedExtraction(appSpec *v1beta1.ApplicationProviderSpec) (bool, error) {
	appType, err := (*appSpec).GetAppType()
	if err != nil {
		return false, err
	}
	switch appType {
	case v1beta1.AppTypeContainer:
		return false, nil
	case v1beta1.AppTypeHelm:
		return true, nil
	case v1beta1.AppTypeCompose:
		composeApp, err := (*appSpec).AsComposeApplication()
		if err != nil {
			return false, err
		}
		providerType, err := composeApp.Type()
		if err != nil {
			return false, err
		}
		return providerType == v1beta1.ImageApplicationProviderType, nil
	case v1beta1.AppTypeQuadlet:
		quadletApp, err := (*appSpec).AsQuadletApplication()
		if err != nil {
			return false, err
		}
		providerType, err := quadletApp.Type()
		if err != nil {
			return false, err
		}
		return providerType == v1beta1.ImageApplicationProviderType, nil
	default:
		return false, nil
	}
}

// ResolveImageRef extracts the OCI image reference from an app spec based on its type.
// For Container and Helm apps, returns the image directly.
// For Compose and Quadlet apps, returns the image from the nested ImageApplicationProviderSpec.
func ResolveImageRef(appSpec *v1beta1.ApplicationProviderSpec) (string, error) {
	appType, err := (*appSpec).GetAppType()
	if err != nil {
		return "", fmt.Errorf("getting app type: %w", err)
	}
	switch appType {
	case v1beta1.AppTypeContainer:
		app, err := (*appSpec).AsContainerApplication()
		if err != nil {
			return "", err
		}
		return app.Image, nil
	case v1beta1.AppTypeHelm:
		app, err := (*appSpec).AsHelmApplication()
		if err != nil {
			return "", err
		}
		return app.Image, nil
	case v1beta1.AppTypeCompose:
		app, err := (*appSpec).AsComposeApplication()
		if err != nil {
			return "", err
		}
		imageSpec, err := app.AsImageApplicationProviderSpec()
		if err != nil {
			return "", err
		}
		return imageSpec.Image, nil
	case v1beta1.AppTypeQuadlet:
		app, err := (*appSpec).AsQuadletApplication()
		if err != nil {
			return "", err
		}
		imageSpec, err := app.AsImageApplicationProviderSpec()
		if err != nil {
			return "", err
		}
		return imageSpec.Image, nil
	default:
		return "", fmt.Errorf("%w: %s", errors.ErrUnsupportedAppType, appType)
	}
}

// ResolveUser returns the User association with the application
func ResolveUser(appSpec *v1beta1.ApplicationProviderSpec) (v1beta1.Username, error) {
	appType, err := (*appSpec).GetAppType()
	if err != nil {
		return "", fmt.Errorf("getting app type: %w", err)
	}
	switch appType {
	case v1beta1.AppTypeContainer:
		app, err := (*appSpec).AsContainerApplication()
		if err != nil {
			return "", err
		}
		return app.RunAsWithDefault(), nil
	case v1beta1.AppTypeCompose:
		return v1beta1.CurrentProcessUsername, nil
	case v1beta1.AppTypeQuadlet:
		app, err := (*appSpec).AsQuadletApplication()
		if err != nil {
			return "", err
		}
		return app.RunAsWithDefault(), nil
	case v1beta1.AppTypeHelm:
		return v1beta1.CurrentProcessUsername, nil
	default:
		return "", fmt.Errorf("%w: %s", errors.ErrUnsupportedAppType, appType)
	}
}

// ExtractNestedTargetsFromImage extracts nested OCI targets from a single image-based application.
// This is called when the parent artifact is known to be available locally.
// Caller is responsible for cleanup.
func ExtractNestedTargetsFromImage(
	ctx context.Context,
	log *log.PrefixLogger,
	podmanFactory client.PodmanFactory,
	clients client.CLIClients,
	rwFactory fileio.ReadWriterFactory,
	appSpec *v1beta1.ApplicationProviderSpec,
	resolver dependency.PullConfigResolver,
) (*AppData, error) {
	p, err := createProviderForAppType(ctx, log, podmanFactory, clients, rwFactory, appSpec, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errors.ErrAppProviders, err)
	}

	if err := p.EnsureDependencies(ctx); err != nil {
		return nil, err
	}

	_, _, available, err := p.parentIsAvailable(ctx)
	if err != nil {
		return nil, err
	}
	if !available {
		return nil, fmt.Errorf("parent artifact not available for extraction")
	}

	return p.extractNestedTargets(ctx, resolver)
}

// FromDeviceSpec parses the application spec and returns a list of providers.
func FromDeviceSpec(
	ctx context.Context,
	log *log.PrefixLogger,
	podmanFactory client.PodmanFactory,
	clients client.CLIClients,
	rwFactory fileio.ReadWriterFactory,
	spec *v1beta1.DeviceSpec,
	opts ...ParseOpt,
) ([]Provider, error) {
	var cfg parseConfig
	for _, opt := range opts {
		opt(&cfg)
	}

	var providers []appProvider
	for _, providerSpec := range lo.FromPtr(spec.Applications) {
		provider, err := createProviderForAppType(ctx, log, podmanFactory, clients, rwFactory, &providerSpec, &cfg)
		if err != nil {
			return nil, err
		}
		if provider == nil {
			continue
		}
		providers = append(providers, provider)
	}

	rootPodman, err := podmanFactory(v1beta1.CurrentProcessUsername)
	if err != nil {
		return nil, fmt.Errorf("creating root podman client: %w", err)
	}

	rootReadWriter, err := rwFactory(v1beta1.CurrentProcessUsername)
	if err != nil {
		return nil, fmt.Errorf("creating root read/writer: %w", err)
	}

	if cfg.installedEmbedded {
		if err := discoverInstalledEmbeddedApps(ctx, log, rootPodman, rootReadWriter, &providers); err != nil {
			log.Warnf("Failed to discover installed embedded apps: %v", err)
		}
	}

	if cfg.embedded {
		if err := parseEmbedded(ctx, log, rootPodman, rootReadWriter, cfg.embeddedBootTime, &providers); err != nil {
			return nil, err
		}
	}

	if cfg.verify {
		for _, provider := range providers {
			if err := provider.Verify(ctx); err != nil {
				return nil, fmt.Errorf("verify: %w", err)
			}
		}
	}

	result := make([]Provider, len(providers))
	for i, p := range providers {
		result[i] = p
	}
	return result, nil
}

func createProviderForAppType(
	ctx context.Context,
	log *log.PrefixLogger,
	podman client.PodmanFactory,
	clients client.CLIClients,
	rwFactory fileio.ReadWriterFactory,
	providerSpec *v1beta1.ApplicationProviderSpec,
	cfg *parseConfig,
) (appProvider, error) {
	appType, err := (*providerSpec).GetAppType()
	if err != nil {
		return nil, fmt.Errorf("getting app type: %w", err)
	}
	switch appType {
	case v1beta1.AppTypeContainer:
		return newContainerProvider(ctx, log, podman, providerSpec, rwFactory)

	case v1beta1.AppTypeCompose:
		return newComposeProvider(ctx, log, podman, providerSpec, rwFactory, cfg)

	case v1beta1.AppTypeQuadlet:
		return newQuadletProvider(ctx, log, podman, providerSpec, rwFactory, cfg)

	case v1beta1.AppTypeHelm:
		return newHelmProvider(ctx, log, clients, providerSpec, rwFactory)

	default:
		return nil, fmt.Errorf("%w: %s", errors.ErrUnsupportedAppType, appType)
	}
}

func discoverEmbeddedApplications(ctx context.Context, log *log.PrefixLogger, podman *client.Podman, readWriter fileio.ReadWriter, providers *[]appProvider, basePath string, appType v1beta1.AppType, patterns []string, bootTime string) error {
	elements, err := readWriter.ReadDir(basePath)
	if err != nil {
		return err
	}

	for _, element := range elements {
		if !element.IsDir() {
			continue
		}

		for _, pattern := range patterns {
			name := element.Name()
			files, err := filepath.Glob(readWriter.PathFor(filepath.Join(basePath, name, pattern)))
			if err != nil {
				log.Warnf("Error searching for pattern %s: %v", pattern, err)
				continue
			}
			if len(files) > 0 {
				log.Debugf("Discovered embedded %s application: %s", appType, name)
				provider, err := newEmbedded(log, podman, readWriter, name, appType, bootTime, false)
				if err != nil {
					return err
				}
				*providers = append(*providers, provider)
				break
			}
		}
	}
	return nil
}

func parseEmbeddedCompose(ctx context.Context, log *log.PrefixLogger, podman *client.Podman, readWriter fileio.ReadWriter, providers *[]appProvider, bootTime string) error {
	patterns := []string{"*.yml", "*.yaml"}
	return discoverEmbeddedApplications(ctx, log, podman, readWriter, providers, lifecycle.EmbeddedComposeAppPath, v1beta1.AppTypeCompose, patterns, bootTime)
}

func parseEmbeddedQuadlet(ctx context.Context, log *log.PrefixLogger, podman *client.Podman, readWriter fileio.ReadWriter, providers *[]appProvider, bootTime string) error {
	var patterns []string
	for ext := range common.SupportedQuadletExtensions {
		patterns = append(patterns, fmt.Sprintf("*%s", ext))
	}
	return discoverEmbeddedApplications(ctx, log, podman, readWriter, providers, lifecycle.EmbeddedQuadletAppPath, v1beta1.AppTypeQuadlet, patterns, bootTime)
}

func parseEmbedded(ctx context.Context, log *log.PrefixLogger, podman *client.Podman, readWriter fileio.ReadWriter, bootTime string, providers *[]appProvider) error {
	if err := parseEmbeddedCompose(ctx, log, podman, readWriter, providers, bootTime); err != nil {
		return fmt.Errorf("parsing embedded compose: %w", err)
	}
	if err := parseEmbeddedQuadlet(ctx, log, podman, readWriter, providers, bootTime); err != nil {
		return fmt.Errorf("parsing embedded quadlet: %w", err)
	}

	// Embedded apps can be verified immediately because no fetch is required
	for _, p := range *providers {
		if p.Spec().Embedded {
			if err := p.Verify(ctx); err != nil {
				return fmt.Errorf("verify embedded app %s: %w", p.Name(), err)
			}
		}
	}

	return nil
}

func discoverInstalledEmbeddedApps(
	ctx context.Context,
	log *log.PrefixLogger,
	podman *client.Podman,
	readWriter fileio.ReadWriter,
	providers *[]appProvider,
) error {
	entries, err := readWriter.ReadDir(lifecycle.RootfulQuadletAppPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("reading quadlet app path: %w", err)
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		appName := entry.Name()
		appPath := filepath.Join(lifecycle.RootfulQuadletAppPath, appName)
		markerPath := filepath.Join(appPath, embeddedQuadletMarkerFile)

		markerExists, err := readWriter.PathExists(markerPath)
		if err != nil {
			return fmt.Errorf("checking %q exists: %w", markerPath, err)
		}

		if !markerExists {
			continue
		}

		markerContent, err := readWriter.ReadFile(markerPath)
		if err != nil {
			return fmt.Errorf("reading %q: %w", markerPath, err)
		}

		storedBootTime := strings.TrimSpace(string(markerContent))

		provider, err := newEmbedded(log, podman, readWriter, appName, v1beta1.AppTypeQuadlet, storedBootTime, true)
		if err != nil {
			return fmt.Errorf("parsing embedded quadlet app %s: %w", appName, err)
		}
		*providers = append(*providers, provider)
	}

	return nil
}

func GetDiff(
	current []Provider,
	desired []Provider,
) (Diff, error) {
	var diff Diff

	diff.Ensure = make([]Provider, 0, len(desired))
	diff.Removed = make([]Provider, 0, len(current))
	diff.Changed = make([]Provider, 0, len(current))

	desiredProviders := make(map[string]Provider)
	for _, provider := range desired {
		if len(provider.ID()) == 0 {
			return diff, errors.ErrAppNameRequired
		}
		desiredProviders[provider.ID()] = provider
	}

	currentProviders := make(map[string]Provider)
	for _, provider := range current {
		if len(provider.ID()) == 0 {
			return diff, errors.ErrAppNameRequired
		}
		currentProviders[provider.ID()] = provider
	}

	currentIDs := slices.Collect(maps.Keys(currentProviders))
	desiredIDs := slices.Collect(maps.Keys(desiredProviders))

	sort.Strings(currentIDs)
	sort.Strings(desiredIDs)

	for _, id := range currentIDs {
		if _, exists := desiredProviders[id]; !exists {
			diff.Removed = append(diff.Removed, currentProviders[id])
		}
	}

	for _, id := range desiredIDs {
		desiredProvider := desiredProviders[id]
		if currentProvider, exists := currentProviders[id]; !exists {
			diff.Ensure = append(diff.Ensure, desiredProvider)
		} else {
			if isEqual(currentProvider, desiredProvider) {
				diff.Ensure = append(diff.Ensure, desiredProvider)
			} else {
				diff.Changed = append(diff.Changed, desiredProvider)
			}
		}
	}

	return diff, nil
}

type Diff struct {
	// Ensure contains both newly added and unchanged app provders
	Ensure []Provider
	// Removed contains app providers that are no longer part of the desired state
	Removed []Provider
	// Changed contains app providers that have changed between the current and desired state
	Changed []Provider
}
type ParseOpt func(*parseConfig)

type parseConfig struct {
	embedded          bool
	embeddedBootTime  string
	verify            bool
	appDataCache      map[string]*AppData
	installedEmbedded bool
}

func WithEmbedded(bootTime string) ParseOpt {
	return func(c *parseConfig) {
		c.embedded = true
		c.embeddedBootTime = bootTime
	}
}

func WithInstalledEmbedded() ParseOpt {
	return func(c *parseConfig) {
		c.installedEmbedded = true
	}
}

func WithVerify() ParseOpt {
	return func(c *parseConfig) {
		c.verify = true
	}
}

func WithAppDataCache(cache map[string]*AppData) ParseOpt {
	return func(c *parseConfig) {
		c.appDataCache = cache
	}
}

// isEqual compares two application providers and returns true if they are equal.
func isEqual(a, b Provider) bool {
	return reflect.DeepEqual(a.Spec(), b.Spec())
}

// AppData holds the extracted application data and cleanup function
type AppData struct {
	Owner     v1beta1.Username
	Targets   []dependency.OCIPullTarget
	TmpPath   string
	CleanupFn func() error
}

// NewAppDataCache creates a new app data cache
func NewAppDataCache() map[string]*AppData {
	return make(map[string]*AppData)
}

func (e *AppData) Cleanup() error {
	if e.CleanupFn != nil {
		return e.CleanupFn()
	}
	return nil
}

// extractAppDataFromOCITarget extracts and parses a container image or artifact to find application images
// based on the app type. It creates a temporary directory, extracts the OCI contents,
// parses the spec, and returns OCI targets for all images found in the application.
func extractAppDataFromOCITarget(
	ctx context.Context,
	podman *client.Podman,
	readWriter fileio.ReadWriter,
	appName string,
	imageRef string,
	appType v1beta1.AppType,
	username v1beta1.Username,
	resolver dependency.PullConfigResolver,
) (*AppData, error) {
	tmpAppPath, err := readWriter.MkdirTemp("app_temp")
	if err != nil {
		return nil, fmt.Errorf("%w %w: %w", errors.ErrCreatingTmpDir, errors.WithElement(appName), err)
	}

	cleanupFn := func() error {
		return readWriter.RemoveAll(tmpAppPath)
	}

	ociType, err := detectOCIType(ctx, podman, imageRef)
	if err != nil {
		if rmErr := cleanupFn(); rmErr != nil {
			return nil, fmt.Errorf("%w %w: %w (cleanup failed: %v)", errors.ErrDetectingOCIType, errors.WithElement(appName), err, rmErr)
		}
		return nil, fmt.Errorf("%w %w: %w", errors.ErrDetectingOCIType, errors.WithElement(appName), err)
	}

	if ociType == dependency.OCITypePodmanArtifact {
		if err := extractAndProcessArtifact(ctx, podman, log.NewPrefixLogger(""), imageRef, tmpAppPath, readWriter); err != nil {
			if rmErr := cleanupFn(); rmErr != nil {
				return nil, fmt.Errorf("%w %w: %w (cleanup failed: %v)", errors.ErrExtractingArtifact, errors.WithElement(appName), err, rmErr)
			}
			return nil, fmt.Errorf("%w %w: %w", errors.ErrExtractingArtifact, errors.WithElement(appName), err)
		}
	} else {
		if err := podman.CopyContainerData(ctx, imageRef, tmpAppPath); err != nil {
			if rmErr := cleanupFn(); rmErr != nil {
				return nil, fmt.Errorf("%w %w: %w (cleanup failed: %v)", errors.ErrCopyingImage, errors.WithElement(appName), err, rmErr)
			}
			return nil, fmt.Errorf("%w %w: %w", errors.ErrCopyingImage, errors.WithElement(appName), err)
		}
	}

	var targets []dependency.OCIPullTarget

	switch appType {
	case v1beta1.AppTypeCompose:
		// parse compose spec from tmpdir
		spec, err := client.ParseComposeSpecFromDir(readWriter, tmpAppPath)
		if err != nil {
			if rmErr := cleanupFn(); rmErr != nil {
				return nil, fmt.Errorf("%w %w: %w (cleanup failed: %v)", errors.ErrParsingComposeSpec, errors.WithElement(appName), err, rmErr)
			}
			return nil, fmt.Errorf("%w %w: %w", errors.ErrParsingComposeSpec, errors.WithElement(appName), err)
		}

		// validate the compose spec
		if errs := validation.ValidateComposeSpec(spec); len(errs) > 0 {
			if rmErr := cleanupFn(); rmErr != nil {
				return nil, fmt.Errorf("%w %w: %w (cleanup failed: %v)", errors.ErrValidatingComposeSpec, errors.WithElement(appName), errors.Join(errs...), rmErr)
			}
			return nil, fmt.Errorf("%w %w: %w", errors.ErrValidatingComposeSpec, errors.WithElement(appName), errors.Join(errs...))
		}

		// extract images
		for _, svc := range spec.Services {
			if svc.Image != "" {
				targets = append(targets, dependency.OCIPullTarget{
					Type:         dependency.OCITypePodmanImage,
					Reference:    svc.Image,
					PullPolicy:   v1beta1.PullIfNotPresent,
					ClientOptsFn: containerPullOptions(resolver, username),
				})
			}
		}

	case v1beta1.AppTypeQuadlet:
		// parse quadlet spec from tmpdir
		spec, err := client.ParseQuadletReferencesFromDir(readWriter, tmpAppPath)
		if err != nil {
			if rmErr := cleanupFn(); rmErr != nil {
				return nil, fmt.Errorf("%w %w: %w (cleanup failed: %v)", errors.ErrParsingQuadletSpec, errors.WithElement(appName), err, rmErr)
			}
			return nil, fmt.Errorf("%w %w: %w", errors.ErrParsingQuadletSpec, errors.WithElement(appName), err)
		}

		// validate all quadlets before extracting targets
		var validationErrs []error
		for quadletPath, quad := range spec {
			if errs := validation.ValidateQuadletSpec(quad, quadletPath); len(errs) > 0 {
				validationErrs = append(validationErrs, errs...)
			}
		}
		if len(validationErrs) > 0 {
			if rmErr := cleanupFn(); rmErr != nil {
				return nil, fmt.Errorf("%w %w: %w (cleanup failed: %v)", errors.ErrValidatingQuadletSpec, errors.WithElement(appName), errors.Join(validationErrs...), rmErr)
			}
			return nil, fmt.Errorf("%w %w: %w", errors.ErrValidatingQuadletSpec, errors.WithElement(appName), errors.Join(validationErrs...))
		}

		// extract images
		for _, quad := range spec {
			targets = append(targets, extractQuadletTargets(quad, resolver, username)...)
		}

	default:
		if rmErr := cleanupFn(); rmErr != nil {
			return nil, fmt.Errorf("%w %w: %s (cleanup failed: %v)", errors.ErrUnsupportedAppType, errors.WithElement(appName), appType, rmErr)
		}
		return nil, fmt.Errorf("%w %w: %s", errors.ErrUnsupportedAppType, errors.WithElement(appName), appType)
	}

	return &AppData{
		Targets:   targets,
		TmpPath:   tmpAppPath,
		CleanupFn: cleanupFn,
	}, nil
}

func ensureCompose(readWriter fileio.ReadWriter, appPath string) error {
	// note: errors like "error converting YAML to JSON: yaml: line 5: found
	// character that cannot start any token" is often improperly formatted yaml
	// (double check the yaml spacing)
	spec, err := client.ParseComposeSpecFromDir(readWriter, appPath)
	if err != nil {
		return fmt.Errorf("%w: %w", errors.ErrParsingComposeSpec, err)
	}

	if errs := validation.ValidateComposeSpec(spec); len(errs) > 0 {
		return fmt.Errorf("%w: %w", errors.ErrValidatingComposeSpec, errors.Join(errs...))
	}

	return nil
}

func ensureQuadlet(readWriter fileio.ReadWriter, appPath string) error {
	// Read top-level directory to validate structure
	entries, err := readWriter.ReadDir(appPath)
	if err != nil {
		return fmt.Errorf("reading directory: %w", err)
	}

	// Track validation state
	hasTopLevelQuadlets := false
	hasWorkloads := false
	var subdirectoriesWithQuadlets []string

	// Check top-level files and subdirectories
	for _, entry := range entries {
		if entry.IsDir() {
			// Check if subdirectory contains quadlet files
			subPath := filepath.Join(appPath, entry.Name())
			hasQuadlets, err := hasQuadletFiles(readWriter, subPath)
			if err != nil {
				return fmt.Errorf("checking subdirectory %s: %w", entry.Name(), err)
			}
			if hasQuadlets {
				subdirectoriesWithQuadlets = append(subdirectoriesWithQuadlets, entry.Name())
			}
		} else {
			// Check if it's a quadlet file
			ext := filepath.Ext(entry.Name())
			if _, ok := common.SupportedQuadletExtensions[ext]; ok {
				hasTopLevelQuadlets = true
				hasWorkloads = hasWorkloads || quadlet.IsWorkload(entry.Name())
			}
		}
	}

	// Validation rules:
	// 1. Quadlet files must exist at the top level
	// 2. Quadlet files are not allowed inside subdirectories
	if len(subdirectoriesWithQuadlets) > 0 {
		return fmt.Errorf("%w: invalid quadlet structure - quadlet files must reside at the top level (found in: %v)",
			errors.ErrInvalidSpec, subdirectoriesWithQuadlets)
	}

	if !hasTopLevelQuadlets {
		return fmt.Errorf("%w: no valid quadlet files found at top level", errors.ErrNoQuadletFile)
	}
	if !hasWorkloads {
		return fmt.Errorf("%w: no valid quadlet workloads found at top level", errors.ErrNoQuadletWorkload)
	}

	// Parse and validate quadlet specifications
	spec, err := client.ParseQuadletReferencesFromDir(readWriter, appPath)
	if err != nil {
		return fmt.Errorf("%w: %w", errors.ErrParsingQuadletSpec, err)
	}

	var errs []error
	for path, quad := range spec {
		if e := validation.ValidateQuadletSpec(quad, path); len(e) > 0 {
			errs = append(errs, e...)
		}
	}

	errs = append(errs, validation.ValidateQuadletNames(spec)...)
	errs = append(errs, validation.ValidateQuadletCrossReferences(spec)...)

	if len(errs) > 0 {
		return fmt.Errorf("%w: %w", errors.ErrValidatingQuadletSpec, errors.Join(errs...))
	}
	return nil
}

// hasQuadletFiles checks if a directory contains any quadlet files
func hasQuadletFiles(readWriter fileio.ReadWriter, dirPath string) (bool, error) {
	entries, err := readWriter.ReadDir(dirPath)
	if err != nil {
		return false, err
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			ext := filepath.Ext(entry.Name())
			if _, ok := common.SupportedQuadletExtensions[ext]; ok {
				return true, nil
			}
		}
	}

	return false, nil
}

// writeENVFile writes the environment variables to a .env file in the appPath
func writeENVFile(appPath string, writer fileio.Writer, envVars map[string]string) error {
	if len(envVars) > 0 {
		var env strings.Builder
		for k, v := range envVars {
			env.WriteString(fmt.Sprintf("%s=%s\n", k, v))
		}
		envPath := fmt.Sprintf("%s/.env", appPath)
		if err := writer.WriteFile(envPath, []byte(env.String()), fileio.DefaultFilePermissions); err != nil {
			return err
		}
	}
	return nil
}

// commandChecker is a function type that checks if a command is available.
type commandChecker func(string) bool

type dependencyBins struct {
	variants []string
}

// ensureDependenciesFromAppType ensures that the dependencies required for the given app type are available.
func ensureDependenciesFromAppType(deps []dependencyBins, checker commandChecker) error {
	var missing []string
	for _, dep := range deps {
		if !slices.ContainsFunc(dep.variants, checker) {
			quoted := make([]string, len(dep.variants))
			for i, v := range dep.variants {
				quoted[i] = fmt.Sprintf("%q", v)
			}
			if len(quoted) == 1 {
				missing = append(missing, quoted[0])
			} else {
				missing = append(missing, fmt.Sprintf("(%s)", strings.Join(quoted, " or ")))
			}
		}
	}

	if len(missing) > 0 {
		return fmt.Errorf("%w: required commands not found: %s", errors.ErrAppDependency, strings.Join(missing, ", "))
	}
	return nil
}

func validateEnvVars(envVars map[string]string) error {
	if envVars != nil {
		// validate the env var keys this cant be done earlier because we there could be fleet templates
		if errs := validation.ValidateStringMap(&envVars, "spec.applications[].envVars", 1, validation.DNS1123MaxLength, validation.EnvVarNameRegexp, nil, ""); len(errs) > 0 {
			return errors.Join(errs...)
		}
	}
	return nil
}

func extractQuadletTargets(quad *common.QuadletReferences, resolver dependency.PullConfigResolver, username v1beta1.Username) []dependency.OCIPullTarget {
	var targets []dependency.OCIPullTarget
	if quad.Image != nil && !quadlet.IsImageReference(*quad.Image) {
		targets = append(targets, dependency.OCIPullTarget{
			Type:         dependency.OCITypePodmanImage,
			Reference:    *quad.Image,
			PullPolicy:   v1beta1.PullIfNotPresent,
			ClientOptsFn: containerPullOptions(resolver, username),
		})
	}
	for _, image := range quad.MountImages {
		if !quadlet.IsImageReference(image) {
			targets = append(targets, dependency.OCIPullTarget{
				Type:         dependency.OCITypePodmanImage,
				Reference:    image,
				PullPolicy:   v1beta1.PullIfNotPresent,
				ClientOptsFn: containerPullOptions(resolver, username),
			})
		}
	}
	return targets
}

func extractVolumeTargets(vols *[]v1beta1.ApplicationVolume, resolver dependency.PullConfigResolver, username v1beta1.Username) ([]dependency.OCIPullTarget, error) {
	var targets []dependency.OCIPullTarget
	if vols == nil {
		return targets, nil
	}

	for _, v := range *vols {
		vType, err := v.Type()
		if err != nil {
			return nil, fmt.Errorf("getting volume type: %w", err)
		}
		var source *v1beta1.ImageVolumeSource
		ociType := dependency.OCITypePodmanArtifact
		switch vType {
		case v1beta1.ImageApplicationVolumeProviderType:
			spec, err := v.AsImageVolumeProviderSpec()
			if err != nil {
				return nil, fmt.Errorf("getting image volume spec: %w", err)
			}
			source = &spec.Image
		case v1beta1.ImageMountApplicationVolumeProviderType:
			spec, err := v.AsImageMountVolumeProviderSpec()
			if err != nil {
				return nil, fmt.Errorf("getting image mount volume spec: %w", err)
			}
			source = &spec.Image
			ociType = dependency.OCITypeAuto
		default:
			continue
		}

		policy := v1beta1.PullIfNotPresent
		if source.PullPolicy != nil {
			policy = *source.PullPolicy
		}
		targets = append(targets, dependency.OCIPullTarget{
			Type:         ociType,
			Reference:    source.Reference,
			PullPolicy:   policy,
			ClientOptsFn: containerPullOptions(resolver, username),
		})
	}

	return targets, nil
}
