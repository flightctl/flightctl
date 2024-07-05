package status

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/pkg/executer"
)

const (
	systemdCommand        = "/usr/bin/systemctl"
	systemdCommandTimeout = 2 * time.Minute
	systemdUnitLoaded     = "loaded"
	systemdUnitActive     = "active"
)

var _ Exporter = (*SystemD)(nil)

// SystemD collects systemd unit status as defined by match patterns.
type SystemD struct {
	exec          executer.Executer
	mu            sync.Mutex
	matchPatterns []string
}

func newSystemD(exec executer.Executer) *SystemD {
	return &SystemD{
		exec: exec,
	}
}

type SystemDUnitList []SystemDUnitListEntry
type SystemDUnitListEntry struct {
	Unit        string `json:"unit"`
	LoadState   string `json:"load"`
	ActiveState string `json:"active"`
	Sub         string `json:"sub"`
	Description string `json:"description"`
}

func (c *SystemD) Export(ctx context.Context, status *v1alpha1.DeviceStatus) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.matchPatterns == nil {
		return nil
	}

	execCtx, cancel := context.WithTimeout(ctx, systemdCommandTimeout)
	defer cancel()
	args := append([]string{"list-units", "--all", "--output", "json"}, c.matchPatterns...)
	out, errOut, exitCode := c.exec.ExecuteWithContext(execCtx, systemdCommand, args...)
	if exitCode != 0 {
		return fmt.Errorf("failed listing systemd units with code %d: %s", exitCode, errOut)
	}

	var units SystemDUnitList
	if err := json.Unmarshal([]byte(out), &units); err != nil {
		return fmt.Errorf("failed unmarshalling systemctl list-units output: %w", err)
	}

	// TODO: handle removed units and use appropriate status
	for _, u := range units {
		status.Applications.Data[u.Unit] = v1alpha1.ApplicationStatus{
			Name:   u.Unit,
			Status: v1alpha1.ApplicationStatusUnknown,
		}
	}

	return nil
}

func (c *SystemD) SetProperties(spec *v1alpha1.RenderedDeviceSpec) {
	if spec.Systemd == nil || spec.Systemd.MatchPatterns == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.matchPatterns = *spec.Systemd.MatchPatterns
}
