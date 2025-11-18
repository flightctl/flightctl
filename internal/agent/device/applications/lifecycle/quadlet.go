package lifecycle

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/coreos/go-systemd/v22/unit"
	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/pkg/log"
)

const (
	QuadletAppPath         = "/etc/containers/systemd"
	EmbeddedQuadletAppPath = "/usr/local/etc/containers/systemd"
)

var _ ActionHandler = (*Quadlet)(nil)

type Quadlet struct {
	systemd        *client.Systemd
	rw             fileio.ReadWriter
	log            *log.PrefixLogger
	actionServices map[string][]string
}

func NewQuadlet(log *log.PrefixLogger, rw fileio.ReadWriter, systemd *client.Systemd) *Quadlet {
	return &Quadlet{
		systemd:        systemd,
		rw:             rw,
		log:            log,
		actionServices: make(map[string][]string),
	}
}

func (q *Quadlet) add(ctx context.Context, action *Action) error {
	appName := action.Name
	q.log.Debugf("Starting quadlet application: %s path: %s", appName, action.Path)

	if err := q.systemd.DaemonReload(ctx); err != nil {
		return fmt.Errorf("daemon reload: %w", err)
	}

	services, err := q.collectTargets(action.Path)
	if err != nil {
		return fmt.Errorf("collecting targets: %w", err)
	}

	if len(services) > 0 {
		q.log.Debugf("Starting quadlet: %s services: %q", appName, strings.Join(services, ","))
		if err := q.systemd.Start(ctx, services...); err != nil {
			return fmt.Errorf("starting units: %w", err)
		}
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
		err := q.systemd.Stop(ctx, services...)
		if err != nil {
			return fmt.Errorf("stopping units: %w", err)
		}
		// a service that is ultimately stopped via sigkill may result in a failed service
		// if it's not reset, systemd may keep the unit around even though it no longer exists
		// clearing this flag will result in the unit being removed
		failedServices := q.getFailedServices(ctx, services)
		if len(failedServices) > 0 {
			q.log.Debugf("Resetting failed state for services: %q", strings.Join(failedServices, ","))
			if resetErr := q.systemd.ResetFailed(ctx, failedServices...); resetErr != nil {
				q.log.Warnf("Failed to reset-failed for services %q: %v", strings.Join(failedServices, ","), resetErr)
			}
		}
	}

	if err := q.systemd.DaemonReload(ctx); err != nil {
		return fmt.Errorf("daemon reload: %w", err)
	}

	delete(q.actionServices, action.ID)
	q.log.Infof("Removed quadlet application: %s", appName)
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
	sections, err := unit.DeserializeSections(bytes.NewReader(contents))
	if err != nil {
		return "", fmt.Errorf("parsing quadlet %q: %w", file, err)
	}
	var section *unit.UnitSection
	for _, s := range sections {
		if s.Section == quadletSection {
			section = s
			break
		}
	}
	if section == nil {
		return "", fmt.Errorf("quadlet %q section %q not found", file, quadletSection)
	}

	for _, entry := range section.Entries {
		if entry.Name == "ServiceName" {
			return entry.Value, nil
		}
	}
	return defaultName, nil
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
		case ".container":
			sectionName = "Container"
			defaultName = fmt.Sprintf("%s.service", baseName)
		case ".pod":
			sectionName = "Pod"
			defaultName = fmt.Sprintf("%s-pod.service", baseName)
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
	units, err := q.systemd.ListUnitsByMatchPattern(ctx, services)
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
