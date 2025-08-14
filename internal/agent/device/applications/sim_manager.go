package applications

import (
	"context"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/agent/device/applications/provider"
	"github.com/flightctl/flightctl/internal/agent/device/dependency"
	"github.com/flightctl/flightctl/internal/agent/device/status"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/samber/lo"
)

var _ Manager = (*simManager)(nil)

type simManager struct {
	log *log.PrefixLogger
}

// newSimManager creates a new simulation applications manager that performs no-op operations
// The simulation manager exists to prevent potentially harmful operations when
// running in simulation mode, particularly avoiding the download and execution
// of OCI artifacts that could contain malicious content or consume unnecessary
// system resources. It also prevents actual container lifecycle management
// operations that are inappropriate in simulation contexts.
func newSimManager(log *log.PrefixLogger) Manager {
	return &simManager{
		log: log,
	}
}

func (m *simManager) Ensure(ctx context.Context, provider provider.Provider) error {
	m.log.Warn("Application ensure operation not supported in simulation mode")
	return nil
}

func (m *simManager) Remove(ctx context.Context, provider provider.Provider) error {
	m.log.Warn("Application remove operation not supported in simulation mode")
	return nil
}

func (m *simManager) Update(ctx context.Context, provider provider.Provider) error {
	m.log.Warn("Application update operation not supported in simulation mode")
	return nil
}

func (m *simManager) BeforeUpdate(ctx context.Context, desired *v1alpha1.DeviceSpec) error {
	return nil
}

func (m *simManager) AfterUpdate(ctx context.Context) error {
	return nil
}

func (m *simManager) Stop(ctx context.Context) error {
	return nil
}

func (m *simManager) CollectOCITargets(ctx context.Context, current, desired *v1alpha1.DeviceSpec) ([]dependency.OCIPullTarget, error) {
	return nil, nil
}

func (m *simManager) Status(ctx context.Context, status *v1alpha1.DeviceStatus, _ ...status.CollectorOpt) error {
	status.Applications = []v1alpha1.DeviceApplicationStatus{}
	status.ApplicationsSummary.Status = v1alpha1.ApplicationsSummaryStatusHealthy
	status.ApplicationsSummary.Info = lo.ToPtr("simulated")
	return nil
}
