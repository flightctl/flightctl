package action

import (
	"context"
	"fmt"

	"github.com/flightctl/flightctl/pkg/executer"
)

const (
	podmanCommand = "/usr/bin/podman"
)

var _ AppActions = (*Systemd)(nil)

func NewPodman(exec executer.Executer) *Podman {
	return &Podman{
		exec: exec,
	}
}

type Podman struct {
	exec executer.Executer
}

func (s *Podman) Reload(ctx context.Context, name string) error {
	args := []string{"reload", name}
	_, errOut, exitCode := s.exec.ExecuteWithContext(ctx, systemdCommand, args...)
	if exitCode != 0 {
		return fmt.Errorf("failed to reload systemd unit:%s  %d: %s", name, exitCode, errOut)
	}
	return nil
}

func (s *Podman) Start(ctx context.Context, name string) error {
	args := []string{"play", "kube", name}
	_, errOut, exitCode := s.exec.ExecuteWithContext(ctx, podmanCommand, args...)
	if exitCode != 0 {
		return fmt.Errorf("failed to start podman pod:%s  %d: %s", name, exitCode, errOut)
	}
	return nil
}

func (s *Podman) Stop(ctx context.Context, name string) error {
	// TODO: parse the name from pod yaml
	args := []string{"pod", "stop", name}
	_, errOut, exitCode := s.exec.ExecuteWithContext(ctx, podmanCommand, args...)
	if exitCode != 0 {
		return fmt.Errorf("failed to stop systemd unit:%s  %d: %s", name, exitCode, errOut)
	}

	// remove the pod?
	// remove the container?
	return nil
}

func (s *Podman) Restart(ctx context.Context, name string) error {
	if err := s.Stop(ctx, name); err != nil {
		return fmt.Errorf("failed to restart podman pod: %w", err)
	}

	if err := s.Start(ctx, name); err != nil {
		return fmt.Errorf("failed to restart podman pod: %w", err)
	}

	return nil
}
