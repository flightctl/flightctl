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

	podmanImageVolumeDriver = "image"
	podmanLocalVolumeDriver = "local"
	podmanTmpfsVolumeType   = "tmpfs"
)

var _ ActionHandler = (*Quadlet)(nil)
var _ LifecycleHandler = (*Quadlet)(nil)

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

// stopUnits stops the application target and all loaded dependent services, then
// clears any failed state left by non-zero exits. That is common when stopping
// VM/virt-launcher units, which exit non-zero when killed by systemctl stop.
// It does not remove unit files or Podman resources.
//
// An empty dependency list is not treated as target absence: list-dependencies
// does not include the queried unit and a loaded target can have no transitive
// deps. The target's load state is checked first; ListDependencies runs only
// for loaded targets so a missing unit does not fail stop.
//
// Returns the dependency list (may be empty) so callers can update exclusions.
func (q *Quadlet) stopUnits(ctx context.Context, action Action, systemctl systemd.Manager, target string) ([]string, error) {
	appName := action.Name

	targetUnits, err := q.loadedUnits(ctx, systemctl, []string{target})
	if err != nil {
		return nil, fmt.Errorf("listing loading units: %w", err)
	}
	if _, loaded := targetUnits[target]; !loaded {
		q.log.Debugf("Skipping stop for %s: target %s is not loaded", appName, target)
		return nil, nil
	}

	services, err := systemctl.ListDependencies(ctx, target)
	if err != nil {
		return nil, fmt.Errorf("listing dependencies: %w", err)
	}

	q.log.Debugf("Stopping quadlet: %s target: %s", appName, target)
	// Stopping the target begins stopping the individual services, but it is not synchronous.
	if err := systemctl.Stop(ctx, target); err != nil {
		return nil, fmt.Errorf("stopping target %s: %w", target, err)
	}

	if len(services) == 0 {
		return services, nil
	}

	unitSet, err := q.loadedUnits(ctx, systemctl, services)
	if err != nil {
		return nil, fmt.Errorf("listing loading units: %w", err)
	}

	if len(unitSet) > 0 {
		servicesToStop := make([]string, 0, len(unitSet))
		for service := range unitSet {
			servicesToStop = append(servicesToStop, service)
		}
		// Stop and wait for all services to finish.
		q.log.Debugf("Stopping quadlet: %s services: %s", appName, strings.Join(servicesToStop, ", "))
		if err := systemctl.Stop(ctx, servicesToStop...); err != nil {
			return nil, fmt.Errorf("stopping services: %w", err)
		}
		q.log.Debugf("Resetting failed state for services: %q", strings.Join(servicesToStop, ", "))
		// Clear failed state (and restart counts) so stopped units do not linger in "Failed Units".
		if err := systemctl.ResetFailed(ctx, servicesToStop...); err != nil {
			return nil, fmt.Errorf("resetting failed: %w", err)
		}
	}

	return services, nil
}

func (q *Quadlet) remove(ctx context.Context, action Action, systemctl systemd.Manager) error {
	target, err := targetName(action.ID)
	if err != nil {
		return fmt.Errorf("target name: %w", err)
	}

	services, err := q.stopUnits(ctx, action, systemctl, target)
	if err != nil {
		return err
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

	if err := cleanPodmanResources(ctx, q.log, podman, labels, filters); err != nil {
		return fmt.Errorf("cleaning podman resources: %w", err)
	}
	return nil
}

// removeEphemeralVolumes removes volumes that require no explicit cleanup:
//   - image-driver volumes: read-only overlays fully derived from their source
//     image, containing no user data. Podman recreates them automatically on
//     next container start.
//   - tmpfs-backed local volumes: Podman's stand-in for a Kubernetes emptyDir.
//     Like emptyDir, their content must not survive pod deletion/recreation;
//     Podman recreates them empty on next container start.
func removeEphemeralVolumes(ctx context.Context, log *log.PrefixLogger, podman *client.Podman, labels, filters []string) error {
	volumes, err := podman.ListVolumes(ctx, labels, filters)
	if err != nil {
		return fmt.Errorf("listing volumes: %w", err)
	}

	var ephemeralVolumes []string
	for _, volume := range volumes {
		driver, err := podman.InspectVolumeDriver(ctx, volume)
		if err != nil {
			log.Warnf("Failed to inspect volume %q driver, skipping: %v", volume, err)
			continue
		}
		if driver == podmanImageVolumeDriver {
			ephemeralVolumes = append(ephemeralVolumes, volume)
			continue
		}
		if driver != podmanLocalVolumeDriver {
			continue
		}

		optionType, err := podman.InspectVolumeOptionType(ctx, volume)
		if err != nil {
			log.Warnf("Failed to inspect volume %q options, skipping: %v", volume, err)
			continue
		}
		if optionType == podmanTmpfsVolumeType {
			ephemeralVolumes = append(ephemeralVolumes, volume)
		}
	}

	if len(ephemeralVolumes) > 0 {
		log.Debugf("Removing %d ephemeral volume(s)", len(ephemeralVolumes))
		if err := podman.RemoveVolumes(ctx, ephemeralVolumes...); err != nil {
			return fmt.Errorf("removing volumes: %w", err)
		}
	}
	return nil
}

// resolveTarget resolves the systemd client and target name for the action's user/app.
func (q *Quadlet) resolveTarget(action Action) (systemd.Manager, string, error) {
	systemctl, err := q.systemdFactory(action.User)
	if err != nil {
		return nil, "", fmt.Errorf("creating systemd client: %w", err)
	}
	target, err := targetName(action.ID)
	if err != nil {
		return nil, "", fmt.Errorf("target name: %w", err)
	}
	return systemctl, target, nil
}

// Stop stops the application's systemd target and dependent services without
// removing files or Podman resources. It also clears failed unit state so that
// expected non-zero exits (for example virt-launcher killed on stop) do not
// remain listed as Failed Units.
func (q *Quadlet) Stop(ctx context.Context, action Action) error {
	systemctl, target, err := q.resolveTarget(action)
	if err != nil {
		return err
	}
	_, err = q.stopUnits(ctx, action, systemctl, target)
	return err
}

// Start starts a previously stopped application's systemd target.
// The unit files are already on disk — no DaemonReload is required.
func (q *Quadlet) Start(ctx context.Context, action Action) error {
	systemctl, target, err := q.resolveTarget(action)
	if err != nil {
		return err
	}
	return systemctl.Start(ctx, target)
}

// Restart restarts the application's systemd target directly. VM workloads are
// rendered as native quadlet units like any other application, so no special
// handling is required.
func (q *Quadlet) Restart(ctx context.Context, action Action) error {
	systemctl, target, err := q.resolveTarget(action)
	if err != nil {
		return err
	}
	return systemctl.Restart(ctx, target)
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
