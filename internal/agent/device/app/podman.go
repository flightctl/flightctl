package app

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/pkg/executer"
)

const (
	PodmanCmd = "/usr/bin/podman"
)

var (
	_ Engine  = (*PodmanClient)(nil)
	_ Runtime = (*PodmanClient)(nil)
)

type PodmanClient struct {
	exec executer.Executer
}

func NewPodmanClient(exec executer.Executer) *PodmanClient {
	return &PodmanClient{
		exec: exec,
	}
}

func (c *PodmanClient) PullImage(ctx context.Context, name string) error {
	return fmt.Errorf("not implemented")
}

func (c *PodmanClient) ImageExists(ctx context.Context, name string) (bool, error) {
	return false, fmt.Errorf("not implemented")
}

func (c *PodmanClient) List(ctx context.Context, matchPatterns ...string) ([]v1alpha1.ApplicationStatus, error) {
	args := []string{"ps", "-a", "--format", "json"}
	for _, pattern := range matchPatterns {
		args = append(args, "--filter")
		args = append(args, fmt.Sprintf("name=%s", pattern))
	}

	stdout, stderr, exitCode := c.exec.ExecuteWithContext(ctx, PodmanCmd, args...)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if exitCode != 0 {
		return nil, fmt.Errorf("failed listing podman containers with code %d: %s", exitCode, stderr)
	}

	var containers []PodmanContainer
	err := json.Unmarshal([]byte(stdout), &containers)
	if err != nil {
		return nil, fmt.Errorf("failed to deserialize podman containers: %s", err)
	}

	appStatus := make([]v1alpha1.ApplicationStatus, 0, len(containers))
	for _, container := range containers {
		status, err := c.getPodmanStatus(ctx, container.Id)
		if err != nil {
			return nil, err
		}

		state, err := c.getApplicationState(container.State, status.RestartCount)
		if err != nil {
			return nil, err
		}

		var name string
		if len(container.Names) == 0 {
			name = container.Id
		} else {
			name = container.Names[0]
		}

		appStatus = append(appStatus, v1alpha1.ApplicationStatus{
			Id:       &container.Id,
			Name:     &name,
			State:    &state,
			Restarts: &status.RestartCount,
		})
	}

	return appStatus, nil
}

func (c *PodmanClient) GetStatus(ctx context.Context, id string) (*v1alpha1.ApplicationStatus, error) {
	ps, err := c.getPodmanStatus(ctx, id)
	if err != nil {
		return nil, err
	}

	state, err := c.getApplicationState(ps.State.Status, ps.RestartCount)
	if err != nil {
		return nil, err
	}

	var name string
	if len(ps.Names) == 0 {
		name = id
	} else {
		name = ps.Names[0]
	}

	return &v1alpha1.ApplicationStatus{
		Id:       &id,
		Name:     &name,
		State:    &state,
		Restarts: &ps.RestartCount,
	}, nil

}

func (c *PodmanClient) getPodmanStatus(ctx context.Context, id string) (*PodmanContainerStatus, error) {
	args := []string{
		"inspect",
		id,
	}
	stdout, stderr, exitCode := c.exec.ExecuteWithContext(ctx, PodmanCmd, args...)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if exitCode != 0 {
		return nil, fmt.Errorf("inspect image id %s: %s", id, stderr)
	}

	var status []PodmanContainerStatus
	err := json.Unmarshal([]byte(stdout), &status)
	if err != nil {
		return nil, fmt.Errorf("failed unmarshalling podman container status: %s", err)
	}

	if len(status) == 0 {
		return nil, fmt.Errorf("no status found for container %s", id)
	}

	return &status[0], nil
}

// getApplicationState returns the application state based on the container id.
func (c *PodmanClient) getApplicationState(state string, restarts int) (v1alpha1.ApplicationState, error) {
	switch state {
	case "created":
		return v1alpha1.ApplicationStatePreparing, nil
	case "running":
		return v1alpha1.ApplicationStateRunning, nil
	case "paused":
		return v1alpha1.ApplicationStateUnknown, nil
	case "exited":
		if restarts > 0 {
			return v1alpha1.ApplicationStateError, nil
		}
		return v1alpha1.ApplicationStateStopped, nil
	default:
		return v1alpha1.ApplicationStateUnknown, nil
	}
}

func (c *PodmanClient) Type() RuntimeType {
	return RuntimeTypePodman
}

// PodmanContainer represents a container.
type PodmanContainer struct {
	Names []string `json:"Names"`
	State string   `json:"State"`
	Image string   `json:"Image"`
	Id    string   `json:"Id"`
}

type PodmanContainerStatus struct {
	Id           string      `json:"Id"`
	Names        []string    `json:"Names"`
	State        PodmanState `json:"State"`
	RestartCount int         `json:"RestartCount"`
}

type PodmanState struct {
	Status     string `json:"Status"`
	Running    bool   `json:"Running"`
	Paused     bool   `json:"Paused"`
	Restarting bool   `json:"Restarting"`
	OOMKilled  bool   `json:"OOMKilled"`
	Dead       bool   `json:"Dead"`
	Pid        int    `json:"Pid"`
	ExitCode   int    `json:"ExitCode"`
	Error      string `json:"Error"`
}
