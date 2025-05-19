package executer

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"syscall"
)

type Executer interface {
	CommandContext(ctx context.Context, command string, args ...string) *exec.Cmd
	Execute(command string, args ...string) (stdout string, stderr string, exitCode int)
	ExecuteWithContext(ctx context.Context, command string, args ...string) (stdout string, stderr string, exitCode int)
	ExecuteWithContextFromDir(ctx context.Context, workingDir string, command string, args []string, env ...string) (stdout string, stderr string, exitCode int)
	TempFile(dir, pattern string) (f *os.File, err error)
	LookPath(file string) (string, error)
}

type CommonExecuter struct{}

func (e *CommonExecuter) TempFile(dir, pattern string) (f *os.File, err error) {
	return os.CreateTemp(dir, pattern)
}

func (e *CommonExecuter) Execute(command string, args ...string) (stdout string, stderr string, exitCode int) {
	cmd := exec.Command(command, args...)
	return e.execute(context.Background(), cmd)
}

func (e *CommonExecuter) ExecuteWithContext(ctx context.Context, command string, args ...string) (stdout string, stderr string, exitCode int) {
	cmd := exec.CommandContext(ctx, command, args...)
	return e.execute(ctx, cmd)
}

func (e *CommonExecuter) ExecuteWithContextFromDir(ctx context.Context, workingDir string, command string, args []string, env ...string) (stdout string, stderr string, exitCode int) {
	cmd := exec.CommandContext(ctx, command, args...)
	cmd.Dir = workingDir
	if len(env) > 0 {
		cmd.Env = env
	}
	return e.execute(ctx, cmd)
}

func (e *CommonExecuter) LookPath(file string) (string, error) {
	return exec.LookPath(file)
}

func getExitCode(err error) int {
	if err == nil {
		return 0
	}

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		if state, ok := exitErr.ProcessState.Sys().(syscall.WaitStatus); ok {
			// sigkill is seen during upgrade reboot
			if state.Signal() == syscall.SIGKILL {
				return 137 // 128 + 9 (SIGKILL)
			}
		}
		return exitErr.ExitCode()
	}

	return -1
}

func getErrorStr(err error, stderr *bytes.Buffer) string {
	b := stderr.Bytes()
	if len(b) > 0 {
		return string(b)
	} else if err != nil {
		return err.Error()
	}

	return ""
}
