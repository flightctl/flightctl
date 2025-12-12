package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/flightctl/flightctl/api/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/agent/device/systemd"
	"github.com/flightctl/flightctl/internal/quadlet"
	"github.com/flightctl/flightctl/pkg/log"
)

const (
	QuadletAppPath         = "/etc/containers/systemd"
	EmbeddedQuadletAppPath = "/usr/local/etc/containers/systemd"
	QuadletTargetPath      = "/etc/systemd/system/"
	QuadletTargetName      = "flightctl-quadlet-app.target"
)

var _ ActionHandler = (*Quadlet)(nil)

type Quadlet struct {
	systemdManager systemd.Manager
	podman         *client.Podman
	rw             fileio.ReadWriter
	log            *log.PrefixLogger
}

func NewQuadlet(log *log.PrefixLogger, rw fileio.ReadWriter, systemdManager systemd.Manager, podman *client.Podman) *Quadlet {
	return &Quadlet{
		systemdManager: systemdManager,
		podman:         podman,
		rw:             rw,
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

func (q *Quadlet) loadedUnits(ctx context.Context, services []string) (map[string]struct{}, error) {
	units, err := q.systemdManager.ListUnitsByMatchPattern(ctx, services)
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

func (q *Quadlet) add(ctx context.Context, action *Action) error {
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
	services, err := q.systemdManager.ListDependencies(ctx, target)
	if err != nil {
		return fmt.Errorf("listing dependencies: %w", err)
	}

	unitSet, err := q.loadedUnits(ctx, services)
	if err != nil {
		return fmt.Errorf("listing loading units: %w", err)
	}

	for _, service := range services {
		if !isServiceLoaded(unitSet, service) {
			err := fmt.Errorf("%s not loaded as a target", service)
			generatorLogs, logsErr := q.systemdManager.Logs(ctx, client.WithLogTag("quadlet-generator"), client.WithLogSince(batchTime))
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
			if err := q.remove(ctx, action); err != nil {
				q.log.Errorf("Failed to remove quadlet application %s after failing to add it: %v", appName, err)
			}
		}
	}()

	if err := q.ensureArtifactVolumes(ctx, action); err != nil {
		return fmt.Errorf("ensuring artifact volumes: %w", err)
	}
	q.log.Debugf("Starting quadlet: %s target: %s", appName, target)
	if err := q.systemdManager.Start(ctx, target); err != nil {
		err = fmt.Errorf("starting target %s: %w", target, err)
		for _, service := range services {
			serviceLogs, serviceErr := q.systemdManager.Logs(ctx, client.WithLogUnit(service), client.WithLogSince(startTime))
			if serviceErr != nil {
				err = fmt.Errorf("gathering service %q logs: %w: %w", service, serviceErr, err)
				continue
			}
			if len(serviceLogs) > 0 {
				q.log.Infof("Service: %q logs: %s", service, strings.Join(serviceLogs, "\n"))
				err = fmt.Errorf("service: %q logs: %s: %w", service, strings.Join(serviceLogs, ","), err)
			}
		}
		return err
	}

	q.systemdManager.AddExclusions(append(services, target)...)

	requiresActionCleanup = false
	q.log.Infof("Started quadlet application: %s", appName)
	return nil
}

func (q *Quadlet) remove(ctx context.Context, action *Action) error {
	appName := action.Name

	target, err := targetName(action.ID)
	if err != nil {
		return fmt.Errorf("target name: %w", err)
	}
	services, err := q.systemdManager.ListDependencies(ctx, target)
	if err != nil {
		return fmt.Errorf("listing dependencies: %w", err)
	}

	q.log.Debugf("Stopping quadlet: %s target: %s", appName, target)
	// stopping the target will begin stopping the individual services, but it is not a synchronous operation.
	if err := q.systemdManager.Stop(ctx, target); err != nil {
		return fmt.Errorf("stopping target %s: %w", target, err)
	}

	unitSet, err := q.loadedUnits(ctx, services)
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
		if err := q.systemdManager.Stop(ctx, servicesToStop...); err != nil {
			return fmt.Errorf("stopping services: %w", err)
		}
		q.log.Debugf("Resetting failed state for services: %q", strings.Join(servicesToStop, ", "))
		// Reset all services so that properties such as restart counts are reset
		if err := q.systemdManager.ResetFailed(ctx, servicesToStop...); err != nil {
			return fmt.Errorf("resetting failed: %w", err)
		}
	}
	q.systemdManager.RemoveExclusions(append(services, target)...)

	return q.cleanResources(ctx, action)
}

func (q *Quadlet) cleanResources(ctx context.Context, action *Action) error {
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
	var errs []error
	if err := cleanPodmanResources(ctx, q.podman, labels, filters); err != nil {
		errs = append(errs, fmt.Errorf("cleaning podman resources: %w", err))
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

type quadletAfterReloadFn func(context.Context) error

func (q *Quadlet) Execute(ctx context.Context, actions ...*Action) error {
	if len(actions) == 0 {
		return nil
	}
	afterReloadFns := make([]quadletAfterReloadFn, 0, len(actions))
	for _, action := range actions {
		switch action.Type {
		// Add requires daemon reload to be called prior to performing any service starting
		case ActionAdd:
			afterReloadFns = append(afterReloadFns, func(ctx context.Context) error {
				return q.add(ctx, action)
			})
		// Remove requires that stops are executed prior to calling daemon-reload
		// but the entirety of its actions can happen prior to reload
		case ActionRemove:
			if err := q.remove(ctx, action); err != nil {
				return fmt.Errorf("removing: %w", err)
			}
		// Update behaves as the combination of Remove + Update
		case ActionUpdate:
			if err := q.remove(ctx, action); err != nil {
				return fmt.Errorf("removing for update: %w", err)
			}
			afterReloadFns = append(afterReloadFns, func(ctx context.Context) error {
				return q.add(ctx, action)
			})
		default:
			return fmt.Errorf("unsupported action type: %s", action.Type)
		}
	}
	if err := q.systemdManager.DaemonReload(ctx); err != nil {
		return fmt.Errorf("daemon reload: %w", err)
	}

	for _, afterReload := range afterReloadFns {
		if err := afterReload(ctx); err != nil {
			return err
		}
	}
	return nil
}

func (q *Quadlet) ensureArtifactVolumes(ctx context.Context, action *Action) error {
	if len(action.Volumes) == 0 {
		return nil
	}
	labels := []string{fmt.Sprintf("%s=%s", client.QuadletProjectLabelKey, action.ID)}
	var artifactVolumes []string
	cleanup := func(err error) error {
		if len(artifactVolumes) > 0 {
			if removeErr := q.podman.RemoveVolumes(ctx, artifactVolumes...); removeErr != nil {
				err = fmt.Errorf("removing artifacts: %w: %w", removeErr, err)
			}
		}
		return err
	}
	for _, volume := range action.Volumes {
		if q.podman.ImageExists(ctx, volume.Reference) {
			q.log.Debugf("Skipping image-backed volume with reference %s", volume.Reference)
			continue
		}

		volumeName := volume.ID
		volumePath := ""
		var err error
		if q.podman.VolumeExists(ctx, volumeName) {
			q.log.Tracef("Volume %q already exists, updating contents", volumeName)
			volumePath, err = q.podman.InspectVolumeMount(ctx, volumeName)
			if err != nil {
				return fmt.Errorf("inspect volume %q: %w", volumeName, err)
			}
			if err := q.rw.RemoveContents(volumePath); err != nil {
				return fmt.Errorf("removing volume content %q: %w", volumePath, err)
			}
		} else {
			q.log.Tracef("Creating volume %q", volumeName)
			volumePath, err = q.podman.CreateVolume(ctx, volumeName, labels)
			if err != nil {
				return cleanup(fmt.Errorf("creating volume %q: %w", volumeName, err))
			}
			artifactVolumes = append(artifactVolumes, volumeName)
		}

		if _, err := q.podman.ExtractArtifact(ctx, volume.Reference, volumePath); err != nil {
			return cleanup(fmt.Errorf("extracting artifact to volume %q: %w", volumeName, err))
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
