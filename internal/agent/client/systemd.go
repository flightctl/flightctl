package client

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/pkg/executer"
)

const (
	systemctlCommand        = "/usr/bin/systemctl"
	defaultSystemctlTimeout = time.Minute
)

var (
	ErrNoSystemDUnits = errors.New("no units defined")
)

func NewSystemd(exec executer.Executer) *Systemd {
	return &Systemd{
		exec: exec,
	}
}

type Systemd struct {
	exec executer.Executer
}

func (s *Systemd) Reload(ctx context.Context, name string) error {
	args := []string{"reload", name}
	_, stderr, exitCode := s.exec.ExecuteWithContext(ctx, systemctlCommand, args...)
	if exitCode != 0 {
		return fmt.Errorf("reload systemd unit:%s :%w", name, errors.FromStderr(stderr, exitCode))
	}
	return nil
}

func (s *Systemd) Start(ctx context.Context, units ...string) error {
	if len(units) == 0 {
		return ErrNoSystemDUnits
	}
	args := append([]string{"start"}, units...)
	_, stderr, exitCode := s.exec.ExecuteWithContext(ctx, systemctlCommand, args...)
	if exitCode != 0 {
		return fmt.Errorf("start systemd unit(s): %q: %w", strings.Join(units, ","), errors.FromStderr(stderr, exitCode))
	}
	return nil
}

func (s *Systemd) Stop(ctx context.Context, units ...string) error {
	if len(units) == 0 {
		return ErrNoSystemDUnits
	}
	args := append([]string{"stop"}, units...)
	_, stderr, exitCode := s.exec.ExecuteWithContext(ctx, systemctlCommand, args...)
	if exitCode != 0 {
		return fmt.Errorf("stop systemd unit(s): %q: %w", strings.Join(units, ","), errors.FromStderr(stderr, exitCode))
	}
	return nil
}

func (s *Systemd) Reboot(ctx context.Context) error {
	args := []string{"reboot"}
	_, stderr, exitCode := s.exec.ExecuteWithContext(ctx, systemctlCommand, args...)
	if exitCode != 0 {
		return fmt.Errorf("reboot systemd: %w", errors.FromStderr(stderr, exitCode))
	}
	return nil
}

func (s *Systemd) Restart(ctx context.Context, name string) error {
	args := []string{"restart", name}
	_, stderr, exitCode := s.exec.ExecuteWithContext(ctx, systemctlCommand, args...)
	if exitCode != 0 {
		return fmt.Errorf("restart systemd unit: %s: %w", name, errors.FromStderr(stderr, exitCode))
	}
	return nil
}

func (s *Systemd) Disable(ctx context.Context, name string) error {
	args := []string{"disable", name}
	_, stderr, exitCode := s.exec.ExecuteWithContext(ctx, systemctlCommand, args...)
	if exitCode != 0 {
		return fmt.Errorf("disable systemd unit: %s: %w", name, errors.FromStderr(stderr, exitCode))
	}
	return nil
}

func (s *Systemd) Enable(ctx context.Context, name string) error {
	args := []string{"enable", name}
	_, stderr, exitCode := s.exec.ExecuteWithContext(ctx, systemctlCommand, args...)
	if exitCode != 0 {
		return fmt.Errorf("enable systemd unit: %s: %w", name, errors.FromStderr(stderr, exitCode))
	}
	return nil
}

func (s *Systemd) DaemonReload(ctx context.Context) error {
	args := []string{"daemon-reload"}
	_, stderr, exitCode := s.exec.ExecuteWithContext(ctx, systemctlCommand, args...)
	if exitCode != 0 {
		return fmt.Errorf("daemon-reload systemd: %w", errors.FromStderr(stderr, exitCode))
	}
	return nil
}

func (s *Systemd) ResetFailed(ctx context.Context, units ...string) error {
	if len(units) == 0 {
		return ErrNoSystemDUnits
	}
	args := append([]string{"reset-failed"}, units...)
	_, stderr, exitCode := s.exec.ExecuteWithContext(ctx, systemctlCommand, args...)
	if exitCode != 0 {
		return fmt.Errorf("reset-failed systemd unit(s): %q: %w", strings.Join(units, ","), errors.FromStderr(stderr, exitCode))
	}
	return nil
}

type SystemDUnitListEntry struct {
	Unit        string `json:"unit"`
	LoadState   string `json:"load"`
	ActiveState string `json:"active"`
	SubState    string `json:"sub"`
	Description string `json:"description"`
}

func (s *Systemd) ListUnitsByMatchPattern(ctx context.Context, matchPatterns []string) ([]SystemDUnitListEntry, error) {
	execCtx, cancel := context.WithTimeout(ctx, defaultSystemctlTimeout)
	defer cancel()

	args := append([]string{"list-units", "--all", "--output", "json", "--"}, matchPatterns...)
	stdout, stderr, exitCode := s.exec.ExecuteWithContext(execCtx, systemctlCommand, args...)
	if exitCode != 0 {
		return nil, fmt.Errorf("list systemd units: %w", errors.FromStderr(stderr, exitCode))
	}

	var units []SystemDUnitListEntry
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &units); err != nil {
		return nil, fmt.Errorf("unmarshalling systemctl list-units output: %w", err)
	}
	return units, nil
}

func (s *Systemd) ShowByMatchPattern(ctx context.Context, matchPatterns []string) ([]map[string]string, error) {
	execCtx, cancel := context.WithTimeout(ctx, defaultSystemctlTimeout)
	defer cancel()

	args := append([]string{"show", "--all", "--"}, matchPatterns...)
	stdout, stderr, exitCode := s.exec.ExecuteWithContext(execCtx, systemctlCommand, args...)
	if exitCode != 0 {
		return nil, fmt.Errorf("show systemd units: %w", errors.FromStderr(stderr, exitCode))
	}

	var units []map[string]string
	currentUnit := make(map[string]string)

	scanner := bufio.NewScanner(strings.NewReader(stdout))
	for scanner.Scan() {
		line := scanner.Text()

		if line == "" {
			if len(currentUnit) > 0 {
				units = append(units, currentUnit)
				currentUnit = make(map[string]string)
			}
			continue
		}

		parts := strings.SplitN(line, "=", 2)
		if len(parts) == 2 {
			key := parts[0]
			value := parts[1]
			currentUnit[key] = value
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}

	// Add the last unit if the output does not end with a blank line
	if len(currentUnit) > 0 {
		units = append(units, currentUnit)
	}

	return units, nil
}

// ListDependencies returns the list of units that the specified unit depends on.
// Uses `systemctl list-dependencies --plain` to get a flat list of dependencies.
func (s *Systemd) ListDependencies(ctx context.Context, unit string) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, defaultSystemctlTimeout)
	defer cancel()

	args := []string{"list-dependencies", "--plain", "--no-pager", unit}
	stdout, stderr, exitCode := s.exec.ExecuteWithContext(ctx, systemctlCommand, args...)
	if exitCode != 0 {
		return nil, fmt.Errorf("list-dependencies for %s: %w", unit, errors.FromStderr(stderr, exitCode))
	}

	var deps []string
	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || line == unit {
			continue
		}
		deps = append(deps, line)
	}
	return deps, nil
}

// SystemdJob represents a systemd job from list-jobs
type SystemdJob struct {
	Job     string
	Unit    string
	JobType string
	State   string
}

// ListJobs lists current systemd jobs in progress
// This is more reliable than is-active for detecting pending shutdown/reboot
func (s *Systemd) ListJobs(ctx context.Context) ([]SystemdJob, error) {
	ctx, cancel := context.WithTimeout(ctx, defaultSystemctlTimeout)
	defer cancel()

	args := []string{"list-jobs", "--no-pager", "--no-legend"}
	stdout, stderr, exitCode := s.exec.ExecuteWithContext(ctx, systemctlCommand, args...)
	if exitCode != 0 {
		return nil, fmt.Errorf("systemctl list-jobs: %w", errors.FromStderr(stderr, exitCode))
	}

	var jobs []SystemdJob
	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	for _, line := range lines {
		if line == "" {
			continue
		}

		fields := strings.Fields(line)
		if len(fields) >= 4 {
			jobs = append(jobs, SystemdJob{
				Job:     fields[0],
				Unit:    fields[1],
				JobType: fields[2],
				State:   fields[3],
			})
		}
	}

	return jobs, nil
}

type systemdShowOpts struct {
	args []string
}

type SystemdShowOptions func(*systemdShowOpts)

func WithShowRestarts() SystemdShowOptions {
	return func(opts *systemdShowOpts) {
		opts.args = append(opts.args, "-p", "NRestarts", "--value")
	}
}

func WithShowLoadState() SystemdShowOptions {
	return func(opts *systemdShowOpts) {
		opts.args = append(opts.args, "-p", "LoadState", "--value")
	}
}

func (s *Systemd) Show(ctx context.Context, unit string, opts ...SystemdShowOptions) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, defaultSystemctlTimeout)
	defer cancel()

	showOpts := &systemdShowOpts{}
	for _, opt := range opts {
		opt(showOpts)
	}

	args := append([]string{"show", "--no-pager", unit}, showOpts.args...)
	stdout, stderr, exitCode := s.exec.ExecuteWithContext(ctx, systemctlCommand, args...)
	if exitCode != 0 {
		return nil, fmt.Errorf("systemctl show: %w", errors.FromStderr(stderr, exitCode))
	}

	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	for i := range lines {
		lines[i] = strings.TrimSpace(lines[i])
	}

	return lines, nil
}
