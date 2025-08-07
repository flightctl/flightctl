package systeminfo

import (
	"context"
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

func TestReloadConfig(t *testing.T) {
	tests := []struct {
		name            string
		initialKeys     []string
		newKeys         []string
		expectCollected bool
		expectRecollect bool
	}{
		{
			name:            "no change in info keys",
			initialKeys:     []string{infoKeyNetInterfaceDefault, infoKeyCPUCores},
			newKeys:         []string{infoKeyNetInterfaceDefault, infoKeyCPUCores},
			expectCollected: true,
			expectRecollect: false,
		},
		{
			name:            "change within same collector type - network",
			initialKeys:     []string{infoKeyNetInterfaceDefault},
			newKeys:         []string{infoKeyNetMACDefault},
			expectCollected: true,
			expectRecollect: false,
		},
		{
			name:            "change within same collector type - CPU",
			initialKeys:     []string{infoKeyCPUCores},
			newKeys:         []string{infoKeyCPUModel},
			expectCollected: true,
			expectRecollect: false,
		},
		{
			name:            "change to different collector type",
			initialKeys:     []string{infoKeyCPUCores},
			newKeys:         []string{infoKeyMemoryTotalKb},
			expectCollected: false,
			expectRecollect: true,
		},
		{
			name:            "add key requiring same collector",
			initialKeys:     []string{infoKeyNetInterfaceDefault},
			newKeys:         []string{infoKeyNetInterfaceDefault, infoKeyNetMACDefault},
			expectCollected: true,
			expectRecollect: false,
		},
		{
			name:            "add key requiring different collector",
			initialKeys:     []string{infoKeyCPUCores},
			newKeys:         []string{infoKeyCPUCores, infoKeyGPU},
			expectCollected: false,
			expectRecollect: true,
		},
		{
			name:            "remove key but keep collector active",
			initialKeys:     []string{infoKeyNetInterfaceDefault, infoKeyNetMACDefault},
			newKeys:         []string{infoKeyNetInterfaceDefault},
			expectCollected: true,
			expectRecollect: false,
		},
		{
			name:            "remove key that disables collector",
			initialKeys:     []string{infoKeyCPUCores, infoKeyMemoryTotalKb},
			newKeys:         []string{infoKeyCPUCores},
			expectCollected: false,
			expectRecollect: true,
		},
		{
			name:            "empty to non-empty",
			initialKeys:     []string{},
			newKeys:         []string{infoKeyCPUCores},
			expectCollected: false,
			expectRecollect: true,
		},
		{
			name:            "non-empty to empty",
			initialKeys:     []string{infoKeyCPUCores},
			newKeys:         []string{},
			expectCollected: false,
			expectRecollect: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)

			tmpDir := t.TempDir()
			dataDir := filepath.Join("etc", "flightctl")
			readWriter := fileio.NewReadWriter()
			readWriter.SetRootdir(tmpDir)
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

			if tt.expectRecollect {
				require.False(manager.collected, "collected flag should be false when recollection is needed")
			} else {
				require.Equal(tt.expectCollected, manager.collected, "collected flag should match expected value")
			}

			require.Equal(tt.newKeys, manager.infoKeys, "info keys should be updated")
		})
	}
}
