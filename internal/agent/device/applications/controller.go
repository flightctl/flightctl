package applications

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/applications/lifecycle"
	"github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/samber/lo"
)

type Controller struct {
	podman     *client.Podman
	readWriter fileio.ReadWriter
	manager    Manager
	log        *log.PrefixLogger
}

func NewController(
	podman *client.Podman,
	manager Manager,
	readWriter fileio.ReadWriter,
	log *log.PrefixLogger,
) *Controller {
	return &Controller{
		log:        log,
		manager:    manager,
		podman:     podman,
		readWriter: readWriter,
	}
}

func (c *Controller) Sync(ctx context.Context, current, desired *v1alpha1.DeviceSpec) error {
	c.log.Debug("Syncing device applications")
	defer c.log.Debug("Finished syncing device applications")

	currentAppProviders, err := parseAppProviders(ctx, c.log, c.podman, c.readWriter, current)
	if err != nil {
		return err
	}

	desiredAppProviders, err := parseAppProviders(ctx, c.log, c.podman, c.readWriter, desired, WithEmbedded())
	if err != nil {
		return err
	}

	if err := c.sync(ctx, currentAppProviders, desiredAppProviders); err != nil {
		return err
	}

	return nil
}

func (c *Controller) sync(ctx context.Context, currentApps, desiredApps []Provider) error {
	diff, err := diffAppProviders(currentApps, desiredApps)
	if err != nil {
		return err
	}

	for _, provider := range diff.Removed {
		c.log.Debugf("Removing application: %s", provider.Name())
		if err := c.manager.Remove(ctx, provider); err != nil {
			return err
		}
	}

	for _, provider := range diff.Ensure {
		c.log.Debugf("Ensuring application: %s", provider.Name())
		if err := c.manager.Ensure(ctx, provider); err != nil {
			return err
		}
	}

	for _, provider := range diff.Changed {
		c.log.Debugf("Updating application: %s", provider.Name())
		if err := c.manager.Update(ctx, provider); err != nil {
			return err
		}
	}

	return nil
}

func parseAppProviders(
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

		if providerType == v1alpha1.ImageApplicationProviderType {
			provider, err := NewImageProvider(log, podman, &providerSpec, readWriter)
			if err != nil {
				return nil, err
			}
			if err := provider.Verify(ctx); err != nil {
				return nil, err
			}

			providers = append(providers, provider)
		}
	}

	if cfg.embedded {
		if err := parseEmbedded(ctx, log, podman, readWriter, &providers); err != nil {
			return nil, err
		}
	}

	return providers, nil
}

type diff struct {
	// Ensure contains both newly added and unchanged app provders
	Ensure []Provider
	// Removed contains app providers that are no longer part of the desired state
	Removed []Provider
	// Changed contains app providers that have changed between the current and desired state
	Changed []Provider
}

func diffAppProviders(
	current []Provider,
	desired []Provider,
) (diff, error) {
	var diff diff

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

// isEqual compares two applications and returns true if they are equal.
func isEqual(a, b Provider) bool {
	return reflect.DeepEqual(a.Spec(), b.Spec())
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
				fmt.Printf("Error searching for pattern %s: %v\n", pattern, err)
				continue
			}
			// TODO: we could do podman config here to verify further.
			if len(files) > 0 {
				log.Debugf("Discovered embedded compose application: %s", name)
				// ensure the embedded application
				provider, err := NewEmbeddedProvider(log, podman, readWriter, name, appType)
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
