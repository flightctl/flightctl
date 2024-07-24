package client

import (
	"context"
	"fmt"

	"github.com/flightctl/flightctl/pkg/executer"
)

const (
	systemctlCommand = "/usr/bin/systemctl"
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
	_, errOut, exitCode := s.exec.ExecuteWithContext(ctx, systemctlCommand, args...)
	if exitCode != 0 {
		return fmt.Errorf("failed to reload systemd unit:%s  %d: %s", name, exitCode, errOut)
	}
	return nil
}

func (s *Systemd) Start(ctx context.Context, name string) error {
	args := []string{"enable", name}
	_, errOut, exitCode := s.exec.ExecuteWithContext(ctx, systemctlCommand, args...)
	if exitCode != 0 {
		return fmt.Errorf("failed to enable systemd unit:%s  %d: %s", name, exitCode, errOut)
	}

	args = []string{"start", name}
	_, errOut, exitCode = s.exec.ExecuteWithContext(ctx, systemctlCommand, args...)
	if exitCode != 0 {
		return fmt.Errorf("failed to start systemd unit:%s  %d: %s", name, exitCode, errOut)
	}

	return nil
}

func (s *Systemd) Stop(ctx context.Context, name string) error {
	args := []string{"stop", name}
	_, errOut, exitCode := s.exec.ExecuteWithContext(ctx, systemctlCommand, args...)
	if exitCode != 0 {
		return fmt.Errorf("failed to stop systemd unit:%s  %d: %s", name, exitCode, errOut)
	}
	return nil
}

func (s *Systemd) Restart(ctx context.Context, name string) error {
	args := []string{"restart", name}
	_, errOut, exitCode := s.exec.ExecuteWithContext(ctx, systemctlCommand, args...)
	if exitCode != 0 {
		return fmt.Errorf("failed to restart systemd unit:%s  %d: %s", name, exitCode, errOut)
	}
	return nil
}

func (s *Systemd) Disable(ctx context.Context, name string) error {
	args := []string{"disable", name}
	_, errOut, exitCode := s.exec.ExecuteWithContext(ctx, systemctlCommand, args...)
	if exitCode != 0 {
		return fmt.Errorf("failed to disable systemd unit:%s  %d: %s", name, exitCode, errOut)
	}
	return nil
}

func (s *Systemd) Enable(ctx context.Context, name string) error {
	args := []string{"enable", name}
	_, errOut, exitCode := s.exec.ExecuteWithContext(ctx, systemctlCommand, args...)
	if exitCode != 0 {
		return fmt.Errorf("failed to enable systemd unit:%s  %d: %s", name, exitCode, errOut)
	}
	return nil
}

func (s *Systemd) DaemonReload(ctx context.Context) error {
	args := []string{"daemon-reload"}
	_, errOut, exitCode := s.exec.ExecuteWithContext(ctx, systemctlCommand, args...)
	if exitCode != 0 {
		return fmt.Errorf("failed to daemon-reload systemd  %d: %s", exitCode, errOut)
	}
	return nil
}
