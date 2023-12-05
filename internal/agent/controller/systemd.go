package controller

import (
	"context"
	"time"

	"github.com/coreos/go-systemd/v22/dbus"
	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/agent"
)

type SystemDController struct {
	agent *agent.DeviceAgent
}

func NewSystemDController() *SystemDController {
	return &SystemDController{}
}

func (c *SystemDController) SetDeviceAgent(a *agent.DeviceAgent) {
	c.agent = a
}

func (c *SystemDController) NeedsUpdate(r *api.Device) bool {
	return false // this controller only updates status
}

func (c *SystemDController) StageUpdate(r *api.Device) (bool, error) {
	return true, nil // this controller only updates status
}

func (c *SystemDController) ApplyUpdate(r *api.Device) (bool, error) {
	return true, nil // this controller only updates status
}

func (c *SystemDController) FinalizeUpdate(r *api.Device) (bool, error) {
	return true, nil // this controller only updates status
}

func (c *SystemDController) SetStatus(r *api.Device) (bool, error) {
	if r == nil {
		return false, nil
	}

	if r.Spec.Systemd == nil || r.Spec.Systemd.MatchPatterns == nil {
		return false, nil
	}

	conn, err := dbus.NewSystemdConnectionContext(context.TODO())
	if err != nil {
		return false, err
	}

	ctx, _ := context.WithTimeout(context.Background(), time.Second)
	unitStatuses, err := conn.ListUnitsByPatternsContext(ctx, nil, *r.Spec.Systemd.MatchPatterns)
	if err != nil {
		return false, err
	}

	apiSystemdUnits := make([]api.DeviceSystemdUnitStatus, len(unitStatuses))
	for i, v := range unitStatuses {
		apiSystemdUnits[i].Name = v.Name
		apiSystemdUnits[i].ActiveState = v.ActiveState
		apiSystemdUnits[i].LoadState = v.LoadState
	}
	r.Status.SystemdUnits = &apiSystemdUnits
	return true, nil
}
