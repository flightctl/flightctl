package action

import (
	"context"
	"fmt"

	"github.com/flightctl/flightctl/pkg/executer"
)

var _ AppActions = (*Systemd)(nil)

func NewSystemD(exec executer.Executer) *Systemd {
	return &Systemd{
		exec: exec,
	}
}

type Systemd struct {
	exec executer.Executer
}

func (s *Systemd) Reload(ctx context.Context, name string) error {
	args := []string{"reload", name}
	_, errOut, exitCode := s.exec.ExecuteWithContext(ctx, systemdCommand, args...)
	if exitCode != 0 {
		return fmt.Errorf("failed to reload systemd unit:%s  %d: %s", name, exitCode, errOut)
	}
	return nil
}

func (s *Systemd) Start(ctx context.Context, name string) error {
	args := []string{"enable", name}
	_, errOut, exitCode := s.exec.ExecuteWithContext(ctx, systemdCommand, args...)
	if exitCode != 0 {
		return fmt.Errorf("failed to reload systemd unit:%s  %d: %s", name, exitCode, errOut)
	}

	args = []string{"start", name}
	_, errOut, exitCode = s.exec.ExecuteWithContext(ctx, systemdCommand, args...)
	if exitCode != 0 {
		return fmt.Errorf("failed to reload systemd unit:%s  %d: %s", name, exitCode, errOut)
	}
	return nil
}

func (s *Systemd) Stop(ctx context.Context, name string) error {
	args := []string{"stop", name}
	_, errOut, exitCode := s.exec.ExecuteWithContext(ctx, systemdCommand, args...)
	if exitCode != 0 {
		return fmt.Errorf("failed to stop systemd unit:%s  %d: %s", name, exitCode, errOut)
	}
	return nil
}

func (s *Systemd) Restart(ctx context.Context, name string) error {
	args := []string{"restart", name}
	_, errOut, exitCode := s.exec.ExecuteWithContext(ctx, systemdCommand, args...)
	if exitCode != 0 {
		return fmt.Errorf("failed to restart of systemd unit:%s  %d: %s", name, exitCode, errOut)
	}
	return nil
}
