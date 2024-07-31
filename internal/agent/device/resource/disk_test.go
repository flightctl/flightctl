package resource

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestDiskMonitor(t *testing.T) {
	require := require.New(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	log := log.NewPrefixLogger("test")
	diskMonitor := NewDiskMonitor(log)

	go diskMonitor.Run(ctx)

	path, err := getRWMountPoint()
	require.NoError(err)

	samplingInterval := 100 * time.Millisecond
	monitorSpec := v1alpha1.DiskResourceMonitorSpec{
		SamplingInterval: samplingInterval.String(),
		MonitorType:      DiskMonitorType,
		Path:             path,
		AlertRules: []v1alpha1.ResourceAlertRule{
			{
				Severity:    v1alpha1.ResourceAlertSeverityTypeInfo,
				Percentage:  0, // 0% disk usage should always fire
				Duration:    "90ms",
				Description: "Info: Disk usage is above 0% for 90ms",
			},
			{
				Severity:    v1alpha1.ResourceAlertSeverityTypeCritical,
				Percentage:  0, // 0% disk usage should always fire
				Duration:    "90ms",
				Description: "Critical: Disk usage is above 0% for 90ms",
			},
			{
				Severity:    v1alpha1.ResourceAlertSeverityTypeWarning,
				Percentage:  100,
				Duration:    "1h",
				Description: "Warning: Disk usage is above 100% for 1h",
			},
		},
	}

	rm := &v1alpha1.ResourceMonitor{}
	err = rm.FromDiskResourceMonitorSpec(monitorSpec)
	require.NoError(err)

	updated, err := diskMonitor.Update(rm)
	require.NoError(err)
	require.True(updated)

	// ensure only 2 alerts are firing
	var alerts []v1alpha1.ResourceAlertRule
	require.Eventually(func() bool {
		alerts = diskMonitor.Alerts()
		return len(alerts) == 2
	}, retryTimeout, retryInterval, "alert add")

	deviceResourceStatusType, alertMsg := GetHighestSeverityResourceStatusFromAlerts(DiskMonitorType, alerts)
	require.NotEmpty(alertMsg) // ensure we have an alert message

	require.Equal(v1alpha1.DeviceResourceStatusCritical, deviceResourceStatusType)

	// update the monitor to remove the critical alert
	monitorSpec.AlertRules = monitorSpec.AlertRules[1:]
	rm = &v1alpha1.ResourceMonitor{}
	err = rm.FromDiskResourceMonitorSpec(monitorSpec)
	require.NoError(err)

	updated, err = diskMonitor.Update(rm)
	require.NoError(err)
	require.True(updated)

	// ensure only 1 alert is firing after removal update
	require.Eventually(func() bool {
		alerts := diskMonitor.Alerts()
		return len(alerts) == 1
	}, retryTimeout, retryInterval, "alert add")
}

// getRWMountPoint returns the first rw mount point.
func getRWMountPoint() (string, error) {
	validFileSystemTypes := []string{
		"ext4", "ext3", "ext2", "btrfs", "xfs", "jfs", "reiserfs", "vfat", "ntfs", "f2fs", "zfs", "ufs", "nfs", "nfs4",
	}
	file, err := os.Open("/proc/mounts")
	if err != nil {
		return "", err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 4 {
			continue
		}
		mountPoint := fields[1]
		mountType := fields[2]
		options := fields[3]

		// use first rw
		if strings.Contains(options, "rw") && lo.Contains(validFileSystemTypes, mountType) {
			var statfs unix.Statfs_t
			if err := unix.Statfs(mountPoint, &statfs); err != nil {
				continue
			}
			return mountPoint, nil
		}
	}

	if err := scanner.Err(); err != nil {
		return "", err
	}

	return "", fmt.Errorf("no rw mount")
}
