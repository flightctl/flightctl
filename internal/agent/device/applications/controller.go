package applications

import (
	"context"
	"fmt"
	"reflect"
	"strings"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/agent/device/spec"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/pkg/log"
)

type Controller struct {
	podman  *client.Podman
	writer  fileio.Writer
	manager Manager
	log     *log.PrefixLogger
}

func NewController(
	podman *client.Podman,
	manager Manager,
	writer fileio.Writer,
	log *log.PrefixLogger,
) *Controller {
	return &Controller{
		log:     log,
		manager: manager,
		podman:  podman,
		writer:  writer,
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

	// if this is the steady state, only ensure apps
	if !spec.IsUpgrading(current, desired) {
		return c.ensureApps(ctx, currentApps)
	}

	// reconcile image based packages
	if err := c.ensureImages(ctx, currentApps.ImageBased(), desiredApps.ImageBased()); err != nil {
		return err
	}

	return nil
}

func (c *Controller) ensureImages(ctx context.Context, currentApps, desiredApps []*application[*v1alpha1.ImageApplicationProvider]) error {
	added, removed, updated, err := diffApps(currentApps, desiredApps)
	if err != nil {
		return err
	}

	for _, app := range removed {
		if err := c.removeImagePackage(app); err != nil {
			return err
		}
		if err := c.manager.Remove(app); err != nil {
			return err
		}
		c.log.Infof("Removed application %s", app.Name())
	}

	for _, app := range added {
		if err := c.ensureImagePackage(ctx, app); err != nil {
			return err
		}
		if err := c.manager.Add(app); err != nil {
			return err
		}
		c.log.Infof("Added application %s", app.Name())
	}

	for _, app := range updated {
		if err := c.removeImagePackage(app); err != nil {
			return err
		}
		if err := c.ensureImagePackage(ctx, app); err != nil {
			return err
		}
		if err := c.manager.Update(app); err != nil {
			return err
		}
		c.log.Infof("Updated application %s", app.Name())
	}

	return nil
}

func (c *Controller) ensureApps(ctx context.Context, currentApps *applications) error {
	for _, app := range currentApps.ImageBased() {
		if err := c.ensureImagePackage(ctx, app); err != nil {
			return err
		}
		if err := c.manager.Add(app); err != nil {
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
	return c.writer.RemoveAll(appPath)
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
	if err := CopyImageManifests(ctx, c.log, c.writer, c.podman, containerImage, appPath); err != nil {
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
		if err := c.writer.WriteFile(envPath, []byte(env.String()), fileio.DefaultFilePermissions); err != nil {
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
			application := NewApplication(
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

// diffApps compares two sets of applications and returns the added, removed, and changed applications.
func diffApps[T any](
	current []*application[T],
	desired []*application[T],
) (added []*application[T], removed []*application[T], changed []*application[T], err error) {

	added = make([]*application[T], 0, len(desired))
	removed = make([]*application[T], 0, len(current))
	changed = make([]*application[T], 0, len(current))

	desiredApps := make(map[string]*application[T])
	for _, app := range desired {
		if len(app.Name()) == 0 {
			return nil, nil, nil, errors.ErrAppNameRequired
		}
		desiredApps[app.Name()] = app
	}

	currentApps := make(map[string]*application[T])
	for _, app := range current {
		if len(app.Name()) == 0 {
			return nil, nil, nil, errors.ErrAppNameRequired
		}
		currentApps[app.Name()] = app
	}

	for name, app := range currentApps {
		if _, exists := desiredApps[name]; !exists {
			removed = append(removed, app)
		}
	}

	for name, desiredApp := range desiredApps {
		if currentApp, exists := currentApps[name]; !exists {
			added = append(added, desiredApp)
		} else {
			if !isEqual(currentApp, desiredApp) {
				changed = append(changed, desiredApp)
			}
		}
	}

	return added, removed, changed, nil
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
