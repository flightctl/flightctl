package applications

import (
	"context"
	"fmt"
	"reflect"
	"strings"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/agent/device/spec"
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
	if !spec.IsUpdating(current, desired) {
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

	for _, app := range added {
		if err := c.ensureImagePackage(ctx, app); err != nil {
			return err
		}
		c.log.Infof("Added application %s", app.Name())
	}

	for _, app := range removed {
		if err := c.manager.Remove(app); err != nil {
			return err
		}
		c.log.Infof("Removed application %s", app.Name())
	}

	for _, app := range updated {
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
		c.log.Infof("Added application %s", app.Name())
	}
	return nil
}

func (c *Controller) ensureImagePackage(ctx context.Context, app *application[*v1alpha1.ImageApplicationProvider]) error {
	containerImage := app.provider.Image

	appPath, err := app.Path()
	if err != nil {
		return err
	}

	// TODO: consider using managed files or other mechanism to reduce disk I/O
	// if the manifests are already present and as expected.

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
		if err := c.writer.WriteFile(envPath, []byte(env.String()), fileio.DefaultFilePermissions); err != nil {
			return err
		}
	}

	// TODO: handle selinux labels

	// track the application if it does not exist
	return c.manager.Add(app)
}

// parseApps parses applications from a rendered device spec.
func parseApps(ctx context.Context, podman *client.Podman, spec *v1alpha1.RenderedDeviceSpec) (*applications, error) {
	var apps applications
	if spec.Applications == nil {
		return &apps, nil
	}
	for _, appSpec := range *spec.Applications {
		t, err := appSpec.Type()
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrFailedToParseAppType, err)
		}
		switch t {
		case v1alpha1.ImageApplicationProviderType:
			provider, err := appSpec.AsImageApplicationProvider()
			if err != nil {
				return nil, fmt.Errorf("failed to convert application to image provider: %w", err)
			}
			name := *appSpec.Name
			if name == "" {
				name = provider.Image
			}

			appType, err := TypeFromImage(ctx, podman, provider.Image)
			if err != nil {
				return nil, fmt.Errorf("%w from image: %w", ErrFailedToParseAppType, err)
			}
			application := NewApplication(
				name,
				&provider,
				appType,
			)
			apps.images = append(apps.images, application)
		default:
			return nil, fmt.Errorf("%w: %s", ErrorUnsupportedAppType, t)
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
			return nil, nil, nil, ErrNameRequired
		}
		desiredApps[app.Name()] = app
	}

	currentApps := make(map[string]*application[T])
	for _, app := range current {
		if len(app.Name()) == 0 {
			return nil, nil, nil, ErrNameRequired
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
			if !cmp(currentApp, desiredApp) {
				changed = append(changed, desiredApp)
			}
		}
	}

	return added, removed, changed, nil
}

// cmp compares two applications and returns true if they are equal.
func cmp[T any](a, b *application[T]) bool {
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
