package deviceexporter

import (
	"context"
	"fmt"
	"runtime"

	"github.com/google/cadvisor/fs"
	"github.com/google/cadvisor/machine"
	"github.com/google/cadvisor/utils/sysfs"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/tpm"
)

type SystemInfoExporter struct {
	tpmChannel *tpm.TPM
	systemInfo *v1alpha1.DeviceSystemInfo
}

func newSystemInfoExporter(tpmChannel *tpm.TPM) *SystemInfoExporter {
	return &SystemInfoExporter{tpmChannel: tpmChannel}
}

func (c *SystemInfoExporter) GetStatus(ctx context.Context) (interface{}, error) {
	if c.systemInfo != nil {
		return c.systemInfo, nil
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
		return systemInfo, fmt.Errorf("getting file system info: %w", err)
	}
	inHostNamespace := true
	info, err := machine.Info(sysFs, fsInfo, inHostNamespace)
	if err != nil {
		return systemInfo, fmt.Errorf("getting machine info: %w", err)
	}
	systemInfo.BootID = info.BootID
	systemInfo.MachineID = info.MachineID
	systemInfo.Measurements = map[string]string{}
	err = c.tpmChannel.GetPCRValues(systemInfo.Measurements)
	if err != nil {
		return systemInfo, fmt.Errorf("getting PCR values: %w", err)
	}

	c.systemInfo = systemInfo

	return c.systemInfo, nil
}
