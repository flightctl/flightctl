package status

import (
	"context"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/agent/device/applications"
)

type Applications struct {
	manager applications.Manager
}

func newApplications(manager applications.Manager) *Applications {
	return &Applications{
		manager: manager,
	}
}

func (a *Applications) Export(ctx context.Context, status *v1alpha1.DeviceStatus) error {
	applicationStatus, applicationSummary, err := a.manager.Status()


	status.ApplicationsSummary.Status = applicationSummary
	// TODO: update in place vs replace
	status.Applications = applications
	return nil
}

func (a *Applications) SetProperties(spec *v1alpha1.RenderedDeviceSpec) {
}
