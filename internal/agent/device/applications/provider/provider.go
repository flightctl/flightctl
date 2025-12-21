package provider

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/flightctl/flightctl/api/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/client"
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
	// ImageProvider is the spec for the image provider
	ImageProvider *v1beta1.ImageApplicationProviderSpec
	// InlineProvider is the spec for the inline provider
	InlineProvider *v1beta1.InlineApplicationProviderSpec
}

// CollectBaseOCITargets collects only the base OCI targets (images and volumes) from the device spec
// without creating providers or extracting nested targets. This is used in phase 1 of prefetching
// before the base images are available locally.
func CollectBaseOCITargets(
	ctx context.Context,
	readWriter fileio.ReadWriter,
	spec *v1beta1.DeviceSpec,
	pullSecret *client.PullSecret,
) ([]dependency.OCIPullTarget, error) {
	if spec.Applications == nil {
		return nil, nil
	}

	var targets []dependency.OCIPullTarget

	for _, providerSpec := range lo.FromPtr(spec.Applications) {
		providerType, err := providerSpec.Type()
		if err != nil {
			return nil, err
		}

		if providerSpec.AppType == "" {
			return nil, fmt.Errorf("application type must be defined")
		}

		switch providerType {
		case v1beta1.ImageApplicationProviderType:
			imageSpec, err := providerSpec.AsImageApplicationProviderSpec()
			if err != nil {
				return nil, fmt.Errorf("getting image provider spec: %w", err)
			}

			ociType := dependency.OCITypeAuto
			// a requirement of container types is that the image reference is a runnable image
			if providerSpec.AppType == v1beta1.AppTypeContainer {
				ociType = dependency.OCITypeImage
			}

			policy := v1beta1.PullIfNotPresent
			targets = append(targets, dependency.OCIPullTarget{
				Type:       ociType,
				Reference:  imageSpec.Image,
				PullPolicy: policy,
				PullSecret: pullSecret,
			})

			// Add volume artifacts
			volTargets, err := extractVolumeTargets(imageSpec.Volumes, pullSecret)
			if err != nil {
				return nil, fmt.Errorf("extracting volume targets: %w", err)
			}
			targets = append(targets, volTargets...)

		case v1beta1.InlineApplicationProviderType:
			inlineSpec, err := providerSpec.AsInlineApplicationProviderSpec()
			if err != nil {
				return nil, fmt.Errorf("getting inline provider spec: %w", err)
			}

			// Extract images from inline content based on app type
			switch providerSpec.AppType {
			case v1beta1.AppTypeCompose:
				// Inline compose specs are already validated by the API
				spec, err := client.ParseComposeFromSpec(inlineSpec.Inline)
				if err != nil {
					return nil, fmt.Errorf("parsing compose spec: %w", err)
				}
				for _, svc := range spec.Services {
					if svc.Image != "" {
						targets = append(targets, dependency.OCIPullTarget{
							Type:       dependency.OCITypeImage,
							Reference:  svc.Image,
							PullPolicy: v1beta1.PullIfNotPresent,
							PullSecret: pullSecret,
						})
					}
				}
			case v1beta1.AppTypeQuadlet:
				spec, err := client.ParseQuadletReferencesFromSpec(inlineSpec.Inline)
				if err != nil {
					return nil, fmt.Errorf("parsing quadlet spec: %w", err)
				}
				for _, quad := range spec {
					targets = append(targets, extractQuadletTargets(quad, pullSecret)...)
				}
			default:
				return nil, fmt.Errorf("%w: %s", errors.ErrUnsupportedAppType, providerSpec.AppType)
			}

			// Add volume artifacts
			volTargets, err := extractVolumeTargets(inlineSpec.Volumes, pullSecret)
			if err != nil {
				return nil, fmt.Errorf("extracting volume targets: %w", err)
			}
			targets = append(targets, volTargets...)

		default:
			return nil, fmt.Errorf("unsupported application provider type: %s", providerType)
		}
	}

	embeddedTargets, err := collectEmbeddedOCITargets(ctx, readWriter, pullSecret)
	if err != nil {
		return nil, fmt.Errorf("collecting embedded OCI targets: %w", err)
	}
	targets = append(targets, embeddedTargets...)

	return targets, nil
}

// collectEmbeddedOCITargets discovers embedded applications and extracts their OCI targets
func collectEmbeddedOCITargets(ctx context.Context, readWriter fileio.ReadWriter, pullSecret *client.PullSecret) ([]dependency.OCIPullTarget, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	var targets []dependency.OCIPullTarget

	// discover embedded compose applications
	composeTargets, err := collectEmbeddedComposeTargets(ctx, readWriter, pullSecret)
	if err != nil {
		return nil, fmt.Errorf("collecting embedded compose targets: %w", err)
	}
	targets = append(targets, composeTargets...)

	// discover embedded quadlet applications
	quadletTargets, err := collectEmbeddedQuadletTargets(ctx, readWriter, pullSecret)
	if err != nil {
		return nil, fmt.Errorf("collecting embedded quadlet targets: %w", err)
	}
	targets = append(targets, quadletTargets...)

	return targets, nil
}

func collectEmbeddedComposeTargets(ctx context.Context, readWriter fileio.ReadWriter, pullSecret *client.PullSecret) ([]dependency.OCIPullTarget, error) {
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
					Type:       dependency.OCITypeImage,
					Reference:  svc.Image,
					PullPolicy: v1beta1.PullIfNotPresent,
					PullSecret: pullSecret,
				})
			}
		}
	}

	return targets, nil
}

func collectEmbeddedQuadletTargets(ctx context.Context, readWriter fileio.ReadWriter, pullSecret *client.PullSecret) ([]dependency.OCIPullTarget, error) {
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
			targets = append(targets, extractQuadletTargets(ref, pullSecret)...)
		}
	}

	return targets, nil
}

// ResolveImageAppName resolves the canonical application name from an ApplicationProviderSpec.
// If the spec has an explicit name, it uses that; otherwise, it falls back to the image reference.
func ResolveImageAppName(appSpec *v1beta1.ApplicationProviderSpec) (string, error) {
	appName := lo.FromPtr(appSpec.Name)
	if appName != "" {
		return appName, nil
	}

	imageSpec, err := appSpec.AsImageApplicationProviderSpec()
	if err != nil {
		return "", err
	}

	return imageSpec.Image, nil
}

// ExtractNestedTargetsFromImage extracts nested OCI targets from a single image-based application.
// This is used by the manager for per-application caching.
// Returns the extracted app data with targets. Caller is responsible for cleanup.
func ExtractNestedTargetsFromImage(
	ctx context.Context,
	log *log.PrefixLogger,
	podman *client.Podman,
	readWriter fileio.ReadWriter,
	appSpec *v1beta1.ApplicationProviderSpec,
	imageSpec *v1beta1.ImageApplicationProviderSpec,
	pullSecret *client.PullSecret,
) (*AppData, error) {
	// Resolve canonical app name
	appName, err := ResolveImageAppName(appSpec)
	if err != nil {
		return nil, fmt.Errorf("resolving app name: %w", err)
	}

	// determine app type
	appType := appSpec.AppType
	// Nothing nested in a container type
	if appType == v1beta1.AppTypeContainer {
		return &AppData{}, nil
	}

	if err := ensureAppTypeFromImage(ctx, podman, appType, imageSpec.Image); err != nil {
		return nil, fmt.Errorf("ensuring app type: %w", err)
	}

	if appType != v1beta1.AppTypeCompose && appType != v1beta1.AppTypeQuadlet {
		return nil, fmt.Errorf("%w for app %s: %s", errors.ErrUnsupportedAppType, appName, appType)
	}

	// extract nested targets
	cachedAppData, err := extractAppDataFromOCITarget(
		ctx,
		podman,
		readWriter,
		appName,
		imageSpec.Image,
		appType,
		pullSecret,
	)
	if err != nil {
		return nil, err
	}

	return cachedAppData, nil
}

// FromDeviceSpec parses the application spec and returns a list of providers.
func FromDeviceSpec(
	ctx context.Context,
	log *log.PrefixLogger,
	podman *client.Podman,
	readWriter fileio.ReadWriter,
	spec *v1beta1.DeviceSpec,
	opts ...ParseOpt,
) ([]Provider, error) {
	var cfg parseConfig
	for _, opt := range opts {
		opt(&cfg)
	}

	var providers []Provider
	for _, providerSpec := range lo.FromPtr(spec.Applications) {
		providerType, err := providerSpec.Type()
		if err != nil {
			return nil, err
		}
		if cfg.providerTypes != nil {
			if _, exists := cfg.providerTypes[providerType]; !exists {
				continue
			}
		}

		switch providerType {
		case v1beta1.ImageApplicationProviderType:
			// determine app type for image provider
			imageSpec, err := providerSpec.AsImageApplicationProviderSpec()
			if err != nil {
				return nil, fmt.Errorf("getting image provider spec: %w", err)
			}

			if err := ensureAppTypeFromImage(ctx, podman, providerSpec.AppType, imageSpec.Image); err != nil {
				return nil, fmt.Errorf("ensuring app type: %w", err)
			}

			imgProvider, err := newImage(log, podman, &providerSpec, readWriter, providerSpec.AppType)
			if err != nil {
				return nil, err
			}
			// inject extraction cache if available
			if cfg.appDataCache != nil {
				appName, err := ResolveImageAppName(&providerSpec)
				if err != nil {
					return nil, fmt.Errorf("resolving app name for cache lookup: %w", err)
				}
				if cachedData, found := cfg.appDataCache[appName]; found {
					imgProvider.AppData = cachedData
				}
			}
			providers = append(providers, imgProvider)
		case v1beta1.InlineApplicationProviderType:
			provider, err := newInline(log, podman, &providerSpec, readWriter)
			if err != nil {
				return nil, err
			}
			providers = append(providers, provider)
		default:
			return nil, fmt.Errorf("unsupported application provider type: %s", providerType)
		}
	}

	if cfg.installedEmbedded {
		if err := discoverInstalledEmbeddedApps(ctx, log, podman, readWriter, &providers); err != nil {
			log.Warnf("Failed to discover installed embedded apps: %v", err)
		}
	}

	if cfg.embedded {
		if err := parseEmbedded(ctx, log, podman, readWriter, cfg.embeddedBootTime, &providers); err != nil {
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
	entries, err := readWriter.ReadDir(lifecycle.QuadletAppPath)
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
		appPath := filepath.Join(lifecycle.QuadletAppPath, appName)
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
		if len(provider.Name()) == 0 {
			return diff, errors.ErrAppNameRequired
		}
		desiredProviders[provider.Name()] = provider
	}

	currentProviders := make(map[string]Provider)
	for _, provider := range current {
		if len(provider.Name()) == 0 {
			return diff, errors.ErrAppNameRequired
		}
		currentProviders[provider.Name()] = provider
	}

	for name, provider := range currentProviders {
		if _, exists := desiredProviders[name]; !exists {
			diff.Removed = append(diff.Removed, provider)
		}
	}

	for name, desiredProvider := range desiredProviders {
		if currentProvider, exists := currentProviders[name]; !exists {
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
	pullSecret *client.PullSecret,
) (*AppData, error) {
	tmpAppPath, err := readWriter.MkdirTemp("app_temp")
	if err != nil {
		return nil, fmt.Errorf("creating tmp dir for app %s (%s): %w", appName, imageRef, err)
	}

	cleanupFn := func() error {
		return readWriter.RemoveAll(tmpAppPath)
	}

	ociType, err := detectOCIType(ctx, podman, imageRef)
	if err != nil {
		if rmErr := cleanupFn(); rmErr != nil {
			return nil, fmt.Errorf("detecting OCI type for app %s (%s): %w (cleanup failed: %v)", appName, imageRef, err, rmErr)
		}
		return nil, fmt.Errorf("detecting OCI type for app %s (%s): %w", appName, imageRef, err)
	}

	if ociType == dependency.OCITypeArtifact {
		if err := extractAndProcessArtifact(ctx, podman, log.NewPrefixLogger(""), imageRef, tmpAppPath, readWriter); err != nil {
			if rmErr := cleanupFn(); rmErr != nil {
				return nil, fmt.Errorf("extracting artifact contents for app %s (%s): %w (cleanup failed: %v)", appName, imageRef, err, rmErr)
			}
			return nil, fmt.Errorf("extracting artifact contents for app %s (%s): %w", appName, imageRef, err)
		}
	} else {
		if err := podman.CopyContainerData(ctx, imageRef, tmpAppPath); err != nil {
			if rmErr := cleanupFn(); rmErr != nil {
				return nil, fmt.Errorf("copying image contents for app %s (%s): %w (cleanup failed: %v)", appName, imageRef, err, rmErr)
			}
			return nil, fmt.Errorf("copying image contents for app %s (%s): %w", appName, imageRef, err)
		}
	}

	var targets []dependency.OCIPullTarget

	switch appType {
	case v1beta1.AppTypeCompose:
		// parse compose spec from tmpdir
		spec, err := client.ParseComposeSpecFromDir(readWriter, tmpAppPath)
		if err != nil {
			if rmErr := cleanupFn(); rmErr != nil {
				return nil, fmt.Errorf("parsing compose spec for app %s (%s): %w (cleanup failed: %v)", appName, imageRef, err, rmErr)
			}
			return nil, fmt.Errorf("parsing compose spec for app %s (%s): %w", appName, imageRef, err)
		}

		// validate the compose spec
		if errs := validation.ValidateComposeSpec(spec, false); len(errs) > 0 {
			if rmErr := cleanupFn(); rmErr != nil {
				return nil, fmt.Errorf("validating compose spec for app %s (%s): %w (cleanup failed: %v)", appName, imageRef, errors.Join(errs...), rmErr)
			}
			return nil, fmt.Errorf("validating compose spec for app %s (%s): %w", appName, imageRef, errors.Join(errs...))
		}

		// extract images
		for _, svc := range spec.Services {
			if svc.Image != "" {
				targets = append(targets, dependency.OCIPullTarget{
					Type:       dependency.OCITypeImage,
					Reference:  svc.Image,
					PullPolicy: v1beta1.PullIfNotPresent,
					PullSecret: pullSecret,
				})
			}
		}

	case v1beta1.AppTypeQuadlet:
		// parse quadlet spec from tmpdir
		spec, err := client.ParseQuadletReferencesFromDir(readWriter, tmpAppPath)
		if err != nil {
			if rmErr := cleanupFn(); rmErr != nil {
				return nil, fmt.Errorf("parsing quadlet spec for app %s (%s): %w (cleanup failed: %v)", appName, imageRef, err, rmErr)
			}
			return nil, fmt.Errorf("parsing quadlet spec for app %s (%s): %w", appName, imageRef, err)
		}

		// validate all quadlets before extracting targets
		var validationErrs []error
		for quadletPath, quad := range spec {
			if errs := validation.ValidateQuadletSpec(quad, quadletPath, false); len(errs) > 0 {
				validationErrs = append(validationErrs, errs...)
			}
		}
		if len(validationErrs) > 0 {
			if rmErr := cleanupFn(); rmErr != nil {
				return nil, fmt.Errorf("validating quadlet spec for app %s (%s): %w (cleanup failed: %v)", appName, imageRef, errors.Join(validationErrs...), rmErr)
			}
			return nil, fmt.Errorf("validating quadlet spec for app %s (%s): %w", appName, imageRef, errors.Join(validationErrs...))
		}

		// extract images
		for _, quad := range spec {
			targets = append(targets, extractQuadletTargets(quad, pullSecret)...)
		}

	default:
		if rmErr := cleanupFn(); rmErr != nil {
			return nil, fmt.Errorf("%w for app %s (%s): %s (cleanup failed: %v)", errors.ErrUnsupportedAppType, appName, imageRef, appType, rmErr)
		}
		return nil, fmt.Errorf("%w for app %s (%s): %s", errors.ErrUnsupportedAppType, appName, imageRef, appType)
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
		return fmt.Errorf("parsing compose spec: %w", err)
	}

	if errs := validation.ValidateComposeSpec(spec, false); len(errs) > 0 {
		return fmt.Errorf("validating compose spec: %w", errors.Join(errs...))
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
		return fmt.Errorf("parsing quadlet spec: %w", err)
	}

	var errs []error
	for path, quad := range spec {
		if e := validation.ValidateQuadletSpec(quad, path, false); len(e) > 0 {
			errs = append(errs, e...)
		}
	}

	errs = append(errs, validation.ValidateQuadletNames(spec)...)
	errs = append(errs, validation.ValidateQuadletCrossReferences(spec)...)

	if len(errs) > 0 {
		return fmt.Errorf("validating quadlets spec: %w", errors.Join(errs...))
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

func extractQuadletTargets(quad *common.QuadletReferences, pullSecret *client.PullSecret) []dependency.OCIPullTarget {
	var targets []dependency.OCIPullTarget
	if quad.Image != nil && !quadlet.IsImageReference(*quad.Image) {
		targets = append(targets, dependency.OCIPullTarget{
			Type:       dependency.OCITypeImage,
			Reference:  *quad.Image,
			PullPolicy: v1beta1.PullIfNotPresent,
			PullSecret: pullSecret,
		})
	}
	for _, image := range quad.MountImages {
		if !quadlet.IsImageReference(image) {
			targets = append(targets, dependency.OCIPullTarget{
				Type:       dependency.OCITypeImage,
				Reference:  image,
				PullPolicy: v1beta1.PullIfNotPresent,
				PullSecret: pullSecret,
			})
		}
	}
	return targets
}

func extractVolumeTargets(vols *[]v1beta1.ApplicationVolume, pullSecret *client.PullSecret) ([]dependency.OCIPullTarget, error) {
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
		ociType := dependency.OCITypeArtifact
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
			PullSecret: pullSecret,
		})
	}

	return targets, nil
}
