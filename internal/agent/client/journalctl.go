package client

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/pkg/executer"
)

const (
	journalctlCommand = "/usr/bin/journalctl"
)

func NewJournalctl(exec executer.Executer) *Journalctl {
	return &Journalctl{
		exec: exec,
	}
}

type Journalctl struct {
	exec executer.Executer
}

type logOptions struct {
	args []string
}

type LogOptions func(*logOptions)

func WithLogUnit(unit string) LogOptions {
	return func(o *logOptions) {
		o.args = append(o.args, "-u", unit)
	}
}

func WithLogTag(tag string) LogOptions {
	return func(o *logOptions) {
		o.args = append(o.args, "-t", tag)
	}
}

func WithLogSince(t time.Time) LogOptions {
	return func(o *logOptions) {
		o.args = append(o.args, "--since", t.Format("2006-01-02 15:04:05"))
	}
}

func (j *Journalctl) Logs(ctx context.Context, options ...LogOptions) ([]string, error) {
	opts := logOptions{args: []string{"-o", "cat"}}
	for _, option := range options {
		option(&opts)
	}

	stdout, stderr, exitCode := j.exec.ExecuteWithContext(ctx, journalctlCommand, opts.args...)
	if exitCode != 0 {
		return nil, fmt.Errorf("journalctl logs: %w", errors.FromStderr(stderr, exitCode))
	}

	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	var result []string
	for _, line := range lines {
		if line != "" {
			result = append(result, strings.TrimSpace(line))
		}
	}

	return result, nil
}
