package os

import (
	"context"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/agent/device/dependency"
	"github.com/flightctl/flightctl/internal/agent/device/status"
	"github.com/flightctl/flightctl/pkg/log"
)

var _ Manager = (*simManager)(nil)

type simManager struct {
	log *log.PrefixLogger
}

// newSimManager creates a new simulation OS manager that performs no-op operations
// The simulation manager exists to prevent potentially harmful operations when
// running in simulation mode, particularly avoiding the download and execution
// of OCI artifacts that could contain malicious content or consume unnecessary
// system resources.
func newSimManager(log *log.PrefixLogger) Manager {
	return &simManager{
		log: log,
	}
}

func (m *simManager) BeforeUpdate(ctx context.Context, current, desired *v1alpha1.DeviceSpec) error {
	// Check if OS spec is changing and log warning
	if current != nil && current.Os != nil && desired != nil && desired.Os != nil {
		if current.Os.Image != desired.Os.Image {
			m.log.Warnf("OS update detected but not supported in simulation mode: current=%s, desired=%s",
				current.Os.Image, desired.Os.Image)
		}
	}
	return nil
}

func (m *simManager) AfterUpdate(ctx context.Context, desired *v1alpha1.DeviceSpec) error {
	return nil
}

func (m *simManager) Reboot(ctx context.Context, desired *v1alpha1.DeviceSpec) error {
	return nil
}

func (m *simManager) CollectOCITargets(ctx context.Context, current, desired *v1alpha1.DeviceSpec) ([]dependency.OCIPullTarget, error) {
	return nil, nil
}

func (m *simManager) Status(ctx context.Context, status *v1alpha1.DeviceStatus, _ ...status.CollectorOpt) error {
	status.Os.Image = "simulated"
	status.Os.ImageDigest = "simulated"
	return nil
}
