package provider

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/applications/lifecycle"
	"github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
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
	AppType v1alpha1.AppType
	// Path to the application
	Path string
	// EnvVars are the environment variables to be passed to the application
	EnvVars map[string]string
	// Embedded is true if the application is embedded in the device
	Embedded bool
	// Volume manager.
	Volume VolumeManager
	// ImageProvider is the spec for the image provider
	ImageProvider *v1alpha1.ImageApplicationProviderSpec
	// InlineProvider is the spec for the inline provider
	InlineProvider *v1alpha1.InlineApplicationProviderSpec
}

// FromDeviceSpec parses the application spec and returns a list of providers.
func FromDeviceSpec(
	ctx context.Context,
	log *log.PrefixLogger,
	podman *client.Podman,
	readWriter fileio.ReadWriter,
	spec *v1alpha1.DeviceSpec,
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
		case v1alpha1.ImageApplicationProviderType:
			provider, err := newImage(log, podman, &providerSpec, readWriter)
			if err != nil {
				return nil, err
			}
			if err := provider.Verify(ctx); err != nil {
				return nil, err
			}
			providers = append(providers, provider)
		case v1alpha1.InlineApplicationProviderType:
			provider, err := newInline(log, podman, &providerSpec, readWriter)
			if err != nil {
				return nil, err
			}
			if err := provider.Verify(ctx); err != nil {
				return nil, err
			}
			providers = append(providers, provider)
		default:
			return nil, fmt.Errorf("unsupported application provider type: %s", providerType)
		}
	}

	if cfg.embedded {
		if err := parseEmbedded(ctx, log, podman, readWriter, &providers); err != nil {
			return nil, err
		}
	}

	return providers, nil
}

func parseEmbedded(ctx context.Context, log *log.PrefixLogger, podman *client.Podman, readWriter fileio.ReadWriter, providers *[]Provider) error {
	// discover embedded compose applications
	appType := v1alpha1.AppTypeCompose
	elements, err := readWriter.ReadDir(lifecycle.EmbeddedComposeAppPath)
	if err != nil {
		return err
	}

	for _, element := range elements {
		if !element.IsDir() {
			continue
		}

		suffixPatterns := []string{"*.yml", "*.yaml"}
		for _, pattern := range suffixPatterns {
			name := element.Name()
			// search for compose files
			files, err := filepath.Glob(readWriter.PathFor(filepath.Join(lifecycle.EmbeddedComposeAppPath, name, pattern)))
			if err != nil {
				log.Warnf("Error searching for pattern %s: %v", pattern, err)
				continue
			}
			// TODO: we could do podman config here to verify further.
			if len(files) > 0 {
				log.Debugf("Discovered embedded compose application: %s", name)
				// ensure the embedded application
				provider, err := newEmbedded(log, podman, readWriter, name, appType)
				if err != nil {
					return err
				}
				if err := provider.Verify(ctx); err != nil {
					return err
				}
				*providers = append(*providers, provider)
				break
			}
		}
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
	embedded      bool
	providerTypes map[v1alpha1.ApplicationProviderType]struct{}
}

func WithEmbedded() ParseOpt {
	return func(c *parseConfig) {
		c.embedded = true
	}
}

func WithProviderTypes(providerTypes ...v1alpha1.ApplicationProviderType) ParseOpt {
	return func(c *parseConfig) {
		if c.providerTypes == nil {
			c.providerTypes = make(map[v1alpha1.ApplicationProviderType]struct{})
		}
		for _, providerType := range providerTypes {
			c.providerTypes[providerType] = struct{}{}
		}
	}
}

// isEqual compares two application providers and returns true if they are equal.
func isEqual(a, b Provider) bool {
	return reflect.DeepEqual(a.Spec(), b.Spec())
}

func pathFromAppType(appType v1alpha1.AppType, name string, embedded bool) (string, error) {
	var typePath string
	switch appType {
	case v1alpha1.AppTypeCompose:
		if embedded {
			typePath = lifecycle.EmbeddedComposeAppPath
			break
		}
		typePath = lifecycle.ComposeAppPath
	default:
		return "", fmt.Errorf("unsupported application type: %s", appType)
	}
	return filepath.Join(typePath, name), nil
}

func ensureCompose(ctx context.Context, log *log.PrefixLogger, podman *client.Podman, readWriter fileio.ReadWriter, appPath string) error {
	// note: errors like "error converting YAML to JSON: yaml: line 5: found
	// character that cannot start any token" is often improperly formatted yaml
	// (double check the yaml spacing)
	spec, err := client.ParseComposeSpecFromDir(readWriter, appPath)
	if err != nil {
		return fmt.Errorf("parsing compose spec: %w", err)
	}

	if errs := validation.ValidateComposeSpec(spec); len(errs) > 0 {
		return fmt.Errorf("validating compose spec: %w", errors.Join(errs...))
	}

	for _, svc := range spec.Services {
		if err := ensureImageExists(ctx, log, podman, svc.Image, v1alpha1.PullIfNotPresent); err != nil {
			return fmt.Errorf("pulling service image: %w", err)
		}
	}
	return nil
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
func ensureDependenciesFromAppType(appType v1alpha1.AppType) error {
	var deps []string
	switch appType {
	case v1alpha1.AppTypeCompose:
		deps = []string{"docker-compose", "podman-compose"}
	default:
		return fmt.Errorf("%w: %s", errors.ErrUnsupportedAppType, appType)
	}

	for _, dep := range deps {
		if client.IsCommandAvailable(dep) {
			return nil
		}
	}

	return fmt.Errorf("%w: %v", errors.ErrAppDependency, deps)
}

// ensureImageExists ensures that the image exists in the container storage.
func ensureImageExists(ctx context.Context, log *log.PrefixLogger, podman *client.Podman, image string, pullPolicy v1alpha1.ImagePullPolicy) error {
	if pullPolicy == v1alpha1.PullNever {
		log.Tracef("Pull policy is set to never, skipping image pull: %s", image)
		return nil
	}
	// if the image is not set, return
	// pull the image if it does not exist. it is possible that the image
	// tag such as latest in which case it will be pulled later. but we
	// don't want to require calling out the network on every sync.
	if podman.ImageExists(ctx, image) && pullPolicy != v1alpha1.PullAlways {
		log.Tracef("Image already exists in container storage: %s", image)
		return nil
	}

	_, err := podman.Pull(ctx, image, client.WithRetry())
	if err != nil {
		log.Warnf("Failed to pull image %q: %v", image, err)
		return err
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
