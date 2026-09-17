//go:build !linux

package executer

import (
	"context"
	"os/exec"
)

func (e *commonExecuter) CommandContext(ctx context.Context, command string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, command, args...)
	return cmd
}

func (e *commonExecuter) execute(ctx context.Context, cmd *exec.Cmd) (stdout string, stderr string, exitCode int) {
	if e.uid >= 0 {
		panic("executing under a different user is only supported on Linux")
	}
	return e.runCmd(ctx, cmd, 0)
}
