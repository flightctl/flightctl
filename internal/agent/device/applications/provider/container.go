package provider

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/applications/lifecycle"
	"github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/quadlet"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/samber/lo"
)

type containerProvider struct {
	log        *log.PrefixLogger
	podman     *client.Podman
	readWriter fileio.ReadWriter
	spec       *ApplicationSpec
}

func newContainerProvider(
	ctx context.Context,
	log *log.PrefixLogger,
	podmanFactory client.PodmanFactory,
	apiSpec *v1beta1.ApplicationProviderSpec,
	rwFactory fileio.ReadWriterFactory,
	cfg *parseConfig,
) (*containerProvider, error) {
	containerApp, err := (*apiSpec).AsContainerApplication()
	if err != nil {
		return nil, fmt.Errorf("getting container application: %w", err)
	}

	appName := lo.FromPtr(containerApp.Name)
	if appName == "" {
		appName = containerApp.Image
	}

	volumeManager, err := NewVolumeManager(log, appName, v1beta1.AppTypeContainer, containerApp.Volumes)
	if err != nil {
		return nil, err
	}

	appPath := filepath.Join(lifecycle.QuadletAppPath, appName)
	appID := client.NewComposeID(appName)

	user := containerApp.UserWithDefault()

	podman, err := podmanFactory(user)
	if err != nil {
		return nil, fmt.Errorf("creating podman client for user %s: %w", user, err)
	}

	readWriter, err := rwFactory(user)
	if err != nil {
		return nil, fmt.Errorf("creating read/writer for user %s: %w", user, err)
	}

	return &containerProvider{
		log:        log,
		podman:     podman,
		readWriter: readWriter,
		spec: &ApplicationSpec{
			Name:         appName,
			ID:           appID,
			AppType:      v1beta1.AppTypeContainer,
			Path:         appPath,
			EnvVars:      lo.FromPtr(containerApp.EnvVars),
			Embedded:     false,
			ContainerApp: &containerApp,
			Volume:       volumeManager,
		},
	}, nil
}

func (p *containerProvider) Verify(ctx context.Context) error {
	if err := validateEnvVars(p.spec.EnvVars); err != nil {
		return fmt.Errorf("%w: validating env vars: %w", errors.ErrInvalidSpec, err)
	}

	if err := ensureDependenciesFromVolumes(ctx, p.podman, p.spec.ContainerApp.Volumes); err != nil {
		return fmt.Errorf("%w: ensuring volume dependencies: %w", errors.ErrNoRetry, err)
	}

	if err := ensureDependenciesFromAppType([]string{"podman"}); err != nil {
		return fmt.Errorf("%w: ensuring dependencies: %w", errors.ErrNoRetry, err)
	}

	version, err := p.podman.Version(ctx)
	if err != nil {
		return fmt.Errorf("podman version: %w", err)
	}
	if err := ensureMinQuadletPodmanVersion(version); err != nil {
		return fmt.Errorf("%w: container app type: %w", errors.ErrNoRetry, err)
	}

	for _, vol := range lo.FromPtr(p.spec.ContainerApp.Volumes) {
		volType, err := vol.Type()
		if err != nil {
			return fmt.Errorf("%w: volume type: %w", errors.ErrNoRetry, err)
		}
		switch volType {
		case v1beta1.MountApplicationVolumeProviderType, v1beta1.ImageMountApplicationVolumeProviderType:
			// supported
		default:
			return fmt.Errorf("%w: container %s", errors.ErrUnsupportedVolumeType, volType)
		}
	}

	return nil
}

func (p *containerProvider) Install(ctx context.Context) error {
	if p.spec.ContainerApp == nil {
		return fmt.Errorf("container application spec is nil")
	}

	if err := p.readWriter.MkdirAll(p.spec.Path, fileio.DefaultDirectoryPermissions); err != nil {
		return fmt.Errorf("creating app directory: %w", err)
	}

	if err := writeENVFile(p.spec.Path, p.readWriter, p.spec.EnvVars); err != nil {
		return fmt.Errorf("writing env file: %w", err)
	}

	if err := generateQuadlet(ctx, p.podman, p.readWriter, p.spec.Path, p.spec.ContainerApp); err != nil {
		return fmt.Errorf("generating quadlet: %w", err)
	}

	if err := installQuadlet(p.readWriter, p.log, p.spec.Path, p.spec.ID); err != nil {
		return fmt.Errorf("installing container: %w", err)
	}

	return nil
}

func (p *containerProvider) Remove(ctx context.Context) error {
	path := filepath.Join(lifecycle.QuadletTargetPath, quadlet.NamespaceResource(p.spec.ID, lifecycle.QuadletTargetName))
	if err := p.readWriter.RemoveFile(path); err != nil {
		return fmt.Errorf("removing container target file: %w", err)
	}
	if err := p.readWriter.RemoveAll(p.spec.Path); err != nil {
		return fmt.Errorf("removing container app path: %w", err)
	}
	return nil
}

func (p *containerProvider) Name() string {
	return p.spec.Name
}

func (p *containerProvider) ID() string {
	return p.spec.ID
}

func (p *containerProvider) Spec() *ApplicationSpec {
	return p.spec
}

func createVolumeQuadlet(rw fileio.ReadWriter, dir string, volumeName string, imageRef string) error {
	unit := quadlet.NewEmptyUnit()
	unit.Add(quadlet.VolumeGroup, quadlet.ImageKey, imageRef)
	unit.Add(quadlet.VolumeGroup, quadlet.DriverKey, "image")

	contents, err := unit.Write()
	if err != nil {
		return fmt.Errorf("serializing volume quadlet: %w", err)
	}

	volumeFile := filepath.Join(dir, fmt.Sprintf("%s.volume", volumeName))
	if err := rw.WriteFile(volumeFile, contents, fileio.DefaultFilePermissions); err != nil {
		return fmt.Errorf("writing volume file: %w", err)
	}

	return nil
}

func generateQuadlet(ctx context.Context, podman *client.Podman, rw fileio.ReadWriter, dir string, spec *v1beta1.ContainerApplication) error {
	unit := quadlet.NewEmptyUnit()
	unit.Add(quadlet.ContainerGroup, quadlet.ImageKey, spec.Image)

	if spec.Resources != nil && spec.Resources.Limits != nil {
		lims := spec.Resources.Limits
		if lims.Cpu != nil {
			unit.Add(quadlet.ContainerGroup, quadlet.PodmanArgsKey, fmt.Sprintf("--cpus %s", *lims.Cpu))
		}
		if lims.Memory != nil {
			unit.Add(quadlet.ContainerGroup, quadlet.PodmanArgsKey, fmt.Sprintf("--memory %s", *lims.Memory))
		}
	}
	for _, port := range lo.FromPtr(spec.Ports) {
		unit.Add(quadlet.ContainerGroup, quadlet.PublishPortKey, port)
	}

	unit.Add("Service", "Restart", "on-failure").
		Add("Service", "RestartSec", "60").
		Add("Install", "WantedBy", "multi-user.target default.target")

	for _, vol := range lo.FromPtr(spec.Volumes) {
		volType, err := vol.Type()
		if err != nil {
			return fmt.Errorf("getting volume type: %w", err)
		}

		switch volType {
		case v1beta1.MountApplicationVolumeProviderType:
			mountSpec, err := vol.AsMountVolumeProviderSpec()
			if err != nil {
				return fmt.Errorf("getting mount volume spec: %w", err)
			}
			unit.Add(quadlet.ContainerGroup, quadlet.VolumeKey, fmt.Sprintf("%s:%s", vol.Name, mountSpec.Mount.Path))
		case v1beta1.ImageMountApplicationVolumeProviderType:
			imageMountSpec, err := vol.AsImageMountVolumeProviderSpec()
			if err != nil {
				return fmt.Errorf("getting image mount volume spec: %w", err)
			}

			if podman.ImageExists(ctx, imageMountSpec.Image.Reference) {
				if err := createVolumeQuadlet(rw, dir, vol.Name, imageMountSpec.Image.Reference); err != nil {
					return fmt.Errorf("creating volume quadlet for %s: %w", vol.Name, err)
				}
				unit.Add(quadlet.ContainerGroup, quadlet.VolumeKey, fmt.Sprintf("%s.volume:%s", vol.Name, imageMountSpec.Mount.Path))
			} else {
				unit.Add(quadlet.ContainerGroup, quadlet.VolumeKey, fmt.Sprintf("%s:%s", vol.Name, imageMountSpec.Mount.Path))
			}
		default:
			return fmt.Errorf("%w: %s", errors.ErrUnsupportedVolumeType, volType)
		}
	}

	contents, err := unit.Write()
	if err != nil {
		return fmt.Errorf("serializing quadlet: %w", err)
	}

	if err := rw.WriteFile(filepath.Join(dir, "app.container"), contents, fileio.DefaultFilePermissions); err != nil {
		return fmt.Errorf("writing container quadlet: %w", err)
	}
	return nil
}
