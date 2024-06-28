package status

import (
	"context"
	"fmt"
	"runtime"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/google/cadvisor/fs"
	"github.com/google/cadvisor/machine"
	"github.com/google/cadvisor/utils/sysfs"
)

var _ Exporter = (*SystemInfo)(nil)

// SystemInfo collects system information.
type SystemInfo struct {
	sysFs sysfs.SysFs
}

func newSystemInfo() *SystemInfo {
	return &SystemInfo{
		sysFs: sysfs.NewRealSysFs(),
	}
}

func (s *SystemInfo) Export(ctx context.Context, status *v1alpha1.DeviceStatus) error {
	if !status.SystemInfo.IsEmpty() {
		return nil
	}

	fsInfo, err := fs.NewFsInfo(fs.Context{})
	if err != nil {
		return fmt.Errorf("getting file system info: %w", err)
	}
	inHostNamespace := true
	info, err := machine.Info(s.sysFs, fsInfo, inHostNamespace)
	if err != nil {
		return fmt.Errorf("getting machine info: %w", err)
	}

	status.SystemInfo = v1alpha1.DeviceSystemInfo{
		Architecture:    runtime.GOARCH,
		OperatingSystem: runtime.GOOS,
		BootID:          info.BootID,
	}

	return nil
}

func (c *SystemInfo) SetProperties(spec *v1alpha1.RenderedDeviceSpec) {
}
