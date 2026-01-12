package podman

import (
	"context"
)

func (c *Client) RemoveImage(ctx context.Context, image string) error {
	cmdArgs := []string{"rmi", image}
	_, stderr, exitCode := c.exec.ExecuteWithContext(ctx, podmanCmd, cmdArgs...)
	if exitCode != 0 {
		if exitCode == 1 {
			return ErrImageDoesNotExist
		}
		return wrapPodmanError(ErrRemoveImage, stderr)
	}
	return nil
}
