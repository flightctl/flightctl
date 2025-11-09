package client

import (
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

type SystemDActiveStateType string
type SystemDSubStateType string

const (
	SystemDActiveStateActivating SystemDActiveStateType = "activating"
	SystemDActiveStateActive     SystemDActiveStateType = "active"
	SystemDActiveStateFailed     SystemDActiveStateType = "failed"
	SystemDActiveStateInactive   SystemDActiveStateType = "inactive"

	SystemDSubStateStartPre  SystemDSubStateType = "start-pre"
	SystemDSubStateStartPost SystemDSubStateType = "start-post"
	SystemDSubStateRunning   SystemDSubStateType = "running"
	SystemDSubStateExited    SystemDSubStateType = "exited"
	SystemDSubStateDead      SystemDSubStateType = "dead"
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
	Unit        string                 `json:"unit"`
	LoadState   string                 `json:"load"`
	ActiveState SystemDActiveStateType `json:"active"`
	Sub         SystemDSubStateType    `json:"sub"`
	Description string                 `json:"description"`
}

func (s *Systemd) ListUnitsByMatchPattern(ctx context.Context, matchPatterns []string) ([]SystemDUnitListEntry, error) {
	execCtx, cancel := context.WithTimeout(ctx, defaultSystemctlTimeout)
	defer cancel()
	args := append([]string{"list-units", "--all", "--output", "json"}, matchPatterns...)
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
