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
	commandTimeout = 10 * time.Second
)

var _ Exporter = (*Container)(nil)

// Container collects container status.
type Container struct {
	exec          executer.Executer
	crictl        app.Engine
	podman        app.Engine
	appManager    *app.Manager
	mu            sync.Mutex
	matchPatterns []string
}

func newContainer(exec executer.Executer, appManager *app.Manager) *Container {
	return &Container{
		exec:       exec,
		crictl:     app.NewCrioClient(exec),
		podman:     app.NewPodmanClient(exec),
		appManager: app.NewManager(),
	}
}

// PodmanExport collects podman container status as defined by match patterns.
func (c *Container) PodmanExport(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()

	apps, err := c.podman.List(ctx, c.matchPatterns...)
	if err != nil {
		return fmt.Errorf("failed listing crio containers: %w", err)
	}

	for _, app := range apps {
		c.appManager.ExportStatus(*app.Name, app)
	}

	return nil
}

// CrioExport collects crio container status as defined by match patterns.
func (c *Container) CrioExport(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()

	apps, err := c.crictl.List(ctx, c.matchPatterns...)
	if err != nil {
		return fmt.Errorf("failed listing crio containers: %w", err)
	}

	for _, app := range apps {
		c.appManager.ExportStatus(*app.Name, app)
	}

	return nil
}

func (c *Container) Export(ctx context.Context, _ *v1alpha1.DeviceStatus) error {
	if _, err := c.exec.LookPath("podman"); err == nil {
		err := c.PodmanExport(ctx)
		if err != nil {
			return fmt.Errorf("failed exporting podman status: %w", err)
		}
	}

	if _, err := c.exec.LookPath("crictl"); err == nil {
		err := c.CrioExport(ctx)
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
	c.mu.Lock()
	defer c.mu.Unlock()
	c.matchPatterns = *spec.Containers.MatchPatterns
}
