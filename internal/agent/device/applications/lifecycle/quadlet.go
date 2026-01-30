package lifecycle

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/agent/device/systemd"
	"github.com/flightctl/flightctl/internal/quadlet"
	"github.com/flightctl/flightctl/pkg/log"
)

const (
	RootfulQuadletAppPath    = "/etc/containers/systemd"
	EmbeddedQuadletAppPath   = "/usr/local/etc/containers/systemd"
	RootfulQuadletTargetPath = "/etc/systemd/system/"
	QuadletTargetName        = "flightctl-quadlet-app.target"
)

var _ ActionHandler = (*Quadlet)(nil)

type Quadlet struct {
	systemdFactory systemd.ManagerFactory
	podmanFactory  client.PodmanFactory
	rwFactory      fileio.ReadWriterFactory
	log            *log.PrefixLogger
}

func NewQuadlet(log *log.PrefixLogger, rwFactory fileio.ReadWriterFactory, systemdFactory systemd.ManagerFactory, podmanFactory client.PodmanFactory) *Quadlet {
	return &Quadlet{
		systemdFactory: systemdFactory,
		podmanFactory:  podmanFactory,
		rwFactory:      rwFactory,
		log:            log,
	}
}

func isServiceLoaded(unitSet map[string]struct{}, service string) bool {
	if filepath.Ext(service) == ".target" {
		return true
	}
	_, exists := unitSet[service]
	return exists
}

func (q *Quadlet) loadedUnits(ctx context.Context, systemctl systemd.Manager, services []string) (map[string]struct{}, error) {
	units, err := systemctl.ListUnitsByMatchPattern(ctx, services)
	if err != nil {
		return nil, fmt.Errorf("listing loaded units: %w", err)
	}

	unitSet := make(map[string]struct{}, len(units))
	for _, u := range units {
		if u.LoadState == string(v1beta1.SystemdLoadStateLoaded) {
			unitSet[u.Unit] = struct{}{}
		}
	}
	return unitSet, nil
}

func (q *Quadlet) add(ctx context.Context, action Action, systemctl systemd.Manager) error {
	appName := action.Name
	q.log.Debugf("Starting quadlet application: %s path: %s", appName, action.Path)

	batchTime, ok := BatchStartTimeFromContext(ctx)
	if !ok {
		batchTime = time.Now()
	}
	startTime := time.Now()

	target, err := targetName(action.ID)
	if err != nil {
		return fmt.Errorf("target name: %w", err)
	}

	services, err := systemctl.ListDependencies(ctx, target)
	if err != nil {
		return fmt.Errorf("listing dependencies: %w", err)
	}

	unitSet, err := q.loadedUnits(ctx, systemctl, services)
	if err != nil {
		return fmt.Errorf("listing loading units: %w", err)
	}

	for _, service := range services {
		if !isServiceLoaded(unitSet, service) {
			err := fmt.Errorf("%s not loaded as a target", service)
			generatorLogs, logsErr := systemctl.Logs(ctx, client.WithLogTag("quadlet-generator"), client.WithLogSince(batchTime))
			if logsErr != nil {
				q.log.Errorf("Failed to fetch quadlet-generator logs: %v", logsErr)
			}
			if len(generatorLogs) > 0 {
				q.log.Errorf("Failed to generate services from the defined Quadlet. Check the syntax of the Quadlet files.\n%s", strings.Join(generatorLogs, "\n"))
				err = fmt.Errorf("quadlet generator: %s %w", strings.Join(generatorLogs, ","), err)
			}
			return err
		}
	}

	requiresActionCleanup := true
	defer func() {
		if requiresActionCleanup {
			if err := q.remove(ctx, action, systemctl); err != nil {
				q.log.Errorf("Failed to remove quadlet application %s after failing to add it: %v", appName, err)
			}
		}
	}()

	if err := q.ensureArtifactVolumes(ctx, action); err != nil {
		return fmt.Errorf("ensuring artifact volumes: %w", err)
	}
	q.log.Debugf("Starting quadlet: %s target: %s", appName, target)
	if err := systemctl.Start(ctx, target); err != nil {
		err = fmt.Errorf("starting target %s: %w", target, err)
		for _, service := range services {
			serviceLogs, serviceErr := systemctl.Logs(ctx, client.WithLogUnit(service), client.WithLogSince(startTime))
			if serviceErr != nil {
				err = fmt.Errorf("gathering service %q logs: %w: %w", service, serviceErr, err)
				continue
			}
			if len(serviceLogs) > 0 {
				q.log.Infof("Service: %q logs: %s", service, strings.Join(serviceLogs, "\n"))
				err = fmt.Errorf("service %w logs: %s: %w", errors.WithElement(service), strings.Join(serviceLogs, ","), err)
			}
		}
		return err
	}

	systemctl.AddExclusions(append(services, target)...)

	requiresActionCleanup = false
	q.log.Infof("Started quadlet application: %s", appName)
	return nil
}

func (q *Quadlet) remove(ctx context.Context, action Action, systemctl systemd.Manager) error {
	appName := action.Name

	target, err := targetName(action.ID)
	if err != nil {
		return fmt.Errorf("target name: %w", err)
	}

	services, err := systemctl.ListDependencies(ctx, target)
	if err != nil {
		return fmt.Errorf("listing dependencies: %w", err)
	}

	q.log.Debugf("Stopping quadlet: %s target: %s", appName, target)
	// stopping the target will begin stopping the individual services, but it is not a synchronous operation.
	if err := systemctl.Stop(ctx, target); err != nil {
		return fmt.Errorf("stopping target %s: %w", target, err)
	}

	unitSet, err := q.loadedUnits(ctx, systemctl, services)
	if err != nil {
		return fmt.Errorf("listing loading units: %w", err)
	}

	if len(unitSet) > 0 {
		servicesToStop := make([]string, 0, len(unitSet))
		for service := range unitSet {
			servicesToStop = append(servicesToStop, service)
		}
		// stop and wait for all services to finish
		q.log.Debugf("Stopping quadlet: %s services: %s", appName, strings.Join(servicesToStop, ", "))
		if err := systemctl.Stop(ctx, servicesToStop...); err != nil {
			return fmt.Errorf("stopping services: %w", err)
		}
		q.log.Debugf("Resetting failed state for services: %q", strings.Join(servicesToStop, ", "))
		// Reset all services so that properties such as restart counts are reset
		if err := systemctl.ResetFailed(ctx, servicesToStop...); err != nil {
			return fmt.Errorf("resetting failed: %w", err)
		}
	}
	systemctl.RemoveExclusions(append(services, target)...)

	return q.cleanResources(ctx, action)
}

func (q *Quadlet) cleanResources(ctx context.Context, action Action) error {
	q.log.Infof("Removed quadlet application: %s", action.Name)
	// the labels applied to quadlets are only directly applied to that quadlet. They do not apply to
	// any resources created indirectly. As an example, a container quadlet can create multiple volumes without referencing
	// a volume quadlet. The label applied to the container will not be applied to the volumes, but since we are
	// namespacing, we can remove any resources that are directly tied to our application. Volumes that are not explicitly
	// tracked by the API remain untouched.
	labels := []string{
		fmt.Sprintf("%s=%s", client.QuadletProjectLabelKey, action.ID),
	}
	filters := []string{
		fmt.Sprintf("name=%s-*", action.ID),
	}

	podman, err := q.podmanFactory(action.User)
	if err != nil {
		return fmt.Errorf("creating podman client: %w", err)
	}

	var errs []error
	if err := cleanPodmanResources(ctx, podman, labels, filters); err != nil {
		errs = append(errs, fmt.Errorf("cleaning podman resources: %w", err))
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

func (q *Quadlet) Execute(ctx context.Context, actions Actions) error {
	for user, byType := range actions.ByUser() {
		systemctl, err := q.systemdFactory(user)
		if err != nil {
			return fmt.Errorf("creating systemd client: %w", err)
		}

		if len(byType.Unknown) > 0 {
			return fmt.Errorf("unknown action type %s", byType.Unknown[0].Type)
		}

		for _, a := range slices.Concat(byType.Removes, byType.Updates) {
			if err := q.remove(ctx, a, systemctl); err != nil {
				return fmt.Errorf("removing: %w", err)
			}
		}

		if err := systemctl.DaemonReload(ctx); err != nil {
			return fmt.Errorf("systemd daemon reload: %w", err)
		}

		// Add requires daemon reload to be called prior to performing any service starting
		for _, a := range slices.Concat(byType.Adds, byType.Updates) {
			if err := q.add(ctx, a, systemctl); err != nil {
				return fmt.Errorf("adding: %w", err)
			}
		}
	}

	return nil
}

func (q *Quadlet) ensureArtifactVolumes(ctx context.Context, action Action) error {
	if len(action.Volumes) == 0 {
		return nil
	}
	podman, err := q.podmanFactory(action.User)
	if err != nil {
		return fmt.Errorf("creating podman client: %w", err)
	}

	rw, err := q.rwFactory(action.User)
	if err != nil {
		return fmt.Errorf("creating read/writer: %w", err)
	}

	labels := []string{fmt.Sprintf("%s=%s", client.QuadletProjectLabelKey, action.ID)}
	var artifactVolumes []string
	cleanup := func(err error) error {
		if len(artifactVolumes) > 0 {
			if removeErr := podman.RemoveVolumes(ctx, artifactVolumes...); removeErr != nil {
				err = fmt.Errorf("removing artifacts: %w: %w", removeErr, err)
			}
		}
		return err
	}
	for _, volume := range action.Volumes {
		if podman.ImageExists(ctx, volume.Reference) {
			q.log.Debugf("Skipping image-backed volume with reference %s", volume.Reference)
			continue
		}

		volumeName := volume.ID
		volumePath := ""
		var err error
		if podman.VolumeExists(ctx, volumeName) {
			q.log.Tracef("Volume %q already exists, updating contents", volumeName)
			volumePath, err = podman.InspectVolumeMount(ctx, volumeName)
			if err != nil {
				return fmt.Errorf("inspect volume %w: %w", errors.WithElement(volumeName), err)
			}
			if err := rw.RemoveContents(volumePath); err != nil {
				return fmt.Errorf("removing volume content %w: %w", errors.WithElement(volumePath), err)
			}
		} else {
			q.log.Tracef("Creating volume %q", volumeName)
			volumePath, err = podman.CreateVolume(ctx, volumeName, labels)
			if err != nil {
				return cleanup(fmt.Errorf("creating volume %w: %w", errors.WithElement(volumeName), err))
			}
			artifactVolumes = append(artifactVolumes, volumeName)
		}

		if _, err := podman.ExtractArtifact(ctx, volume.Reference, volumePath); err != nil {
			return cleanup(fmt.Errorf("extracting artifact to volume %w: %w", errors.WithElement(volumeName), err))
		}

		q.log.Infof("Creating artifact volume %q from artifact %q", volume.ID, volume.Reference)
	}

	return nil
}

func targetName(appID string) (string, error) {
	if appID == "" {
		return "", fmt.Errorf("empty appID")
	}
	return quadlet.NamespaceResource(appID, QuadletTargetName), nil
}
