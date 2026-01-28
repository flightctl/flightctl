package applications

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"time"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/client"
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

	k8sClient      *client.Kube
	kubeconfigPath string
	enabled        bool
}

// NewKubernetesMonitor creates a new KubernetesMonitor.
func NewKubernetesMonitor(
	log *log.PrefixLogger,
	clients client.CLIClients,
	rwFactory fileio.ReadWriterFactory,
) *KubernetesMonitor {
	k8sClient := clients.Kube()
	if k8sClient == nil {
		log.Info("Kubernetes based applications disabled: kubernetes client not available")
		return &KubernetesMonitor{
			monitor: newMonitor(log, "kubernetes"),
			enabled: false,
		}
	}

	kubeconfigPath, err := k8sClient.ResolveKubeconfig()
	if err != nil {
		log.Infof("Kubernetes based applications disabled: %v", err)
		return &KubernetesMonitor{
			monitor: newMonitor(log, "kubernetes"),
			enabled: false,
		}
	}

	m := newMonitor(log, "kubernetes",
		handlerRegistration{
			appType: v1beta1.AppTypeHelm,
			handler: lifecycle.NewHelmHandler(log, clients, kubeconfigPath, lifecycle.OSExecutableResolver{}, rwFactory),
		},
	)

	return &KubernetesMonitor{
		monitor:        m,
		k8sClient:      k8sClient,
		kubeconfigPath: kubeconfigPath,
		enabled:        true,
	}
}

// IsEnabled returns true if kubernetes-based applications are supported.
func (m *KubernetesMonitor) IsEnabled() bool {
	return m.enabled
}

// Ensure adds an application to be monitored.
func (m *KubernetesMonitor) Ensure(app Application) error {
	if !m.enabled {
		return errors.ErrKubernetesAppsDisabled
	}
	return m.monitor.Ensure(app)
}

// Remove removes an application from monitoring.
func (m *KubernetesMonitor) Remove(app Application) error {
	if !m.enabled {
		return errors.ErrKubernetesAppsDisabled
	}
	return m.monitor.Remove(app)
}

// Update updates an existing application, preserving workloads from the old app.
func (m *KubernetesMonitor) Update(app Application) error {
	if !m.enabled {
		return errors.ErrKubernetesAppsDisabled
	}
	return m.monitor.updateWithWorkloads(app)
}

// CreateCommand implements streamingMonitor interface.
func (m *KubernetesMonitor) CreateCommand(ctx context.Context) (*exec.Cmd, error) {
	return m.k8sClient.WatchPodsCmd(ctx,
		client.WithKubeKubeconfig(m.kubeconfigPath),
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

// ExecuteActions executes all queued actions.
func (m *KubernetesMonitor) ExecuteActions(ctx context.Context) error {
	if _, err := m.executeActions(ctx, nil); err != nil {
		return fmt.Errorf("execute kubernetes actions: %w", err)
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
