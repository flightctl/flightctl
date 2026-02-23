package provider

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/applications/lifecycle"
	"github.com/flightctl/flightctl/internal/agent/device/dependency"
	"github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/quadlet"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/samber/lo"
)

var _ Provider = (*containerProvider)(nil)
var _ appProvider = (*containerProvider)(nil)

type containerProvider struct {
	log            *log.PrefixLogger
	podman         *client.Podman
	readWriter     fileio.ReadWriter
	commandChecker commandChecker
	spec           *ApplicationSpec
}

func newContainerProvider(
	ctx context.Context,
	log *log.PrefixLogger,
	podmanFactory client.PodmanFactory,
	apiSpec *v1beta1.ApplicationProviderSpec,
	rwFactory fileio.ReadWriterFactory,
) (*containerProvider, error) {
	containerApp, err := (*apiSpec).AsContainerApplication()
	if err != nil {
		return nil, fmt.Errorf("getting container application: %w", err)
	}

	appName := lo.FromPtr(containerApp.Name)
	if appName == "" {
		appName = containerApp.Image
	}

	user := containerApp.RunAsWithDefault()

	volumeManager, err := NewVolumeManager(log, appName, v1beta1.AppTypeContainer, user, containerApp.Volumes)
	if err != nil {
		return nil, err
	}

	appPath, err := quadletAppPath(appName, user)
	if err != nil {
		return nil, err
	}
	appID := lifecycle.GenerateAppID(appName, user)

	podman, err := podmanFactory(user)
	if err != nil {
		return nil, fmt.Errorf("creating podman client for user %s: %w", user, err)
	}

	readWriter, err := rwFactory(user)
	if err != nil {
		return nil, fmt.Errorf("creating read/writer for user %s: %w", user, err)
	}

	return &containerProvider{
		log:            log,
		podman:         podman,
		readWriter:     readWriter,
		commandChecker: client.IsCommandAvailable,
		spec: &ApplicationSpec{
			Name:         appName,
			ID:           appID,
			AppType:      v1beta1.AppTypeContainer,
			User:         user,
			Path:         appPath,
			EnvVars:      lo.FromPtr(containerApp.EnvVars),
			Embedded:     false,
			ContainerApp: &containerApp,
			Volume:       volumeManager,
		},
	}, nil
}

func (p *containerProvider) EnsureDependencies(ctx context.Context) error {
	if err := ensureDependenciesFromAppType(quadletBinaryDeps, p.commandChecker); err != nil {
		return err
	}

	version, err := p.podman.Version(ctx)
	if err != nil {
		return fmt.Errorf("%w: podman version: %w", errors.ErrAppDependency, err)
	}
	if err := ensureMinQuadletPodmanVersion(version); err != nil {
		return fmt.Errorf("%w: %w", errors.ErrAppDependency, err)
	}
	if err := ensureDependenciesFromVolumes(ctx, p.podman, p.spec.ContainerApp.Volumes); err != nil {
		return fmt.Errorf("%w: volume dependencies: %w", errors.ErrAppDependency, err)
	}
	return nil
}

func (p *containerProvider) Verify(ctx context.Context) error {
	if err := p.EnsureDependencies(ctx); err != nil {
		return err
	}

	if err := validateEnvVars(p.spec.EnvVars); err != nil {
		return fmt.Errorf("%w: validating env vars: %w", errors.ErrInvalidSpec, err)
	}

	if err := validateQuadletUser(p.spec.User, p.readWriter); err != nil {
		return fmt.Errorf("%w: application user %s is not valid: %w", errors.ErrNoRetry, p.spec.User, err)
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

	if err := installQuadlet(p.readWriter, p.log, p.spec.Path, quadletSystemdTargetPath(p.spec.User, p.spec.ID), p.spec.ID); err != nil {
		return fmt.Errorf("installing container: %w", err)
	}

	return nil
}

func (p *containerProvider) Remove(ctx context.Context) error {
	targetPath := quadletSystemdTargetPath(p.spec.User, p.spec.ID)
	if err := p.readWriter.RemoveFile(targetPath); err != nil {
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

func (p *containerProvider) collectOCITargets(ctx context.Context, configProvider dependency.PullConfigResolver) (dependency.OCIPullTargetsByUser, error) {
	var targets dependency.OCIPullTargetsByUser
	targets = targets.Add(p.spec.User, dependency.OCIPullTarget{
		Type:         dependency.OCITypePodmanImage,
		Reference:    p.spec.ContainerApp.Image,
		PullPolicy:   v1beta1.PullIfNotPresent,
		ClientOptsFn: containerPullOptions(configProvider, p.spec.User),
	})
	volTargets, err := extractVolumeTargets(p.spec.ContainerApp.Volumes, configProvider, p.spec.User)
	if err != nil {
		return nil, fmt.Errorf("extracting container volume targets: %w", err)
	}
	return targets.Add(p.spec.User, volTargets...), nil
}

func (p *containerProvider) extractNestedTargets(_ context.Context, _ dependency.PullConfigResolver) (*AppData, error) {
	// Container apps don't have nested targets to extract
	return &AppData{}, nil
}

func (p *containerProvider) parentIsAvailable(_ context.Context) (string, string, bool, error) {
	// Container apps don't have nested targets, so parent availability doesn't matter
	return p.spec.ContainerApp.Image, "", true, nil
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
		Add("Install", "WantedBy", "default.target")

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
