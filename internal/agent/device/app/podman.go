package app

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/pkg/executer"
)

const (
	podmanCmd = "/usr/bin/podman"
)

type PodmanClient struct {
	exec executer.Executer
}

func NewPodmanClient(exec executer.Executer) *PodmanClient {
	return &PodmanClient{
		exec: exec,
	}
}

func (c *PodmanClient) PullImage(name string) error {
	return nil
}

func (c *PodmanClient) ImageExists(name string) (bool, error) {
	return false, nil
}

// ListContainers returns a list of containers based on the match patterns.
func (c *PodmanClient) ListContainers(ctx context.Context, matchPatterns ...string) ([]PodmanContainer, error) {
	args := []string{"ps", "-a", "--output", "json"}
	for _, pattern := range matchPatterns {
		args = append(args, "--name")
		args = append(args, pattern)
	}

	stdout, stderr, exitCode := c.exec.ExecuteWithContext(ctx, podmanCmd, args...)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if exitCode != 0 {
		return nil, fmt.Errorf("failed listing podman containers with code %d: %s", exitCode, stderr)
	}

	type ListContainers struct {
		Containers []PodmanContainer `json:"containers"`
	}

	var list ListContainers
	err := json.Unmarshal([]byte(stdout), &list)
	if err != nil {
		return nil, fmt.Errorf("failed unmarshalling podman containers: %s", err)
	}

	return list.Containers, nil
}

// GetContainerStatus returns the status of a container.
func (c *PodmanClient) GetContainerStatus(ctx context.Context, id string) (*PodmanContainerStatus, error) {
	args := []string{"inspect", id}
	stdout, stderr, exitCode := c.exec.ExecuteWithContext(ctx, podmanCmd, args...)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if exitCode != 0 {
		return nil, fmt.Errorf("inspect image id %s: %s", id, stderr)
	}

	var status PodmanContainerStatus
	err := json.Unmarshal([]byte(stdout), &status)
	if err != nil {
		return nil, fmt.Errorf("failed unmarshalling podman container status: %s", err)
	}

	return &status, nil
}

// GetApplicationStatus returns the application status based on the container status.
func (c *PodmanClient) GetApplicationStatus(ctx context.Context, id string) (*v1alpha1.ApplicationStatus, error) {
	cs, err := c.GetContainerStatus(ctx, id)
	if err != nil {
		return nil, err
	}
	restarts := cs.Status.RestartCount
	state, err := c.GetApplicationState(ctx, cs)
	if err != nil {
		return nil, err
	}
	return &v1alpha1.ApplicationStatus{
		Name:     &id,
		State:    &state,
		Restarts: &restarts,
	}, nil
}

// GetApplicationStateFromId returns the application state based on the container id.
func (c *PodmanClient) GetApplicationState(ctx context.Context, cs *PodmanContainerStatus) (v1alpha1.ApplicationState, error) {
	restarts := cs.Status.RestartCount
	switch cs.Status.State {
	case "created":
		return v1alpha1.ApplicationStateInitializing, nil
	case "running":
		return v1alpha1.ApplicationStateRunning, nil
	case "paused":
		return v1alpha1.ApplicationStateInitializing, nil
	case "exited":
		if restarts > 0 {
			return v1alpha1.ApplicationStateCrashed, nil
		}
		return v1alpha1.ApplicationStateStopped, nil
	default:
		return v1alpha1.ApplicationStateUnknown, nil
	}
}

// PodmanContainerStatus represents the status of a container.
type PodmanContainerStatus struct {
	Status struct {
		Running      bool   `json:"running"`
		Paused       bool   `json:"paused"`
		RestartCount int    `json:"restartCount"`
		State        string `json:"state"`
	} `json:"status"`
}

// PodmanContainer represents a container.
type PodmanContainer struct {
	Id       string `json:"id"`
	Metadata struct {
		Name string `json:"name"`
	} `json:"metadata"`
	Image string `json:"imageRef"`
	State string `json:"state"`
}
