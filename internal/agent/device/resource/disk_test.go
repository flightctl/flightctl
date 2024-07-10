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
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestDiskMonitor(t *testing.T) {
	require := require.New(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	log := log.NewPrefixLogger("test")
	diskMonitor := NewDiskMonitor(log)

	go diskMonitor.Run(ctx)

	usage := diskMonitor.Usage()
	require.NotNil(usage)
	require.Zero(usage.Total)

	path, err := getRWMountPoint()
	require.NoError(err)

	monitorSpec := v1alpha1.DiskResourceMonitorSpec{
		SamplingInterval: "1s",
		MonitorType:      DiskMonitorType,
		Path:             path,
		AlertRules: []v1alpha1.ResourceAlertRule{
			{
				Severity:    v1alpha1.ResourceAlertSeverityTypeInfo,
				Percentage:  0, // 0% disk usage should always fire
				Duration:    "1s",
				Description: "Info: Disk usage is above 0% for 1s",
			},
			{
				Severity:    v1alpha1.ResourceAlertSeverityTypeCritical,
				Percentage:  0, // 0% disk usage should always fire
				Duration:    "1s",
				Description: "Critical: Disk usage is above 0% for 1s",
			},
			{
				Severity:    v1alpha1.ResourceAlertSeverityTypeWarning,
				Percentage:  100,
				Duration:    "1h",
				Description: "Warning: Disk usage is above 100% for 1s",
			},
		},
	}

	rm := v1alpha1.ResourceMonitor{}
	err = rm.FromDiskResourceMonitorSpec(monitorSpec)
	require.NoError(err)

	updated, err := diskMonitor.Update(rm)
	require.NoError(err)
	require.True(updated)

	<-ctx.Done()

	usage = diskMonitor.Usage()
	require.NotNil(usage)
	require.NotZero(usage.Total)

	// ensure only 2 alerts are firing
	alerts := diskMonitor.Alerts()
	require.Len(alerts, 2)

	deviceResourceStatusType, err := GetHighestSeverityResourceStatusFromAlerts(alerts)
	require.NoError(err)

	require.Equal(v1alpha1.DeviceResourceStatusCritical, deviceResourceStatusType)

	// update the monitor to remove the critical alert
	monitorSpec.AlertRules = monitorSpec.AlertRules[1:]
	rm = v1alpha1.ResourceMonitor{}
	err = rm.FromDiskResourceMonitorSpec(monitorSpec)
	require.NoError(err)

	updated, err = diskMonitor.Update(rm)
	require.NoError(err)
	require.True(updated)

	// ensure only 1 alert is firing after removal update
	alerts = diskMonitor.Alerts()
	require.Len(alerts, 1)
}

// getRWMountPoint returns the first rw mount point.
func getRWMountPoint() (string, error) {
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
		options := fields[3]

		// use first rw
		if strings.Contains(options, "rw") {
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
