package status

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/pkg/executer"
)

const (
	podmanCommand          = "/usr/bin/podman"
	podmanCommandTimeout   = 10 * time.Second
	podmanContainerRunning = "running"
	crioCommand            = "/usr/bin/crictl"
	crioCommandTimeout     = 10 * time.Second
	crioContainerRunning   = "CONTAINER_RUNNING"
)

var _ Exporter = (*Container)(nil)

// Container collects container status.
type Container struct {
	exec          executer.Executer
	mu            sync.Mutex
	matchPatterns []string
}

func newContainer(exec executer.Executer) *Container {
	return &Container{
		exec: exec,
	}
}

type PodmanContainerList []PodmanContainerListEntry
type PodmanContainerListEntry struct {
	Names []string `json:"Names"`
	State string   `json:"State"`
	Image string   `json:"Image"`
	Id    string   `json:"Id"`
}

type CrioContainerList struct {
	Containers []CrioContainerListEntry `json:"containers"`
}
type CrioContainerListEntry struct {
	Id       string `json:"id"`
	Metadata struct {
		Name string `json:"name"`
	} `json:"metadata"`
	Image string `json:"imageRef"`
	State string `json:"state"`
}

type Shell interface {
	Command(cmd string) (output []byte, err error)
}

func (c *Container) PodmanExport(ctx context.Context, status *v1alpha1.DeviceStatus) error {
	podmanExecCtx, cancel := context.WithTimeout(ctx, podmanCommandTimeout)
	defer cancel()
	args := []string{"ps", "-a", "--format", "json"}
	for _, pattern := range c.matchPatterns {
		args = append(args, "--filter")
		args = append(args, fmt.Sprintf("name=%s", pattern))
	}
	podmanOut, podmanErrOut, podmanExitCode := c.exec.ExecuteWithContext(podmanExecCtx, podmanCommand, args...)
	if podmanExitCode != 0 {
		return fmt.Errorf("failed listing podman containers with code %d: %s", podmanExitCode, podmanErrOut)
	}

	var containers PodmanContainerList
	err := json.Unmarshal([]byte(podmanOut), &containers)
	if err != nil {
		return fmt.Errorf("failed unmarshalling podman containers: %s", err)
	}

	notRunning := 0
	runningCondition := v1alpha1.Condition{
		Type: v1alpha1.DeviceContainersRunning,
	}

	deviceContainerStatus := make([]v1alpha1.ContainerStatus, len(containers))
	for i, c := range containers {
		deviceContainerStatus[i].Name = c.Names[0]
		deviceContainerStatus[i].Status = c.State
		deviceContainerStatus[i].Image = c.Image
		deviceContainerStatus[i].Id = c.Id

		if c.State != podmanContainerRunning {
			notRunning++
		}
	}

	if notRunning == 0 {
		runningCondition.Status = v1alpha1.ConditionStatusTrue
		runningCondition.Reason = util.StrToPtr("Running")
	} else {
		runningCondition.Status = v1alpha1.ConditionStatusFalse
		runningCondition.Reason = util.StrToPtr("NotRunning")
		containerStr := "container"
		if notRunning > 1 {
			containerStr = "containers"
		}
		runningCondition.Message = util.StrToPtr(fmt.Sprintf("%d %s not running", notRunning, containerStr))
	}
	v1alpha1.SetStatusCondition(status.Conditions, runningCondition)
	status.Containers = &deviceContainerStatus

	return nil
}

func (c *Container) CrioExport(ctx context.Context, status *v1alpha1.DeviceStatus) error {
	crioExecCtx, cancel := context.WithTimeout(ctx, podmanCommandTimeout)
	defer cancel()
	args := []string{"ps", "-a", "--output", "json"}
	for _, pattern := range c.matchPatterns {
		args = append(args, "--name")
		args = append(args, pattern)
	}

	crioOut, crioErrOut, crioExitCode := c.exec.ExecuteWithContext(crioExecCtx, crioCommand, args...)
	if crioExitCode != 0 {
		return fmt.Errorf("failed listing crio containers with code %d: %s", crioExitCode, crioErrOut)
	}

	var containers CrioContainerList
	err := json.Unmarshal([]byte(crioOut), &containers)
	if err != nil {
		return fmt.Errorf("failed unmarshalling crio containers: %s", err)
	}

	notRunning := 0
	runningCondition := v1alpha1.Condition{
		Type: v1alpha1.DeviceContainersRunning,
	}

	deviceContainerStatus := make([]v1alpha1.ContainerStatus, len(containers.Containers))
	for i, c := range containers.Containers {
		deviceContainerStatus[i].Name = c.Metadata.Name
		deviceContainerStatus[i].Status = c.State
		deviceContainerStatus[i].Image = c.Image
		deviceContainerStatus[i].Id = c.Id

		if c.State != crioContainerRunning {
			notRunning++
		}
	}

	if notRunning == 0 {
		runningCondition.Status = v1alpha1.ConditionStatusTrue
		runningCondition.Reason = util.StrToPtr("Running")
	} else {
		runningCondition.Status = v1alpha1.ConditionStatusFalse
		runningCondition.Reason = util.StrToPtr("NotRunning")
		containerStr := "container"
		if notRunning > 1 {
			containerStr = "containers"
		}
		runningCondition.Message = util.StrToPtr(fmt.Sprintf("%d %s not running", notRunning, containerStr))
	}

	v1alpha1.SetStatusCondition(status.Conditions, runningCondition)
	if status.Containers == nil {
		status.Containers = &deviceContainerStatus
	} else {
		*status.Containers = append(*status.Containers, deviceContainerStatus...)
	}

	return nil
}

func (c *Container) Export(ctx context.Context, status *v1alpha1.DeviceStatus) error {
	if _, err := c.exec.LookPath("podman"); err == nil {
		err := c.PodmanExport(ctx, status)
		if err != nil {
			return fmt.Errorf("failed exporting podman status: %w", err)
		}
	}

	if _, err := c.exec.LookPath("crictl"); err == nil {
		err := c.CrioExport(ctx, status)
		if err != nil {
			return fmt.Errorf("failed exporting crio status: %w", err)
		}
	}

	return nil
}

func (c *Container) SetProperties(spec *v1alpha1.RenderedDeviceSpec) {
	if spec.Containers == nil || spec.Containers.MatchPatterns == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.matchPatterns = *spec.Containers.MatchPatterns
}
