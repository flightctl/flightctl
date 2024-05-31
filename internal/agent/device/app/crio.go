package app

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/pkg/executer"
)

const (
	crictlCmd = "/usr/bin/crictl"
)

type CrioClient struct {
	exec executer.Executer
}

func NewCrioClient(exec executer.Executer) *CrioClient {
	return &CrioClient{
		exec: exec,
	}
}

// PullImage pulls an image from the registry.
func (c *CrioClient) PullImage() error {
	return nil
}

// ImageExists checks if an image exists in the local container storage.
func (c *CrioClient) ImageExists() (bool, error) {
	return false, nil
}

// ListContainers returns a list of containers based on the match patterns.
func (c *CrioClient) ListContainers(ctx context.Context, matchPatterns ...string) ([]CrioContainer, error) {
	args := []string{"ps", "-a", "--output", "json"}
	for _, pattern := range matchPatterns {
		args = append(args, "--name")
		args = append(args, pattern)
	}

	stdout, stderr, exitCode := c.exec.ExecuteWithContext(ctx, crictlCmd, args...)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if exitCode != 0 {
		return nil, fmt.Errorf("failed listing crio containers with code %d: %s", exitCode, stderr)
	}

	type ListContainers struct {
		Containers []CrioContainer `json:"containers"`
	}

	var list ListContainers
	err := json.Unmarshal([]byte(stdout), &list)
	if err != nil {
		return nil, fmt.Errorf("failed unmarshalling crio containers: %s", err)
	}

	return list.Containers, nil
}

// GetContainerStatus returns the status of a container.
func (c *CrioClient) GetContainerStatus(ctx context.Context, id string) (*CrioContainerStatus, error) {
	args := []string{"inspect", id}
	stdout, stderr, exitCode := c.exec.ExecuteWithContext(ctx, crictlCmd, args...)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if exitCode != 0 {
		return nil, fmt.Errorf("inspect image id %s: %s", id, stderr)
	}

	var status CrioContainerStatus
	if err := json.Unmarshal([]byte(stdout), &status); err != nil {
		return nil, fmt.Errorf("unmarshal container status: %w", err)
	}

	return &status, nil
}

// GetApplicationStatus returns the application status based on the container status.
func (c *CrioClient) GetApplicationStatus(ctx context.Context, id string) (*v1alpha1.ApplicationStatus, error) {
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
func (c *CrioClient) GetApplicationState(ctx context.Context, cs *CrioContainerStatus) (v1alpha1.ApplicationState, error) {
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

// CrioContainerStatus represents the status of a container.
type CrioContainerStatus struct {
	Status struct {
		Running      bool   `json:"running"`
		Paused       bool   `json:"paused"`
		RestartCount int    `json:"restartCount"`
		State        string `json:"state"`
	} `json:"status"`
}

// CrictlContainer represents a container.
type CrioContainer struct {
	Id       string `json:"id"`
	Metadata struct {
		Name string `json:"name"`
	} `json:"metadata"`
	Image string `json:"imageRef"`
	State string `json:"state"`
}
