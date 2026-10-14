package lifecycle

import (
	"context"
	"fmt"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/deltastatus"
	"github.com/flightctl/flightctl/internal/agent/device/dependency"
	"github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/pkg/log"
)

const (
	ComposeAppPath         = "/etc/compose/manifests"
	EmbeddedComposeAppPath = "/usr/local/etc/compose/manifests"
)

var _ ActionHandler = (*Compose)(nil)
var _ LifecycleHandler = (*Compose)(nil)

type Compose struct {
	podmanFactory client.PodmanFactory
	writerFactory fileio.ReadWriterFactory
	deltaStore    *deltastatus.Store
	log           *log.PrefixLogger
}

func NewCompose(log *log.PrefixLogger, rwFactory fileio.ReadWriterFactory, podmanFactory client.PodmanFactory, options ...HandlerOption) *Compose {
	configured := applyHandlerOptions(options)
	return &Compose{
		podmanFactory: podmanFactory,
		writerFactory: rwFactory,
		deltaStore:    configured.deltaStatusStore,
		log:           log,
	}
}

func (c *Compose) add(ctx context.Context, action *Action) error {
	appName := action.Name
	projectName := action.ID
	c.log.Debugf("Starting application: %s projectName: %s path: %s", appName, projectName, action.Path)

	podman, err := c.podmanFactory(action.User)
	if err != nil {
		return fmt.Errorf("creating podman client: %w", err)
	}

	writer, err := c.writerFactory(action.User)
	if err != nil {
		return fmt.Errorf("creating writer: %w", err)
	}

	if err := c.ensurePodmanVolumes(ctx, action, podman, writer); err != nil {
		return fmt.Errorf("creating volumes: %w", err)
	}

	noRecreate := true
	if err := podman.Compose().UpFromWorkDir(ctx, action.Path, projectName, noRecreate, action.StartServices...); err != nil {
		return err
	}

	c.log.Infof("Started application: %s", appName)
	return nil
}

func (c *Compose) remove(ctx context.Context, action *Action) error {
	appName := action.Name
	c.log.Debugf("Removing application: %s projectName: %s", appName, action.ID)

	podman, err := c.podmanFactory(action.User)
	if err != nil {
		return fmt.Errorf("creating podman client: %w", err)
	}

	if err := c.stopAndRemoveContainers(ctx, action, podman); err != nil {
		return err
	}

	c.log.Infof("Removed application: %s", appName)
	return nil
}

func (c *Compose) update(ctx context.Context, action *Action) error {
	projectName := action.ID
	c.log.Debugf("Updating application: %s projectName: %s path: %s", action.Name, projectName, action.Path)

	podman, err := c.podmanFactory(action.User)
	if err != nil {
		return fmt.Errorf("creating podman client: %w", err)
	}

	writer, err := c.writerFactory(action.User)
	if err != nil {
		return fmt.Errorf("creating writer: %w", err)
	}

	if err := c.stopAndRemoveContainers(ctx, action, podman); err != nil {
		return err
	}

	if err := c.ensurePodmanVolumes(ctx, action, podman, writer); err != nil {
		return fmt.Errorf("creating volumes: %w", err)
	}

	// change to work dir and run `docker compose up -d`
	noRecreate := true
	if err := podman.Compose().UpFromWorkDir(ctx, action.Path, projectName, noRecreate); err != nil {
		return err
	}

	c.log.Infof("Updated application: %s", action.Name)

	return nil
}

// stopAndRemoveContainers stops and removes all containers, pods, networks,
// and ephemeral (image-backed or tmpfs-backed) volumes created by the compose application.
func (c *Compose) stopAndRemoveContainers(ctx context.Context, action *Action, podman *client.Podman) error {
	return cleanPodmanResources(
		ctx,
		c.log,
		podman,
		[]string{
			fmt.Sprintf("%s=%s", client.ComposeDockerProjectLabelKey, action.ID),
		},
		[]string{},
	)
}

func cleanPodmanResources(ctx context.Context, log *log.PrefixLogger, podman *client.Podman, labels []string, filters []string) error {
	var errs []error
	networks, err := podman.ListNetworks(ctx, labels, filters)
	if err != nil {
		errs = append(errs, err)
	}

	pods, err := podman.ListPods(ctx, labels)
	if err != nil {
		errs = append(errs, err)
	}

	if err := podman.StopContainers(ctx, labels); err != nil {
		errs = append(errs, err)
	}
	if err := podman.RemoveContainer(ctx, labels); err != nil {
		errs = append(errs, err)
	}
	if err := podman.RemovePods(ctx, pods...); err != nil {
		errs = append(errs, err)
	}
	if err := podman.RemoveNetworks(ctx, networks...); err != nil {
		errs = append(errs, err)
	}
	if err := removeEphemeralVolumes(ctx, log, podman, labels, filters); err != nil {
		errs = append(errs, err)
	}

	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

// Stop stops compose containers without removing them or their associated resources.
func (c *Compose) Stop(ctx context.Context, action Action) error {
	podman, err := c.podmanFactory(action.User)
	if err != nil {
		return fmt.Errorf("creating podman client: %w", err)
	}
	return podman.Compose().Stop(ctx, action.Path, action.ID)
}

// Start starts a previously stopped compose application.
func (c *Compose) Start(ctx context.Context, action Action) error {
	podman, err := c.podmanFactory(action.User)
	if err != nil {
		return fmt.Errorf("creating podman client: %w", err)
	}
	noRecreate := true
	return podman.Compose().UpFromWorkDir(ctx, action.Path, action.ID, noRecreate, action.StartServices...)
}

// Restart stops then starts the compose application.
func (c *Compose) Restart(ctx context.Context, action Action) error {
	if err := c.Stop(ctx, action); err != nil {
		return err
	}
	return c.Start(ctx, action)
}

func (c *Compose) Execute(ctx context.Context, actions Actions) error {
	for _, action := range actions {
		switch action.Type {
		case ActionAdd:
			if err := c.add(ctx, &action); err != nil {
				return err
			}
		case ActionRemove:
			if err := c.remove(ctx, &action); err != nil {
				return err
			}
		case ActionUpdate:
			if err := c.update(ctx, &action); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unsupported action type: %s", action.Type)
		}
	}
	return nil
}

// ensurePodmanVolumes creates and populates each image-backed volume in Podman.
func (c *Compose) ensurePodmanVolumes(
	ctx context.Context,
	action *Action,
	podman *client.Podman,
	writer fileio.Writer,
) error {
	if len(action.Volumes) == 0 {
		return nil
	}

	labels := []string{fmt.Sprintf("%s=%s", client.ComposeDockerProjectLabelKey, action.ID)}
	// ensure the volume content is pulled and available
	for _, volume := range action.Volumes {
		// Compose image-backed content volumes use OCI artifacts. Executable
		// image volumes are created by Podman's image volume driver and carry
		// their own same-create provenance labels.
		if volume.OCIType != string(dependency.OCITypePodmanArtifact) {
			continue
		}
		if err := c.ensurePodmanVolume(ctx, action, volume, labels, podman, writer); err != nil {
			return fmt.Errorf("pulling image volume: %w", err)
		}
	}
	return nil
}

// ensurePodmanVolume creates and populates a image-backed podman volume.
func (c *Compose) ensurePodmanVolume(
	ctx context.Context,
	action *Action,
	volume Volume,
	labels []string,
	podman *client.Podman,
	writer fileio.Writer,
) error {
	name := volume.ID
	imageRef := volume.Reference
	var artifactDigest string
	var digestErr error
	immutableReference := imageRef
	canRecordIdentity := c.deltaStore != nil && action.Name != "" && action.Lifetime != ""
	if canRecordIdentity {
		immutableReference, artifactDigest, digestErr = podman.ImmutableArtifactReference(ctx, imageRef)
		if digestErr != nil {
			c.log.Warnf("Unable to pin artifact identity for volume %s; volume lastDelta evidence will be withheld: %v", name, digestErr)
		}
	}
	if c.deltaStore != nil {
		if err := c.deltaStore.ClearApplicationVolumeImage(action.Name, action.Lifetime, name); err != nil {
			return fmt.Errorf("clear old volume identity %w: %w", errors.WithElement(name), err)
		}
	}
	var volumePath string
	var err error
	if podman.VolumeExists(ctx, name) {
		c.log.Tracef("Volume %q already exists, updating contents", name)
		inspection, err := podman.InspectVolume(ctx, name)
		if err != nil {
			return fmt.Errorf("inspect volume identity %w: %w", errors.WithElement(name), err)
		}
		if inspection.Driver != "local" {
			return fmt.Errorf("volume %w has driver %q, expected local artifact volume", errors.WithElement(name), inspection.Driver)
		}
		volumePath, err = podman.InspectVolumeMount(ctx, name)
		if err != nil {
			return fmt.Errorf("inspect volume %w: %w", errors.WithElement(name), err)
		}
		if err := writer.RemoveContents(volumePath); err != nil {
			return fmt.Errorf("removing volume content %w: %w", errors.WithElement(volumePath), err)
		}
		if _, err := podman.ExtractArtifact(ctx, immutableReference, volumePath); err != nil {
			return fmt.Errorf("extract artifact for volume %w: %w", errors.WithElement(name), err)
		}
	} else {
		c.log.Infof("Creating volume %q from image %q", name, imageRef)
		volumePath, err = podman.CreateVolume(ctx, name, labels)
		if err != nil {
			return fmt.Errorf("creating volume %w: %w", errors.WithElement(name), err)
		}
		if _, err := podman.ExtractArtifact(ctx, immutableReference, volumePath); err != nil {
			return fmt.Errorf("copy image contents for volume %w: %w", errors.WithElement(name), err)
		}
	}
	if canRecordIdentity && digestErr == nil && artifactDigest != "" {
		if err := recordArtifactVolumeImage(ctx, c.log, c.deltaStore, *action, volume, podman, artifactDigest); err != nil {
			return err
		}
	}

	return nil
}

// ComposeVolumeName generates a unique Compose-compatible volume name
// based on the application and volume names.
func ComposeVolumeName(appName, volumeName string, user v1beta1.Username) string {
	return GenerateAppID(appName+"-"+volumeName, user)
}
