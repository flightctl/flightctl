package devicestatus

import (
	"context"
	"fmt"
	"runtime"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/tpm"
	"github.com/google/cadvisor/fs"
	"github.com/google/cadvisor/machine"
	"github.com/google/cadvisor/utils/sysfs"
)

var _ Exporter = (*SystemInfo)(nil)

type SystemInfo struct {
	tpmChannel *tpm.TPM
	systemInfo *v1alpha1.DeviceSystemInfo
}

func newSystemInfo(tpmChannel *tpm.TPM) *SystemInfo {
	return &SystemInfo{tpmChannel: tpmChannel}
}

func (c *SystemInfo) Export(ctx context.Context, status *v1alpha1.DeviceStatus) error {
	if c.systemInfo != nil {
		status.SystemInfo = c.systemInfo
		return nil
	}

	// collect what we can and use previous values on error
	// retry until completed
	systemInfo := &v1alpha1.DeviceSystemInfo{
		Architecture:    runtime.GOARCH,
		OperatingSystem: runtime.GOOS,
	}

	sysFs := sysfs.NewRealSysFs()
	fsInfo, err := fs.NewFsInfo(fs.Context{})
	if err != nil {
		return fmt.Errorf("getting file system info: %w", err)
	}
	inHostNamespace := true
	info, err := machine.Info(sysFs, fsInfo, inHostNamespace)
	if err != nil {
		return fmt.Errorf("getting machine info: %w", err)
	}
	systemInfo.BootID = info.BootID
	systemInfo.MachineID = info.MachineID
	systemInfo.Measurements = map[string]string{}
	err = c.tpmChannel.GetPCRValues(systemInfo.Measurements)
	if err != nil {
		return fmt.Errorf("getting PCR values: %w", err)
	}

	c.systemInfo = systemInfo
	status.SystemInfo = c.systemInfo

	return nil
}
