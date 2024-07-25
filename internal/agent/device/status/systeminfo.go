package status

import (
	"context"
	"fmt"
	"runtime"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/container"
	"github.com/flightctl/flightctl/pkg/executer"
)

const (
	// BootIDPath is the path to the boot ID file.
	DefaultBootIDPath = "/proc/sys/kernel/random/boot_id"
)

var _ Exporter = (*SystemInfo)(nil)

// SystemInfo collects system information.
type SystemInfo struct {
	bootcClient *container.BootcCmd
}

func newSystemInfo(exec executer.Executer) *SystemInfo {
	return &SystemInfo{
		bootcClient: container.NewBootcCmd(exec),
	}
}

func (s *SystemInfo) Export(ctx context.Context, status *v1alpha1.DeviceStatus) error {
	if !status.SystemInfo.IsEmpty() {
		return nil
	}

	bootID, err := getBootID(DefaultBootIDPath)
	if err != nil {
		return fmt.Errorf("getting boot ID: %w", err)
	}

	status.SystemInfo = v1alpha1.DeviceSystemInfo{
		Architecture:    runtime.GOARCH,
		OperatingSystem: runtime.GOOS,
		BootID:          bootID,
	}

	bootcInfo, err := s.bootcClient.Status(ctx)
	if err != nil {
		return fmt.Errorf("getting bootc status: %w", err)
	}

	osImage := container.GetImage(bootcInfo)
	if osImage == "" {
		return fmt.Errorf("getting os image: %w", err)
	}

	status.Os.Image = osImage

	return nil
}

func (c *SystemInfo) SetProperties(spec *v1alpha1.RenderedDeviceSpec) {
}
