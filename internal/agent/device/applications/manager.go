package applications

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
)

var _ Manager = (*manager)(nil)

type manager struct {
	podmanMonitor *PodmanMonitor
	readWriter    fileio.ReadWriter
	log           *log.PrefixLogger
}

func NewManager(
	log *log.PrefixLogger,
	readWriter fileio.ReadWriter,
	exec executer.Executer,
	podmanClient *client.Podman,
	systemClient client.System,
) Manager {
	bootTime := systemClient.BootTime()
	return &manager{
		readWriter:    readWriter,
		podmanMonitor: NewPodmanMonitor(log, exec, podmanClient, bootTime),
		log:           log,
	}
}

// Add an application to be managed
func (m *manager) Ensure(app Application) error {
	appType := app.Type()
	switch appType {
	case AppCompose:
		return m.podmanMonitor.ensure(app)
	default:
		return fmt.Errorf("%w: %s", errors.ErrUnsupportedAppType, appType)
	}
}

// Remove by name
func (m *manager) Remove(app Application) error {
	appType := app.Type()
	switch appType {
	case AppCompose:
		return m.podmanMonitor.remove(app)
	default:
		return fmt.Errorf("%w: %s", errors.ErrUnsupportedAppType, appType)
	}
}

// Update an application
func (m *manager) Update(app Application) error {
	appType := app.Type()
	switch appType {
	case AppCompose:
		return m.podmanMonitor.update(app)
	default:
		return fmt.Errorf("%w: %s", errors.ErrUnsupportedAppType, appType)
	}
}

// BeforeUpdate prepares the manager for reconciliation.
func (m *manager) BeforeUpdate(ctx context.Context, desired *v1alpha1.RenderedDeviceSpec) error {
	if desired.Applications == nil {
		m.log.Debug("No applications to pre-check")
		return nil
	}
	m.log.Info("Pre-checking application dependencies")
	defer m.log.Info("Finished pre-checking application dependencies")

	// ensure dependencies for image based application manifests
	imageProviders, err := ImageProvidersFromSpec(desired)
	if err != nil {
		return fmt.Errorf("%w: parsing image providers: %w", errors.ErrNoRetry, err)
	}

	if err := m.pullAppPackages(ctx, imageProviders); err != nil {
		return fmt.Errorf("pulling application packages: %w", err)
	}

	// images must be pulled before parsing apps
	apps, err := parseApps(ctx, m.podmanMonitor.client, desired)
	if err != nil {
		return fmt.Errorf("parsing apps: %w", err)
	}

	// validate image based application specs and pull images
	imageBasedApps := apps.ImageBased()
	if err := m.ensureApps(ctx, imageBasedApps); err != nil {
		return err
	}

	return nil
}

// pullAppPackages pulls the images for the application packages to ensure authentication works and the images are available.
func (m *manager) pullAppPackages(ctx context.Context, imageProviders []v1alpha1.ImageApplicationProvider) error {
	for _, imageProvider := range imageProviders {
		// pull the image if it does not exist. it is possible that the image
		// tag such as latest in which case it will be pulled later. but we
		// don't want to require calling out the network on every sync.
		if m.podmanMonitor.client.ImageExists(ctx, imageProvider.Image) {
			m.log.Debugf("Image %q already exists in container storage", imageProvider.Image)
			continue
		}

		providerImage := imageProvider.Image
		_, err := m.podmanMonitor.client.Pull(ctx, providerImage, client.WithRetry())
		if err != nil {
			m.log.Warnf("Failed to pull image %q: %v", providerImage, err)
			return err
		}
		m.log.Infof("Pulled image based application package: %s", providerImage)

		addType, err := typeFromImage(ctx, m.podmanMonitor.client, providerImage)
		if err != nil {
			return fmt.Errorf("%w: getting application type: %w", errors.ErrNoRetry, err)
		}
		if err := ensureDependenciesFromType(addType); err != nil {
			return fmt.Errorf("%w: ensuring dependencies: %w", errors.ErrNoRetry, err)
		}
	}
	return nil
}

// ensureApps validates and pulls images for image based applications.
func (m *manager) ensureApps(ctx context.Context, imageBasedApps []*application[*v1alpha1.ImageApplicationProvider]) error {
	appTmpDir, err := os.MkdirTemp("", "app_temp")
	if err != nil {
		return fmt.Errorf("error creating tmp dir: %w", err)
	}
	// cleanup tmp dir
	defer func() {
		if err := m.readWriter.RemoveAll(appTmpDir); err != nil {
			m.log.Errorf("cleaning up temporary directory %q: %v", appTmpDir, err)
		}
	}()

	// validate compose specs and pull images
	m.log.Infof("Validating compose specs and pulling service images")
	// TODO: this validation should be encapsulated by the application type
	for _, app := range imageBasedApps {
		containerImage := app.provider.Image
		appPath, err := app.Path()
		if err != nil {
			return fmt.Errorf("getting app path: %w", err)
		}
		appDir := filepath.Join(appTmpDir, appPath)
		if err := copyImageManifests(ctx, m.log, m.readWriter, m.podmanMonitor.client, containerImage, appDir); err != nil {
			return fmt.Errorf("copying compose image manifests: %w", err)
		}

		spec, err := client.ParseComposeSpecFromDir(m.readWriter, appDir)
		if err != nil {
			return fmt.Errorf("parsing compose spec: %w", err)
		}

		if err := spec.Verify(); err != nil {
			return fmt.Errorf("validating compose spec: %w", err)
		}

		// pull service level images
		images := spec.Images()
		for _, image := range images {
			if m.podmanMonitor.client.ImageExists(ctx, image) {
				m.log.Debugf("Image %q already exists in container storage", image)
				continue
			}

			m.log.Infof("Pulling compose application service image: %s", image)
			_, err := m.podmanMonitor.client.Pull(ctx, image, client.WithRetry())
			if err != nil {
				return fmt.Errorf("error pulling image %s: %w", image, err)
			}
		}
	}

	return nil
}

// AfterUpdate executes actions generated by the manager during reconciliation.
func (m *manager) AfterUpdate(ctx context.Context) error {
	// execute actions for applications using the podman runtime this includes
	// compose and quadlets.
	if err := m.podmanMonitor.ExecuteActions(ctx); err != nil {
		return fmt.Errorf("error executing actions: %w", err)
	}
	return nil
}

func (m *manager) Status(ctx context.Context, status *v1alpha1.DeviceStatus) error {
	applicationsStatus, applicationSummary, err := m.podmanMonitor.Status()
	if err != nil {
		return err
	}

	status.ApplicationsSummary.Status = applicationSummary.Status
	status.ApplicationsSummary.Info = applicationSummary.Info
	status.Applications = applicationsStatus
	return nil
}

func (m *manager) Stop(ctx context.Context) error {
	return m.podmanMonitor.Stop(ctx)
}
