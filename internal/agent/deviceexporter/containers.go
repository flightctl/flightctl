package deviceexporter

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/pkg/executer"
)

const (
	podmanCommand        = "/usr/bin/podman"
	podmanCommandTimeout = 2 * time.Minute
)

type ContainerExporter struct {
	exec executer.Executer
}

func newContainerExporter(exec executer.Executer) *ContainerExporter {
	return &ContainerExporter{
		exec: exec,
	}
}

type PodmanList []PodmanListEntry
type PodmanListEntry struct {
	Names []string `json:"Names"`
	State string   `json:"State"`
	Image string   `json:"Image"`
	Id    string   `json:"Id"`
}

type Shell interface {
	Command(cmd string) (output []byte, err error)
}

func (c *ContainerExporter) GetStatus(ctx context.Context) (interface{}, error) {
	execCtx, cancel := context.WithTimeout(ctx, podmanCommandTimeout)
	defer cancel()
	args := []string{"ps", "-a", "--format", "json"}
	out, errOut, exitCode := c.exec.ExecuteWithContext(execCtx, podmanCommand, args...)
	if exitCode != 0 {
		return nil, fmt.Errorf("failed listing podman containers with code %d: %s", exitCode, errOut)
	}

	var containers PodmanList
	if err := json.Unmarshal([]byte(out), &containers); err != nil {
		return nil, fmt.Errorf("failed unmarshalling podman list output: %w", err)
	}

	deviceContainerStatus := make([]v1alpha1.ContainerStatus, len(containers))
	for i, c := range containers {
		deviceContainerStatus[i].Name = c.Names[0]
		deviceContainerStatus[i].Status = c.State
		deviceContainerStatus[i].Image = c.Image
		deviceContainerStatus[i].Id = c.Id
	}

	return deviceContainerStatus, nil
}
