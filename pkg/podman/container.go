package podman

import (
	"context"
	"encoding/json"
)

type ListContainersOptions struct {
	All    bool
	Filter string
}

type Container struct {
	ID string `json:"ID"`
}

func (c *Client) ListContainers(ctx context.Context, options ListContainersOptions) ([]Container, error) {
	cmdArgs := []string{"ps", "--format", "json"}
	if options.All {
		cmdArgs = append(cmdArgs, "-a")
	}
	if options.Filter != "" {
		cmdArgs = append(cmdArgs, "--filter", options.Filter)
	}

	stdout, stderr, exitCode := c.exec.ExecuteWithContext(ctx, podmanCmd, cmdArgs...)
	if exitCode != 0 {
		return nil, wrapPodmanError(ErrListContainers, stderr)
	}
	var containers []Container
	err := json.Unmarshal([]byte(stdout), &containers)
	if err != nil {
		return nil, wrapPodmanError(ErrUnmarshalContainers, err.Error())
	}
	return containers, nil
}
