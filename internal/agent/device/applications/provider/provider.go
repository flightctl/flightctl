package provider

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
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

// CollectBaseOCITargets collects only the base OCI targets (images and volumes) from the device spec
// without creating providers or extracting nested targets. This is used in phase 1 of prefetching
// before the base images are available locally.
func CollectBaseOCITargets(
	ctx context.Context,
	rwFactory fileio.ReadWriterFactory,
	spec *v1beta1.DeviceSpec,
	configProvider client.PullConfigProvider,
) (dependency.OCIPullTargetsByUser, error) {
	if spec.Applications == nil {
		return nil, nil
	}

	var targets dependency.OCIPullTargetsByUser

	for _, providerSpec := range lo.FromPtr(spec.Applications) {
		appType, err := providerSpec.GetAppType()
		if err != nil || appType == "" {
			return nil, fmt.Errorf("application type must be defined")
		}

		appTargets, err := collectAppTypeOCITargets(appType, &providerSpec, configProvider)
		if err != nil {
			return nil, fmt.Errorf("%w: image: %w", errors.ErrGettingProviderSpec, err)
		}
		if len(appTargets) > 0 {
			targets = targets.MergeWith(appTargets)
		}
	}

	// Embedded apps are always owned by root for now.
	readWriter, err := rwFactory(v1beta1.CurrentProcessUsername)
	if err != nil {
		return nil, err
	}
	embeddedTargets, err := collectEmbeddedOCITargets(ctx, readWriter, configProvider)
	if err != nil {
		return nil, fmt.Errorf("%w: OCI targets: %w", errors.ErrCollectingEmbedded, err)
	}
	targets = targets.Add(v1beta1.CurrentProcessUsername, embeddedTargets...)

	return targets, nil
}

func collectAppTypeOCITargets(appType v1beta1.AppType, providerSpec *v1beta1.ApplicationProviderSpec, configProvider client.PullConfigProvider) (dependency.OCIPullTargetsByUser, error) {
	switch appType {
	case v1beta1.AppTypeContainer:
		return collectContainerOCITargets(providerSpec, configProvider)
	case v1beta1.AppTypeCompose:
		return collectComposeOCITargets(providerSpec, configProvider)
	case v1beta1.AppTypeQuadlet:
		return collectQuadletOCITargets(providerSpec, configProvider)
	case v1beta1.AppTypeHelm:
		return collectHelmOCITargets(providerSpec, configProvider)
	default:
		return nil, fmt.Errorf("%w: %s", errors.ErrUnsupportedAppType, appType)
	}
}

func collectContainerOCITargets(providerSpec *v1beta1.ApplicationProviderSpec, configProvider client.PullConfigProvider) (dependency.OCIPullTargetsByUser, error) {
	containerApp, err := providerSpec.AsContainerApplication()
	if err != nil {
		return nil, fmt.Errorf("getting container application: %w", err)
	}
	targetUser := containerApp.RunAsWithDefault()
	var targets dependency.OCIPullTargetsByUser
	targets = targets.Add(containerApp.RunAsWithDefault(), dependency.OCIPullTarget{
		Type:       dependency.OCITypePodmanImage,
		Reference:  containerApp.Image,
		PullPolicy: v1beta1.PullIfNotPresent,
		Configs:    configProvider,
	})
	volTargets, err := extractVolumeTargets(containerApp.Volumes, configProvider)
	if err != nil {
		return nil, fmt.Errorf("extracting container volume targets: %w", err)
	}
	return targets.Add(targetUser, volTargets...), nil
}

func collectComposeOCITargets(providerSpec *v1beta1.ApplicationProviderSpec, configProvider client.PullConfigProvider) (dependency.OCIPullTargetsByUser, error) {
	composeApp, err := providerSpec.AsComposeApplication()
	if err != nil {
		return nil, fmt.Errorf("getting compose application: %w", err)
	}
	providerType, err := composeApp.Type()
	if err != nil {
		return nil, fmt.Errorf("getting compose provider type: %w", err)
	}

	targetUser := v1beta1.CurrentProcessUsername

	var targets dependency.OCIPullTargetsByUser
	switch providerType {
	case v1beta1.ImageApplicationProviderType:
		imageSpec, err := composeApp.AsImageApplicationProviderSpec()
		if err != nil {
			return nil, fmt.Errorf("getting compose image provider: %w", err)
		}
		targets = targets.Add(targetUser, dependency.OCIPullTarget{
			Type:       dependency.OCITypeAuto,
			Reference:  imageSpec.Image,
			PullPolicy: v1beta1.PullIfNotPresent,
			Configs:    configProvider,
		})
	case v1beta1.InlineApplicationProviderType:
		inlineSpec, err := composeApp.AsInlineApplicationProviderSpec()
		if err != nil {
			return nil, fmt.Errorf("getting compose inline provider: %w", err)
		}
		composeSpec, err := client.ParseComposeFromSpec(inlineSpec.Inline)
		if err != nil {
			return nil, fmt.Errorf("parsing compose spec: %w", err)
		}
		for _, svc := range composeSpec.Services {
			if svc.Image != "" {
				targets = targets.Add(targetUser, dependency.OCIPullTarget{
					Type:       dependency.OCITypePodmanImage,
					Reference:  svc.Image,
					PullPolicy: v1beta1.PullIfNotPresent,
					Configs:    configProvider,
				})
			}
		}
	}
	volTargets, err := extractVolumeTargets(composeApp.Volumes, configProvider)
	if err != nil {
		return nil, fmt.Errorf("extracting compose volume targets: %w", err)
	}
	return targets.Add(targetUser, volTargets...), nil
}

func collectQuadletOCITargets(providerSpec *v1beta1.ApplicationProviderSpec, configProvider client.PullConfigProvider) (dependency.OCIPullTargetsByUser, error) {
	quadletApp, err := providerSpec.AsQuadletApplication()
	if err != nil {
		return nil, fmt.Errorf("getting quadlet application: %w", err)
	}
	providerType, err := quadletApp.Type()
	if err != nil {
		return nil, fmt.Errorf("getting quadlet provider type: %w", err)
	}

	targetUser := quadletApp.RunAsWithDefault()
	var targets dependency.OCIPullTargetsByUser
	switch providerType {
	case v1beta1.ImageApplicationProviderType:
		imageSpec, err := quadletApp.AsImageApplicationProviderSpec()
		if err != nil {
			return nil, fmt.Errorf("getting quadlet image provider: %w", err)
		}
		targets = targets.Add(targetUser, dependency.OCIPullTarget{
			Type:       dependency.OCITypeAuto,
			Reference:  imageSpec.Image,
			PullPolicy: v1beta1.PullIfNotPresent,
			Configs:    configProvider,
		})
	case v1beta1.InlineApplicationProviderType:
		inlineSpec, err := quadletApp.AsInlineApplicationProviderSpec()
		if err != nil {
			return nil, fmt.Errorf("getting quadlet inline provider: %w", err)
		}
		quadletSpec, err := client.ParseQuadletReferencesFromSpec(inlineSpec.Inline)
		if err != nil {
			return nil, fmt.Errorf("parsing quadlet spec: %w", err)
		}
		for _, quad := range quadletSpec {
			targets = targets.Add(targetUser, extractQuadletTargets(quad, configProvider)...)
		}
	}
	volTargets, err := extractVolumeTargets(quadletApp.Volumes, configProvider)
	if err != nil {
		return nil, fmt.Errorf("extracting quadlet volume targets: %w", err)
	}
	return targets.Add(targetUser, volTargets...), nil
}

func collectHelmOCITargets(providerSpec *v1beta1.ApplicationProviderSpec, configProvider client.PullConfigProvider) (dependency.OCIPullTargetsByUser, error) {
	helmApp, err := providerSpec.AsHelmApplication()
	if err != nil {
		return nil, fmt.Errorf("getting helm application: %w", err)
	}

	var targets dependency.OCIPullTargetsByUser
	targets = targets.Add(v1beta1.CurrentProcessUsername, dependency.OCIPullTarget{
		Type:       dependency.OCITypeHelmChart,
		Reference:  helmApp.Image,
		PullPolicy: v1beta1.PullIfNotPresent,
		Configs:    configProvider,
	})

	return targets, nil
}

// collectEmbeddedOCITargets discovers embedded applications and extracts their OCI targets
func collectEmbeddedOCITargets(ctx context.Context, readWriter fileio.ReadWriter, configProvider client.PullConfigProvider) ([]dependency.OCIPullTarget, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	var targets []dependency.OCIPullTarget

	// discover embedded compose applications
	composeTargets, err := collectEmbeddedComposeTargets(ctx, readWriter, configProvider)
	if err != nil {
		return nil, fmt.Errorf("%w: compose targets: %w", errors.ErrCollectingEmbedded, err)
	}
	targets = append(targets, composeTargets...)

	// discover embedded quadlet applications
	quadletTargets, err := collectEmbeddedQuadletTargets(ctx, readWriter, configProvider)
	if err != nil {
		return nil, fmt.Errorf("%w: quadlet targets: %w", errors.ErrCollectingEmbedded, err)
	}
	targets = append(targets, quadletTargets...)

	return targets, nil
}

func collectEmbeddedComposeTargets(ctx context.Context, readWriter fileio.ReadWriter, configProvider client.PullConfigProvider) ([]dependency.OCIPullTarget, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	var targets []dependency.OCIPullTarget

	elements, err := readWriter.ReadDir(lifecycle.EmbeddedComposeAppPath)
	if err != nil {
		// nothing to do
		return nil, nil
	}

	for _, element := range elements {
		if !element.IsDir() {
			continue
		}

		name := element.Name()
		appPath := filepath.Join(lifecycle.EmbeddedComposeAppPath, name)

		// search for compose files
		suffixPatterns := []string{"*.yml", "*.yaml"}
		var composeFound bool
		for _, pattern := range suffixPatterns {
			files, err := filepath.Glob(readWriter.PathFor(filepath.Join(appPath, pattern)))
			if err != nil {
				continue
			}
			if len(files) > 0 {
				composeFound = true
				break
			}
		}

		if !composeFound {
			continue
		}

		// parse compose spec to extract images
		spec, err := client.ParseComposeSpecFromDir(readWriter, appPath)
		if err != nil {
			// skip apps that can't be parsed
			continue
		}

		// extract images from services
		for _, svc := range spec.Services {
			if svc.Image != "" {
				targets = append(targets, dependency.OCIPullTarget{
					Type:       dependency.OCITypePodmanImage,
					Reference:  svc.Image,
					PullPolicy: v1beta1.PullIfNotPresent,
					Configs:    configProvider,
				})
			}
		}
	}

	return targets, nil
}

func collectEmbeddedQuadletTargets(ctx context.Context, readWriter fileio.ReadWriter, configProvider client.PullConfigProvider) ([]dependency.OCIPullTarget, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	var targets []dependency.OCIPullTarget

	elements, err := readWriter.ReadDir(lifecycle.EmbeddedQuadletAppPath)
	if err != nil {
		// nothing to do
		return nil, nil
	}

	for _, element := range elements {
		if !element.IsDir() {
			continue
		}

		name := element.Name()
		appPath := filepath.Join(lifecycle.EmbeddedQuadletAppPath, name)

		// parse quadlet references from directory
		refs, err := client.ParseQuadletReferencesFromDir(readWriter, appPath)
		if err != nil {
			// skip apps that can't be parsed
			continue
		}

		// extract images from quadlet references using the helper function
		// which handles IsImageReference checks properly
		for _, ref := range refs {
			targets = append(targets, extractQuadletTargets(ref, configProvider)...)
		}
	}

	return targets, nil
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

type multiProviderApp interface {
	Type() (v1beta1.ApplicationProviderType, error)
	RunAsWithDefault() v1beta1.Username
	AsImageApplicationProviderSpec() (v1beta1.ImageApplicationProviderSpec, error)
}

func extractMultipProviderAppTargets(ctx context.Context, name string, podmanFactory client.PodmanFactory, rwFactory fileio.ReadWriterFactory, app multiProviderApp, appType v1beta1.AppType, configProvider client.PullConfigProvider) (*AppData, error) {
	providerType, err := app.Type()
	if err != nil {
		return nil, fmt.Errorf("getting compose provider type: %w", err)
	}
	if providerType != v1beta1.ImageApplicationProviderType {
		return &AppData{}, nil
	}

	user := app.RunAsWithDefault()
	podman, err := podmanFactory(user)
	if err != nil {
		return nil, fmt.Errorf("creating podman client for user %s: %w", user, err)
	}

	readWriter, err := rwFactory(user)
	if err != nil {
		return nil, fmt.Errorf("creating read/writer for user %s: %w", user, err)
	}
	imageSpec, err := app.AsImageApplicationProviderSpec()
	if err != nil {
		return nil, fmt.Errorf("getting compose image provider: %w", err)
	}
	if err := ensureAppTypeFromImage(ctx, podman, appType, imageSpec.Image); err != nil {
		return nil, fmt.Errorf("ensuring app type: %w", err)
	}
	return extractAppDataFromOCITarget(ctx, podman, readWriter, name, imageSpec.Image, appType, configProvider)
}

// ExtractNestedTargetsFromImage extracts nested OCI targets from a single image-based application.
// This is used by the manager for per-application caching.
// Returns the extracted app data with targets. Caller is responsible for cleanup.
func ExtractNestedTargetsFromImage(
	ctx context.Context,
	log *log.PrefixLogger,
	podmanFactory client.PodmanFactory,
	clients client.CLIClients,
	rwFactory fileio.ReadWriterFactory,
	appSpec *v1beta1.ApplicationProviderSpec,
	configProvider client.PullConfigProvider,
) (*AppData, error) {
	appName, err := ResolveImageAppName(appSpec)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errors.ErrResolvingAppName, err)
	}

	appType, err := (*appSpec).GetAppType()
	if err != nil {
		return nil, fmt.Errorf("getting app type: %w", err)
	}

	switch appType {
	case v1beta1.AppTypeContainer:
		return &AppData{}, nil

	case v1beta1.AppTypeCompose:
		composeApp, err := (*appSpec).AsComposeApplication()
		if err != nil {
			return nil, fmt.Errorf("getting compose application: %w", err)
		}
		return extractMultipProviderAppTargets(ctx, appName, podmanFactory, rwFactory, &composeApp, v1beta1.AppTypeCompose, configProvider)

	case v1beta1.AppTypeQuadlet:
		quadletApp, err := (*appSpec).AsQuadletApplication()
		if err != nil {
			return nil, fmt.Errorf("getting quadlet application: %w", err)
		}
		return extractMultipProviderAppTargets(ctx, appName, podmanFactory, rwFactory, &quadletApp, v1beta1.AppTypeQuadlet, configProvider)

	case v1beta1.AppTypeHelm:
		helmApp, err := (*appSpec).AsHelmApplication()
		if err != nil {
			return nil, fmt.Errorf("getting helm application: %w", err)
		}
		readWriter, err := rwFactory(v1beta1.CurrentProcessUsername)
		if err != nil {
			return nil, fmt.Errorf("creating read/writer: %w", err)
		}
		return extractHelmNestedTargets(ctx, log, clients, readWriter, appName, &helmApp, configProvider)

	default:
		return nil, fmt.Errorf("%w %w: %s", errors.ErrUnsupportedAppType, errors.WithElement(appName), appType)
	}
}

// extractHelmNestedTargets extracts container images from Helm chart manifests via dry-run.
func extractHelmNestedTargets(
	ctx context.Context,
	log *log.PrefixLogger,
	clients client.CLIClients,
	readWriter fileio.ReadWriter,
	appName string,
	helmApp *v1beta1.HelmApplication,
	configProvider client.PullConfigProvider,
) (*AppData, error) {
	if clients == nil {
		return nil, fmt.Errorf("CLIClients required for Helm app extraction")
	}

	chartRef := helmApp.Image

	resolved, err := clients.Helm().IsResolved(chartRef)
	if err != nil {
		return nil, fmt.Errorf("check chart resolved: %w", err)
	}
	if !resolved {
		return nil, fmt.Errorf("chart %s not resolved", chartRef)
	}

	kubeconfigPath, err := clients.Kube().ResolveKubeconfig()
	if err != nil {
		return nil, fmt.Errorf("resolve kubeconfig: %w", err)
	}

	chartPath := clients.Helm().GetChartPath(chartRef)

	valuesPaths, cleanup, err := resolveHelmValues(appName, chartPath, lo.FromPtr(helmApp.ValuesFiles), helmApp.Values, "", readWriter)
	if err != nil {
		return nil, fmt.Errorf("resolving values: %w", err)
	}
	defer cleanup()

	dryRunOpts := []client.HelmOption{
		client.WithKubeconfig(kubeconfigPath),
		client.WithNamespace(helm.AppNamespace(helmApp.Namespace, appName)),
		client.WithCreateNamespace(),
	}

	if len(valuesPaths) > 0 {
		dryRunOpts = append(dryRunOpts, client.WithValuesFiles(valuesPaths))
	}

	manifests, err := clients.Helm().DryRun(ctx, appName, chartPath, dryRunOpts...)
	if err != nil {
		return nil, fmt.Errorf("helm dry-run: %w", err)
	}

	images, err := helm.ExtractImagesFromManifests(manifests)
	if err != nil {
		return nil, fmt.Errorf("extract images from manifests: %w", err)
	}

	var targets []dependency.OCIPullTarget
	for _, img := range images {
		targets = append(targets, dependency.OCIPullTarget{
			Type:       dependency.OCITypeCRIImage,
			Reference:  img,
			PullPolicy: v1beta1.PullIfNotPresent,
			Configs:    configProvider,
		})
	}

	log.Debugf("Extracted %d images from Helm chart %s", len(targets), chartRef)

	return &AppData{Targets: targets}, nil
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

	var providers []Provider
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

	return providers, nil
}

func createProviderForAppType(
	ctx context.Context,
	log *log.PrefixLogger,
	podman client.PodmanFactory,
	clients client.CLIClients,
	rwFactory fileio.ReadWriterFactory,
	providerSpec *v1beta1.ApplicationProviderSpec,
	cfg *parseConfig,
) (Provider, error) {
	appType, err := (*providerSpec).GetAppType()
	if err != nil {
		return nil, fmt.Errorf("getting app type: %w", err)
	}
	switch appType {
	case v1beta1.AppTypeContainer:
		return newContainerProvider(ctx, log, podman, providerSpec, rwFactory, cfg)

	case v1beta1.AppTypeCompose:
		return newComposeProvider(ctx, log, podman, providerSpec, rwFactory, cfg)

	case v1beta1.AppTypeQuadlet:
		return newQuadletProvider(ctx, log, podman, providerSpec, rwFactory, cfg)

	case v1beta1.AppTypeHelm:
		return newHelmProvider(ctx, log, clients, providerSpec, rwFactory, cfg)

	default:
		return nil, fmt.Errorf("%w: %s", errors.ErrUnsupportedAppType, appType)
	}
}

func discoverEmbeddedApplications(ctx context.Context, log *log.PrefixLogger, podman *client.Podman, readWriter fileio.ReadWriter, providers *[]Provider, basePath string, appType v1beta1.AppType, patterns []string, bootTime string) error {
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

func parseEmbeddedCompose(ctx context.Context, log *log.PrefixLogger, podman *client.Podman, readWriter fileio.ReadWriter, providers *[]Provider, bootTime string) error {
	patterns := []string{"*.yml", "*.yaml"}
	return discoverEmbeddedApplications(ctx, log, podman, readWriter, providers, lifecycle.EmbeddedComposeAppPath, v1beta1.AppTypeCompose, patterns, bootTime)
}

func parseEmbeddedQuadlet(ctx context.Context, log *log.PrefixLogger, podman *client.Podman, readWriter fileio.ReadWriter, providers *[]Provider, bootTime string) error {
	var patterns []string
	for ext := range common.SupportedQuadletExtensions {
		patterns = append(patterns, fmt.Sprintf("*%s", ext))
	}
	return discoverEmbeddedApplications(ctx, log, podman, readWriter, providers, lifecycle.EmbeddedQuadletAppPath, v1beta1.AppTypeQuadlet, patterns, bootTime)
}

func parseEmbedded(ctx context.Context, log *log.PrefixLogger, podman *client.Podman, readWriter fileio.ReadWriter, bootTime string, providers *[]Provider) error {
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
	providers *[]Provider,
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

	for id, provider := range currentProviders {
		if _, exists := desiredProviders[id]; !exists {
			diff.Removed = append(diff.Removed, provider)
		}
	}

	for id, desiredProvider := range desiredProviders {
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
	providerTypes     map[v1beta1.ApplicationProviderType]struct{}
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

func WithProviderTypes(providerTypes ...v1beta1.ApplicationProviderType) ParseOpt {
	return func(c *parseConfig) {
		if c.providerTypes == nil {
			c.providerTypes = make(map[v1beta1.ApplicationProviderType]struct{})
		}
		for _, providerType := range providerTypes {
			c.providerTypes[providerType] = struct{}{}
		}
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
	configProvider client.PullConfigProvider,
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
					Type:       dependency.OCITypePodmanImage,
					Reference:  svc.Image,
					PullPolicy: v1beta1.PullIfNotPresent,
					Configs:    configProvider,
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
			targets = append(targets, extractQuadletTargets(quad, configProvider)...)
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

// ensureDependenciesFromAppType ensures that the dependencies required for the given app type are available.
func ensureDependenciesFromAppType(deps []string) error {
	for _, dep := range deps {
		if client.IsCommandAvailable(dep) {
			return nil
		}
	}

	return fmt.Errorf("%w: %v", errors.ErrAppDependency, deps)
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

func extractQuadletTargets(quad *common.QuadletReferences, configProvider client.PullConfigProvider) []dependency.OCIPullTarget {
	var targets []dependency.OCIPullTarget
	if quad.Image != nil && !quadlet.IsImageReference(*quad.Image) {
		targets = append(targets, dependency.OCIPullTarget{
			Type:       dependency.OCITypePodmanImage,
			Reference:  *quad.Image,
			PullPolicy: v1beta1.PullIfNotPresent,
			Configs:    configProvider,
		})
	}
	for _, image := range quad.MountImages {
		if !quadlet.IsImageReference(image) {
			targets = append(targets, dependency.OCIPullTarget{
				Type:       dependency.OCITypePodmanImage,
				Reference:  image,
				PullPolicy: v1beta1.PullIfNotPresent,
				Configs:    configProvider,
			})
		}
	}
	return targets
}

func extractVolumeTargets(vols *[]v1beta1.ApplicationVolume, configProvider client.PullConfigProvider) ([]dependency.OCIPullTarget, error) {
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
			Type:       ociType,
			Reference:  source.Reference,
			PullPolicy: policy,
			Configs:    configProvider,
		})
	}

	return targets, nil
}
