package applications

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/applications/lifecycle"
	"github.com/flightctl/flightctl/internal/agent/device/applications/provider"
	"github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/agent/device/systemd"
	"github.com/flightctl/flightctl/pkg/log"
)

const (
	expectedPodmanSigTermExitCode = 1
	quadletSystemdLabel           = "PODMAN_SYSTEMD_UNIT"
)

type PodmanMonitor struct {
	mu sync.Mutex
	// apps is a map of application ID to application.
	apps                   map[string]Application
	actions                []lifecycle.Action
	startTime              time.Time
	lastActionsSuccessTime time.Time

	handlers       map[v1beta1.AppType]lifecycle.ActionHandler
	clientFactory  client.PodmanFactory
	systemdFactory systemd.ManagerFactory
	watchers       map[v1beta1.Username]*podmanEventWatcher
	events         chan client.PodmanEvent

	log *log.PrefixLogger
}

func NewPodmanMonitor(
	log *log.PrefixLogger,
	podmanFactory client.PodmanFactory,
	systemdFactory systemd.ManagerFactory,
	bootTime string,
	rwFactory fileio.ReadWriterFactory,
) *PodmanMonitor {
	// don't fail for this. This is being parsed purely for informational reasons in the event something fails
	startTime, err := time.Parse(time.RFC3339, bootTime)
	if err != nil {
		log.Errorf("Failed to parse bootTime %q: %v", bootTime, err)
		startTime = time.Now()
	}
	return &PodmanMonitor{
		clientFactory:  podmanFactory,
		systemdFactory: systemdFactory,
		handlers: map[v1beta1.AppType]lifecycle.ActionHandler{
			v1beta1.AppTypeCompose: lifecycle.NewCompose(log, rwFactory, podmanFactory),
			v1beta1.AppTypeQuadlet: lifecycle.NewQuadlet(log, rwFactory, systemdFactory, podmanFactory),
		},
		watchers:               make(map[v1beta1.Username]*podmanEventWatcher),
		apps:                   make(map[string]Application),
		startTime:              startTime,
		lastActionsSuccessTime: startTime,
		log:                    log,
	}
}

// isRunning returns true if the monitor is currently running
func (m *PodmanMonitor) isRunning(username v1beta1.Username) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.watchers[username]
	return ok
}

func (m *PodmanMonitor) ensureMonitorForUser(ctx context.Context, username v1beta1.Username) error {
	if _, ok := m.watchers[username]; ok {
		m.log.Debugf("Podman monitor is already running for user %s", username)
		return nil
	}
	m.log.Infof("Starting podman monitor for user %s", username)

	if m.events == nil {
		m.events = make(chan client.PodmanEvent)
		go m.listenForEvents(ctx)
	}

	client, err := m.clientFactory(username)
	if err != nil {
		return err
	}

	var watcher podmanEventWatcher
	watcher.Init(m.log, username, client, m.startTime)
	if err := watcher.Watch(ctx, m.events); err != nil {
		return err
	}

	m.watchers[username] = &watcher

	return nil
}

func (m *PodmanMonitor) stopMonitorForUser(username v1beta1.Username) error {
	watcher, ok := m.watchers[username]
	if !ok {
		m.log.Debugf("Podman monitor is already stopped for user %s", username)
		return nil
	}
	delete(m.watchers, username)

	return watcher.Stop()
}

// Stop stops the podman monitor without draining applications
func (m *PodmanMonitor) Stop() error {
	var errs []error
	for _, watcher := range m.watchers {
		if err := watcher.Stop(); err != nil {
			errs = append(errs, err)
		}
	}
	m.watchers = nil
	if m.events != nil {
		close(m.events)
	}
	return errors.Join(errs...)
}

// Drain stops and removes all applications, then stops the monitor
func (m *PodmanMonitor) Drain(ctx context.Context) error {
	// Drain may be called as the result of an OS upgrade. Any applications added during that spec update
	// may have "start" actions pending. Clear out any pending actions so that they can be replaced with "stops"
	m.drainActions()

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

func (m *PodmanMonitor) drain(ctx context.Context) error {
	var errs []error

	apps := m.getApps()
	m.log.Infof("Draining %d applications", len(apps))
	for _, app := range apps {
		if err := m.QueueRemove(app); err != nil {
			errs = append(errs, err)
		}
	}

	if err := m.ExecuteActions(ctx); err != nil {
		errs = append(errs, err)
	}

	return errors.Join(errs...)
}

func (m *PodmanMonitor) Has(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	_, ok := m.apps[id]
	return ok
}

// Ensures that and application is added to the monitor. if the application
// is added for the first time an Add action is queued to be executed by the
// lifecycle manager. so additional adds for the same app will be idempotent.
func (m *PodmanMonitor) Ensure(ctx context.Context, app Application) error {
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
		User:     app.User(),
		Name:     appName,
		ID:       appID,
		Path:     app.Path(),
		Embedded: app.IsEmbedded(),
		Volumes:  provider.ToLifecycleVolumes(app.Volume().List()),
	}

	m.actions = append(m.actions, action)
	return nil
}

// expects mutex to be held
func (m *PodmanMonitor) canRemoveApp(app Application) bool {
	_, ok := m.apps[app.ID()]
	// embedded applications can adhere to slightly different lifecycles
	// making it possible to remove an app that was never added.
	return ok || app.IsEmbedded()
}

func (m *PodmanMonitor) QueueRemove(app Application) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	appID := app.ID()
	if !m.canRemoveApp(app) {
		m.log.Errorf("Podman application not found: %s", app.Name())
		// app is already removed
		return nil
	}

	delete(m.apps, appID)
	appName := app.Name()

	action := lifecycle.Action{
		AppType: app.AppType(),
		Type:    lifecycle.ActionRemove,
		Name:    appName,
		User:    app.User(),
		ID:      appID,
		Volumes: provider.ToLifecycleVolumes(app.Volume().List()),
	}

	m.actions = append(m.actions, action)

	return nil
}

func (m *PodmanMonitor) QueueUpdate(app Application) error {
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
		User:    app.User(),
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

func normalizeActionAppType(appType v1beta1.AppType) v1beta1.AppType {
	// utilize the quadlet handler for containers
	if appType == v1beta1.AppTypeContainer {
		return v1beta1.AppTypeQuadlet
	}
	return appType
}

func (m *PodmanMonitor) ExecuteActions(ctx context.Context) error {
	ctx = m.addBatchTimeToCtx(ctx)
	actions := m.drainActions()

	groupedActions := make(map[v1beta1.AppType][]lifecycle.Action)
	for i := range actions {
		action := actions[i]
		appType := normalizeActionAppType(action.AppType)
		_, ok := m.handlers[appType]
		if !ok {
			return fmt.Errorf("%w: no action handler registered: %s", errors.ErrUnsupportedAppType, action.AppType)
		}
		groupedActions[appType] = append(groupedActions[appType], action)
	}

	for appType, actions := range groupedActions {
		if err := m.handlers[appType].Execute(ctx, actions); err != nil {
			return err
		}
	}

	m.updateLastSuccessTime(time.Now())

	m.mu.Lock()
	defer m.mu.Unlock()

	for _, a := range actions {
		switch a.Type {
		case lifecycle.ActionAdd, lifecycle.ActionUpdate:
			if err := m.ensureMonitorForUser(ctx, a.User); err != nil {
				return fmt.Errorf("failed to start podman monitor: %w", err)
			}
		case lifecycle.ActionRemove:
			// Stop the monitor for the app user if no other apps for that user are running.
			user := a.User
			exists := false
			for _, app := range m.apps {
				if app.User() == user {
					exists = true
				}
			}
			if !exists {
				if err := m.stopMonitorForUser(user); err != nil {
					return fmt.Errorf("failed to stop podman monitor for user %s: %w", user, err)
				}
			}
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

func (m *PodmanMonitor) Status() ([]AppStatusResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	var errs []error
	results := make([]AppStatusResult, 0, len(m.apps))

	for _, app := range m.apps {
		appStatus, appSummary, err := app.Status()
		if err != nil {
			errs = append(errs, err)
			continue
		}
		results = append(results, AppStatusResult{
			Status:  *appStatus,
			Summary: appSummary,
		})
	}

	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}

	return results, nil
}

func (m *PodmanMonitor) listenForEvents(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case event := <-m.events:
			m.handleEvent(ctx, event)
		}
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

func (m *PodmanMonitor) handleEvent(ctx context.Context, event client.PodmanEvent) {
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
	appType := normalizeActionAppType(app.AppType())
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
	exitedStatuses := map[StatusType]struct {
	}{
		StatusRemove: {},
		StatusDie:    {},
		StatusDied:   {},
	}
	_, ok := exitedStatuses[status]
	return ok
}

func (m *PodmanMonitor) updateApplicationStatus(app Application, event *client.PodmanEvent, status StatusType, restarts int) {
	m.mu.Lock()
	defer m.mu.Unlock()

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

func (m *PodmanMonitor) updateQuadletContainerStatus(ctx context.Context, app Application, event *client.PodmanEvent) {
	systemdUnit, ok := event.Attributes[quadletSystemdLabel]
	if !ok {
		m.log.Errorf("Could not find systemd unit label in event %v", event)
		return
	}

	systemctl, err := m.systemdFactory(app.User())
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
		// likely from event replay
		m.log.Debugf("Received event for an unloaded unit file: %s. Skipping processing event.", systemdUnit)
		return
	}

	restarts, err := systemctl.Show(ctx, systemdUnit, client.WithShowRestarts())
	if err != nil || len(restarts) == 0 {
		m.log.Errorf("Could not show systemd unit: %s restarts: %v", systemdUnit, err)
		// default to no restarts similar to how compose handles it.
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
	client, err := m.clientFactory(app.User())
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
		m.mu.Lock()
		defer m.mu.Unlock()
		// remove existing container
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
	// podman events don't properly event exited in the case where the container exits 0.
	if initialStatus == StatusDie || initialStatus == StatusDied {
		if len(inspectData) > 0 && inspectData[0].State.ExitCode == 0 && inspectData[0].State.FinishedAt != "" {
			return StatusExited
		}
	}
	return initialStatus
}

type podmanEventWatcher struct {
	log      *log.PrefixLogger
	username v1beta1.Username
	client   *client.Podman
	// listenerCloseChan is used to ensure that the event listening goroutine comes to completion
	// during the stopMonitor invocation
	listenerCloseChan chan struct{}
	// lastEventTime tracks the timestamp at which the podman monitor should start listening to events for
	lastEventTime atomic.Pointer[time.Time]
	cmd           *exec.Cmd
	cancel        context.CancelFunc
}

func (e *podmanEventWatcher) Init(log *log.PrefixLogger, username v1beta1.Username, client *client.Podman, startTime time.Time) {
	e.log = log
	e.username = username
	e.client = client
	e.listenerCloseChan = make(chan struct{})
	e.lastEventTime.Store(&startTime)
}

func (e *podmanEventWatcher) Watch(ctx context.Context, events chan<- client.PodmanEvent) error {
	// list of podman events to listen for
	eventsTypes := []string{"create", "init", "start", "stop", "die", "sync", "remove", "exited"}
	e.log.Debugf("Replaying podman events for user %s since: %s", e.username, e.lastEventTime.Load())

	ctx, e.cancel = context.WithCancel(ctx)

	cmd := e.client.EventsSinceCmd(ctx, eventsTypes, e.lastEventTime.Load().Format(time.RFC3339))

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("failed to get stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start podman events: %w", err)
	}

	e.cmd = cmd
	e.listenerCloseChan = make(chan struct{})

	go func() {
		defer close(e.listenerCloseChan)
		e.listenForEvents(ctx, stdoutPipe, events)
	}()

	return nil
}

func (e *podmanEventWatcher) listenForEvents(ctx context.Context, stdoutPipe io.ReadCloser, events chan<- client.PodmanEvent) {
	// the pipe will be closed by calling cmd.Wait
	defer e.log.Debugf("Done listening for podman events")

	scanner := bufio.NewScanner(stdoutPipe)
	for scanner.Scan() {
		e.log.Debugf("Received podman event: %s", scanner.Text())
		select {
		case <-ctx.Done():
			return
		default:
			e.handleEvent(ctx, scanner.Bytes(), events)
		}
	}
	// scanner won't emit an io.EOF error if it encounters one
	if err := scanner.Err(); err != nil {
		e.log.Errorf("Error reading podman events: %v", err)
	}
}

func (e *podmanEventWatcher) handleEvent(ctx context.Context, data []byte, events chan<- client.PodmanEvent) {
	var event client.PodmanEvent
	if err := json.Unmarshal(data, &event); err != nil {
		e.log.Errorf("Failed to decode podman event: %s %v", string(data), err)
		return
	}
	eventTime := time.Unix(0, event.TimeNano)
	e.lastEventTime.Store(&eventTime)
	e.log.Tracef("Received podman event: %q at %s", event.Type, eventTime)
	// sync event means we are now in sync with the current state of the containers
	if event.Type == "sync" {
		e.log.Debugf("Received bootSync event : %v", event)
		return
	}

	select {
	case <-ctx.Done():
		return
	case events <- event:
		return
	}
}

func (e *podmanEventWatcher) Stop() error {
	if e.cmd == nil {
		return nil
	}

	// When a monitor is started, a separate goroutine is created to consume events from a stream
	// To prevent unexpected errors from occurring in the reading routine, we attempt to let it
	// gracefully exit before calling .Wait and cleaning up the commands resources
	// see https://pkg.go.dev/os/exec#Cmd.StdoutPipe
	e.log.Info("Stopping podman monitor")

	// send a graceful shutdown signal to the command
	if err := e.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		e.log.Warnf("Failed to send SIGTERM to podman monitor: %v", err)
	}

	if e.cancel != nil {
		e.cancel()
	}

	// wait for our consuming goroutine to complete
	if !waitForChannelWithTimeout(e.listenerCloseChan, stopMonitorWaitDuration) {
		e.log.Warn("Timeout waiting for podman monitor reader to shutdown")
		if err := e.cmd.Process.Signal(syscall.SIGKILL); err != nil {
			e.log.Warnf("Failed to send SIGKILL to podman monitor: %v", err)
		}
		if !waitForChannelWithTimeout(e.listenerCloseChan, stopMonitorWaitDuration) {
			e.log.Error("Timeout waiting for podman monitor reader to shutdown after force killed")
		}
	}

	// wait for the command to exit.
	if err := e.cmd.Wait(); err != nil && !isExpectedShutdownError(err) {
		return fmt.Errorf("unexpected error during podman monitor shutdown: %w", err)
	}
	e.log.Info("Podman monitor stopped")
	return nil
}
