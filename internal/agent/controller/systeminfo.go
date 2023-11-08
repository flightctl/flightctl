package controller

import (
	"runtime"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/agent"
	"github.com/google/cadvisor/fs"
	"github.com/google/cadvisor/machine"
	"github.com/google/cadvisor/utils/sysfs"
	"k8s.io/klog/v2"
)

type SystemInfoController struct {
	agent *agent.DeviceAgent

	systemInfo *api.DeviceSystemInfo
}

func NewSystemInfoController() *SystemInfoController {
	return &SystemInfoController{}
}

func (c *SystemInfoController) SetDeviceAgent(a *agent.DeviceAgent) {
	c.agent = a
}

func (c *SystemInfoController) NeedsUpdate(r *api.Device) bool {
	return false // this controller only updates status
}

func (c *SystemInfoController) StageUpdate(r *api.Device) (bool, error) {
	return true, nil // this controller only updates status
}

func (c *SystemInfoController) ApplyUpdate(r *api.Device) (bool, error) {
	return true, nil // this controller only updates status
}

func (c *SystemInfoController) FinalizeUpdate(r *api.Device) (bool, error) {
	return true, nil // this controller only updates status
}

func (c *SystemInfoController) SetStatus(r *api.Device) (bool, error) {
	if r == nil {
		return false, nil
	}
	if c.systemInfo == nil {
		c.systemInfo = &api.DeviceSystemInfo{
			Architecture:    runtime.GOARCH,
			OperatingSystem: runtime.GOOS,
		}

		sysFs := sysfs.NewRealSysFs()
		fsInfo, err := fs.NewFsInfo(fs.Context{})
		if err != nil {
			klog.Errorf("getting file system info: %v", err)
		} else {
			inHostNamespace := true
			info, err := machine.Info(sysFs, fsInfo, inHostNamespace)
			if err != nil {
				klog.Errorf("getting machine info: %v", err)
			} else {
				c.systemInfo.BootID = info.BootID
				c.systemInfo.MachineID = info.MachineID
			}
		}
	}
	r.Status.SystemInfo = c.systemInfo
	return true, nil
}
