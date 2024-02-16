package deviceexporter

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
)

type SystemDExporter struct {
	exec          executer.Executer
	mu            sync.Mutex
	matchPatterns []string
}

func newSystemDExporter(exec executer.Executer) *SystemDExporter {
	return &SystemDExporter{
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

func (c *SystemDExporter) GetStatus(ctx context.Context) (interface{}, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.matchPatterns == nil {
		return nil, nil
	}

	execCtx, cancel := context.WithTimeout(ctx, systemdCommandTimeout)
	defer cancel()
	args := append([]string{"list-units", "--all", "--output", "json"}, c.matchPatterns...)
	out, errOut, exitCode := c.exec.ExecuteWithContext(execCtx, systemdCommand, args...)
	if exitCode != 0 {
		return nil, fmt.Errorf("failed listing systemd units with code %d: %s", exitCode, errOut)
	}

	var units SystemDUnitList
	if err := json.Unmarshal([]byte(out), &units); err != nil {
		return nil, fmt.Errorf("failed unmarshalling systemctl list-units output: %w", err)
	}

	deviceSystemdUnitStatus := make([]v1alpha1.DeviceSystemdUnitStatus, len(units))
	for i, u := range units {
		deviceSystemdUnitStatus[i].Name = u.Unit
		deviceSystemdUnitStatus[i].LoadState = u.LoadState
		deviceSystemdUnitStatus[i].ActiveState = u.ActiveState
	}

	return deviceSystemdUnitStatus, nil
}

func (c *SystemDExporter) SetMatchPatterns(matchPatterns []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.matchPatterns = matchPatterns
}
