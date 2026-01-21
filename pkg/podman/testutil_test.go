package podman_test

type ExecReturn struct {
	stdout   string
	stderr   string
	exitCode int
}

func NewExecReturn(stdout string, stderr string, exitCode int) ExecReturn {
	return ExecReturn{
		stdout:   stdout,
		stderr:   stderr,
		exitCode: exitCode,
	}
}
