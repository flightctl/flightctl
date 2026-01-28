
package container

import (
	"context"
	"fmt"
	"strings"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/pkg/log"
	"github.comcom/containers/common/libnetwork/types/container"
	types "github.com/containers/podman/v4/pkg/bindings/types"
)

type Compose struct {
	runner  Runner
	log     *log.PrefixLogger
	workdir string
}

func NewCompose(runner Runner, log *log.PrefixLogger, workdir string) *Compose {
	return &Compose{
		runner:  runner,
		log:     log,
		workdir: workdir,
	}
}

func (c *Compose) Pull(ctx context.Context, name string, spec *api.Compose) (string, error) {
	c.log.Infof("Pulling compose application %s", name)
	_, err := c.runner.Compose(ctx, name, spec.Path, "pull")
	if err != nil {
		return "", fmt.Errorf("pulling compose application %s: %w", name, err)
	}
	return "", nil
}

func (c *Compose) Run(ctx context.Context, name string, spec *api.Compose) error {
	c.log.Infof("Running compose application %s", name)
	_, err := c.runner.Compose(ctx, name, spec.Path, "up", "-d")
	if err != nil {
		return fmt.Errorf("running compose application %s: %w", name, err)
	}
	return nil
}

func (c *Compose) Stop(ctx context.Context, name string, spec *api.Compose) error {
	c.log.Infof("Stopping compose application %s", name)
	_, err := c.runner.Compose(ctx, name, spec.Path, "down")
	if err != nil {
		return fmt.Errorf("stopping compose application %s: %w", name, err)
	}
	return nil
}

func (c *Compose) GetPodmanComposeStatus(ctx context.Context, name string, spec *api.Compose) (api.ApplicationStatus, error) {
	out, err := c.runner.Compose(ctx, name, spec.Path, "ps", "-q")
	if err != nil {
		return api.ApplicationStatus{}, fmt.Errorf("getting compose application %s status: %w", name, err)
	}
	containerIds := strings.Split(string(out), "\n")

	var containerStatuses []api.ContainerStatus
	allFinished := true
	anyFailed := false
	anyRunning := false

	for _, containerId := range containerIds {
		if len(containerId) == 0 {
			continue
		}
		inspect, err := c.runner.Inspect(ctx, containerId)
		if err != nil {
			return api.ApplicationStatus{}, fmt.Errorf("inspecting container %s: %w", containerId, err)
		}
		status := getContainerStatus(inspect)
		if status == api.ContainerStatusRunning {
			allFinished = false
			anyRunning = true
		}
		if status == api.ContainerStatusExited {
			if inspect.State.ExitCode != 0 {
				anyFailed = true
			} else {
				if inspect.State.Status == "stopped" {
					anyFailed = true
				} else if inspect.HostConfig != nil && (inspect.HostConfig.RestartPolicy.Name == "always" || inspect.HostConfig.RestartPolicy.Name == "unless-stopped") {
					anyFailed = true
				}
			}
		}
		exitCode := inspect.State.ExitCode
		containerStatuses = append(containerStatuses, api.ContainerStatus{
			Name:     inspect.Name,
			Id:       inspect.Id,
			Image:    inspect.ImageName,
			Status:   status,
			ExitCode: &exitCode,
		})
	}

	summary := api.ApplicationSummary{Status: api.ApplicationStatusRunning}
	if allFinished {
		if anyFailed {
			summary.Status = api.ApplicationStatusFailed
		} else {
			summary.Status = api.ApplicationStatusCompleted
		}
	} else if anyRunning {
		summary.Status = api.ApplicationStatusRunning
	} else {
		summary.Status = api.ApplicationStatusPreparing
	}

	return api.ApplicationStatus{
		Summary:    summary,
		Containers: containerStatuses,
	}, nil
}

func (c *Compose) Exists(ctx context.Context, name string, spec *api.Compose) (bool, error) {
	_, err := c.runner.Compose(ctx, name, spec.Path, "ps")
	if err != nil {
		if util.IsNoContainersFoundError(err) {
			return false, nil
		}
		return false, fmt.Errorf("checking if compose application %s exists: %w", name, err)
	}

	return true, nil
}

func getContainerStatus(inspect *types.InspectContainerData) api.ContainerStatus {
	switch inspect.State.Status {
	case "running":
		return api.ContainerStatusRunning
	case "exited", "stopped":
		return api.ContainerStatusExited
	case "created":
		return api.ContainerStatusCreated
	default:
		return api.ContainerStatusUnknown
	}
}

