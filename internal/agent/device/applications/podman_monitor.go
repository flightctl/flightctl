package applications

import (
	"bufio"
	"context"
	"encoding/json"
	stderrs "errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/applications/lifecycle"
	"github.com/flightctl/flightctl/internal/agent/device/applications/provider"
	"github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/agent/device/systemd"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
)

const (
	expectedPodmanSigTermExitCode = 1
	stopMonitorWaitDuration       = 5 * time.Second
)

type PodmanMonitor struct {
	mu       sync.Mutex
	cmd      *exec.Cmd
	cancelFn context.CancelFunc
	running  bool
	// listenerCloseChan is used to ensure that the event listening goroutine comes to completion
	// during the stopMonitor invocation
	listenerCloseChan chan struct{}
	// lastEventTime tracks the timestamp at which the podman monitor should start listening to events for
	lastEventTime string

	// apps is a map of application ID to application.
	apps    map[string]Application
	actions []lifecycle.Action

	handlers map[v1alpha1.AppType]lifecycle.ActionHandler
	client   *client.Podman
	rw       fileio.ReadWriter

	log *log.PrefixLogger
}

func NewPodmanMonitor(
	log *log.PrefixLogger,
	podman *client.Podman,
	systemdManager systemd.Manager,
	bootTime string,
	rw fileio.ReadWriter,
) *PodmanMonitor {
	return &PodmanMonitor{
		client: podman,
		handlers: map[v1alpha1.AppType]lifecycle.ActionHandler{
			v1alpha1.AppTypeCompose: lifecycle.NewCompose(log, rw, podman),
			v1alpha1.AppTypeQuadlet: lifecycle.NewQuadlet(log, rw, systemdManager, podman),
		},
		apps:          make(map[string]Application),
		lastEventTime: bootTime,
		log:           log,
		rw:            rw,
	}
}

// hasApps returns true if there are applications to monitor
func (m *PodmanMonitor) hasApps() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.apps) > 0
}

// isRunning returns true if the monitor is currently running
func (m *PodmanMonitor) isRunning() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.running
}
func (m *PodmanMonitor) getLastEventTime() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lastEventTime
}

// startMonitor starts the podman monitor if it's not already running
func (m *PodmanMonitor) startMonitor(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.running {
		m.log.Debug("Podman monitor is already running")
		return nil
	}
	m.log.Info("Starting podman monitor")
	ctx, cancelFn := context.WithCancel(ctx)

	// list of podman events to listen for
	events := []string{"create", "init", "start", "stop", "die", "sync", "remove", "exited"}
	since := m.lastEventTime
	m.log.Debugf("Replaying podman events since: %s", since)
	cmd := m.client.EventsSinceCmd(ctx, events, since)

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		cancelFn()
		return fmt.Errorf("failed to get stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		cancelFn()
		return fmt.Errorf("failed to start podman events: %w", err)
	}

	listenerChannel := make(chan struct{})
	m.cancelFn = cancelFn
	m.cmd = cmd
	m.running = true
	m.listenerCloseChan = listenerChannel

	go func() {
		defer close(listenerChannel)
		m.listenForEvents(ctx, stdoutPipe)
	}()

	return nil
}

func waitForChannelWithTimeout(c <-chan struct{}, dur time.Duration) bool {
	timer := time.NewTimer(dur)
	defer timer.Stop()
	select {
	case <-c:
		return true
	case <-timer.C:
		return false
	}
}

// stopMonitor stops the podman monitor and records the stop time
func (m *PodmanMonitor) stopMonitor() error {
	m.mu.Lock()
	if !m.running {
		m.log.Debug("Podman monitor is already stopped")
		m.mu.Unlock()
		return nil
	}
	cancelFn := m.cancelFn
	cmd := m.cmd
	listenerChan := m.listenerCloseChan
	m.cmd = nil
	m.cancelFn = nil
	m.running = false
	m.listenerCloseChan = nil
	m.mu.Unlock()
	if cmd == nil {
		return nil
	}
	defer cancelFn()

	// When a monitor is started, a separate goroutine is created to consume events from a stream
	// To prevent unexpected errors from occurring in the reading routine, we attempt to let it
	// gracefully exit before calling .Wait and cleaning up the commands resources
	// see https://pkg.go.dev/os/exec#Cmd.StdoutPipe
	m.log.Info("Stopping podman monitor")

	killed := false
	// send a graceful shutdown signal to the command
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		m.log.Warnf("Failed to send SIGTERM to podman monitor: %v", err)
		// If we fail to term our only option is to kill
		cancelFn()
		killed = true
	}

	// wait for our consuming goroutine to complete
	if !waitForChannelWithTimeout(listenerChan, stopMonitorWaitDuration) {
		timeoutMessage := "Timeout waiting for podman monitor reader to shutdown"
		forceKilledMessage := fmt.Sprintf("%s after force killing the process", timeoutMessage)
		message := timeoutMessage
		logLevel := logrus.WarnLevel
		if killed {
			logLevel = logrus.ErrorLevel
			message = forceKilledMessage
		}
		m.log.Log(logLevel, message)
		if !killed {
			cancelFn()
			if !waitForChannelWithTimeout(listenerChan, stopMonitorWaitDuration) {
				m.log.Error(forceKilledMessage)
			}
		}
	}

	// wait for the command to exit.
	if err := cmd.Wait(); err != nil && !isExpectedShutdownError(err) {
		return fmt.Errorf("unexpected error during podman monitor shutdown: %w", err)
	}
	m.log.Info("Podman monitor stopped")
	return nil
}

// Stop stops the podman monitor without draining applications
func (m *PodmanMonitor) Stop() error {
	return m.stopMonitor()
}

// Drain stops and removes all applications, then stops the monitor
func (m *PodmanMonitor) Drain(ctx context.Context) error {
	return m.drain(ctx)
}

func (m *PodmanMonitor) getApps() []Application {
	m.mu.Lock()
	defer m.mu.Unlock()

	apps := make([]Application, 0, len(m.apps))
	for _, app := range m.apps {
		apps = append(apps, app)
	}
	return apps
}

// isExpectedShutdownError determines if an error is expected during shutdown
func isExpectedShutdownError(err error) bool {
	if errors.IsContext(err) {
		return true
	}
	var exitErr *exec.ExitError
	if stderrs.As(err, &exitErr) {
		if status, ok := exitErr.ProcessState.Sys().(syscall.WaitStatus); ok {
			// when a SIGTERM is sent to the podman events process, it will output an exit code 1
			// if a SIGKILL is sent, it will indicate that it was signaled via SIGKILL
			if status.ExitStatus() == expectedPodmanSigTermExitCode {
				return true
			}
			// if the process indicates a TERM or KILL signal then that was likely the agent and
			// we shouldn't treat that as an error
			if status.Signaled() {
				signal := status.Signal()
				return signal == syscall.SIGKILL || signal == syscall.SIGTERM
			}
		}
	}
	return false
}

func (m *PodmanMonitor) drain(ctx context.Context) error {
	var errs []error

	apps := m.getApps()
	m.log.Infof("Draining %d applications", len(apps))
	for _, app := range apps {
		if err := m.Remove(app); err != nil {
			errs = append(errs, err)
		}
	}

	if err := m.ExecuteActions(ctx); err != nil {
		errs = append(errs, err)
	}

	if len(errs) > 0 {
		return errors.Join(errs...)
	}

	return nil
}

func (m *PodmanMonitor) Has(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, ok := m.apps[id]; ok {
		return true
	}
	return false
}

// Ensures that and application is added to the monitor. if the application
// is added for the first time an Add action is queued to be executed by the
// lifecycle manager. so additional adds for the same app will be idempotent.
func (m *PodmanMonitor) Ensure(app Application) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	appID := app.ID()
	_, ok := m.apps[appID]
	if ok {
		// app already exists
		return nil
	}
	m.apps[appID] = app

	appName := app.Name()
	action := lifecycle.Action{
		AppType:  app.AppType(),
		Type:     lifecycle.ActionAdd,
		Name:     appName,
		ID:       appID,
		Path:     app.Path(),
		Embedded: app.IsEmbedded(),
		Volumes:  provider.ToLifecycleVolumes(app.Volume().List()),
	}

	m.actions = append(m.actions, action)
	return nil
}

func (m *PodmanMonitor) Remove(app Application) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	appID := app.ID()
	if _, ok := m.apps[appID]; !ok {
		m.log.Errorf("Podman application not found: %s", app.Name())
		// app is already removed
		return nil
	}

	delete(m.apps, appID)
	appName := app.Name()

	// currently we don't support removing embedded applications
	action := lifecycle.Action{
		AppType: app.AppType(),
		Type:    lifecycle.ActionRemove,
		Name:    appName,
		ID:      appID,
		Volumes: provider.ToLifecycleVolumes(app.Volume().List()),
	}

	m.actions = append(m.actions, action)
	return nil
}

func (m *PodmanMonitor) Update(app Application) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	appID := app.ID()
	_, ok := m.apps[appID]
	if !ok {
		return errors.ErrAppNotFound
	}

	m.apps[appID] = app

	// currently we don't support updating embedded applications
	action := lifecycle.Action{
		AppType: app.AppType(),
		Type:    lifecycle.ActionUpdate,
		Name:    app.Name(),
		ID:      appID,
		Path:    app.Path(),
		Volumes: provider.ToLifecycleVolumes(app.Volume().List()),
	}

	m.actions = append(m.actions, action)
	return nil
}

func (m *PodmanMonitor) getByID(appID string) (Application, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	app, ok := m.apps[appID]
	if ok {
		return app, true
	}

	return nil, false
}

func (m *PodmanMonitor) ExecuteActions(ctx context.Context) error {
	actions := m.drainActions()
	for i := range actions {
		action := actions[i]
		handler, ok := m.handlers[action.AppType]
		if !ok {
			return fmt.Errorf("%w: no action handler registered", errors.ErrUnsupportedAppType)
		}
		if err := handler.Execute(ctx, &action); err != nil {
			// this error should result in a failed status for the revision
			// and not retried
			return err
		}
	}

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

// drainActions returns a copy of the current actions and clears the existing. this
// ensures actions can only be executed once and on failure the remaining
// actions will not be executed.
func (m *PodmanMonitor) drainActions() []lifecycle.Action {
	m.mu.Lock()
	defer m.mu.Unlock()
	actions := make([]lifecycle.Action, len(m.actions))
	copy(actions, m.actions)
	// reset actions to ensure we don't execute the same actions again
	m.actions = nil
	return actions
}

func (m *PodmanMonitor) Status() ([]v1alpha1.DeviceApplicationStatus, v1alpha1.DeviceApplicationsSummaryStatus, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	var errs []error
	var summary v1alpha1.DeviceApplicationsSummaryStatus
	statuses := make([]v1alpha1.DeviceApplicationStatus, 0, len(m.apps))
	var unstarted []string
	for _, app := range m.apps {
		appStatus, appSummary, err := app.Status()
		if err != nil {
			errs = append(errs, err)
			continue
		}
		statuses = append(statuses, *appStatus)

		// phases can get worse but not better
		switch appSummary.Status {
		case v1alpha1.ApplicationsSummaryStatusError:
			summary.Status = v1alpha1.ApplicationsSummaryStatusError
			summary.Info = nil
		case v1alpha1.ApplicationsSummaryStatusDegraded:
			// ensure we don't override Error status with Degraded
			if summary.Status != v1alpha1.ApplicationsSummaryStatusError {
				summary.Status = v1alpha1.ApplicationsSummaryStatusDegraded
				summary.Info = nil
			}
		case v1alpha1.ApplicationsSummaryStatusHealthy:
			// ensure we don't override Error or Degraded status with Healthy
			if summary.Status != v1alpha1.ApplicationsSummaryStatusError && summary.Status != v1alpha1.ApplicationsSummaryStatusDegraded {
				summary.Status = v1alpha1.ApplicationsSummaryStatusHealthy
				summary.Info = nil
			}
		case v1alpha1.ApplicationsSummaryStatusUnknown:
			unstarted = append(unstarted, app.Name())
			if summary.Status != v1alpha1.ApplicationsSummaryStatusError {
				summary.Status = v1alpha1.ApplicationsSummaryStatusDegraded
				summary.Info = lo.ToPtr("Not started: " + strings.Join(unstarted, ", "))
			}
		default:
			errs = append(errs, fmt.Errorf("unknown application summary status: %s", appSummary.Status))
		}
	}

	if len(statuses) == 0 {
		summary.Status = v1alpha1.ApplicationsSummaryStatusUnknown
	}

	if len(errs) > 0 {
		return nil, summary, errors.Join(errs...)
	}

	return statuses, summary, nil
}

func (m *PodmanMonitor) listenForEvents(ctx context.Context, stdoutPipe io.ReadCloser) {
	// the pipe will be closed by calling cmd.Wait
	defer m.log.Debugf("Done listening for podman events")

	scanner := bufio.NewScanner(stdoutPipe)
	for scanner.Scan() {
		m.log.Debugf("Received podman event: %s", scanner.Text())
		select {
		case <-ctx.Done():
			return
		default:
			m.handleEvent(ctx, scanner.Bytes())
		}
	}
	// scanner won't emit an io.EOF error if it encounters one
	if err := scanner.Err(); err != nil {
		m.log.Errorf("Error reading podman events: %v", err)
	}
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
	// sync event means we are now in sync with the current state of the containers
	if event.Type == "sync" {
		m.log.Debugf("Received bootSync event : %v", event)
		return
	}

	appID := appIDFromEvent(&event)
	if appID == "" {
		m.log.Debugf("Application id not found in event attributes: %v", event)
		return
	}

	m.mu.Lock()
	app, ok := m.apps[appID]
	m.mu.Unlock()
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
	inspectData, err := m.inspectContainer(ctx, event.ID)
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

	m.mu.Lock()
	defer m.mu.Unlock()

	status := m.resolveStatus(event.Status, inspectData)
	if status == StatusRemove {
		// remove existing container
		if removed := app.RemoveWorkload(event.Name); removed {
			m.log.Debugf("Removed container: %s", event.Name)
		}
		return
	}

	container, exists := app.Workload(event.Name)
	if exists {
		// update existing container
		container.Status = status
		// restarts can only increase
		if restarts > container.Restarts {
			container.Restarts = restarts
		}

		return
	}

	// add new container
	m.log.Debugf("Adding container: %s to app %s", event.Name, app.Name())
	app.AddWorkload(&Workload{
		ID:       event.ID,
		Name:     event.Name,
		Status:   status,
		Restarts: restarts,
	})
}

func (m *PodmanMonitor) getContainerRestarts(inspectData []client.PodmanInspect) (int, error) {
	var restarts int
	if len(inspectData) > 0 {
		restarts = inspectData[0].Restarts
	}

	return restarts, nil
}

func (m *PodmanMonitor) inspectContainer(ctx context.Context, containerID string) ([]client.PodmanInspect, error) {
	resp, err := m.client.Inspect(ctx, containerID)
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
	// podman events don't properly event exited in the case where the container exits 0.
	if initialStatus == StatusDie || initialStatus == StatusDied {
		if len(inspectData) > 0 && inspectData[0].State.ExitCode == 0 && inspectData[0].State.FinishedAt != "" {
			return StatusExited
		}
	}
	return initialStatus
}
