package client

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/stretchr/testify/require"
	gomock "go.uber.org/mock/gomock"
)

func TestSystemClient(t *testing.T) {
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

	// set mock boot_id
	mockBootID := "c4070599-f0f0-472d-8084-09b7274ebf18"
	err = readWriter.WriteFile(DefaultBootIDPath, []byte(mockBootID), 0644)
	require.NoError(err)

	ctrl := gomock.NewController(t)
	mockExecuter := executer.NewMockExecuter(ctrl)
	bootTime := "2024-12-13 11:01:08"
	mockExecuter.EXPECT().Execute("uptime", "-s").Return(bootTime, "", 0).Times(2)

	// initialize client new device
	client := NewSystem(mockExecuter, readWriter, dataDir)
	err = client.Initialize()
	require.NoError(err)
	require.NotNil(client)
	require.NotEmpty(client.BootTime())
	require.False(client.IsRebooted())
	require.Equal(mockBootID, client.BootID())

	// test rebooted
	// change bootID stored in system.json on disk
	mockBootID2 := "c4070599-f0f0-472d-8084-09b7274ebf19"
	mockStatus := &systemStatus{
		BootTime: bootTime,
		BootID:   mockBootID2,
	}
	mockStatusBytes, err := json.Marshal(mockStatus)
	require.NoError(err)
	err = readWriter.WriteFile(filepath.Join(dataDir, SystemStatusFileName), mockStatusBytes, 0644)
	require.NoError(err)

	// reinitialize client
	client = NewSystem(mockExecuter, readWriter, dataDir)
	err = client.Initialize()
	require.NoError(err)
	require.NotEmpty(client.BootTime())
	require.Equal(mockBootID, client.BootID())
	require.True(client.IsRebooted())
}
