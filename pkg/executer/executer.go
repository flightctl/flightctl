package executer

import (
	"bytes"
	"context"
	"errors"
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
	cmd := exec.Command(command, args...)
	return e.execute(context.Background(), cmd)
}

func (e *commonExecuter) ExecuteWithContext(ctx context.Context, command string, args ...string) (stdout string, stderr string, exitCode int) {
	cmd := exec.CommandContext(ctx, command, args...)
	return e.execute(ctx, cmd)
}

func (e *commonExecuter) ExecuteWithContextFromDir(ctx context.Context, workingDir string, command string, args []string, env ...string) (stdout string, stderr string, exitCode int) {
	cmd := exec.CommandContext(ctx, command, args...)
	cmd.Dir = workingDir
	if len(env) > 0 {
		cmd.Env = env
	}
	return e.execute(ctx, cmd)
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
