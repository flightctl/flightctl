package status

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/agent/device/app"
	"github.com/flightctl/flightctl/pkg/executer"
)

const (
	systemdCommandTimeout = 2 * time.Minute
)

var _ Exporter = (*SystemD)(nil)

// SystemD collects systemd unit status as defined by match patterns.
type SystemD struct {
	client        *app.SystemDClient
	mu            sync.Mutex
	matchPatterns []string
	AppManager    *app.Manager
}

func newSystemD(exec executer.Executer, appManager *app.Manager) *SystemD {
	return &SystemD{
		AppManager: appManager,
		client:     app.NewSystemDClient(exec),
	}
}

func (c *SystemD) Export(ctx context.Context, _ *v1alpha1.DeviceStatus) error {
	ctx, cancel := context.WithTimeout(ctx, systemdCommandTimeout)
	defer cancel()

	matchPatterns := c.getMatchPatterns()
	units, err := c.client.List(ctx, matchPatterns...)
	if err != nil {
		return fmt.Errorf("failed listing systemd units: %w", err)
	}

	for _, unit := range units {
		name := *unit.Name
		app, err := c.client.GetStatus(ctx, name)
		if err != nil {
			return fmt.Errorf("failed getting application status: %w", err)
		}
		c.AppManager.ExportStatus(name, *app)
	}
	return nil
}

func (c *SystemD) getMatchPatterns() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.matchPatterns
}

func (c *SystemD) SetProperties(spec *v1alpha1.RenderedDeviceSpec) {
	if spec.Systemd == nil || spec.Systemd.MatchPatterns == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.matchPatterns = *spec.Systemd.MatchPatterns
}
