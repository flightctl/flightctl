package resource

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/stretchr/testify/require"
)

const (
	procStatData = `cpu  5085309 2167763 1898394 153559678 869399 636486 449717 0 156334 0
cpu0 422035 237451 164680 12724283 70955 47473 40517 0 13314 0
cpu1 416332 186330 161674 12810448 71615 42559 26204 0 12359 0
cpu2 415577 187217 152110 12828727 74139 40454 22049 0 13171 0
cpu3 410703 187895 168448 12497106 76501 184172 201234 0 14252 0
cpu4 426090 187033 170370 12810394 73040 37679 20057 0 12550 0
cpu5 444768 193814 154113 12804104 68261 39532 20574 0 13176 0
cpu6 429560 149927 144057 12869345 74354 37394 18782 0 12913 0
cpu7 425436 197818 156839 12814785 71208 39612 21129 0 12935 0
cpu8 433494 178256 156062 12833512 69394 37052 19303 0 13315 0
cpu9 444353 165600 151326 12833850 77061 35924 17701 0 12705 0
cpu10 426877 157700 167697 12837497 71765 43227 19872 0 13343 0
cpu11 390080 138716 151011 12895622 71100 51402 22288 0 12296 0`
)

func TestCPUMonitor(t *testing.T) {
	require := require.New(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	tmpDir := t.TempDir()
	fakeStatPath := filepath.Join(tmpDir, "stat")
	err := os.WriteFile(fakeStatPath, []byte(procStatData), 0600)
	require.NoError(err)

	log := log.NewPrefixLogger("test")
	cpuMonitor := NewCPUMonitor(log)
	cpuMonitor.statPath = fakeStatPath

	go cpuMonitor.Run(ctx)

	samplingInterval := 100 * time.Millisecond
	monitorSpec := v1alpha1.CpuResourceMonitorSpec{
		SamplingInterval: samplingInterval.String(),
		MonitorType:      CPUMonitorType,
		AlertRules: []v1alpha1.ResourceAlertRule{
			{
				Severity:    v1alpha1.ResourceAlertSeverityTypeCritical,
				Percentage:  7, // 7% usage should never fire an alert
				Duration:    "90ms",
				Description: "Critical: CPU usage is above 7% for 90ms",
			},
			{
				Severity:    v1alpha1.ResourceAlertSeverityTypeWarning,
				Percentage:  5, // 5% usage should always fire an alert
				Duration:    "90ms",
				Description: "Warning: CPU usage is above 5% for 90ms",
			},
			{
				Severity:    v1alpha1.ResourceAlertSeverityTypeInfo,
				Percentage:  5, // 5% usage should never fire an alert because of duration
				Duration:    "1h",
				Description: "Warning: CPU usage is above 5% for 90ms",
			},
		},
	}

	rm := &v1alpha1.ResourceMonitor{}
	err = rm.FromCpuResourceMonitorSpec(monitorSpec)
	require.NoError(err)
	updated, err := cpuMonitor.Update(rm)
	require.NoError(err)
	require.True(updated)

	var alerts []v1alpha1.ResourceAlertRule
	require.Eventually(func() bool {
		alerts = cpuMonitor.Alerts()
		return len(alerts) == 1
	}, retryTimeout, retryInterval, "alert add")

	deviceResourceStatusType, alertMsg := getHighestSeverityResourceStatusFromAlerts(CPUMonitorType, alerts)
	require.NotEmpty(alertMsg) // ensure we have an alert message

	require.Equal(v1alpha1.DeviceResourceStatusWarning, deviceResourceStatusType)

	// update the monitor to remove all alerts
	monitorSpec.AlertRules = monitorSpec.AlertRules[:0]
	rm = &v1alpha1.ResourceMonitor{}
	err = rm.FromCpuResourceMonitorSpec(monitorSpec)
	require.NoError(err)

	updated, err = cpuMonitor.Update(rm)
	require.NoError(err)
	require.True(updated)

	// ensure no alerts after clearing
	require.Eventually(func() bool {
		alerts := cpuMonitor.Alerts()
		return len(alerts) == 0
	}, retryTimeout, retryInterval, "alert remove")
}
