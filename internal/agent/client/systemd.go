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
	systemctlCommand        = "/usr/bin/systemctl"
	defaultSystemctlTimeout = time.Minute
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

func (s *Systemd) Start(ctx context.Context, name string) error {
	args := []string{"start", name}
	_, stderr, exitCode := s.exec.ExecuteWithContext(ctx, systemctlCommand, args...)
	if exitCode != 0 {
		return fmt.Errorf("start systemd unit: %s: %w", name, errors.FromStderr(stderr, exitCode))
	}

	return nil
}

func (s *Systemd) Stop(ctx context.Context, name string) error {
	args := []string{"stop", name}
	_, stderr, exitCode := s.exec.ExecuteWithContext(ctx, systemctlCommand, args...)
	if exitCode != 0 {
		return fmt.Errorf("stop systemd unit: %s: %w", name, errors.FromStderr(stderr, exitCode))
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

func (s *Systemd) ListUnitsByMatchPattern(ctx context.Context, matchPatterns []string) (string, error) {
	execCtx, cancel := context.WithTimeout(ctx, defaultSystemctlTimeout)
	defer cancel()
	args := append([]string{"list-units", "--all", "--output", "json"}, matchPatterns...)
	stdout, stderr, exitCode := s.exec.ExecuteWithContext(execCtx, systemctlCommand, args...)
	if exitCode != 0 {
		return "", fmt.Errorf("list systemd units: %w", errors.FromStderr(stderr, exitCode))
	}
	out := strings.TrimSpace(stdout)
	return out, nil
}

// IsActive checks if a systemd unit is active or activating
func (s *Systemd) IsActive(ctx context.Context, unit string) (bool, error) {
	execCtx, cancel := context.WithTimeout(ctx, defaultSystemctlTimeout)
	defer cancel()

	args := []string{"is-active", unit}
	stdout, stderr, exitCode := s.exec.ExecuteWithContext(execCtx, systemctlCommand, args...)

	// exit code 3 means unit is not active, which is not an error
	if exitCode != 0 && exitCode != 3 {
		return false, fmt.Errorf("systemctl is-active %s: %w", unit, errors.FromStderr(stderr, exitCode))
	}

	status := strings.TrimSpace(stdout)
	switch status {
	case "active", "activating":
		return true, nil
	case "inactive", "deactivating", "failed", "unknown":
		return false, nil
	default:
		return false, nil
	}
}
