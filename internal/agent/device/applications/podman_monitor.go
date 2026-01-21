package applications

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/applications/lifecycle"
	"github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/agent/device/systemd"
	"github.com/flightctl/flightctl/pkg/log"
)

const (
	quadletSystemdLabel = "PODMAN_SYSTEMD_UNIT"
)

type PodmanMonitor struct {
	*monitor

	mu sync.Mutex
	// lastEventTime tracks the timestamp at which the podman monitor should start listening to events for
	lastEventTime string
	// lastActionsSuccessTime tracks the last timestamp at which the podman monitor successfully executed events
	lastActionsSuccessTime time.Time

	clientFactory  client.PodmanFactory
	systemdFactory systemd.ManagerFactory
	rwFactory      fileio.ReadWriterFactory

	log *log.PrefixLogger
}

func NewPodmanMonitor(
	log *log.PrefixLogger,
	podmanFactory client.PodmanFactory,
	systemdFactory systemd.ManagerFactory,
	bootTime string,
	rwFactory fileio.ReadWriterFactory,
) *PodmanMonitor {
	startTime, err := time.Parse(time.RFC3339, bootTime)
	if err != nil {
		log.Errorf("Failed to parse bootTime %q: %v", bootTime, err)
		startTime = time.Now()
	}

	m := newMonitor(log, "podman",
		handlerRegistration{appType: v1beta1.AppTypeCompose, handler: lifecycle.NewCompose(log, rwFactory, podmanFactory)},
		handlerRegistration{appType: v1beta1.AppTypeQuadlet, handler: lifecycle.NewQuadlet(log, rwFactory, systemdFactory, podmanFactory)},
	)

	return &PodmanMonitor{
		monitor:                m,
		clientFactory:          podmanFactory,
		systemdFactory:         systemdFactory,
		lastEventTime:          bootTime,
		lastActionsSuccessTime: startTime,
		log:                    log,
		rwFactory:              rwFactory,
	}
}

func (m *PodmanMonitor) getLastEventTime() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lastEventTime
}

func (m *PodmanMonitor) startMonitor(ctx context.Context) error {
	if m.isRunning() {
		return nil
	}

	events := []string{"create", "init", "start", "stop", "die", "sync", "remove", "exited"}
	since := m.getLastEventTime()
	m.log.Debugf("Replaying podman events since: %s", since)

	// TODO: Create multiple clients and aggregate events from multiple users
	// Right now it just looks at root podman.
	client, err := m.clientFactory("")
	if err != nil {
		return fmt.Errorf("creating podman client: %w", err)
	}

	cmd := client.EventsSinceCmd(ctx, events, since)

	return m.startStreaming(ctx, cmd, lineParser{}, m.handleEvent)
}

func (m *PodmanMonitor) stopMonitor() error {
	return m.stopStreaming()
}

// Stop stops the podman monitor without draining applications
func (m *PodmanMonitor) Stop() error {
	return m.stopMonitor()
}

// Drain stops and removes all applications, then stops the monitor
func (m *PodmanMonitor) Drain(ctx context.Context) error {
	m.drainActions()

	if err := m.drain(ctx); err != nil {
		return err
	}

	return m.ExecuteActions(ctx)
}

func (m *PodmanMonitor) addBatchTimeToCtx(ctx context.Context) context.Context {
	m.mu.Lock()
	defer m.mu.Unlock()
	return lifecycle.ContextWithBatchStartTime(ctx, m.lastActionsSuccessTime)
}

func (m *PodmanMonitor) updateLastSuccessTime(t time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastActionsSuccessTime = t
}

func podmanNormalizeAppType(appType v1beta1.AppType) v1beta1.AppType {
	if appType == v1beta1.AppTypeContainer {
		return v1beta1.AppTypeQuadlet
	}
	return appType
}

func (m *PodmanMonitor) ExecuteActions(ctx context.Context) error {
	ctx = m.addBatchTimeToCtx(ctx)

	if _, err := m.executeActions(ctx, podmanNormalizeAppType); err != nil {
		return err
	}

	m.updateLastSuccessTime(time.Now())

	if m.hasApps() {
		if err := m.startMonitor(ctx); err != nil {
			return fmt.Errorf("failed to start podman monitor: %w", err)
		}
	} else {
		if err := m.stopMonitor(); err != nil {
			return fmt.Errorf("failed to stop podman monitor: %w", err)
		}
	}

	return nil
}

func appIDFromEvent(event *client.PodmanEvent) string {
	if appID, ok := event.Attributes[client.ComposeDockerProjectLabelKey]; ok {
		return appID
	}
	if appID, ok := event.Attributes[client.QuadletProjectLabelKey]; ok {
		return appID
	}
	return ""
}

func (m *PodmanMonitor) handleEvent(ctx context.Context, data []byte) {
	var event client.PodmanEvent
	if err := json.Unmarshal(data, &event); err != nil {
		m.log.Errorf("Failed to decode podman event: %s %v", string(data), err)
		return
	}

	eventTime := time.Unix(0, event.TimeNano).Format(time.RFC3339)
	m.mu.Lock()
	m.lastEventTime = eventTime
	m.mu.Unlock()

	m.log.Tracef("Received podman event: %q at %s", event.Type, eventTime)

	if event.Type == "sync" {
		m.log.Debugf("Received bootSync event : %v", event)
		return
	}

	appID := appIDFromEvent(&event)
	if appID == "" {
		m.log.Debugf("Application id not found in event attributes: %v", event)
		return
	}

	app, ok := m.getByID(appID)
	if !ok {
		m.log.Debugf("Application not found: %s", appID)
		return
	}

	m.updateAppStatus(ctx, app, &event)
}

func (m *PodmanMonitor) updateAppStatus(ctx context.Context, app Application, event *client.PodmanEvent) {
	if event.Type == "container" {
		m.updateContainerStatus(ctx, app, event)
		return
	}
	if event.Type == "volume" {
		app.Volume().UpdateStatus(event)
	}
}

func (m *PodmanMonitor) updateContainerStatus(ctx context.Context, app Application, event *client.PodmanEvent) {
	appType := podmanNormalizeAppType(app.AppType())
	switch appType {
	case v1beta1.AppTypeCompose:
		m.updateComposeContainerStatus(ctx, app, event)
	case v1beta1.AppTypeQuadlet:
		m.updateQuadletContainerStatus(ctx, app, event)
	default:
		m.log.Errorf("Cannot update container status for unknown app type: %s", appType)
	}
}

func isFinishedStatus(status StatusType) bool {
	exitedStatuses := map[StatusType]struct{}{
		StatusRemove: {},
		StatusDie:    {},
		StatusDied:   {},
	}
	_, ok := exitedStatuses[status]
	return ok
}

func (m *PodmanMonitor) updateApplicationStatus(app Application, event *client.PodmanEvent, status StatusType, restarts int) {
	m.monitor.mu.Lock()
	defer m.monitor.mu.Unlock()

	container, exists := app.Workload(event.Name)
	if exists {
		container.Status = status
		if restarts > container.Restarts {
			container.Restarts = restarts
		}
		return
	}

	m.log.Debugf("Adding container: %s to app %s", event.Name, app.Name())
	app.AddWorkload(&Workload{
		ID:       event.ID,
		Name:     event.Name,
		Status:   status,
		Restarts: restarts,
	})
}

func (m *PodmanMonitor) updateQuadletContainerStatus(ctx context.Context, app Application, event *client.PodmanEvent) {
	systemdUnit, ok := event.Attributes[quadletSystemdLabel]
	if !ok {
		m.log.Errorf("Could not find systemd unit label in event %v", event)
		return
	}

	systemctl, err := m.systemdFactory("")
	if err != nil {
		m.log.Errorf("Failed to create systemctl client for %s: %v", systemdUnit, err)
		return
	}
	states, err := systemctl.Show(ctx, systemdUnit, client.WithShowLoadState())
	if err != nil || len(states) == 0 {
		m.log.Errorf("Could not show systemd unit: %s state: %v", systemdUnit, err)
		return
	}
	state := states[0]
	if v1beta1.SystemdLoadStateType(state) == v1beta1.SystemdLoadStateNotFound {
		m.log.Debugf("Received event for an unloaded unit file: %s. Skipping processing event.", systemdUnit)
		return
	}

	restarts, err := systemctl.Show(ctx, systemdUnit, client.WithShowRestarts())
	if err != nil || len(restarts) == 0 {
		m.log.Errorf("Could not show systemd unit: %s restarts: %v", systemdUnit, err)
		restarts = []string{"0"}
	}

	restartCount, err := strconv.Atoi(restarts[0])
	if err != nil {
		m.log.Errorf("Could not parse systemd unit restarts: %v", err)
	}

	status := StatusType(event.Status)
	if isFinishedStatus(status) && event.ContainerExitCode == 0 {
		status = StatusExited
	}
	m.updateApplicationStatus(app, event, status, restartCount)
}

func (m *PodmanMonitor) updateComposeContainerStatus(ctx context.Context, app Application, event *client.PodmanEvent) {
	client, err := m.clientFactory("")
	if err != nil {
		m.log.Errorf("Failed to create podman client for %s: %v", app.Name(), err)
		return
	}
	inspectData, err := m.inspectContainer(ctx, event.ID, client)
	if err != nil {
		if errors.Is(err, errors.ErrNotFound) {
			m.log.Debugf("Container %s not found; likely removed during app restart", event.ID)
		} else {
			m.log.Errorf("Failed to inspect container: %v", err)
		}
	}

	restarts, err := m.getContainerRestarts(inspectData)
	if err != nil {
		m.log.Errorf("Failed to get container restarts: %v", err)
	}

	status := m.resolveStatus(event.Status, inspectData)
	if status == StatusRemove {
		m.monitor.mu.Lock()
		defer m.monitor.mu.Unlock()
		if removed := app.RemoveWorkload(event.Name); removed {
			m.log.Debugf("Removed container: %s", event.Name)
		}
		return
	}

	m.updateApplicationStatus(app, event, status, restarts)
}

func (m *PodmanMonitor) getContainerRestarts(inspectData []client.PodmanInspect) (int, error) {
	var restarts int
	if len(inspectData) > 0 {
		restarts = inspectData[0].Restarts
	}
	return restarts, nil
}

func (m *PodmanMonitor) inspectContainer(ctx context.Context, containerID string, podman *client.Podman) ([]client.PodmanInspect, error) {
	resp, err := podman.Inspect(ctx, containerID)
	if err != nil {
		return nil, err
	}

	var inspectData []client.PodmanInspect
	if err := json.Unmarshal([]byte(resp), &inspectData); err != nil {
		return nil, fmt.Errorf("unmarshal podman inspect output: %v", err)
	}
	return inspectData, nil
}

func (m *PodmanMonitor) resolveStatus(status string, inspectData []client.PodmanInspect) StatusType {
	initialStatus := StatusType(status)
	if initialStatus == StatusDie || initialStatus == StatusDied {
		if len(inspectData) > 0 && inspectData[0].State.ExitCode == 0 && inspectData[0].State.FinishedAt != "" {
			return StatusExited
		}
	}
	return initialStatus
}
