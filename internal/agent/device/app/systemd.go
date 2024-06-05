package app

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/pkg/executer"
)

const (
	SystemdCommand = "/usr/bin/systemctl"
)

var _ Engine = (*SystemDClient)(nil)

// SystemDClient is a client for interacting with systemd.
type SystemDClient struct {
	exec executer.Executer
}

// NewSystemDClient creates a new SystemDClient.
func NewSystemDClient(exec executer.Executer) *SystemDClient {
	return &SystemDClient{
		exec: exec,
	}
}

func (c *SystemDClient) List(ctx context.Context, matchPatterns ...string) ([]v1alpha1.ApplicationStatus, error) {
	args := append([]string{"list-units", "--all", "--output", "json"}, matchPatterns...)

	stdout, stderr, exitCode := c.exec.ExecuteWithContext(ctx, SystemdCommand, args...)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if exitCode != 0 {
		return nil, fmt.Errorf("failed listing systemd units with code %d: %s", exitCode, stderr)
	}

	var list []SystemDUnit
	err := json.Unmarshal([]byte(stdout), &list)
	if err != nil {
		return nil, fmt.Errorf("failed unmarshalling systemd units: %s", err)
	}

	status := make([]v1alpha1.ApplicationStatus, 0, len(list))
	for _, unit := range list {
		state := c.getApplicationState(ctx, &unit)
		status = append(status, v1alpha1.ApplicationStatus{
			Name:  &unit.Unit,
			State: &state,
		})
	}

	return status, nil
}

func (c *SystemDClient) GetStatus(ctx context.Context, name string) (*v1alpha1.ApplicationStatus, error) {
	// systemctl status output is not optimal to unmarshal json status.
	units, err := c.List(ctx)
	if err != nil {
		return nil, err
	}

	var unit *v1alpha1.ApplicationStatus
	for _, u := range units {
		if *u.Name == name {
			unit = &u
			break
		}
	}

	if unit == nil {
		return nil, fmt.Errorf("unit %s: %w", name, ErrNotFound)
	}

	return &v1alpha1.ApplicationStatus{
		Name:  unit.Name,
		State: unit.State,
	}, nil
}

// getApplicationState returns the application state based on the systemd unit state.
func (c *SystemDClient) getApplicationState(_ context.Context, unit *SystemDUnit) v1alpha1.ApplicationState {
	switch unit.Sub {
	case "failed":
		return v1alpha1.ApplicationStateError
	case "starting":
		return v1alpha1.ApplicationStateStarting
	case "running":
		return v1alpha1.ApplicationStateRunning
	case "dead":
		return v1alpha1.ApplicationStateStopped
	default:
		return v1alpha1.ApplicationStateUnknown
	}
}

// systemDUnit represents a systemd unit.
type SystemDUnit struct {
	Unit        string `json:"unit"`
	LoadState   string `json:"load"`
	ActiveState string `json:"active"`
	Sub         string `json:"sub"`
	Description string `json:"description"`
}
