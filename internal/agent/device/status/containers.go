package status

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/pkg/executer"
)

const (
	podmanCommand          = "/usr/bin/podman"
	podmanCommandTimeout   = 10 * time.Second
	podmanContainerRunning = "running"
	PodmanEngine           = "podman"
	crioCommand            = "/usr/bin/crictl"
	crioCommandTimeout     = 10 * time.Second
	crioContainerRunning   = "CONTAINER_RUNNING"
	CrioEngine             = "crio"
)

var _ Exporter = (*Container)(nil)

// Container collects container status.
type Container struct {
	exec          executer.Executer
	matchPatterns []string
	notRunning    int
}

func newContainer(exec executer.Executer) *Container {
	return &Container{
		exec:       exec,
		notRunning: 0,
	}
}

type PodmanContainerList []PodmanContainerListEntry
type PodmanContainerListEntry struct {
	Names    []string `json:"Names"`
	State    string   `json:"State"`
	Image    string   `json:"Image"`
	Id       string   `json:"Id"`
	ExitCode int      `json:"ExitCode"`
	Restarts int      `json:"Restarts"`
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

func podmanApplicationStatus(entry PodmanContainerListEntry) v1alpha1.ApplicationStatusType {
	switch entry.State {
	case "running":
		return v1alpha1.ApplicationStatusRunning
	case "exited":
		if entry.ExitCode == 0 {
			return v1alpha1.ApplicationStatusCompleted
		}
		return v1alpha1.ApplicationStatusError
	default:
		return v1alpha1.ApplicationStatusUnknown
	}
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

	// TODO: handle removed containers and use appropriate status
	for _, c := range containers {
		status.Applications = append(status.Applications, v1alpha1.DeviceApplicationStatus{
			Name:     c.Names[0],
			Status:   podmanApplicationStatus(c),
			Restarts: c.Restarts,
		})
	}

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

	// TODO: handle removed containers and use appropriate status
	for _, c := range containers.Containers {
		name := c.Metadata.Name
		status.Applications = append(status.Applications, v1alpha1.DeviceApplicationStatus{
			Name:   name,
			Status: v1alpha1.ApplicationStatusUnknown,
		})
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
	c.matchPatterns = *spec.Containers.MatchPatterns
}
