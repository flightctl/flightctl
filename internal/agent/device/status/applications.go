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
	applicationsStatus, applicationSummary, err := a.manager.Status()
	if err != nil {
		return err
	}

	status.ApplicationsSummary.Status = applicationSummary.Status
	status.ApplicationsSummary.Info = applicationSummary.Info
	status.Applications = applicationsStatus
	return nil
}
