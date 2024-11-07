package applications

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/applications/lifecycle"
	"github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
)

// PodmanInspect represents the overall structure of podman inspect output
type PodmanInspect struct {
	Restarts int                   `json:"RestartCount"`
	State    PodmanContainerState  `json:"State"`
	Config   PodmanContainerConfig `json:"Config"`
}

// ContainerState represents the container state part of the podman inspect output
type PodmanContainerState struct {
	OciVersion  string `json:"OciVersion"`
	Status      string `json:"Status"`
	Running     bool   `json:"Running"`
	Paused      bool   `json:"Paused"`
	Restarting  bool   `json:"Restarting"`
	OOMKilled   bool   `json:"OOMKilled"`
	Dead        bool   `json:"Dead"`
	Pid         int    `json:"Pid"`
	ExitCode    int    `json:"ExitCode"`
	Error       string `json:"Error"`
	StartedAt   string `json:"StartedAt"`
	FinishedAt  string `json:"FinishedAt"`
	Healthcheck string `json:"Healthcheck"`
}

type PodmanContainerConfig struct {
	Labels map[string]string `json:"Labels"`
}

type PodmanEvent struct {
	ID         string            `json:"ID"`
	Image      string            `json:"Image"`
	Name       string            `json:"Name"`
	Status     string            `json:"Status"`
	Type       string            `json:"Type"`
	Attributes map[string]string `json:"Attributes"`
}

type PodmanMonitor struct {
	mu          sync.Mutex
	cmd         *exec.Cmd
	once        sync.Once
	cancelFn    context.CancelFunc
	initialized bool

	apps    map[string]Application
	actions []lifecycle.Action

	compose lifecycle.ActionHandler
	client  *client.Podman
	boot    *client.Boot

	log *log.PrefixLogger
}

func NewPodmanMonitor(log *log.PrefixLogger, exec executer.Executer, podman *client.Podman) *PodmanMonitor {
	return &PodmanMonitor{
		client:  podman,
		boot:    client.NewBoot(exec),
		compose: lifecycle.NewCompose(log, podman),
		apps:    make(map[string]Application),
		log:     log,
	}
}

func (m *PodmanMonitor) Run(ctx context.Context) error {
	m.log.Debugf("Starting podman monitor")
	ctx, m.cancelFn = context.WithCancel(ctx)

	// get boot time
	bootTime, err := m.boot.Time(ctx)
	if err != nil {
		return fmt.Errorf("failed to get boot time: %w", err)
	}
	m.log.Debugf("Boot time: %s", bootTime)

	// list of podman events to listen for
	events := []string{"init", "start", "die", "sync", "remove", "exited"}
	m.cmd = m.client.EventsSinceCmd(ctx, events, bootTime)

	stdoutPipe, err := m.cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("failed to get stdout pipe: %w", err)
	}

	if err := m.cmd.Start(); err != nil {
		return fmt.Errorf("failed to start podman events: %w", err)
	}

	go m.listenForEvents(ctx, stdoutPipe)

	return nil
}

func (m *PodmanMonitor) Stop(ctx context.Context) error {
	var errs []error
	m.once.Do(func() {
		m.log.Info("Stopping podman monitor")
		if err := m.drain(ctx); err != nil {
			errs = append(errs, err)
		}
		m.log.Infof("podman drain complete")
		m.cancelFn()

		// its possible that we call stop before the monitor has been
		// initialized
		if m.cmd != nil {
			if err := m.cmd.Wait(); err != nil {
				errs = append(errs, fmt.Errorf("failed to wait for podman events: %v", err))
			}
		}
		m.log.Info("Podman monitor stopped!!")
	})

	if len(errs) > 0 {
		return errors.Join(errs...)
	}

	return nil
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
		if err := m.remove(app); err != nil {
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

// ensures that and application is added to the monitor. if the application
// is added for the first time an Add action is queued to be executed by the
// lifecycle manager. so additional adds for the same app will be idempotent.
func (m *PodmanMonitor) ensure(app Application) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	appName := app.Name()
	_, ok := m.apps[appName]
	if ok {
		// app already exists
		return nil
	}
	m.apps[appName] = app

	handler, err := app.Type().ActionHandler()
	if err != nil {
		return err
	}

	action := lifecycle.Action{
		Handler:  handler,
		Type:     lifecycle.ActionAdd,
		Name:     appName,
		Embedded: app.IsEmbedded(),
	}

	m.actions = append(m.actions, action)
	return nil
}

func (m *PodmanMonitor) remove(app Application) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	appName := app.Name()
	if _, ok := m.apps[appName]; !ok {
		// app is already removed
		return nil
	}

	handler, err := app.Type().ActionHandler()
	if err != nil {
		return err
	}

	delete(m.apps, appName)

	// currently we don't support removing embedded applications
	action := lifecycle.Action{
		Handler: handler,
		Type:    lifecycle.ActionRemove,
		Name:    appName,
	}

	m.actions = append(m.actions, action)
	return nil
}

func (m *PodmanMonitor) update(app Application) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	appName := app.Name()
	existingApp, ok := m.apps[appName]
	if !ok {
		return errors.ErrAppNotFound
	}

	if updated := existingApp.SetEnvVars(app.EnvVars()); updated {
		m.log.Debugf("Updated environment variables for application: %s", appName)
	}

	handler, err := existingApp.Type().ActionHandler()
	if err != nil {
		return err
	}

	// currently we don't support updating embedded applications
	action := lifecycle.Action{
		Handler: handler,
		Type:    lifecycle.ActionUpdate,
		Name:    appName,
	}

	m.actions = append(m.actions, action)
	return nil
}

func (m *PodmanMonitor) get(name string) (Application, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	app, ok := m.apps[name]
	if ok {
		return app, true
	}

	return nil, false
}

func (m *PodmanMonitor) Initialize() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.initialized = true
}

func (m *PodmanMonitor) IsInitialized() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.initialized
}

func (m *PodmanMonitor) ExecuteActions(ctx context.Context) error {
	actions := m.drainActions()
	for i := range actions {
		action := actions[i]
		if action.Handler == lifecycle.ActionHandlerCompose {
			if err := m.compose.Execute(ctx, &action); err != nil {
				// this error should result in a failed status for the revision
				// and not retried.
				return err
			}
		}
	}

	// if this is the first time we are executing actions we need to ensure
	// the monitor is initialized before we return.
	if !m.IsInitialized() {
		if err := m.Run(ctx); err != nil {
			return err
		}
		m.Initialize()
	}

	return nil
}

// drainActions returns a copy of the current actions and clears the existing. this
// ensures actions can only be executed once and on failure the remaining
// actions will not be executed.
func (m *PodmanMonitor) drainActions() []lifecycle.Action {
	m.mu.Lock()
	defer m.mu.Unlock()
	actions := m.actions
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
				summary.Info = util.StrToPtr("Not started: " + strings.Join(unstarted, ", "))
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
	defer func() {
		m.log.Info("Podman application monitor stopped")
		stdoutPipe.Close()
	}()

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

	if err := scanner.Err(); err != nil && !errors.Is(err, io.EOF) {
		m.log.Errorf("Error reading podman events: %v", err)
	}
}

func (m *PodmanMonitor) handleEvent(ctx context.Context, data []byte) {
	var event PodmanEvent
	if err := json.Unmarshal(data, &event); err != nil {
		m.log.Errorf("Failed to decode podman event: %v", err)
		return
	}

	// sync event means we are now in sync with the current state of the containers
	if event.Type == "sync" {
		m.log.Debugf("Received bootSync event : %v", event)
		return
	}

	appName, ok := event.Attributes["com.docker.compose.project"]
	if !ok {
		m.log.Debugf("Application name not found in event attributes: %v", event)
		return
	}

	app, ok := m.get(appName)
	if !ok {
		m.log.Debugf("Application not found: %s", appName)
		return
	}
	m.updateAppStatus(ctx, app, &event)
}

func (m *PodmanMonitor) updateAppStatus(ctx context.Context, app Application, event *PodmanEvent) {
	m.mu.Lock()
	defer m.mu.Unlock()

	inspectData, err := m.inspectContainer(ctx, event.ID)
	if err != nil {
		m.log.Errorf("Failed to inspect container: %v", err)
	}

	status := m.resolveStatus(event.Status, inspectData)
	if status == ContainerStatusRemove {
		// remove existing container
		if removed := app.RemoveContainer(event.Name); removed {
			m.log.Debugf("Removed container: %s", event.Name)
		}
		return
	}

	restarts, err := m.getContainerRestarts(inspectData)
	if err != nil {
		m.log.Errorf("Failed to get container restarts: %v", err)
	}

	container, exists := app.Container(event.Name)
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
	app.AddContainer(Container{
		ID:       event.ID,
		Image:    event.Image,
		Name:     event.Name,
		Status:   status,
		Restarts: restarts,
	})
}

func (m *PodmanMonitor) getContainerRestarts(inspectData []PodmanInspect) (int, error) {
	var restarts int
	if len(inspectData) > 0 {
		restarts = inspectData[0].Restarts
	}

	return restarts, nil
}

func (m *PodmanMonitor) inspectContainer(ctx context.Context, containerID string) ([]PodmanInspect, error) {
	resp, err := m.client.Inspect(ctx, containerID)
	if err != nil {
		return nil, err
	}

	var inspectData []PodmanInspect
	if err := json.Unmarshal([]byte(resp), &inspectData); err != nil {
		return nil, fmt.Errorf("unmarshal podman inspect output: %v", err)
	}
	return inspectData, nil
}

func (m *PodmanMonitor) resolveStatus(status string, inspectData []PodmanInspect) ContainerStatusType {
	initialStatus := ContainerStatusType(status)
	// podman events don't properly event exited in the case where the container exits 0.
	if initialStatus == ContainerStatusDie || initialStatus == ContainerStatusDied {
		if len(inspectData) > 0 && inspectData[0].State.ExitCode == 0 && inspectData[0].State.FinishedAt != "" {
			return ContainerStatusExited
		}
	}
	return initialStatus
}
