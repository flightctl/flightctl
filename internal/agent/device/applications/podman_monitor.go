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
	"github.com/flightctl/flightctl/pkg/log"
)

const (
	//TODO: enum
	podmanEventRunningName = "start"
	podmanEventInitName    = "init"
	podmanEventDieName     = "die"
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
	mu       sync.Mutex
	cmd      *exec.Cmd
	once     sync.Once
	cancelFn context.CancelFunc

	apps    map[string]Application
	actions []lifecycle.Action

	compose lifecycle.ActionHandler
	client  *client.Podman

	log *log.PrefixLogger
}

func NewPodmanMonitor(log *log.PrefixLogger, podman *client.Podman) *PodmanMonitor {
	return &PodmanMonitor{
		client:  podman,
		compose: lifecycle.NewCompose(log, podman),
		apps:    make(map[string]Application),
		log:     log,
	}
}

func (m *PodmanMonitor) Run(ctx context.Context) error {
	m.log.Debugf("Starting podman monitor")
	ctx, m.cancelFn = context.WithCancel(ctx)
	m.cmd = m.client.EventsCmd(ctx, []string{"init", "start", "die"})

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

	return nil
}

// drainActions returns the current actions and clears the existing. this ensures actions can onl be executed once and on failure the remaining actions will not be executed.
func (m *PodmanMonitor) drainActions() []lifecycle.Action {
	m.mu.Lock()
	defer m.mu.Unlock()
	actions := m.actions
	// reset actions to ensure we don't execute the same actions again
	m.actions = nil
	return actions
}

func (m *PodmanMonitor) Status() ([]v1alpha1.ApplicationStatus, v1alpha1.ApplicationsSummaryStatusType, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	var errs []error
	var summary v1alpha1.ApplicationsSummaryStatusType
	statuses := make([]v1alpha1.ApplicationStatus, 0, len(m.apps))
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
			summary = v1alpha1.ApplicationsSummaryStatusUnknown
		}
	}

	m.log.Debugf("Applications podman summary status: %s", summary)

	return statuses, summary, nil
}

func (m *PodmanMonitor) listenForEvents(ctx context.Context, stdoutPipe io.ReadCloser) {
	defer func() {
		m.log.Info("Podman monitor stopped")
		stdoutPipe.Close()
	}()

	scanner := bufio.NewScanner(stdoutPipe)
	for scanner.Scan() {
		m.log.Debugf("Received podman event: %s", scanner.Text())
		select {
		case <-ctx.Done():
			m.log.Debugf("Podman monitor stopped")
			return
		default:
			// parse podman event
			var event PodmanEvent
			if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
				m.log.Errorf("Failed to decode podman event: %v", err)
				continue
			}
			m.updateAppStatus(ctx, &event)
		}
	}

	if err := scanner.Err(); err != nil && !errors.Is(err, io.EOF) {
		m.log.Errorf("Error reading podman events: %v", err)
	}
}

func (m *PodmanMonitor) updateAppStatus(ctx context.Context, event *PodmanEvent) {
	m.log.Debugf("Updating application status for event: %v", event)
	// extract the working directory from the event attributes which is the key for the map
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

	// get restart count from client best effort
	resp, err := m.client.Inspect(ctx, event.ID)
	if err != nil {
		m.log.Errorf("Failed to inspect container %s: %v", event.ID, err)
		return
	}

	// inspect output is best effort
	var inspectData []PodmanInspect
	if err := json.Unmarshal([]byte(resp), &inspectData); err != nil {
		m.log.Errorf("Failed to unmarshal podman inspect output: %v", err)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	containers := app.Containers()
	container, ok := containers[event.Name]
	if !ok {
		c := &Container{
			ID:     event.ID,
			Image:  event.Image,
			Name:   event.Name,
			Status: event.Status,
		}
		if len(inspectData) > 0 {
			c.Restarts = inspectData[0].Restarts
		}
		containers[event.Name] = c
		return
	}

	if container.Status != event.Status {
		container.Status = event.Status
	}

	if len(inspectData) > 0 && container.Restarts != inspectData[0].Restarts {
		container.Restarts = inspectData[0].Restarts
	}
}
