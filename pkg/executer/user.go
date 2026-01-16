package executer

import (
	"context"
	"os"
	"os/exec"
	"os/user"
	"strconv"
)

type userExecuter struct {
	executer Executer
	user     string
}

var _ Executer = &userExecuter{}

func NewUserExecutor(user string) *userExecuter {
	return &userExecuter{
		executer: NewCommonExecuter(),
		user:     user,
	}
}

func (u *userExecuter) commandArgsWithUser(command string, args []string) (string, []string) {
	return "sudo", append([]string{
		"-u", u.user,
		command,
	}, args...)
}

func (u *userExecuter) CommandContext(ctx context.Context, command string, args ...string) *exec.Cmd {
	command, args = u.commandArgsWithUser(command, args)
	return u.executer.CommandContext(ctx, command, args...)
}

func (u *userExecuter) Execute(command string, args ...string) (stdout string, stderr string, exitCode int) {
	command, args = u.commandArgsWithUser(command, args)
	return u.executer.Execute(command, args...)
}

func (u *userExecuter) ExecuteWithContext(ctx context.Context, command string, args ...string) (stdout string, stderr string, exitCode int) {
	command, args = u.commandArgsWithUser(command, args)
	return u.executer.ExecuteWithContext(ctx, command, args...)
}

func (u *userExecuter) ExecuteWithContextFromDir(ctx context.Context, workingDir string, command string, args []string, env ...string) (stdout string, stderr string, exitCode int) {
	command, args = u.commandArgsWithUser(command, args)
	return u.executer.ExecuteWithContextFromDir(ctx, workingDir, command, args, env...)
}

func (u *userExecuter) TempFile(dir, pattern string) (f *os.File, err error) {
	f, err = u.executer.TempFile(dir, pattern)
	if err != nil {
		return nil, err
	}

	user, err := user.Lookup(u.user)
	if err != nil {
		return nil, err
	}

	uid, err := strconv.Atoi(user.Uid)
	if err != nil {
		return nil, err
	}
	gid, err := strconv.Atoi(user.Gid)
	if err != nil {
		return nil, err
	}

	if err := f.Chown(uid, gid); err != nil {
		return nil, err
	}
	return f, nil
}

func (u *userExecuter) LookPath(file string) (string, error) {
	return u.executer.LookPath(file)
}
