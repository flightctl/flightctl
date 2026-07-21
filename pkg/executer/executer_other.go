//go:build !linux

package executer

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"strings"
)

func (e *commonExecuter) CommandContext(ctx context.Context, command string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, command, args...)
	return cmd
}

func (e *commonExecuter) execute(ctx context.Context, cmd *exec.Cmd) (stdout string, stderr string, exitCode int) {
	var stdoutBytes, stderrBytes bytes.Buffer
	cmd.Stdout = &stdoutBytes
	cmd.Stderr = &stderrBytes
	if e.uid >= 0 {
		panic("executing under a different user is only supported on Linux")
	}

	if err := cmd.Run(); err != nil {
		// handle timeout error
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			stderr := strings.TrimSpace(stderrBytes.String())
			if stderr == "" {
				stderr = context.DeadlineExceeded.Error()
			}
			return stdoutBytes.String(), stderr, 124
		}
		return stdoutBytes.String(), getErrorStr(err, &stderrBytes), getExitCode(err)
	}

	return stdoutBytes.String(), stderrBytes.String(), 0
}
