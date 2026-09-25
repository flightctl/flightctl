package executer

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os/exec"
	"os/user"
	"strconv"
	"syscall"

	"github.com/flightctl/flightctl/api/core/v1beta1"
)

type Executer interface {
	CommandContext(ctx context.Context, command string, args ...string) *exec.Cmd
	Execute(command string, args ...string) (stdout string, stderr string, exitCode int)
	ExecuteWithContext(ctx context.Context, command string, args ...string) (stdout string, stderr string, exitCode int)
	ExecuteWithContextFromDir(ctx context.Context, workingDir string, command string, args []string, env ...string) (stdout string, stderr string, exitCode int)
	// ExecuteWithBoundedOutputFromDir limits stdout and stderr capture independently
	// to maxCombinedOutputBytes/2 bytes each. Zero or negative means unlimited.
	ExecuteWithBoundedOutputFromDir(ctx context.Context, workingDir string, command string, args []string, maxCombinedOutputBytes int, env ...string) (stdout string, stderr string, exitCode int)
}

type commonExecuter struct {
	// The user uid and gid under which commands are executed. Blank implies the current process user. If set, the
	// process must have root privileges or the CAP_SETUID and CAP_SETGID capabilities.
	uid     int
	gid     int
	homeDir string
}

type ExecuterOption func(e *commonExecuter)

// LookupUserOptions generates a set of options to NewCommonExecuter used to execute commands as a
// different user.
func LookupUserOptions(username v1beta1.Username) ([]ExecuterOption, error) {
	if username.IsCurrentProcessUser() {
		return nil, nil
	}
	u, err := user.Lookup(username.String())
	if err != nil {
		return nil, err
	}

	uid, err := strconv.Atoi(u.Uid)
	if err != nil {
		return nil, err
	}

	gid, err := strconv.Atoi(u.Gid)
	if err != nil {
		return nil, err
	}

	return []ExecuterOption{
		WithUIDAndGID(uint32(uid), uint32(gid)), //nolint:gosec // Linux UIDs are at most 2^32-1
		WithHomeDir(u.HomeDir),
	}, nil
}

func WithUIDAndGID(uid uint32, gid uint32) ExecuterOption {
	return func(e *commonExecuter) {
		e.uid = int(uid)
		e.gid = int(gid)
	}
}

func WithHomeDir(homeDir string) ExecuterOption {
	return func(e *commonExecuter) {
		e.homeDir = homeDir
	}
}

func NewCommonExecuter(options ...ExecuterOption) *commonExecuter {
	e := &commonExecuter{
		uid:     -1,
		gid:     -1,
		homeDir: "",
	}
	for _, o := range options {
		o(e)
	}
	return e
}

func (e *commonExecuter) Execute(command string, args ...string) (stdout string, stderr string, exitCode int) {
	cmd := e.CommandContext(context.Background(), command, args...)
	return e.execute(context.Background(), cmd)
}

func (e *commonExecuter) ExecuteWithContext(ctx context.Context, command string, args ...string) (stdout string, stderr string, exitCode int) {
	cmd := e.CommandContext(ctx, command, args...)
	return e.execute(ctx, cmd)
}

func (e *commonExecuter) ExecuteWithContextFromDir(ctx context.Context, workingDir string, command string, args []string, env ...string) (stdout string, stderr string, exitCode int) {
	cmd := e.CommandContext(ctx, command, args...)
	cmd.Dir = workingDir
	if len(env) > 0 {
		cmd.Env = env
	}
	return e.runCmd(ctx, cmd, 0)
}

func (e *commonExecuter) ExecuteWithBoundedOutputFromDir(ctx context.Context, workingDir string, command string, args []string, maxCombinedOutputBytes int, env ...string) (stdout string, stderr string, exitCode int) {
	cmd := e.CommandContext(ctx, command, args...)
	cmd.Dir = workingDir
	if len(env) > 0 {
		cmd.Env = env
	}
	return e.runCmd(ctx, cmd, maxCombinedOutputBytes)
}

func (e *commonExecuter) runCmd(ctx context.Context, cmd *exec.Cmd, maxCombinedOutput int) (stdout string, stderr string, exitCode int) {
	var stdoutBytes, stderrBytes bytes.Buffer
	if maxCombinedOutput > 0 {
		perStream := maxCombinedOutput / 2
		if perStream == 0 {
			perStream = 1
		}
		cmd.Stdout = &limitWriter{w: &stdoutBytes, n: int64(perStream)}
		cmd.Stderr = &limitWriter{w: &stderrBytes, n: int64(perStream)}
	} else {
		cmd.Stdout = &stdoutBytes
		cmd.Stderr = &stderrBytes
	}

	if err := cmd.Run(); err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return stdoutBytes.String(), context.DeadlineExceeded.Error(), 124
		}
		return stdoutBytes.String(), getErrorStr(err, &stderrBytes), getExitCode(err)
	}

	return stdoutBytes.String(), stderrBytes.String(), 0
}

func getExitCode(err error) int {
	if err == nil {
		return 0
	}

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		if state, ok := exitErr.ProcessState.Sys().(syscall.WaitStatus); ok {
			if state.Signal() == syscall.SIGKILL {
				return 137
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
	}
	if err != nil {
		return err.Error()
	}

	return ""
}

// limitWriter is a bytes.Buffer wrapper that discards writes beyond n bytes.
type limitWriter struct {
	w io.Writer
	n int64
}

func (l *limitWriter) Write(p []byte) (int, error) {
	n := len(p)
	if l.n <= 0 {
		return n, nil
	}
	if int64(n) > l.n {
		p = p[:l.n]
	}
	written, err := l.w.Write(p)
	l.n -= int64(written)
	return n, err
}
