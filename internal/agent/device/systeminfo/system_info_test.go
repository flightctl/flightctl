package systeminfo

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/stretchr/testify/require"
	gomock "go.uber.org/mock/gomock"
)

func TestManager(t *testing.T) {
	require := require.New(t)

	// setup
	tmpDir := t.TempDir()
	dataDir := filepath.Join("etc", "flightctl")
	readWriter := fileio.NewReadWriter()
	readWriter.SetRootdir(tmpDir)
	err := readWriter.MkdirAll(dataDir, 0755)
	require.NoError(err)
	err = readWriter.MkdirAll("/proc/sys/kernel/random", 0755)
	require.NoError(err)
	log := log.NewPrefixLogger("test")

	// set mock boot_id
	mockBootID := "c4070599-f0f0-472d-8084-09b7274ebf18"
	err = readWriter.WriteFile(DefaultBootIDPath, []byte(mockBootID), 0644)
	require.NoError(err)

	ctrl := gomock.NewController(t)
	mockExecuter := executer.NewMockExecuter(ctrl)
	bootTime := "2024-12-13 11:01:08"
	collectTimeout := util.Duration(5 * time.Second)
	mockExecuter.EXPECT().Execute("uptime", "-s").Return(bootTime, "", 0).Times(2)

	// initialize client new device
	factKeys := []string{""}
	manager := NewManager(log, mockExecuter, readWriter, dataDir, factKeys, collectTimeout)
	err = manager.Initialize()
	require.NoError(err)
	require.NotNil(manager)
	require.NotEmpty(manager.BootTime())
	require.False(manager.IsRebooted())
	require.Equal(mockBootID, manager.BootID())

	// test rebooted
	// change bootID stored in system.json on disk
	mockBootID2 := "c4070599-f0f0-472d-8084-09b7274ebf19"
	mockStatus := &Boot{
		Time: bootTime,
		ID:   mockBootID2,
	}
	mockStatusBytes, err := json.Marshal(mockStatus)
	require.NoError(err)
	err = readWriter.WriteFile(filepath.Join(dataDir, SystemBootFileName), mockStatusBytes, 0644)
	require.NoError(err)

	// reinitialize client
	manager = NewManager(log, mockExecuter, readWriter, dataDir, factKeys, collectTimeout)
	err = manager.Initialize()
	require.NoError(err)
	require.NotEmpty(manager.BootTime())
	require.Equal(mockBootID, manager.BootID())
	require.True(manager.IsRebooted())
}

// go test -benchmem -run=^$ -bench ^BenchmarkCollectInfo$ -cpuprofile=cpu.pprof -memprofile=mem.pprof github.com/flightctl/flightctl/internal/agent/device/systeminfo
func BenchmarkCollectInfo(b *testing.B) {
	ctx := context.Background()
	log := log.NewPrefixLogger("test")
	exec := &executer.CommonExecuter{}
	reader := fileio.NewReadWriter()
	hardwareMapPath := "/var/lib/flightctl/hardware_map.json"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		info, err := CollectInfo(ctx, log, exec, reader, hardwareMapPath)
		if err != nil {
			b.Fatalf("CollectInfo failed: %v", err)
		}
		// use value to prevent compiler optimization
		if info == nil {
			b.Fatal("Expected non-nil info")
		}
	}
}
