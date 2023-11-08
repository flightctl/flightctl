package agent

import (
	api "github.com/flightctl/flightctl/api/v1alpha1"
)

type DeviceAgentController interface {
	SetDeviceAgent(a *DeviceAgent)

	NeedsUpdate(r *api.Device) bool
	StageUpdate(r *api.Device) (bool, error)
	ApplyUpdate(r *api.Device) (bool, error)
	FinalizeUpdate(r *api.Device) (bool, error)

	SetStatus(r *api.Device) (bool, error)
}

func (a *DeviceAgent) AddController(c DeviceAgentController) *DeviceAgent {
	c.SetDeviceAgent(a)
	a.controllers = append(a.controllers, c)
	return a
}

func (a *DeviceAgent) NeedsUpdate(r *api.Device) bool {
	for _, c := range a.controllers {
		if c.NeedsUpdate(r) {
			return true
		}
	}
	return false
}

func (a *DeviceAgent) StageUpdate(r *api.Device) (bool, error) {
	allComplete := true
	for _, c := range a.controllers {
		complete, err := c.StageUpdate(r)
		if err != nil {
			return false, err
		}
		allComplete = allComplete && complete
	}
	return allComplete, nil
}

func (a *DeviceAgent) ApplyUpdate(r *api.Device) (bool, error) {
	allComplete := true
	for _, c := range a.controllers {
		complete, err := c.ApplyUpdate(r)
		if err != nil {
			return false, err
		}
		allComplete = allComplete && complete
	}
	return allComplete, nil
}

func (a *DeviceAgent) FinalizeUpdate(r *api.Device) (bool, error) {
	allComplete := true
	for _, c := range a.controllers {
		complete, err := c.FinalizeUpdate(r)
		if err != nil {
			return false, err
		}
		allComplete = allComplete && complete
	}
	return allComplete, nil
}

func (a *DeviceAgent) SetStatus(r *api.Device) (bool, error) {
	for _, c := range a.controllers {
		_, err := c.SetStatus(r)
		if err != nil {
			return false, err
		}
	}
	return true, nil
}
