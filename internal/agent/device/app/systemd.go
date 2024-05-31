package app

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/pkg/executer"
)

const (
	systemdCommand = "/usr/bin/systemctl"
)

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

// ListUnits returns a list of systemd units based on the match patterns.
func (c *SystemDClient) ListUnits(ctx context.Context, matchPatterns ...string) ([]SystemDUnit, error) {
	args := append([]string{"list-units", "--all", "--output", "json"}, matchPatterns...)

	stdout, stderr, exitCode := c.exec.ExecuteWithContext(ctx, systemdCommand, args...)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if exitCode != 0 {
		return nil, fmt.Errorf("failed listing systemd units with code %d: %s", exitCode, stderr)
	}

	type ListUnits struct {
		Units []SystemDUnit `json:"units"`
	}

	var list ListUnits
	err := json.Unmarshal([]byte(stdout), &list)
	if err != nil {
		return nil, fmt.Errorf("failed unmarshalling systemd units: %s", err)
	}

	return list.Units, nil
}

// GetApplicationStatus returns the application status based on the container status.
func (c *SystemDClient) GetApplicationStatus(ctx context.Context, id string) (*v1alpha1.ApplicationStatus, error) {
	return nil, nil
}

// GetApplicationState returns the application state based on the systemd unit state.
func (c *SystemDClient) GetApplicationState(unit SystemDUnit) v1alpha1.ApplicationState {
	switch unit.Sub {
	case "failed":
		return v1alpha1.ApplicationStateCrashed
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

// SystemDUnit represents a systemd unit.
type SystemDUnit struct {
	Unit        string `json:"unit"`
	LoadState   string `json:"load"`
	ActiveState string `json:"active"`
	Sub         string `json:"sub"`
	Description string `json:"description"`
}
