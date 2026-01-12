package podman

import (
	"context"
)

func (c *Client) RemoveVolume(ctx context.Context, volume string) error {
	cmdArgs := []string{"volume", "rm", volume}
	_, stderr, exitCode := c.exec.ExecuteWithContext(ctx, podmanCmd, cmdArgs...)
	if exitCode != 0 {
		if exitCode == 1 {
			return ErrVolumeDoesNotExist
		}
		return wrapPodmanError(ErrRemoveVolume, stderr)
	}
	return nil
}
