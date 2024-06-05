package app

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/pkg/executer"
)

const (
	CrictlCmd = "/usr/bin/crictl"
)

var _ Engine = (*CrioClient)(nil)

type CrioClient struct {
	exec executer.Executer
}

func NewCrioClient(exec executer.Executer) *CrioClient {
	return &CrioClient{
		exec: exec,
	}
}

func (c *CrioClient) PullImage() error {
	return fmt.Errorf("not implemented")
}

func (c *CrioClient) ImageExists() (bool, error) {
	return false, fmt.Errorf("not implemented")
}

func (c *CrioClient) List(ctx context.Context, matchPatterns ...string) ([]v1alpha1.ApplicationStatus, error) {
	args := []string{"ps", "-a", "--output", "json"}
	for _, pattern := range matchPatterns {
		args = append(args, "--name")
		args = append(args, fmt.Sprintf("^%s$", pattern)) // crio uses regex for matching
	}

	stdout, stderr, exitCode := c.exec.ExecuteWithContext(ctx, CrictlCmd, args...)
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

	status := make([]v1alpha1.ApplicationStatus, 0, len(list.Containers))
	for _, container := range list.Containers {
		cs, err := c.getCrioStatus(ctx, container.Id)
		if err != nil {
			return nil, err
		}
		state, err := c.GetApplicationState(ctx, cs)
		if err != nil {
			return nil, err
		}
		restarts := cs.Status.Metadata.Attempt
		status = append(status, v1alpha1.ApplicationStatus{
			Id:       &container.Id,
			Name:     &container.Metadata.Name,
			State:    &state,
			Restarts: &restarts,
		})
	}

	return status, nil
}

func (c *CrioClient) GetStatus(ctx context.Context, id string) (*v1alpha1.ApplicationStatus, error) {
	cs, err := c.getCrioStatus(ctx, id)
	if err != nil {
		return nil, err
	}
	state, err := c.GetApplicationState(ctx, cs)
	if err != nil {
		return nil, err
	}
	restarts := cs.Status.Metadata.Attempt

	return &v1alpha1.ApplicationStatus{
		Id:       &id,
		Name:     &cs.Status.Metadata.Name,
		State:    &state,
		Restarts: &restarts,
	}, nil
}

func (c *CrioClient) getCrioStatus(ctx context.Context, id string) (*CrioContainerStatus, error) {
	args := []string{"inspect", id}
	stdout, stderr, exitCode := c.exec.ExecuteWithContext(ctx, CrictlCmd, args...)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if exitCode != 0 {
		return nil, fmt.Errorf("inspect image id %s: %s", id, stderr)
	}

	var cs CrioContainerStatus
	if err := json.Unmarshal([]byte(stdout), &cs); err != nil {
		return nil, fmt.Errorf("unmarshal container status: %w", err)
	}

	return &cs, nil
}

// GetApplicationStateFromId returns the application state based on the container id.
func (c *CrioClient) GetApplicationState(ctx context.Context, cs *CrioContainerStatus) (v1alpha1.ApplicationState, error) {
	restarts := cs.Status.Metadata.Attempt
	switch cs.Status.State {
	case "CONTAINER_CREATED":
		return v1alpha1.ApplicationStatePreparing, nil
	case "CONTAINER_RUNNING":
		return v1alpha1.ApplicationStateRunning, nil
	case "CONTAINER_STOPPED":
		return v1alpha1.ApplicationStateStopped, nil
	case "CONTAINER_EXITED":
		if restarts > 0 {
			return v1alpha1.ApplicationStateError, nil
		}
		return v1alpha1.ApplicationStateStopped, nil
	default:
		return v1alpha1.ApplicationStateUnknown, nil
	}
}

// CrioContainerStatus represents the status of a CRI-O container.
type CrioContainerStatus struct {
	Status CrioStatus `json:"status"`
}

// CrioStatus represents the status of a crio managed container.
type CrioStatus struct {
	ID       string   `json:"id"`
	Metadata Metadata `json:"metadata"`
	State    string   `json:"state"`
	ExitCode int      `json:"exitCode"`
	Image    Image    `json:"image"`
}

// Image represents the image information of a container.
type Image struct {
	Image string `json:"image"`
}

type Metadata struct {
	Attempt int    `json:"attempt"`
	Name    string `json:"name"`
}

// CrictlContainer represents a CRI-O container.
type CrioContainer struct {
	Id       string `json:"id"`
	Metadata struct {
		Name string `json:"name"`
	} `json:"metadata"`
	Image string `json:"imageRef"`
	State string `json:"state"`
}
