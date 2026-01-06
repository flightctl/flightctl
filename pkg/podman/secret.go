package podman

import (
	"context"
)

type RemoveSecretOptions struct {
	Ignore bool
}

func (c *Client) RemoveSecret(ctx context.Context, secret string, options RemoveSecretOptions) error {
	cmdArgs := []string{"secret", "rm", secret}
	if options.Ignore {
		cmdArgs = append(cmdArgs, "--ignore")
	}

	_, stderr, exitCode := c.exec.ExecuteWithContext(ctx, podmanCmd, cmdArgs...)
	if exitCode != 0 {
		return wrapPodmanError(ErrRemoveSecret, stderr)
	}
	return nil
}
