package podman

import (
	"context"
)

func (c *Client) RemoveNetwork(ctx context.Context, network string) error {
	cmdArgs := []string{"network", "rm", network}
	_, stderr, exitCode := c.exec.ExecuteWithContext(ctx, podmanCmd, cmdArgs...)
	if exitCode != 0 {
		if exitCode == 1 {
			return ErrNetworkDoesNotExist
		}
		return wrapPodmanError(ErrRemoveNetwork, stderr)
	}
	return nil
}
