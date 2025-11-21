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

func (j *Journalctl) logsSince(ctx context.Context, baseArgs []string, since time.Time) ([]string, error) {
	formattedTime := since.Format("2006-01-02 15:04:05")
	args := append(baseArgs, "-o", "cat", "--since", formattedTime)
	stdout, stderr, exitCode := j.exec.ExecuteWithContext(ctx, journalctlCommand, args...)
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

func (j *Journalctl) LogsByTagSince(ctx context.Context, tag string, since time.Time) ([]string, error) {
	return j.logsSince(ctx, []string{"-t", tag}, since)
}

func (j *Journalctl) LogsByUnitSince(ctx context.Context, unit string, since time.Time) ([]string, error) {
	return j.logsSince(ctx, []string{"-u", unit}, since)
}
