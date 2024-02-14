package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/pkg/errors"
	"k8s.io/klog/v2"
)

type SystemDController struct {
	exec executer.Executer
}

func NewSystemDController() *SystemDController {
	return &SystemDController{
		exec: &executer.CommonExecuter{},
	}
}

func NewSystemDControllerWithExecuter(exec executer.Executer) *SystemDController {
	return &SystemDController{
		exec: exec,
	}
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

type SystemDUnitList []SystemDUnitListEntry
type SystemDUnitListEntry struct {
	Unit        string `json:"unit"`
	LoadState   string `json:"load"`
	ActiveState string `json:"active"`
	Sub         string `json:"sub"`
	Description string `json:"description"`
}

func (c *SystemDController) SetStatus(r *api.Device) (bool, error) {
	if r == nil {
		return false, nil
	}

	if r.Spec.Systemd == nil || r.Spec.Systemd.MatchPatterns == nil {
		return false, nil
	}

	execCtx, cancel := context.WithTimeout(context.TODO(), 2*time.Minute)
	defer cancel()
	args := append([]string{"list-units", "--all", "--output", "json"}, (*r.Spec.Systemd.MatchPatterns)...)
	out, errOut, exitCode := c.exec.ExecuteWithContext(execCtx, "/usr/bin/systemctl", args...)
	if exitCode != 0 {
		msg := fmt.Sprintf("listing systemd units failed with code %d: %s\n", exitCode, errOut)
		err := errors.Errorf(msg)
		klog.Errorf(msg)
		return false, err
	}

	var units SystemDUnitList
	if err := json.Unmarshal([]byte(out), &units); err != nil {
		klog.Errorf("error unmarshalling systemctl list-units output: %s\n", err)
		return false, err
	}

	deviceSystemdUnitStatus := make([]api.DeviceSystemdUnitStatus, len(units))
	for i, u := range units {
		deviceSystemdUnitStatus[i].Name = u.Unit
		deviceSystemdUnitStatus[i].LoadState = u.LoadState
		deviceSystemdUnitStatus[i].ActiveState = u.ActiveState
	}
	r.Status.SystemdUnits = &deviceSystemdUnitStatus

	return true, nil
}
