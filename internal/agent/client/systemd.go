package client

import (
	"context"
	"fmt"
	"strings"
	"time"

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
		return fmt.Errorf("failed to reload systemd unit:%s  %d: %s", name, exitCode, stderr)
	}
	return nil
}

func (s *Systemd) Start(ctx context.Context, name string) error {
	args := []string{"start", name}
	_, stderr, exitCode := s.exec.ExecuteWithContext(ctx, systemctlCommand, args...)
	if exitCode != 0 {
		return fmt.Errorf("failed to start systemd unit:%s  %d: %s", name, exitCode, stderr)
	}

	return nil
}

func (s *Systemd) Stop(ctx context.Context, name string) error {
	args := []string{"stop", name}
	_, stderr, exitCode := s.exec.ExecuteWithContext(ctx, systemctlCommand, args...)
	if exitCode != 0 {
		return fmt.Errorf("failed to stop systemd unit:%s  %d: %s", name, exitCode, stderr)
	}
	return nil
}

func (s *Systemd) Restart(ctx context.Context, name string) error {
	args := []string{"restart", name}
	_, stderr, exitCode := s.exec.ExecuteWithContext(ctx, systemctlCommand, args...)
	if exitCode != 0 {
		return fmt.Errorf("failed to restart systemd unit:%s  %d: %s", name, exitCode, stderr)
	}
	return nil
}

func (s *Systemd) Disable(ctx context.Context, name string) error {
	args := []string{"disable", name}
	_, stderr, exitCode := s.exec.ExecuteWithContext(ctx, systemctlCommand, args...)
	if exitCode != 0 {
		return fmt.Errorf("failed to disable systemd unit:%s  %d: %s", name, exitCode, stderr)
	}
	return nil
}

func (s *Systemd) Enable(ctx context.Context, name string) error {
	args := []string{"enable", name}
	_, stderr, exitCode := s.exec.ExecuteWithContext(ctx, systemctlCommand, args...)
	if exitCode != 0 {
		return fmt.Errorf("failed to enable systemd unit:%s  %d: %s", name, exitCode, stderr)
	}
	return nil
}

func (s *Systemd) DaemonReload(ctx context.Context) error {
	args := []string{"daemon-reload"}
	_, stderr, exitCode := s.exec.ExecuteWithContext(ctx, systemctlCommand, args...)
	if exitCode != 0 {
		return fmt.Errorf("failed to daemon-reload systemd  %d: %s", exitCode, stderr)
	}
	return nil
}

func (s *Systemd) ListUnitsByMatchPattern(ctx context.Context, matchPatterns []string) (string, error) {
	execCtx, cancel := context.WithTimeout(ctx, defaultSystemctlTimeout)
	defer cancel()
	args := append([]string{"list-units", "--all", "--output", "json"}, matchPatterns...)
	stdout, stderr, exitCode := s.exec.ExecuteWithContext(execCtx, systemctlCommand, args...)
	if exitCode != 0 {
		return "", fmt.Errorf("failed listing systemd units with code %d: %s", exitCode, stderr)
	}
	out := strings.TrimSpace(stdout)
	return out, nil
}
