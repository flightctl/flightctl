//go:build linux

package executer

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"syscall"
)

func (e *commonExecuter) CommandContext(ctx context.Context, command string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, command, args...)

	// Setpgid ensures that all child processes are in the same process group.
	// Pdeathsig ensures cleanup if the parent Go process dies unexpectedly.
	// ref. https://pkg.go.dev/syscall#SysProcAttr
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid:   true,
		Pdeathsig: syscall.SIGTERM,
	}

	// Inherit the current env if none was already specified in the cmd.
	if len(cmd.Env) == 0 {
		cmd.Env = os.Environ()
	}
	if e.homeDir != "" {
		// Forcefully override any existing HOME envvar if homeDir is set.
		cmd.Env = append(cmd.Env, "HOME="+e.homeDir)
	}

	if e.uid >= 0 {
		cmd.SysProcAttr.Credential = &syscall.Credential{
			Uid: uint32(e.uid), //nolint:gosec
			Gid: uint32(e.gid), //nolint:gosec
		}
	}

	// if context is canceled, kill the entire process group
	cmd.Cancel = func() error {
		if cmd.Process != nil {
			if cmd.SysProcAttr.Setpgid {
				// negative PID kills the process group
				// ref. https://man7.org/linux/man-pages/man2/kill.2.html
				return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			} else {
				return cmd.Process.Kill()
			}
		}
		return nil
	}

	return cmd
}

func (e *commonExecuter) execute(ctx context.Context, cmd *exec.Cmd) (stdout string, stderr string, exitCode int) {
	var stdoutBytes, stderrBytes bytes.Buffer
	cmd.Stdout = &stdoutBytes
	cmd.Stderr = &stderrBytes

	if err := cmd.Run(); err != nil {
		// handle timeout error
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return stdoutBytes.String(), context.DeadlineExceeded.Error(), 124
		}
		return stdoutBytes.String(), getErrorStr(err, &stderrBytes), getExitCode(err)
	}

	return stdoutBytes.String(), stderrBytes.String(), 0
}
