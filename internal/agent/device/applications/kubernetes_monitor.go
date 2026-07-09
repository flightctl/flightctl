package applications

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"time"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/client"
	appconsole "github.com/flightctl/flightctl/internal/agent/device/applications/console"
	"github.com/flightctl/flightctl/internal/agent/device/applications/helm"
	"github.com/flightctl/flightctl/internal/agent/device/applications/lifecycle"
	"github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/pkg/log"
)

const (
	// pod phases
	podPhasePending   = "Pending"
	podPhaseRunning   = "Running"
	podPhaseSucceeded = "Succeeded"
	podPhaseFailed    = "Failed"

	// container waiting reasons
	containerReasonCrashLoopBackOff         = "CrashLoopBackOff"
	containerReasonImagePullBackOff         = "ImagePullBackOff"
	containerReasonCreateContainerConfigErr = "CreateContainerConfigError"
	containerReasonCreateContainerErr       = "CreateContainerError"
	containerReasonInvalidImageName         = "InvalidImageName"

	// container terminated reasons
	containerReasonOOMKilled = "OOMKilled"
)

// KubernetesMonitor monitors Kubernetes-based applications such as Helm releases.
type KubernetesMonitor struct {
	*monitor

	clients   client.CLIClients
	rwFactory fileio.ReadWriterFactory
}

// NewKubernetesMonitor creates a new KubernetesMonitor with lazy initialization.
// Kubernetes availability is checked at operation time via k8sClient.Resolve().
func NewKubernetesMonitor(
	log *log.PrefixLogger,
	clients client.CLIClients,
	rwFactory fileio.ReadWriterFactory,
) *KubernetesMonitor {
	m := newMonitor(log, "kubernetes",
		handlerRegistration{
			appType: v1beta1.AppTypeHelm,
			handler: lifecycle.NewHelmHandler(log, clients, lifecycle.OSExecutableResolver{}, rwFactory),
		},
	)

	return &KubernetesMonitor{
		monitor:   m,
		clients:   clients,
		rwFactory: rwFactory,
	}
}

// Ensure adds an application to be monitored.
func (m *KubernetesMonitor) Ensure(app Application) error {
	return m.monitor.Ensure(app)
}

// Remove removes an application from monitoring.
func (m *KubernetesMonitor) Remove(app Application) error {
	return m.monitor.Remove(app)
}

// Update updates an existing application, preserving workloads from the old app.
func (m *KubernetesMonitor) Update(app Application) error {
	return m.monitor.updateWithWorkloads(app)
}

// CreateCommand implements streamingMonitor interface.
func (m *KubernetesMonitor) CreateCommand(ctx context.Context) (*exec.Cmd, error) {
	kubeconfigPath, err := m.clients.Kube().ResolveKubeconfig()
	if err != nil {
		return nil, fmt.Errorf("resolving kubeconfig: %w", err)
	}
	return m.clients.Kube().WatchPodsCmd(ctx,
		client.WithKubeKubeconfig(kubeconfigPath),
		client.WithKubeLabels([]string{helm.AppLabelKey}),
	)
}

// Parser implements streamingMonitor interface.
func (m *KubernetesMonitor) Parser() streamParser {
	return jsonStreamParser{}
}

// HandleEvent implements streamingMonitor interface.
func (m *KubernetesMonitor) HandleEvent(ctx context.Context, data []byte) {
	m.handlePodEvent(ctx, data)
}

// OnRestart implements streamingMonitor interface.
func (m *KubernetesMonitor) OnRestart() {
	m.clearAllWorkloads()
}

func (m *KubernetesMonitor) startMonitor(ctx context.Context) error {
	return m.startStreaming(ctx, m)
}

func (m *KubernetesMonitor) stopMonitor() error {
	return m.stopStreaming()
}

// Stop stops the Kubernetes monitor.
func (m *KubernetesMonitor) Stop() error {
	return m.stopMonitor()
}

// Drain stops and removes all applications.
func (m *KubernetesMonitor) Drain(ctx context.Context) error {
	m.drainActions()

	if err := m.drain(ctx); err != nil {
		return err
	}

	return m.ExecuteActions(ctx)
}

// QueueLifecycle compares the new spec's lifecycle intent against the stored intent on
// the tracked application and queues ActionStop, ActionStart, or ActionRestart as needed.
// The stored desiredState/restartGeneration are only advanced by StopApp/StartApp/RestartApp
// once the corresponding action succeeds, so a failed action is retried on the next sync.
// No-op if the app is not tracked or if intent is unchanged.
func (m *KubernetesMonitor) QueueLifecycle(appID string, desiredState v1beta1.ApplicationDesiredState, restartGeneration int) {
	m.mu.Lock()
	defer m.mu.Unlock()

	app, ok := m.apps[appID]
	if !ok {
		return
	}

	currentState := app.DesiredState()
	currentGen := app.RestartGeneration()

	var actionType lifecycle.ActionType
	switch {
	case restartGeneration > currentGen && desiredState == v1beta1.ApplicationDesiredStateRunning:
		actionType = lifecycle.ActionRestart
	case desiredState != currentState:
		if desiredState == v1beta1.ApplicationDesiredStateStopped {
			actionType = lifecycle.ActionStop
		} else {
			actionType = lifecycle.ActionStart
		}
	default:
		return
	}

	m.actions = append(m.actions, lifecycle.Action{
		ID:                appID,
		Name:              app.Name(),
		AppType:           app.AppType(),
		Path:              app.Path(),
		Type:              actionType,
		RestartGeneration: restartGeneration,
	})
}

// StopApp stops the Helm application identified by appID by scaling its workloads to 0.
func (m *KubernetesMonitor) StopApp(ctx context.Context, appID string) error {
	handler, action, app, err := m.lifecycleDispatch(appID)
	if err != nil {
		return err
	}
	if err := handler.Stop(ctx, action); err != nil {
		return err
	}
	m.mu.Lock()
	app.SetDesiredState(v1beta1.ApplicationDesiredStateStopped)
	m.mu.Unlock()
	return nil
}

// StartApp re-applies the Helm chart for the application identified by appID.
func (m *KubernetesMonitor) StartApp(ctx context.Context, appID string) error {
	handler, action, app, err := m.lifecycleDispatch(appID)
	if err != nil {
		return err
	}
	if err := handler.Start(ctx, action); err != nil {
		return err
	}
	m.mu.Lock()
	app.SetDesiredState(v1beta1.ApplicationDesiredStateRunning)
	m.mu.Unlock()
	return nil
}

// RestartApp scales workloads to 0 then re-applies the chart for the application identified by appID.
func (m *KubernetesMonitor) RestartApp(ctx context.Context, appID string, restartGeneration int) error {
	handler, action, app, err := m.lifecycleDispatch(appID)
	if err != nil {
		return err
	}
	if err := handler.Restart(ctx, action); err != nil {
		return err
	}
	m.mu.Lock()
	app.SetRestartGeneration(restartGeneration)
	m.mu.Unlock()
	return nil
}

// lifecycleDispatch looks up the application and resolves its LifecycleHandler.
func (m *KubernetesMonitor) lifecycleDispatch(appID string) (lifecycle.LifecycleHandler, lifecycle.Action, Application, error) {
	m.mu.Lock()
	app, ok := m.apps[appID]
	m.mu.Unlock()

	if !ok {
		return nil, lifecycle.Action{}, nil, fmt.Errorf("%w: %s", errors.ErrAppNotFound, appID)
	}

	handler, ok := m.handlers[app.AppType()].(lifecycle.LifecycleHandler)
	if !ok {
		return nil, lifecycle.Action{}, nil, fmt.Errorf("%w: no lifecycle handler for app type %s", errors.ErrUnsupportedAppType, app.AppType())
	}

	action := lifecycle.Action{
		ID:      app.ID(),
		Name:    app.Name(),
		AppType: app.AppType(),
		Path:    app.Path(),
	}
	return handler, action, app, nil
}

// ExecuteActions executes all queued actions.
func (m *KubernetesMonitor) ExecuteActions(ctx context.Context) error {
	return m.executeActions(ctx)
}

func (m *KubernetesMonitor) executeActions(ctx context.Context) error {
	actions := m.drainActions()

	// Group structural actions (Add/Update/Remove) by app type for batch execution.
	// Lifecycle actions (Stop/Start/Restart) are dispatched directly below.
	groupedActions := make(map[v1beta1.AppType][]lifecycle.Action)
	for i := range actions {
		action := actions[i]
		if action.Type == lifecycle.ActionStop || action.Type == lifecycle.ActionStart || action.Type == lifecycle.ActionRestart {
			continue
		}
		if _, ok := m.handlers[action.AppType]; !ok {
			return fmt.Errorf("%w: no action handler registered: %s", errors.ErrUnsupportedAppType, action.AppType)
		}
		groupedActions[action.AppType] = append(groupedActions[action.AppType], action)
	}

	for appType, typeActions := range groupedActions {
		if err := m.handlers[appType].Execute(ctx, typeActions); err != nil {
			return fmt.Errorf("execute kubernetes actions: %w", err)
		}
	}

	// After installing or updating an app, apply desiredState=stopped if the operator
	// wants it off. The install/update handler always starts workloads; stop them here.
	for _, a := range actions {
		if a.Type != lifecycle.ActionAdd && a.Type != lifecycle.ActionUpdate {
			continue
		}
		m.mu.Lock()
		app, ok := m.apps[a.ID]
		m.mu.Unlock()
		if !ok || app.DesiredState() != v1beta1.ApplicationDesiredStateStopped {
			continue
		}
		m.log.Infof("Stopping application %s (desiredState: stopped, post-install)", a.Name)
		if err := m.StopApp(ctx, a.ID); err != nil {
			m.log.Warnf("Failed to stop application %s after install: %v", a.Name, err)
		}
	}

	// Dispatch explicit lifecycle actions queued by QueueLifecycle.
	for _, a := range actions {
		switch a.Type {
		case lifecycle.ActionStop:
			m.log.Infof("Stopping application %s", a.Name)
			if err := m.StopApp(ctx, a.ID); err != nil {
				m.log.Warnf("Failed to stop application %s: %v", a.Name, err)
			}
		case lifecycle.ActionStart:
			m.log.Infof("Starting application %s", a.Name)
			if err := m.StartApp(ctx, a.ID); err != nil {
				m.log.Warnf("Failed to start application %s: %v", a.Name, err)
			}
		case lifecycle.ActionRestart:
			m.log.Infof("Restarting application %s", a.Name)
			if err := m.RestartApp(ctx, a.ID, a.RestartGeneration); err != nil {
				m.log.Warnf("Failed to restart application %s: %v", a.Name, err)
			}
		}
	}

	if m.hasApps() {
		if err := m.startMonitor(ctx); err != nil {
			return fmt.Errorf("failed to start kubernetes monitor: %w", err)
		}
	} else {
		if err := m.stopMonitor(); err != nil {
			return fmt.Errorf("failed to stop kubernetes monitor: %w", err)
		}
	}

	return nil
}

type kubernetesWatchEvent struct {
	Type   string        `json:"type"`
	Object kubernetesPod `json:"object"`
}

type kubernetesPod struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Metadata   struct {
		Name      string            `json:"name"`
		Namespace string            `json:"namespace"`
		UID       string            `json:"uid"`
		Labels    map[string]string `json:"labels,omitempty"`
	} `json:"metadata"`
	Status kubernetesPodStatus `json:"status"`
}

type kubernetesPodStatus struct {
	Phase             string                      `json:"phase"`
	ContainerStatuses []kubernetesContainerStatus `json:"containerStatuses,omitempty"`
}

type kubernetesContainerStatus struct {
	Name         string                       `json:"name"`
	Ready        bool                         `json:"ready"`
	RestartCount int                          `json:"restartCount"`
	State        kubernetesContainerStateInfo `json:"state,omitempty"`
}

type kubernetesContainerStateInfo struct {
	Waiting    *kubernetesContainerStateWaiting    `json:"waiting,omitempty"`
	Running    *kubernetesContainerStateRunning    `json:"running,omitempty"`
	Terminated *kubernetesContainerStateTerminated `json:"terminated,omitempty"`
}

type kubernetesContainerStateWaiting struct {
	Reason  string `json:"reason,omitempty"`
	Message string `json:"message,omitempty"`
}

type kubernetesContainerStateRunning struct {
	StartedAt time.Time `json:"startedAt,omitempty"`
}

type kubernetesContainerStateTerminated struct {
	ExitCode   int       `json:"exitCode"`
	Reason     string    `json:"reason,omitempty"`
	FinishedAt time.Time `json:"finishedAt,omitempty"`
}

func (m *KubernetesMonitor) handlePodEvent(_ context.Context, data []byte) {
	var event kubernetesWatchEvent
	if err := json.Unmarshal(data, &event); err != nil {
		m.log.Errorf("Failed to decode kubernetes watch event: %s %v", string(data), err)
		return
	}

	pod := &event.Object
	if pod.Kind != "Pod" {
		m.log.Debugf("Ignoring non-pod event: %s", pod.Kind)
		return
	}

	releaseID, ok := pod.Metadata.Labels[helm.AppLabelKey]
	if !ok {
		m.log.Debugf("Pod %s missing %s label, skipping", pod.Metadata.Name, helm.AppLabelKey)
		return
	}

	m.mu.Lock()
	app, ok := m.apps[releaseID]
	m.mu.Unlock()
	if !ok {
		m.log.Debugf("Application not found for release: %s", releaseID)
		return
	}

	switch event.Type {
	case "DELETED":
		m.removeWorkload(app, pod)
	case "ADDED", "MODIFIED":
		m.updatePodStatus(app, pod)
	default:
		m.log.Warnf("Unknown watch event type: %s", event.Type)
	}
}

func (m *KubernetesMonitor) removeWorkload(app Application, pod *kubernetesPod) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if app.RemoveWorkload(pod.Metadata.Name) {
		m.log.Debugf("Removed deleted pod: %s from app %s", pod.Metadata.Name, app.Name())
	}
}

func (m *KubernetesMonitor) updatePodStatus(app Application, pod *kubernetesPod) {
	status := m.mapPodPhaseToStatus(pod)
	restarts := m.getPodRestartCount(pod)

	m.mu.Lock()
	defer m.mu.Unlock()

	workload, exists := app.Workload(pod.Metadata.Name)
	if exists {
		workload.Status = status
		if pod.Metadata.UID != workload.ID {
			workload.ID = pod.Metadata.UID
			workload.Restarts = restarts
		} else if restarts > workload.Restarts {
			workload.Restarts = restarts
		}
		return
	}

	m.log.Debugf("Adding pod: %s to app %s", pod.Metadata.Name, app.Name())
	app.AddWorkload(&Workload{
		ID:       pod.Metadata.UID,
		Name:     pod.Metadata.Name,
		Status:   status,
		Restarts: restarts,
	})
}

func (m *KubernetesMonitor) mapPodPhaseToStatus(pod *kubernetesPod) StatusType {
	switch pod.Status.Phase {
	case podPhasePending:
		return StatusInit
	case podPhaseRunning:
		if m.hasContainerErrors(pod) {
			return StatusDied
		}
		if m.allContainersReady(pod) {
			return StatusRunning
		}
		return StatusInit
	case podPhaseSucceeded:
		return StatusExited
	case podPhaseFailed:
		return StatusDied
	default:
		return StatusInit
	}
}

func (m *KubernetesMonitor) allContainersReady(pod *kubernetesPod) bool {
	if len(pod.Status.ContainerStatuses) == 0 {
		return false
	}
	for _, cs := range pod.Status.ContainerStatuses {
		if !cs.Ready {
			return false
		}
	}
	return true
}

func (m *KubernetesMonitor) hasContainerErrors(pod *kubernetesPod) bool {
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.State.Waiting != nil {
			switch cs.State.Waiting.Reason {
			case containerReasonCrashLoopBackOff,
				containerReasonImagePullBackOff,
				containerReasonCreateContainerConfigErr,
				containerReasonCreateContainerErr,
				containerReasonInvalidImageName:
				return true
			}
		}
		if cs.State.Terminated != nil && cs.State.Terminated.Reason == containerReasonOOMKilled {
			return true
		}
	}
	return false
}

func (m *KubernetesMonitor) getPodRestartCount(pod *kubernetesPod) int {
	var total int
	for _, cs := range pod.Status.ContainerStatuses {
		total += cs.RestartCount
	}
	return total
}

// resolveConsole is a stub — Kubernetes app console is not yet implemented.
// Returns errConsoleAppNotFound so the caller tries the next monitor.
func (m *KubernetesMonitor) resolveConsole(_, _ string) (appconsole.Session, error) {
	return nil, errConsoleAppNotFound
}
