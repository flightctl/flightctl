package systeminfo

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/config"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/agent/device/systeminfo/common"
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
	manager := NewManager(log, mockExecuter, readWriter, dataDir, nil, nil, collectTimeout, 0)
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
	manager = NewManager(log, mockExecuter, readWriter, dataDir, nil, nil, collectTimeout, 0)
	err = manager.Initialize(context.Background())
	require.NoError(err)
	require.NotEmpty(manager.BootTime())
	require.Equal(mockBootID, manager.BootID())
	require.True(manager.IsRebooted())
}

func TestRun(t *testing.T) {
	t.Run("When Run is called it should collect immediately and cache results", func(t *testing.T) {
		require := require.New(t)

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

		mockBootID := "c4070599-f0f0-472d-8084-09b7274ebf18"
		err = readWriter.WriteFile(bootIDPath, []byte(mockBootID), 0644)
		require.NoError(err)

		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		mockExecuter := executer.NewMockExecuter(ctrl)
		bootTime := "2024-12-13 11:01:08"
		collectTimeout := util.Duration(5 * time.Second)
		// Once for Initialize, once for collect
		mockExecuter.EXPECT().ExecuteWithContext(gomock.Any(), "uptime", "-s").Return(bootTime, "", 0).MinTimes(2)

		log := log.NewPrefixLogger("test")

		// No periodic interval — Run should collect once and return
		manager := NewManager(log, mockExecuter, readWriter, dataDir, nil, nil, collectTimeout, 0)
		err = manager.Initialize(context.Background())
		require.NoError(err)

		// Before Run, no cached info
		require.Nil(manager.cachedSystemInfo)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		// With interval=0, Run collects once and returns
		manager.Run(ctx)

		// After Run, cached info should be populated
		require.NotNil(manager.cachedSystemInfo)
		require.Equal(mockBootID, manager.cachedSystemInfo.BootID)
	})

	t.Run("When Run is called with interval it should collect periodically", func(t *testing.T) {
		require := require.New(t)

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

		mockBootID := "c4070599-f0f0-472d-8084-09b7274ebf18"
		err = readWriter.WriteFile(bootIDPath, []byte(mockBootID), 0644)
		require.NoError(err)

		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		mockExecuter := executer.NewMockExecuter(ctrl)
		bootTime := "2024-12-13 11:01:08"
		collectTimeout := util.Duration(5 * time.Second)
		// Initial + periodic collections
		mockExecuter.EXPECT().ExecuteWithContext(gomock.Any(), "uptime", "-s").Return(bootTime, "", 0).MinTimes(3)

		log := log.NewPrefixLogger("test")

		// Short interval for test
		collectionInterval := util.Duration(50 * time.Millisecond)
		manager := NewManager(log, mockExecuter, readWriter, dataDir, nil, nil, collectTimeout, collectionInterval)
		err = manager.Initialize(context.Background())
		require.NoError(err)

		ctx, cancel := context.WithCancel(context.Background())

		done := make(chan struct{})
		go func() {
			manager.Run(ctx)
			close(done)
		}()

		// Wait for at least 2 collection cycles
		time.Sleep(150 * time.Millisecond)
		cancel()
		<-done

		require.NotNil(manager.cachedSystemInfo)
	})

	t.Run("When context is cancelled it should stop Run", func(t *testing.T) {
		require := require.New(t)

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

		mockBootID := "c4070599-f0f0-472d-8084-09b7274ebf18"
		err = readWriter.WriteFile(bootIDPath, []byte(mockBootID), 0644)
		require.NoError(err)

		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		mockExecuter := executer.NewMockExecuter(ctrl)
		bootTime := "2024-12-13 11:01:08"
		collectTimeout := util.Duration(5 * time.Second)
		mockExecuter.EXPECT().ExecuteWithContext(gomock.Any(), "uptime", "-s").Return(bootTime, "", 0).MinTimes(1)

		log := log.NewPrefixLogger("test")

		collectionInterval := util.Duration(1 * time.Second)
		manager := NewManager(log, mockExecuter, readWriter, dataDir, nil, nil, collectTimeout, collectionInterval)
		err = manager.Initialize(context.Background())
		require.NoError(err)

		ctx, cancel := context.WithCancel(context.Background())

		done := make(chan struct{})
		go func() {
			manager.Run(ctx)
			close(done)
		}()

		// Cancel quickly — Run should exit promptly
		time.Sleep(10 * time.Millisecond)
		cancel()

		select {
		case <-done:
			// Run exited as expected
		case <-time.After(2 * time.Second):
			require.Fail("Run did not stop after context cancellation")
		}
	})
}

func TestStatusReturnsCachedResults(t *testing.T) {
	t.Run("When Status is cancelled before the initial collection completes it should return the context error", func(t *testing.T) {
		require := require.New(t)
		manager := NewManager(log.NewPrefixLogger("test"), nil, nil, "", nil, nil, 0, 0)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		err := manager.Status(ctx, &v1beta1.DeviceStatus{})
		require.ErrorIs(err, context.Canceled)
	})

	t.Run("When Status is called while the initial collection is running it should wait for the cached result", func(t *testing.T) {
		require := require.New(t)

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

		mockBootID := "c4070599-f0f0-472d-8084-09b7274ebf18"
		err = readWriter.WriteFile(bootIDPath, []byte(mockBootID), 0644)
		require.NoError(err)

		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		mockExecuter := executer.NewMockExecuter(ctrl)
		bootTime := "2024-12-13 11:01:08"
		collectTimeout := util.Duration(5 * time.Second)
		// Once during initialization and once during Run's initial collection.
		// Status must not begin a second collection while Run is in progress.
		mockExecuter.EXPECT().ExecuteWithContext(gomock.Any(), "uptime", "-s").Return(bootTime, "", 0).Times(2)

		log := log.NewPrefixLogger("test")

		manager := NewManager(log, mockExecuter, readWriter, dataDir, []string{common.TPMVendorInfoKey}, nil, collectTimeout, 0)
		err = manager.Initialize(context.Background())
		require.NoError(err)

		collectionStarted := make(chan struct{})
		completeCollection := make(chan struct{})
		manager.RegisterCollector(context.Background(), common.TPMVendorInfoKey, func(context.Context) string {
			close(collectionStarted)
			<-completeCollection
			return "TPM vendor"
		})

		runDone := make(chan struct{})
		go func() {
			manager.Run(context.Background())
			close(runDone)
		}()
		<-collectionStarted

		deviceStatus := &v1beta1.DeviceStatus{}
		statusDone := make(chan error, 1)
		go func() {
			statusDone <- manager.Status(context.Background(), deviceStatus)
		}()

		select {
		case err := <-statusDone:
			require.Failf("Status returned before initial collection completed", "unexpected error: %v", err)
		case <-time.After(50 * time.Millisecond):
		}

		close(completeCollection)
		require.NoError(<-statusDone)
		<-runDone
		require.Equal(mockBootID, deviceStatus.SystemInfo.BootID)
		require.Equal("TPM vendor", deviceStatus.SystemInfo.AdditionalProperties[common.TPMVendorInfoKey])
	})

	t.Run("When the initial collection fails it should return the cached default result", func(t *testing.T) {
		require := require.New(t)
		manager := NewManager(log.NewPrefixLogger("test"), nil, nil, "", nil, nil, 0, 0)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		manager.Run(ctx)

		deviceStatus := &v1beta1.DeviceStatus{}
		err := manager.Status(context.Background(), deviceStatus)
		require.NoError(err)
		require.NotEmpty(deviceStatus.SystemInfo.AgentVersion)
		require.NotNil(deviceStatus.SystemInfo.AdditionalProperties)
	})

	t.Run("When Status is called after collection it should return cached results", func(t *testing.T) {
		require := require.New(t)

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

		mockBootID := "c4070599-f0f0-472d-8084-09b7274ebf18"
		err = readWriter.WriteFile(bootIDPath, []byte(mockBootID), 0644)
		require.NoError(err)

		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		mockExecuter := executer.NewMockExecuter(ctrl)
		bootTime := "2024-12-13 11:01:08"
		collectTimeout := util.Duration(5 * time.Second)
		mockExecuter.EXPECT().ExecuteWithContext(gomock.Any(), "uptime", "-s").Return(bootTime, "", 0).MinTimes(2)

		log := log.NewPrefixLogger("test")

		manager := NewManager(log, mockExecuter, readWriter, dataDir, nil, nil, collectTimeout, 0)
		err = manager.Initialize(context.Background())
		require.NoError(err)

		// Run collection (with interval=0, collects once and returns)
		manager.Run(context.Background())

		// Now Status should return the cached results
		deviceStatus := &v1beta1.DeviceStatus{}
		err = manager.Status(context.Background(), deviceStatus)
		require.NoError(err)
		require.Equal(mockBootID, deviceStatus.SystemInfo.BootID)
		require.NotEmpty(deviceStatus.SystemInfo.AgentVersion)
	})
}

func TestReloadConfig(t *testing.T) {
	tests := []struct {
		name        string
		initialKeys []string
		newKeys     []string
	}{
		{
			name:        "no change in info keys",
			initialKeys: []string{common.NetInterfaceDefaultKey, common.CPUCoresKey},
			newKeys:     []string{common.NetInterfaceDefaultKey, common.CPUCoresKey},
		},
		{
			name:        "change within same collector type - network",
			initialKeys: []string{common.NetInterfaceDefaultKey},
			newKeys:     []string{common.NetMACDefaultKey},
		},
		{
			name:        "change within same collector type - CPU",
			initialKeys: []string{common.CPUCoresKey},
			newKeys:     []string{common.CPUModelKey},
		},
		{
			name:        "change to different collector type",
			initialKeys: []string{common.CPUCoresKey},
			newKeys:     []string{common.MemoryTotalKbKey},
		},
		{
			name:        "add key requiring same collector",
			initialKeys: []string{common.NetInterfaceDefaultKey},
			newKeys:     []string{common.NetInterfaceDefaultKey, common.NetMACDefaultKey},
		},
		{
			name:        "add key requiring different collector",
			initialKeys: []string{common.CPUCoresKey},
			newKeys:     []string{common.CPUCoresKey, common.GPUKey},
		},
		{
			name:        "remove key but keep collector active",
			initialKeys: []string{common.NetInterfaceDefaultKey, common.NetMACDefaultKey},
			newKeys:     []string{common.NetInterfaceDefaultKey},
		},
		{
			name:        "remove key that disables collector",
			initialKeys: []string{common.CPUCoresKey, common.MemoryTotalKbKey},
			newKeys:     []string{common.CPUCoresKey},
		},
		{
			name:        "empty to non-empty",
			initialKeys: []string{},
			newKeys:     []string{common.CPUCoresKey},
		},
		{
			name:        "non-empty to empty",
			initialKeys: []string{common.CPUCoresKey},
			newKeys:     []string{},
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

			manager := NewManager(log, mockExecuter, readWriter, dataDir, tt.initialKeys, nil, collectTimeout, 0)

			cfg := &config.Config{
				SystemInfo: tt.newKeys,
			}

			err = manager.ReloadConfig(context.Background(), cfg)
			require.NoError(err)

			require.Equal(tt.newKeys, manager.infoKeys, "info keys should be updated")
		})
	}
}
