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
	memoryInfoData = `MemTotal:       65589900 kB
MemFree:         1640484 kB
MemAvailable:   49384032 kB
Buffers:           12016 kB
Cached:         48655904 kB
SwapCached:            0 kB
Active:         18675344 kB
Inactive:       40268568 kB
Active(anon):   12451376 kB
Inactive(anon):   206168 kB
Active(file):    6223968 kB
Inactive(file): 40062400 kB
Unevictable:     1483764 kB
Mlocked:            5880 kB
SwapTotal:       8388604 kB
SwapFree:        8387836 kB
Zswap:                 0 kB
Zswapped:              0 kB
Dirty:              3528 kB
Writeback:             0 kB
AnonPages:      11759820 kB
Mapped:          2449576 kB
Shmem:           2376300 kB
KReclaimable:    2186624 kB
Slab:            3054568 kB
SReclaimable:    2186624 kB
SUnreclaim:       867944 kB
KernelStack:       51776 kB
PageTables:       158988 kB
SecPageTables:       800 kB
NFS_Unstable:          0 kB
Bounce:                0 kB
WritebackTmp:          0 kB
CommitLimit:    41183552 kB
Committed_AS:   65400068 kB
VmallocTotal:   34359738367 kB
VmallocUsed:      150684 kB
VmallocChunk:          0 kB
Percpu:            29568 kB
HardwareCorrupted:     0 kB
AnonHugePages:    530432 kB
ShmemHugePages:  1454080 kB
ShmemPmdMapped:        0 kB
FileHugePages:         0 kB
FilePmdMapped:         0 kB
CmaTotal:              0 kB
CmaFree:               0 kB
Unaccepted:            0 kB
HugePages_Total:       0
HugePages_Free:        0
HugePages_Rsvd:        0
HugePages_Surp:        0
Hugepagesize:       2048 kB
Hugetlb:               0 kB
DirectMap4k:      577248 kB
DirectMap2M:    24340480 kB
DirectMap1G:    41943040 kB`
)

func TestMemoryMonitor(t *testing.T) {
	require := require.New(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	tmpDir := t.TempDir()
	fakeMemInfoPath := filepath.Join(tmpDir, "meminfo")
	err := os.WriteFile(fakeMemInfoPath, []byte(memoryInfoData), 0600)
	require.NoError(err)

	log := log.NewPrefixLogger("test")
	memoryMonitor := NewMemoryMonitor(log)
	memoryMonitor.memInfoPath = fakeMemInfoPath

	go memoryMonitor.Run(ctx)

	usage := memoryMonitor.Usage()
	require.NotNil(usage)

	monitorSpec := v1alpha1.CPUResourceMonitorSpec{
		SamplingInterval: "1s",
		MonitorType:      CPUMonitorType,
		AlertRules: []v1alpha1.ResourceAlertRule{
			{
				Severity:    v1alpha1.ResourceAlertSeverityTypeCritical,
				Percentage:  90, // 90% usage should never fire an alert
				Duration:    "1s",
				Description: "Critical: memory usage is above 90% for 1s",
			},
			{
				Severity:    v1alpha1.ResourceAlertSeverityTypeWarning,
				Percentage:  5, // 5% usage should always fire an alert
				Duration:    "1s",
				Description: "Warning: Memory usage is above 5% for 1s",
			},
			{
				Severity:    v1alpha1.ResourceAlertSeverityTypeInfo,
				Percentage:  5, // 5% usage should never fire an alert because of duration
				Duration:    "1h",
				Description: "Warning: CPU usage is above 5% for 1s",
			},
		},
	}

	rm := v1alpha1.ResourceMonitor{}
	err = rm.FromCPUResourceMonitorSpec(monitorSpec)
	require.NoError(err)

	updated, err := memoryMonitor.Update(rm)
	require.NoError(err)
	require.True(updated)

	<-ctx.Done()

	usage = memoryMonitor.Usage()
	require.NotNil(usage)
	require.Equal(usage.UsedPercent, int64(23))

	alerts := memoryMonitor.Alerts()
	require.Len(alerts, 1)

	deviceResourceStatusType, err := GetHighestSeverityResourceStatusFromAlerts(alerts)
	require.ErrorIs(err, ErrAlertFiring)

	require.Equal(v1alpha1.DeviceResourceStatusWarning, deviceResourceStatusType)

	// update the monitor to remove all alerts
	monitorSpec.AlertRules = monitorSpec.AlertRules[:0]
	rm = v1alpha1.ResourceMonitor{}
	err = rm.FromCPUResourceMonitorSpec(monitorSpec)
	require.NoError(err)

	updated, err = memoryMonitor.Update(rm)
	require.NoError(err)
	require.True(updated)

	// ensure no alerts after clearing
	alerts = memoryMonitor.Alerts()
	require.Len(alerts, 0)
}
