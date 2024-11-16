package status

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/container"
	"github.com/flightctl/flightctl/pkg/executer"
)

const (
	// BootIDPath is the path to the boot ID file.
	DefaultBootIDPath   = "/proc/sys/kernel/random/boot_id"
	DefaultTimeZone     = "UTC"
	DefaultZoneInfoPath = "/usr/share/zoneinfo"
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
		// update time zone if changed during runtime
		return ensureTimeZone(&status.SystemInfo)
	}

	bootID, err := getBootID(DefaultBootIDPath)
	if err != nil {
		return fmt.Errorf("getting boot ID: %w", err)
	}

	// these values are static and can be set once for the life of the agent process.
	systemInfo := v1alpha1.DeviceSystemInfo{
		Architecture:    runtime.GOARCH,
		OperatingSystem: runtime.GOOS,
		BootID:          bootID,
	}

	if err := ensureTimeZone(&systemInfo); err != nil {
		return err
	}
	status.SystemInfo = systemInfo

	bootcInfo, err := s.bootcClient.Status(ctx)
	if err != nil {
		return fmt.Errorf("getting bootc status: %w", err)
	}

	osImage := bootcInfo.GetBootedImage()
	if osImage == "" {
		return fmt.Errorf("getting booted os image: %w", err)
	}

	status.Os.Image = osImage
	status.Os.ImageDigest = bootcInfo.GetBootedImageDigest()

	return nil
}

func ensureTimeZone(systemInfo *v1alpha1.DeviceSystemInfo) error {
	timeZone, err := getTimeZone()
	if err != nil {
		return fmt.Errorf("getting time zone: %w", err)
	}

	systemInfo.TimeZone = timeZone
	return nil
}

// getTimeZone returns the time zone of the system, the time zone can be
// populated by the TZ environment variable which takes precedence over the
// /etc/localtime file. If the time zone can not be determined, the default
// time zone is (UTC) returned.
// ref. https://www.freedesktop.org/software/systemd/man/latest/localtime.html
func getTimeZone() (string, error) {
	value := os.Getenv("TZ")
	if value != "" {
		return value, nil
	}

	path, err := filepath.EvalSymlinks("/etc/localtime")
	if err != nil {
		if os.IsNotExist(err) {
			return DefaultTimeZone, nil
		}
		return "", fmt.Errorf("failed to evaluate /etc/localtime: %w", err)
	}

	timeZone, err := filepath.Rel(DefaultZoneInfoPath, path)
	if err != nil {
		return "", fmt.Errorf("failed to extract time zone: %w", err)
	}

	return timeZone, nil
}
