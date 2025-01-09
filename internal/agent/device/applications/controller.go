package applications

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
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/pkg/log"
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

func (c *Controller) Sync(ctx context.Context, current, desired *v1alpha1.RenderedDeviceSpec) error {
	c.log.Debug("Syncing device applications")
	defer c.log.Debug("Finished syncing device applications")

	currentApps, err := parseApps(ctx, c.podman, current)
	if err != nil {
		return err
	}

	desiredApps, err := parseApps(ctx, c.podman, desired)
	if err != nil {
		return err
	}

	if err := c.ensureEmbedded(); err != nil {
		return err
	}

	// reconcile image based packages
	if err := c.ensureImages(ctx, currentApps.ImageBased(), desiredApps.ImageBased()); err != nil {
		return err
	}

	return nil
}

func (c *Controller) ensureImages(ctx context.Context, currentApps, desiredApps []*application[*v1alpha1.ImageApplicationProvider]) error {
	diff, err := diffApps(currentApps, desiredApps)
	if err != nil {
		return err
	}

	for _, app := range diff.Removed {
		if err := c.removeImagePackage(app); err != nil {
			return err
		}
		if err := c.manager.Remove(app); err != nil {
			return err
		}
	}

	for _, app := range diff.Ensure {
		if err := c.ensureImagePackage(ctx, app); err != nil {
			return err
		}
		if err := c.manager.Ensure(app); err != nil {
			return err
		}
	}

	for _, app := range diff.Changed {
		if err := c.removeImagePackage(app); err != nil {
			return err
		}
		if err := c.ensureImagePackage(ctx, app); err != nil {
			return err
		}
		if err := c.manager.Update(app); err != nil {
			return err
		}
	}

	return nil
}

func (c *Controller) removeImagePackage(app *application[*v1alpha1.ImageApplicationProvider]) error {
	appPath, err := app.Path()
	if err != nil {
		return err
	}
	// remove the application directory for compose.
	return c.readWriter.RemoveAll(appPath)
}

func (c *Controller) ensureImagePackage(ctx context.Context, app *application[*v1alpha1.ImageApplicationProvider]) error {
	appPath, err := app.Path()
	if err != nil {
		return err
	}

	// TODO: consider using managed files or other mechanism to reduce disk I/O
	// if the manifests are already present and as expected.

	containerImage := app.provider.Image
	// copy image manifests from container image to the application path
	if err := copyImageManifests(ctx, c.log, c.readWriter, c.podman, containerImage, appPath); err != nil {
		return err
	}

	// write env vars to file
	envVars := app.EnvVars()
	if len(envVars) > 0 {
		var env strings.Builder
		for k, v := range envVars {
			env.WriteString(fmt.Sprintf("%s=%s\n", k, v))
		}
		envPath := fmt.Sprintf("%s/.env", appPath)
		c.log.Debugf("writing env vars to %s", envPath)
		if err := c.readWriter.WriteFile(envPath, []byte(env.String()), fileio.DefaultFilePermissions); err != nil {
			return err
		}
	}

	// TODO: handle selinux labels
	return nil
}

// parseApps parses applications from a rendered device spec.
func parseApps(ctx context.Context, podman *client.Podman, spec *v1alpha1.RenderedDeviceSpec) (*applications, error) {
	var apps applications
	if spec.Applications == nil {
		return &apps, nil
	}
	for _, appSpec := range *spec.Applications {
		providerType, err := appSpec.Type()
		if err != nil {
			return nil, fmt.Errorf("%w: %w", errors.ErrUnsupportedAppProvider, err)
		}
		switch providerType {
		case v1alpha1.ImageApplicationProviderType:
			provider, err := appSpec.AsImageApplicationProvider()
			if err != nil {
				return nil, fmt.Errorf("failed to convert application to image provider: %w", err)
			}
			name := util.FromPtr(appSpec.Name)
			if name == "" {
				name = provider.Image
			}

			appType, err := TypeFromImage(ctx, podman, provider.Image)
			if err != nil {
				return nil, fmt.Errorf("%w from image: %w", errors.ErrParseAppType, err)
			}
			id := newComposeID(name)
			application := NewApplication(
				id,
				name,
				&provider,
				appType,
			)
			application.SetEnvVars(util.FromPtr(appSpec.EnvVars))
			apps.images = append(apps.images, application)
		default:
			return nil, fmt.Errorf("%w: %s", errors.ErrUnsupportedAppType, providerType)
		}
	}
	return &apps, nil
}

type diff[T any] struct {
	// Ensure contains both newly added and unchanged apps
	Ensure []*application[T]
	// Removed contains apps that are no longer part of the desired state
	Removed []*application[T]
	// Changed contains apps that have changed between the current and desired state
	Changed []*application[T]
}

func diffApps[T any](
	current []*application[T],
	desired []*application[T],
) (diff[T], error) {
	var diff diff[T]

	diff.Ensure = make([]*application[T], 0, len(desired))
	diff.Removed = make([]*application[T], 0, len(current))
	diff.Changed = make([]*application[T], 0, len(current))

	desiredApps := make(map[string]*application[T])
	for _, app := range desired {
		if len(app.Name()) == 0 {
			return diff, errors.ErrAppNameRequired
		}
		desiredApps[app.Name()] = app
	}

	currentApps := make(map[string]*application[T])
	for _, app := range current {
		if len(app.Name()) == 0 {
			return diff, errors.ErrAppNameRequired
		}
		currentApps[app.Name()] = app
	}

	for name, app := range currentApps {
		if _, exists := desiredApps[name]; !exists {
			diff.Removed = append(diff.Removed, app)
		}
	}

	for name, desiredApp := range desiredApps {
		if currentApp, exists := currentApps[name]; !exists {
			diff.Ensure = append(diff.Ensure, desiredApp)
		} else {
			if isEqual(currentApp, desiredApp) {
				diff.Ensure = append(diff.Ensure, desiredApp)
			} else {
				diff.Changed = append(diff.Changed, desiredApp)
			}
		}
	}

	return diff, nil
}

// isEqual compares two applications and returns true if they are equal.
func isEqual[T any](a, b *application[T]) bool {
	if a.appType != b.appType {
		return false
	}
	if a.Name() != b.Name() {
		return false
	}
	if !reflect.DeepEqual(a.EnvVars(), b.EnvVars()) {
		return false
	}
	if !reflect.DeepEqual(a.provider, b.provider) {
		return false
	}
	return true
}

func (c *Controller) ensureEmbedded() error {
	// discover embedded compose applications
	elements, err := c.readWriter.ReadDir(lifecycle.EmbeddedComposeAppPath)
	if err != nil {
		return err
	}

	for _, element := range elements {
		if !element.IsDir() {
			continue
		}

		suffixPatterns := []string{"*.yml", "*.yaml"}
		for _, pattern := range suffixPatterns {
			// search for compose files
			files, err := filepath.Glob(filepath.Join(lifecycle.EmbeddedComposeAppPath, element.Name(), pattern))
			if err != nil {
				fmt.Printf("Error searching for pattern %s: %v\n", pattern, err)
				continue
			}
			// TODO: we could do podman config here to verify further.
			if len(files) > 0 {
				// ensure the embedded application
				provider := EmbeddedProvider{}
				id := newComposeID(element.Name())
				name := element.Name()
				app := NewApplication(id, name, provider, AppCompose)
				if err := c.manager.Ensure(app); err != nil {
					return err
				}
				c.log.Infof("Observed embedded compose application %s", app.Name())
			}
		}
	}
	return nil
}
