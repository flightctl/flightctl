package status

import (
	"context"
	"fmt"
	"os"
	"runtime"

	"github.com/flightctl/flightctl/api/v1alpha1"
)

const (
	// BootIDPath is the path to the boot ID file.
	DefaultBootIDPath = "/proc/sys/kernel/random/boot_id"
)

func systemInfoStatus(_ context.Context, status *v1alpha1.DeviceStatus) error {
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

	return nil
}

func getBootID(bootIDPath string) (string, error) {
	bootID, err := os.ReadFile(bootIDPath)
	if err != nil {
		return "", err
	}
	return string(bootID), nil
}
