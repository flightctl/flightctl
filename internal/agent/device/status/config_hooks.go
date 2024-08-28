package status

import (
	"context"
	"errors"
	"fmt"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/agent/device/hook"
	"github.com/flightctl/flightctl/pkg/log"
)

var _ Exporter = (*Hooks)(nil)

// Hooks collects config hook status.
type Hooks struct {
	manager hook.Manager
	log     *log.PrefixLogger
}

func newHooks(log *log.PrefixLogger, manager hook.Manager) *Hooks {
	return &Hooks{
		manager: manager,
		log:     log,
	}
}

// Export returns the status of the config hooks.
func (s *Hooks) Export(ctx context.Context, status *v1alpha1.DeviceStatus) error {
	if err := errors.Join(s.manager.Errors()...); err != nil {
		return fmt.Errorf("hook manager: %v", err)
	}
	return nil
}

func (s *Hooks) SetProperties(spec *v1alpha1.RenderedDeviceSpec) {
}
