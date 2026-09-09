package executer

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os/exec"
	"syscall"
)

type outputBudget struct {
	max   int
	total int
}

func newOutputBudget(max int) *outputBudget {
	return &outputBudget{max: max}
}

type combinedOutputWriter struct {
	dst    *bytes.Buffer
	budget *outputBudget
}

func (b *outputBudget) writerFor(dst *bytes.Buffer) io.Writer {
	return &combinedOutputWriter{dst: dst, budget: b}
}

func (w *combinedOutputWriter) Write(p []byte) (int, error) {
	if w.budget.max <= 0 {
		return w.dst.Write(p)
	}

	remaining := w.budget.max - w.budget.total
	if remaining <= 0 {
		return len(p), nil
	}

	if len(p) > remaining {
		_, err := w.dst.Write(p[:remaining])
		w.budget.total += remaining
		return len(p), err
	}

	written, err := w.dst.Write(p)
	w.budget.total += written
	return len(p), err
}

func (e *commonExecuter) runCmd(ctx context.Context, cmd *exec.Cmd, maxCombinedOutput int) (stdout string, stderr string, exitCode int) {
	var stdoutBytes, stderrBytes bytes.Buffer
	if maxCombinedOutput > 0 {
		budget := newOutputBudget(maxCombinedOutput)
		cmd.Stdout = budget.writerFor(&stdoutBytes)
		cmd.Stderr = budget.writerFor(&stderrBytes)
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
