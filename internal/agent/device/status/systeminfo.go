package status

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

// SystemInfo collects system information.
type SystemInfo struct {
	tpmChannel *tpm.TPM
}

func newSystemInfo(tpmChannel *tpm.TPM) *SystemInfo {
	return &SystemInfo{tpmChannel: tpmChannel}
}

func (c *SystemInfo) Export(ctx context.Context, status *v1alpha1.DeviceStatus) error {
	// collect what we can and use previous values on error
	// retry until completed
	status.SystemInfo.Architecture = runtime.GOARCH
	status.SystemInfo.OperatingSystem = runtime.GOOS

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
	status.SystemInfo.BootID = info.BootID
	status.SystemInfo.MachineID = info.MachineID

	status.SystemInfo.Measurements = make(map[string]string)
	err = c.tpmChannel.GetPCRValues(status.SystemInfo.Measurements)
	if err != nil {
		return fmt.Errorf("getting PCR values: %w", err)
	}

	return nil
}

func (c *SystemInfo) SetProperties(spec *v1alpha1.RenderedDeviceSpec) {
}
