package lifecycle

import (
	"context"
	"fmt"

	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/pkg/log"
)

const (
	ComposeAppPath         = "/etc/compose/manifests"
	EmbeddedComposeAppPath = "/usr/local/etc/compose/manifests"
)

var _ ActionHandler = (*Compose)(nil)

type Compose struct {
	podman *client.Podman
	writer fileio.Writer
	log    *log.PrefixLogger
}

func NewCompose(log *log.PrefixLogger, writer fileio.Writer, podman *client.Podman) *Compose {
	return &Compose{
		podman: podman,
		writer: writer,
		log:    log,
	}
}

func (c *Compose) add(ctx context.Context, action *Action) error {
	appName := action.Name
	projectName := action.ID
	c.log.Debugf("Starting application: %s projectName: %s path: %s", appName, projectName, action.Path)

	if err := c.ensurePodmanVolumes(ctx, action.Volumes, appName); err != nil {
		return fmt.Errorf("creating volumes: %w", err)
	}

	noRecreate := true
	if err := c.podman.Compose().UpFromWorkDir(ctx, action.Path, projectName, noRecreate); err != nil {
		return err
	}

	c.log.Infof("Started application: %s", appName)
	return nil
}

func (c *Compose) remove(ctx context.Context, action *Action) error {
	appName := action.Name
	c.log.Debugf("Removing application: %s projectName: %s", appName, action.ID)

	var errs []error
	if err := c.stopAndRemoveContainers(ctx, action); err != nil {
		errs = append(errs, err)
	}

	for _, vol := range action.Volumes {
		if err := c.podman.RemoveVolumes(ctx, vol.ID); err != nil {
			errs = append(errs, err)
		}
	}

	if len(errs) > 0 {
		return errors.Join(errs...)
	}

	c.log.Infof("Removed application: %s", appName)
	return nil
}

func (c *Compose) update(ctx context.Context, action *Action) error {
	projectName := action.ID
	c.log.Debugf("Updating application: %s projectName: %s path: %s", action.Name, projectName, action.Path)

	if err := c.stopAndRemoveContainers(ctx, action); err != nil {
		return err
	}

	if err := c.ensurePodmanVolumes(ctx, action.Volumes, projectName); err != nil {
		return fmt.Errorf("creating volumes: %w", err)
	}

	// change to work dir and run `docker compose up -d`
	noRecreate := true
	if err := c.podman.Compose().UpFromWorkDir(ctx, action.Path, projectName, noRecreate); err != nil {
		return err
	}

	c.log.Infof("Updated application: %s", action.Name)

	return nil
}

// stopAndRemoveContainers stops and removes all containers and networks created by the compose application.
func (c *Compose) stopAndRemoveContainers(ctx context.Context, action *Action) error {
	var errs []error

	// project name is derived from the application ID
	projectName := action.ID
	labels := []string{fmt.Sprintf("%s=%s", client.ComposeDockerProjectLabelKey, projectName)}
	networks, err := c.podman.ListNetworks(ctx, labels)
	if err != nil {
		errs = append(errs, err)
	}

	if err := c.podman.StopContainers(ctx, labels); err != nil {
		errs = append(errs, err)
	}
	if err := c.podman.RemoveContainer(ctx, labels); err != nil {
		errs = append(errs, err)
	}
	if err := c.podman.RemoveNetworks(ctx, networks...); err != nil {
		errs = append(errs, err)
	}

	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

func (c *Compose) Execute(ctx context.Context, action *Action) error {
	switch action.Type {
	case ActionAdd:
		return c.add(ctx, action)
	case ActionRemove:
		return c.remove(ctx, action)
	case ActionUpdate:
		return c.update(ctx, action)
	default:
		return fmt.Errorf("unsupported action type: %s", action.Type)
	}
}

// ensurePodmanVolumes creates and populates each image-backed volume in Podman.
func (c *Compose) ensurePodmanVolumes(
	ctx context.Context,
	volumes []Volume,
	appID string,
) error {
	if len(volumes) == 0 {
		return nil
	}

	labels := []string{fmt.Sprintf("%s=%s", client.ComposeDockerProjectLabelKey, appID)}
	// ensure the volume content is pulled and available
	for _, volume := range volumes {
		if err := c.ensurePodmanVolume(ctx, volume, labels); err != nil {
			return fmt.Errorf("pulling image volume: %w", err)
		}
	}
	return nil
}

// ensurePodmanVolume creates and populates a image-backed podman volume.
func (c *Compose) ensurePodmanVolume(
	ctx context.Context,
	volume Volume,
	labels []string,
) error {
	name := volume.ID
	imageRef := volume.Reference
	if c.podman.VolumeExists(ctx, name) {
		c.log.Tracef("Volume %q already exists, updating contents", name)
		volumePath, err := c.podman.InspectVolumeMount(ctx, name)
		if err != nil {
			return fmt.Errorf("inspect volume %q: %w", name, err)
		}
		if err := c.writer.RemoveContents(volumePath); err != nil {
			return fmt.Errorf("removing volume content %q: %w", volumePath, err)
		}
		if _, err := c.podman.ExtractArtifact(ctx, imageRef, volumePath); err != nil {
			return fmt.Errorf("extract artifact: %w", err)
		}
		return nil
	}

	c.log.Infof("Creating volume %q from image %q", name, imageRef)

	volumePath, err := c.podman.CreateVolume(ctx, name, labels)
	if err != nil {
		return fmt.Errorf("creating volume %q: %w", name, err)
	}
	if _, err := c.podman.ExtractArtifact(ctx, imageRef, volumePath); err != nil {
		return fmt.Errorf("copy image contents: %w", err)
	}

	return nil
}
