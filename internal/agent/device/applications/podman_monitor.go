package applications

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sync"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/applications/lifecycle"
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
	Time       int64             `json:"time"`
	TimeNano   int64             `json:"timeNano"`
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

	m.cmd = m.client.EventsSinceCmd(ctx, []string{"init", "start", "die", "sync"}, bootTime)

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

func (m *PodmanMonitor) Stop() {
	m.once.Do(func() {
		m.cancelFn()
		if err := m.cmd.Wait(); err != nil {
			m.log.Errorf("Failed to wait for podman events: %v", err)
		}
	})
}

// add ensures that and application is added to the monitor. if the application
// is added for the first time an Add action is queued to be executed by the
// lifecycle manager. so additional adds for the same app will be idempotent.
func (m *PodmanMonitor) add(app Application) error {
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
		Handler: handler,
		Type:    lifecycle.ActionAdd,
		Name:    appName,
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
		return ErrNotFound
	}

	if updated := existingApp.SetEnvVars(app.EnvVars()); updated {
		m.log.Debugf("Updated environment variables for application: %s", appName)
	}

	handler, err := existingApp.Type().ActionHandler()
	if err != nil {
		return err
	}

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

func (m *PodmanMonitor) Status() ([]v1alpha1.DeviceApplicationStatus, v1alpha1.ApplicationsSummaryStatusType, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	var errs []error
	var summary v1alpha1.ApplicationsSummaryStatusType
	statuses := make([]v1alpha1.DeviceApplicationStatus, 0, len(m.apps))
	for _, app := range m.apps {
		appStatus, appSummary, err := app.Status()
		if err != nil {
			errs = append(errs, err)
			continue
		}
		statuses = append(statuses, *appStatus)

		// phases can get worse but not better
		switch appSummary {
		case v1alpha1.ApplicationsSummaryStatusError:
			summary = v1alpha1.ApplicationsSummaryStatusError
		case v1alpha1.ApplicationsSummaryStatusDegraded:
			// ensure we don't override Error status with Degraded
			if summary != v1alpha1.ApplicationsSummaryStatusError {
				summary = v1alpha1.ApplicationsSummaryStatusDegraded
			}
		case v1alpha1.ApplicationsSummaryStatusHealthy:
			// ensure we don't override Error or Degraded status with Healthy
			if summary != v1alpha1.ApplicationsSummaryStatusError && summary != v1alpha1.ApplicationsSummaryStatusDegraded {
				summary = v1alpha1.ApplicationsSummaryStatusHealthy
			}
		default:
			errs = append(errs, fmt.Errorf("unknown application summary status: %s", appSummary))
		}
	}

	if len(statuses) == 0 {
		summary = v1alpha1.ApplicationsSummaryStatusUnknown
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

	m.log.Debugf("Updating application status for event: %v", event)
	resp, err := m.client.Inspect(ctx, event.ID)
	if err != nil {
		m.log.Errorf("Failed to inspect container %s: %v", event.ID, err)
		return
	}

	var inspectData []PodmanInspect
	if err := json.Unmarshal([]byte(resp), &inspectData); err != nil {
		m.log.Errorf("Failed to unmarshal podman inspect output: %v", err)
		return
	}

	var restarts int
	if len(inspectData) > 0 {
		restarts = inspectData[0].Restarts
	}

	container, exists := app.Container(event.Name)
	if exists {
		container.Status = ContainerStatusType(event.Status)
		container.Restarts = restarts
		return
	}

	// add new container
	app.AddContainer(Container{
		ID:       event.ID,
		Image:    event.Image,
		Name:     event.Name,
		Status:   ContainerStatusType(event.Status),
		Restarts: restarts,
	})
}
