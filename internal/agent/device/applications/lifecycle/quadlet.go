package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/agent/device/systemd"
	"github.com/flightctl/flightctl/internal/quadlet"
	"github.com/flightctl/flightctl/pkg/log"
)

const (
	QuadletAppPath         = "/etc/containers/systemd"
	EmbeddedQuadletAppPath = "/usr/local/etc/containers/systemd"
)

var _ ActionHandler = (*Quadlet)(nil)

type Quadlet struct {
	systemdManager systemd.Manager
	podman         *client.Podman
	rw             fileio.ReadWriter
	log            *log.PrefixLogger
	actionServices map[string][]string
}

func NewQuadlet(log *log.PrefixLogger, rw fileio.ReadWriter, systemdManager systemd.Manager, podman *client.Podman) *Quadlet {
	return &Quadlet{
		systemdManager: systemdManager,
		podman:         podman,
		rw:             rw,
		log:            log,
		actionServices: make(map[string][]string),
	}
}

func (q *Quadlet) add(ctx context.Context, action *Action) error {
	appName := action.Name
	q.log.Debugf("Starting quadlet application: %s path: %s", appName, action.Path)

	// systemd daemon-reload can trigger the reload of all quadlet applications within a spec.
	// use the batch time to gather logs for the systemd generator call
	batchTime, ok := BatchStartTimeFromContext(ctx)
	if !ok {
		batchTime = time.Now()
	}
	// use the start time to gather logs for failed services
	startTime := time.Now()

	if err := q.systemdManager.DaemonReload(ctx); err != nil {
		return fmt.Errorf("daemon reload: %w", err)
	}

	services, err := q.collectTargets(action.Path)
	if err != nil {
		return fmt.Errorf("collecting targets: %w", err)
	}

	if len(services) > 0 {
		q.log.Debugf("Starting quadlet: %s services: %q", appName, strings.Join(services, ","))
		if err := q.systemdManager.Start(ctx, services...); err != nil {
			// if starting fails, attempt to gather as many logs as possible
			// check the individual service logs and the quadlet generator logs for potential issues
			err = fmt.Errorf("starting units: %w", err)
			for _, service := range services {
				serviceLogs, serviceErr := q.systemdManager.Logs(ctx, client.WithLogUnit(service), client.WithLogSince(startTime))
				if serviceErr != nil {
					q.log.Errorf("Failed to gather service: %q logs: %v", service, serviceErr)
					continue
				}
				if len(serviceLogs) > 0 {
					q.log.Infof("Service: %q logs: %s", service, strings.Join(serviceLogs, "\n"))
					err = fmt.Errorf("service: %q logs: %s: %w", service, strings.Join(serviceLogs, ","), err)
				}
			}
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
		q.systemdManager.AddExclusions(services...)
	}
	q.actionServices[action.ID] = services

	q.log.Infof("Started quadlet application: %s", appName)
	return nil
}

// remove disables and reloads the systemd services associated with the specified application
// note, the current state of the application directory can't be used as it has likely been modified already.
func (q *Quadlet) remove(ctx context.Context, action *Action) error {
	appName := action.Name
	services, ok := q.actionServices[action.ID]
	if !ok {
		q.log.Debugf("Quadlet application not found: %s for stopping services", appName)
		return nil
	}

	if len(services) > 0 {
		q.log.Debugf("Stopping quadlet: %s services: %q", appName, strings.Join(services, ","))
		err := q.systemdManager.Stop(ctx, services...)
		if err != nil {
			return fmt.Errorf("stopping units: %w", err)
		}
		// a service that is ultimately stopped via sigkill may result in a failed service
		// if it's not reset, systemd may keep the unit around even though it no longer exists
		// clearing this flag will result in the unit being removed
		failedServices := q.getFailedServices(ctx, services)
		if len(failedServices) > 0 {
			q.log.Debugf("Resetting failed state for services: %q", strings.Join(failedServices, ","))
			if resetErr := q.systemdManager.ResetFailed(ctx, failedServices...); resetErr != nil {
				q.log.Warnf("Failed to reset-failed for services %q: %v", strings.Join(failedServices, ","), resetErr)
			}
		}
		q.systemdManager.RemoveExclusions(services...)
	}

	if err := q.systemdManager.DaemonReload(ctx); err != nil {
		return fmt.Errorf("daemon reload: %w", err)
	}

	delete(q.actionServices, action.ID)
	q.log.Infof("Removed quadlet application: %s", appName)

	// the labels applied to quadlets are only directly applied to that quadlet. They do not apply to
	// any resources created indirectly. As an example, a container quadlet can create multiple volumes without referencing
	// a volume quadlet. The label applied to the container will not be applied to the volumes, but since we are
	// namespacing, we can remove any resources that are directly tied to our application
	labels := []string{
		fmt.Sprintf("%s=%s", client.QuadletProjectLabelKey, action.ID),
	}
	filters := []string{
		// any resource that has a name that is prefixed with the quadlet's ID
		fmt.Sprintf("name=%s-*", action.ID),
	}
	var errs []error
	if err := cleanPodmanResources(ctx, q.podman, labels, filters); err != nil {
		errs = append(errs, fmt.Errorf("cleaning podman resources: %w", err))
	}

	// The agent currently doesn't distinguish between "stop for a graceful shutdown", and "remove application". Both elicit
	// a "remove" operation. Most resources are fine to remove and recreate on startup, as they should be application specific,
	// but volumes MUST survive restarts. Image backed volumes are removed to allow for updating to newer images if updated,
	// and on restart, repopulating the volume from the already downloaded image is non-destructive
	volumes, err := q.podman.ListVolumes(ctx, labels, filters)
	if err != nil {
		errs = append(errs, fmt.Errorf("listing volumes: %w", err))
	}
	var volsToRemove []string
	for _, volume := range volumes {
		driver, err := q.podman.InspectVolumeDriver(ctx, volume)
		if err != nil {
			errs = append(errs, fmt.Errorf("inspecting volume %q: %w", volume, err))
			continue
		}
		if driver == "image" {
			volsToRemove = append(volsToRemove, volume)
		}
	}
	if len(volsToRemove) > 0 {
		if err := q.podman.RemoveVolumes(ctx, volsToRemove...); err != nil {
			errs = append(errs, fmt.Errorf("removing volumes: %w", err))
		}
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

// update is just a combination of stopping the existing units and then starting the new ones based on the current state
func (q *Quadlet) update(ctx context.Context, action *Action) error {
	if err := q.remove(ctx, action); err != nil {
		return fmt.Errorf("removing app: %q: %w", action.Name, err)
	}
	if err := q.add(ctx, action); err != nil {
		return fmt.Errorf("adding app: %q: %w", action.Name, err)
	}
	return nil
}

func (q *Quadlet) Execute(ctx context.Context, action *Action) error {
	switch action.Type {
	case ActionAdd:
		return q.add(ctx, action)
	case ActionRemove:
		return q.remove(ctx, action)
	case ActionUpdate:
		return q.update(ctx, action)
	default:
		return fmt.Errorf("unsupported action type: %s", action.Type)
	}
}

func (q *Quadlet) serviceName(file string, quadletSection string, defaultName string) (string, error) {
	contents, err := q.rw.ReadFile(file)
	if err != nil {
		return "", fmt.Errorf("reading quadlet %s: %w", file, err)
	}
	unit, err := quadlet.NewUnit(contents)
	if err != nil {
		return "", fmt.Errorf("parsing quadlet %q: %w", file, err)
	}

	name, err := unit.Lookup(quadletSection, quadlet.ServiceNameKey)
	if err != nil {
		if errors.Is(err, quadlet.ErrKeyNotFound) {
			return defaultName, nil
		}
		return "", fmt.Errorf("looking up %q: %w", quadletSection, err)
	}
	return name, nil
}

func (q *Quadlet) collectTargets(path string) ([]string, error) {
	entries, err := q.rw.ReadDir(path)
	if err != nil {
		return nil, fmt.Errorf("reading directory: %w", err)
	}

	var services []string
	var targets []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		filename := entry.Name()
		ext := filepath.Ext(filename)
		baseName := strings.TrimSuffix(filename, ext)

		var sectionName string
		var defaultName string
		switch ext {
		case quadlet.ContainerExtension:
			sectionName = quadlet.ContainerGroup
			defaultName = fmt.Sprintf("%s.service", baseName)
		case quadlet.PodExtension:
			sectionName = quadlet.PodGroup
			defaultName = fmt.Sprintf("%s-pod.service", baseName)
		case quadlet.VolumeExtension:
			sectionName = quadlet.VolumeGroup
			defaultName = fmt.Sprintf("%s-volume.service", baseName)
		case quadlet.NetworkExtension:
			sectionName = quadlet.NetworkGroup
			defaultName = fmt.Sprintf("%s-network.service", baseName)
		case quadlet.ImageExtension:
			sectionName = quadlet.ImageGroup
			defaultName = fmt.Sprintf("%s-image.service", baseName)
		case ".target":
			targets = append(targets, filename)
			continue
		default:
			continue
		}

		serviceName, err := q.serviceName(filepath.Join(path, entry.Name()), sectionName, defaultName)
		if err != nil {
			return nil, fmt.Errorf("getting %s service name: %w", filename, err)
		}

		services = append(services, serviceName)
	}

	// ensure that targets are processed first and services are
	// secondary.
	return append(targets, services...), nil
}

func (q *Quadlet) getFailedServices(ctx context.Context, services []string) []string {
	units, err := q.systemdManager.ListUnitsByMatchPattern(ctx, services)
	if err != nil {
		q.log.Warnf("Failed to list units to check for failed state: %v", err)
		return nil
	}

	var failedServices []string
	for _, u := range units {
		if u.ActiveState == string(v1alpha1.SystemdActiveStateFailed) {
			failedServices = append(failedServices, u.Unit)
		}
	}
	return failedServices
}
