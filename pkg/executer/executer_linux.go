//go:build linux

package executer

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"syscall"
)

func (e *CommonExecuter) CommandContext(ctx context.Context, command string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, command, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGTERM}
	return cmd
}

func (e *CommonExecuter) execute(ctx context.Context, cmd *exec.Cmd) (stdout string, stderr string, exitCode int) {
	var stdoutBytes, stderrBytes bytes.Buffer
	cmd.Stdout = &stdoutBytes
	cmd.Stderr = &stderrBytes
	// Set Pdeathsig to SIGTERM to kill the process and its children when the parent process is killed.
	// This should prevent orphaned processes and allow for the subprocess to gracefully terminate.
	// ref. https://github.com/golang/go/blob/release-branch.go1.21/src/syscall/exec_linux.go#L91
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGTERM}

	if err := cmd.Run(); err != nil {
		// handle timeout error
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return stdoutBytes.String(), context.DeadlineExceeded.Error(), 124
		}
		return stdoutBytes.String(), getErrorStr(err, &stderrBytes), getExitCode(err)
	}

	return stdoutBytes.String(), stderrBytes.String(), 0
}
