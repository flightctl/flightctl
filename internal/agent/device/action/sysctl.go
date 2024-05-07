package action

import (
	"context"
	"fmt"

	"github.com/flightctl/flightctl/pkg/executer"
)

const (
	sysctlCommand = "/usr/sbin/sysctl"
)

var _ AppActions = (*Systemd)(nil)

func NewSysctl(exec executer.Executer) *Sysctl {
	return &Sysctl{
		exec: exec,
	}
}

type Sysctl struct {
	exec executer.Executer
}

func (s *Sysctl) Reload(ctx context.Context, _ string) error {
	args := []string{"--system"}
	_, errOut, exitCode := s.exec.ExecuteWithContext(ctx, sysctlCommand, args...)
	if exitCode != 0 {
		return fmt.Errorf("failed to reload sysctl exit code: %d: %s", exitCode, errOut)
	}
	return nil
}

func (s *Sysctl) Start(context.Context, string) error {
	return nil
}

func (s *Sysctl) Stop(context.Context, string) error {
	return nil
}

func (s *Sysctl) Restart(context.Context, string) error {
	return nil
}

func (s *Sysctl) Reboot(context.Context) error {
	return nil
}
