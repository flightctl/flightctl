package systeminfo

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/flightctl/flightctl/internal/agent/config"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestManager(t *testing.T) {
	require := require.New(t)

	// setup
	tmpDir := t.TempDir()
	dataDir := filepath.Join("etc", "flightctl")
	readWriter := fileio.NewReadWriter(
		fileio.NewReader(fileio.WithReaderRootDir(tmpDir)),
		fileio.NewWriter(fileio.WithWriterRootDir(tmpDir)),
	)
	err := readWriter.MkdirAll(dataDir, 0755)
	require.NoError(err)
	err = readWriter.MkdirAll("/proc/sys/kernel/random", 0755)
	require.NoError(err)
	log := log.NewPrefixLogger("test")

	// set mock boot_id
	mockBootID := "c4070599-f0f0-472d-8084-09b7274ebf18"
	err = readWriter.WriteFile(bootIDPath, []byte(mockBootID), 0644)
	require.NoError(err)

	ctrl := gomock.NewController(t)
	mockExecuter := executer.NewMockExecuter(ctrl)
	bootTime := "2024-12-13 11:01:08"
	collectTimeout := util.Duration(5 * time.Second)
	mockExecuter.EXPECT().ExecuteWithContext(context.Background(), "uptime", "-s").Return(bootTime, "", 0).Times(2)

	// initialize client new device
	manager := NewManager(log, mockExecuter, readWriter, dataDir, nil, nil, collectTimeout)
	err = manager.Initialize(context.Background())
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
	err = readWriter.WriteFile(filepath.Join(dataDir, SystemFileName), mockStatusBytes, 0644)
	require.NoError(err)

	// reinitialize client
	manager = NewManager(log, mockExecuter, readWriter, dataDir, nil, nil, collectTimeout)
	err = manager.Initialize(context.Background())
	require.NoError(err)
	require.NotEmpty(manager.BootTime())
	require.Equal(mockBootID, manager.BootID())
	require.True(manager.IsRebooted())
}

func TestReloadConfig(t *testing.T) {
	tests := []struct {
		name        string
		initialKeys []string
		newKeys     []string
		expected    bool
	}{
		{
			name:        "no change in info keys",
			initialKeys: []string{netInterfaceDefaultKey, cpuCoresKey},
			newKeys:     []string{netInterfaceDefaultKey, cpuCoresKey},
			expected:    true,
		},
		{
			name:        "change within same collector type - network",
			initialKeys: []string{netInterfaceDefaultKey},
			newKeys:     []string{netMACDefaultKey},
			expected:    true,
		},
		{
			name:        "change within same collector type - CPU",
			initialKeys: []string{cpuCoresKey},
			newKeys:     []string{cpuModelKey},
			expected:    true,
		},
		{
			name:        "change to different collector type",
			initialKeys: []string{cpuCoresKey},
			newKeys:     []string{memoryTotalKbKey},
			expected:    false,
		},
		{
			name:        "add key requiring same collector",
			initialKeys: []string{netInterfaceDefaultKey},
			newKeys:     []string{netInterfaceDefaultKey, netMACDefaultKey},
			expected:    true,
		},
		{
			name:        "add key requiring different collector",
			initialKeys: []string{cpuCoresKey},
			newKeys:     []string{cpuCoresKey, gpuKey},
			expected:    false,
		},
		{
			name:        "remove key but keep collector active",
			initialKeys: []string{netInterfaceDefaultKey, netMACDefaultKey},
			newKeys:     []string{netInterfaceDefaultKey},
			expected:    true,
		},
		{
			name:        "remove key that disables collector",
			initialKeys: []string{cpuCoresKey, memoryTotalKbKey},
			newKeys:     []string{cpuCoresKey},
			expected:    true,
		},
		{
			name:        "empty to non-empty",
			initialKeys: []string{},
			newKeys:     []string{cpuCoresKey},
			expected:    false,
		},
		{
			name:        "non-empty to empty",
			initialKeys: []string{cpuCoresKey},
			newKeys:     []string{},
			expected:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)

			tmpDir := t.TempDir()
			dataDir := filepath.Join("etc", "flightctl")
			readWriter := fileio.NewReadWriter(
				fileio.NewReader(fileio.WithReaderRootDir(tmpDir)),
				fileio.NewWriter(fileio.WithWriterRootDir(tmpDir)),
			)
			err := readWriter.MkdirAll(dataDir, 0755)
			require.NoError(err)

			ctrl := gomock.NewController(t)
			mockExecuter := executer.NewMockExecuter(ctrl)
			log := log.NewPrefixLogger("test")
			collectTimeout := util.Duration(5 * time.Second)

			manager := NewManager(log, mockExecuter, readWriter, dataDir, tt.initialKeys, nil, collectTimeout)

			// Simulate that data has been collected
			manager.collected = true

			cfg := &config.Config{
				SystemInfo: tt.newKeys,
			}

			err = manager.ReloadConfig(context.Background(), cfg)
			require.NoError(err)

			require.Equal(tt.expected, manager.collected)

			require.Equal(tt.newKeys, manager.infoKeys, "info keys should be updated")
		})
	}
}
