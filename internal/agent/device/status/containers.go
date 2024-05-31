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
	podmanCommand          = "/usr/bin/podman"
	podmanCommandTimeout   = 10 * time.Second
	podmanContainerRunning = "running"
	PodmanEngine           = "podman"
	crioCommand            = "/usr/bin/crictl"
	crioCommandTimeout     = 10 * time.Second
	crioContainerRunning   = "CONTAINER_RUNNING"
	CrioEngine             = "crio"
)

var _ Exporter = (*Container)(nil)

// Container collects container status.
type Container struct {
	exec          executer.Executer
	crictl        *app.CrioClient
	podman        *app.PodmanClient
	appManager    *AppManager
	mu            sync.Mutex
	matchPatterns []string
}

func newContainer(exec executer.Executer, appManager *AppManager) *Container {
	return &Container{
		exec:       exec,
		crictl:     app.NewCrioClient(exec),
		podman:     app.NewPodmanClient(exec),
		appManager: appManager,
	}
}

type PodmanContainerList []PodmanContainerListEntry
type PodmanContainerListEntry struct {
	Names []string `json:"Names"`
	State string   `json:"State"`
	Image string   `json:"Image"`
	Id    string   `json:"Id"`
}

type Shell interface {
	Command(cmd string) (output []byte, err error)
}

func (c *Container) PodmanExport(ctx context.Context, status *v1alpha1.DeviceStatus) error {
	ctx, cancel := context.WithTimeout(ctx, podmanCommandTimeout)
	defer cancel()

	containers, err := c.podman.ListContainers(ctx, c.matchPatterns...)
	if err != nil {
		return fmt.Errorf("failed listing crio containers: %w", err)
	}

	for _, container := range containers {
		appStatus, err := c.podman.GetApplicationStatus(ctx, container.Id)
		if err != nil {
			return err
		}
		c.appManager.ExportStatus(container.Metadata.Name, *appStatus)
	}

	return nil
}

func (c *Container) CrioExport(ctx context.Context, status *v1alpha1.DeviceStatus) error {
	ctx, cancel := context.WithTimeout(ctx, podmanCommandTimeout)
	defer cancel()

	containers, err := c.crictl.ListContainers(ctx, c.matchPatterns...)
	if err != nil {
		return fmt.Errorf("failed listing crio containers: %w", err)
	}

	for _, container := range containers {
		appStatus, err := c.crictl.GetApplicationStatus(ctx, container.Id)
		if err != nil {
			return err
		}
		c.appManager.ExportStatus(container.Metadata.Name, *appStatus)
	}

	return nil
}

func (c *Container) Export(ctx context.Context, status *v1alpha1.DeviceStatus) error {
	if _, err := c.exec.LookPath("podman"); err == nil {
		err := c.PodmanExport(ctx, status)
		if err != nil {
			return fmt.Errorf("failed exporting podman status: %w", err)
		}
	}

	if _, err := c.exec.LookPath("crictl"); err == nil {
		err := c.CrioExport(ctx, status)
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
