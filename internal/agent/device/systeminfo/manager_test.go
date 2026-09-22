package systeminfo

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/config"
	deviceerrors "github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/agent/device/status"
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
	mockExecuter.EXPECT().ExecuteWithContext(gomock.Any(), "uptime", "-s").Return(bootTime, "", 0).Times(4)

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
			select {
			case <-manager.collectionChanged:
			default:
				require.FailNow("reload should request collection")
			}
		})
	}
}

func TestRunCollectsOnItsPeriodicSchedule(t *testing.T) {
	require := require.New(t)
	collected := make(chan struct{}, 2)
	manager := &manager{
		collectionTimeout:  time.Second,
		collectionInterval: time.Millisecond,
		collectionChanged:  make(chan struct{}, 1),
		now:                time.Now,
		log:                log.NewPrefixLogger("test"),
		collection: []*collector{{
			source: &sourceDefinition{},
			collect: func(context.Context, *Info) error {
				select {
				case collected <- struct{}{}:
				default:
				}
				return nil
			},
		}},
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		manager.Run(ctx)
		close(done)
	}()

	select {
	case <-collected:
	case <-time.After(time.Second):
		require.FailNow("timed out waiting for periodic collection")
	}
	select {
	case <-collected:
	case <-time.After(time.Second):
		require.FailNow("timed out waiting for the next periodic collection")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		require.FailNow("Run did not stop after context cancellation")
	}
}

func TestStatusDoesNotCollect(t *testing.T) {
	require := require.New(t)
	manager := &manager{
		now: time.Now,
		collection: []*collector{{
			source: &sourceDefinition{},
			collect: func(context.Context, *Info) error {
				t.Fatal("Status must not invoke a collector")
				return nil
			},
		}},
	}

	deviceStatus := &v1beta1.DeviceStatus{}
	require.NoError(manager.Status(context.Background(), deviceStatus))
	require.Equal(v1beta1.SystemInfoSummaryStatusUnknown, deviceStatus.SystemInfoStatus.Summary.Status)
}

func TestStatusWithForceRequestsCollection(t *testing.T) {
	require := require.New(t)
	collected := make(chan struct{}, 1)
	manager := &manager{
		collectionTimeout: time.Second,
		now:               time.Now,
		collection: []*collector{{
			source: &sourceDefinition{},
			collect: func(context.Context, *Info) error {
				collected <- struct{}{}
				return nil
			},
		}},
	}

	deviceStatus := &v1beta1.DeviceStatus{}
	require.NoError(manager.Status(context.Background(), deviceStatus, status.WithForceCollect()))
	select {
	case <-collected:
	case <-time.After(time.Second):
		require.FailNow("timed out waiting for forced collection")
	}

}

func TestCollectPendingCollectsOnlyUnattemptedSources(t *testing.T) {
	require := require.New(t)
	oldCalls := 0
	newCalls := 0
	oldKey := common.TPMVendorInfoKey
	newKey := common.ManagementCertSerialKey
	manager := &manager{
		collectionTimeout: time.Second,
		now:               time.Now,
	}
	entries := []sourceEntry{
		{key: oldKey, kind: systemInfoSource, definition: runtimeDefinition(oldKey, func(context.Context) string {
			oldCalls++
			return "old"
		})},
		{key: newKey, kind: systemInfoSource, definition: runtimeDefinition(newKey, func(context.Context) string {
			newCalls++
			return "new"
		})},
	}
	manager.collection = collectorsForEntries(
		log.NewPrefixLogger("test"),
		nil,
		nil,
		"",
		entries[:1],
		nil,
	)

	manager.collect(context.Background())
	require.Equal(1, oldCalls)
	require.Zero(newCalls)

	manager.collection = collectorsForEntries(
		log.NewPrefixLogger("test"),
		nil,
		nil,
		"",
		entries,
		manager.collection,
	)
	manager.CollectPending(context.Background())

	require.Equal(1, oldCalls)
	require.Equal(1, newCalls)
	manager.CollectPending(context.Background())
	require.Equal(1, oldCalls)
	require.Equal(1, newCalls)
}

func TestCollectPendingDoesNotRetryFailedSources(t *testing.T) {
	require := require.New(t)
	calls := 0
	manager := &manager{
		collectionTimeout: time.Second,
		now:               time.Now,
		log:               log.NewPrefixLogger("test"),
		collection: []*collector{{
			source: &sourceDefinition{},
			collect: func(context.Context, *Info) error {
				calls++
				return errors.New("failed")
			},
			executors: []*cachedExecutor{{key: "failed", kind: systemInfoSource}},
		}},
	}

	manager.CollectPending(context.Background())
	manager.CollectPending(context.Background())

	require.Equal(1, calls)
	require.True(manager.collection[0].executors[0].attempted)
}

func TestStatusCachesCustomScriptResults(t *testing.T) {
	require := require.New(t)
	tmpDir := t.TempDir()
	readWriter := fileio.NewReadWriter(
		fileio.NewReader(fileio.WithReaderRootDir(tmpDir)),
		fileio.NewWriter(fileio.WithWriterRootDir(tmpDir)),
	)
	require.NoError(readWriter.MkdirAll(config.SystemInfoCustomScriptDir, fileio.DefaultDirectoryPermissions))

	const scriptName = "site.sh"
	writeScript := func(content string) {
		require.NoError(readWriter.WriteFile(
			filepath.Join(config.SystemInfoCustomScriptDir, scriptName),
			[]byte(content),
			fileio.DefaultExecutablePermissions,
		))
	}
	writeScript("#!/bin/sh\necho first\n")

	manager := NewManager(
		log.NewPrefixLogger("test"),
		executer.NewCommonExecuter(),
		readWriter,
		"etc/flightctl",
		nil,
		nil,
		util.Duration(time.Second),
		0,
	)
	now := time.Date(2026, time.September, 14, 20, 0, 0, 0, time.UTC)
	manager.now = func() time.Time {
		current := now
		now = now.Add(time.Second)
		return current
	}
	collect := func() *v1beta1.DeviceStatus {
		deviceStatus := &v1beta1.DeviceStatus{}
		manager.collect(context.Background())
		require.NoError(manager.Status(context.Background(), deviceStatus))
		return deviceStatus
	}

	initial := collect()
	require.Equal("first", (*initial.SystemInfo.CustomInfo)["site"])
	initialSource := initial.SystemInfoStatus.Statuses.CustomInfo["site"]
	require.Zero(initialSource.LastTransitionTime.Nanosecond())
	require.Nil(initialSource.Message)
	require.Equal(v1beta1.SystemInfoSourceStatusHealthy, initialSource.Status)

	writeScript("#!/bin/sh\necho second\n")
	repeatedSuccess := collect()
	require.Equal("second", (*repeatedSuccess.SystemInfo.CustomInfo)["site"])
	require.NotEqual(initialSource.LastTransitionTime, repeatedSuccess.SystemInfoStatus.Statuses.CustomInfo["site"].LastTransitionTime)
	require.Equal(v1beta1.SystemInfoSourceStatusHealthy, initialSource.Status)

	writeScript("#!/bin/sh\necho sensitive failure >&2\nexit 7\n")
	failed := collect()
	require.Equal("second", (*failed.SystemInfo.CustomInfo)["site"], "a failed script retains its last value")
	failedSource := failed.SystemInfoStatus.Statuses.CustomInfo["site"]
	require.Equal(deviceerrors.FromStderr("sensitive failure", 7).Error(), *failedSource.Message)
	require.NotEqual(initialSource.LastTransitionTime, failedSource.LastTransitionTime)
	require.Equal(v1beta1.SystemInfoSourceStatusError, failedSource.Status)

	writeScript("#!/bin/sh\necho another sensitive failure >&2\nexit 8\n")
	repeatedFailure := collect()
	require.Equal("second", (*repeatedFailure.SystemInfo.CustomInfo)["site"])
	require.Equal(deviceerrors.FromStderr("another sensitive failure", 8).Error(), *repeatedFailure.SystemInfoStatus.Statuses.CustomInfo["site"].Message)
	require.Equal(failedSource.LastTransitionTime, repeatedFailure.SystemInfoStatus.Statuses.CustomInfo["site"].LastTransitionTime)
	require.Equal(v1beta1.SystemInfoSourceStatusError, repeatedFailure.SystemInfoStatus.Statuses.CustomInfo["site"].Status)

	writeScript("#!/bin/sh\nexit 9\n")
	withoutStderr := collect()
	require.Equal(deviceerrors.FromStderr("exit status 9", 9).Error(), *withoutStderr.SystemInfoStatus.Statuses.CustomInfo["site"].Message)
	require.Equal(failedSource.LastTransitionTime, withoutStderr.SystemInfoStatus.Statuses.CustomInfo["site"].LastTransitionTime)
	require.Equal(v1beta1.SystemInfoSourceStatusError, withoutStderr.SystemInfoStatus.Statuses.CustomInfo["site"].Status)

	longMessage := strings.Repeat("x", status.MaxMessageLength+1)
	writeScript("#!/bin/sh\nprintf '" + longMessage + "' >&2\nexit 10\n")
	truncatedFailure := collect()
	require.Equal(log.Truncate(deviceerrors.FromStderr(longMessage, 10).Error(), status.MaxMessageLength), *truncatedFailure.SystemInfoStatus.Statuses.CustomInfo["site"].Message)
	require.Equal(failedSource.LastTransitionTime, truncatedFailure.SystemInfoStatus.Statuses.CustomInfo["site"].LastTransitionTime)
	require.Equal(v1beta1.SystemInfoSourceStatusError, truncatedFailure.SystemInfoStatus.Statuses.CustomInfo["site"].Status)

	writeScript("#!/bin/sh\necho recovered\n")
	recovered := collect()
	require.Equal("recovered", (*recovered.SystemInfo.CustomInfo)["site"])
	require.Nil(recovered.SystemInfoStatus.Statuses.CustomInfo["site"].Message)
	require.NotEqual(failedSource.LastTransitionTime, recovered.SystemInfoStatus.Statuses.CustomInfo["site"].LastTransitionTime)
	require.Equal(v1beta1.SystemInfoSourceStatusHealthy, recovered.SystemInfoStatus.Statuses.CustomInfo["site"].Status)
}

func TestStatusDiscoversDefaultCustomScriptsOnReload(t *testing.T) {
	require := require.New(t)
	tmpDir := t.TempDir()
	readWriter := fileio.NewReadWriter(
		fileio.NewReader(fileio.WithReaderRootDir(tmpDir)),
		fileio.NewWriter(fileio.WithWriterRootDir(tmpDir)),
	)
	require.NoError(readWriter.MkdirAll(config.SystemInfoCustomScriptDir, fileio.DefaultDirectoryPermissions))

	manager := NewManager(
		log.NewPrefixLogger("test"),
		executer.NewCommonExecuter(),
		readWriter,
		"etc/flightctl",
		nil,
		nil,
		util.Duration(time.Second),
		0,
	)
	collect := func() *v1beta1.DeviceStatus {
		deviceStatus := &v1beta1.DeviceStatus{}
		manager.collect(context.Background())
		require.NoError(manager.Status(context.Background(), deviceStatus))
		return deviceStatus
	}

	initial := collect()
	require.Empty(initial.SystemInfoStatus.Statuses.CustomInfo)
	require.Equal(v1beta1.SystemInfoSummaryStatusUnknown, initial.SystemInfoStatus.Summary.Status)
	require.NoError(readWriter.WriteFile(
		filepath.Join(config.SystemInfoCustomScriptDir, "discovered.sh"),
		[]byte("#!/bin/sh\necho discovered\n"),
		fileio.DefaultExecutablePermissions,
	))
	require.NoError(readWriter.WriteFile(
		filepath.Join(config.SystemInfoCustomScriptDir, "ignored.sh"),
		[]byte("#!/bin/sh\necho ignored\n"),
		0644,
	))
	beforeReload := collect()
	require.Empty(beforeReload.SystemInfoStatus.Statuses.CustomInfo)

	require.NoError(manager.ReloadConfig(context.Background(), &config.Config{
		SystemInfoTimeout: util.Duration(time.Second),
	}))
	discovered := collect()
	require.Contains(discovered.SystemInfoStatus.Statuses.CustomInfo, "discovered")
	require.NotContains(discovered.SystemInfoStatus.Statuses.CustomInfo, "ignored")
	require.Equal("discovered", (*discovered.SystemInfo.CustomInfo)["discovered"])

	require.NoError(readWriter.RemoveFile(filepath.Join(config.SystemInfoCustomScriptDir, "discovered.sh")))
	require.NoError(manager.ReloadConfig(context.Background(), &config.Config{
		SystemInfoTimeout: util.Duration(time.Second),
	}))
	require.Empty(collect().SystemInfoStatus.Statuses.CustomInfo)
}

func TestStatusReportsMissingAllowListedCustomScript(t *testing.T) {
	require := require.New(t)
	tmpDir := t.TempDir()
	readWriter := fileio.NewReadWriter(
		fileio.NewReader(fileio.WithReaderRootDir(tmpDir)),
		fileio.NewWriter(fileio.WithWriterRootDir(tmpDir)),
	)
	require.NoError(readWriter.MkdirAll(config.SystemInfoCustomScriptDir, fileio.DefaultDirectoryPermissions))

	manager := NewManager(
		log.NewPrefixLogger("test"),
		executer.NewCommonExecuter(),
		readWriter,
		"etc/flightctl",
		nil,
		[]string{"missing"},
		util.Duration(time.Second),
		0,
	)
	deviceStatus := &v1beta1.DeviceStatus{}
	manager.collect(context.Background())
	require.NoError(manager.Status(context.Background(), deviceStatus))

	require.Nil(deviceStatus.SystemInfo.CustomInfo)
	source := deviceStatus.SystemInfoStatus.Statuses.CustomInfo["missing"]
	require.Equal("script not found", *source.Message)
	require.Equal(v1beta1.SystemInfoSummaryStatusError, deviceStatus.SystemInfoStatus.Summary.Status)
}

func TestStatusClearsAnAllowListedValueWhenTheScriptIsMissingAfterReload(t *testing.T) {
	require := require.New(t)
	tmpDir := t.TempDir()
	readWriter := fileio.NewReadWriter(
		fileio.NewReader(fileio.WithReaderRootDir(tmpDir)),
		fileio.NewWriter(fileio.WithWriterRootDir(tmpDir)),
	)
	require.NoError(readWriter.MkdirAll(config.SystemInfoCustomScriptDir, fileio.DefaultDirectoryPermissions))
	const script = "site.sh"
	require.NoError(readWriter.WriteFile(
		filepath.Join(config.SystemInfoCustomScriptDir, script),
		[]byte("#!/bin/sh\necho available\n"),
		fileio.DefaultExecutablePermissions,
	))

	manager := NewManager(
		log.NewPrefixLogger("test"),
		executer.NewCommonExecuter(),
		readWriter,
		"etc/flightctl",
		nil,
		[]string{"site"},
		util.Duration(time.Second),
		0,
	)
	now := time.Date(2026, time.September, 14, 20, 0, 0, 0, time.UTC)
	manager.now = func() time.Time {
		current := now
		now = now.Add(time.Second)
		return current
	}
	collect := func() *v1beta1.DeviceStatus {
		deviceStatus := &v1beta1.DeviceStatus{}
		manager.collect(context.Background())
		require.NoError(manager.Status(context.Background(), deviceStatus))
		return deviceStatus
	}

	available := collect()
	require.Equal("available", (*available.SystemInfo.CustomInfo)["site"])
	availableTime := available.SystemInfoStatus.Statuses.CustomInfo["site"].LastTransitionTime

	require.NoError(readWriter.RemoveFile(filepath.Join(config.SystemInfoCustomScriptDir, script)))
	require.NoError(manager.ReloadConfig(context.Background(), &config.Config{
		SystemInfoCustom:  []string{"site"},
		SystemInfoTimeout: util.Duration(time.Second),
	}))
	missing := collect()
	require.Nil(missing.SystemInfo.CustomInfo)
	missingSource := missing.SystemInfoStatus.Statuses.CustomInfo["site"]
	require.Equal("script not found", *missingSource.Message)
	require.NotEqual(availableTime, missingSource.LastTransitionTime)
}

func TestStatusReportsSelectedBuiltInSources(t *testing.T) {
	require := require.New(t)
	manager := NewManager(
		log.NewPrefixLogger("test"),
		executer.NewCommonExecuter(),
		fileio.NewReadWriter(fileio.NewReader(), fileio.NewWriter()),
		"etc/flightctl",
		[]string{common.ArchitectureKey},
		[]string{},
		util.Duration(time.Second),
		0,
	)
	deviceStatus := &v1beta1.DeviceStatus{}
	manager.collect(context.Background())
	require.NoError(manager.Status(context.Background(), deviceStatus))

	require.Equal(runtime.GOARCH, deviceStatus.SystemInfo.AdditionalProperties[common.ArchitectureKey])
	require.Contains(deviceStatus.SystemInfoStatus.Statuses.SystemInfo, common.ArchitectureKey)
	require.Empty(deviceStatus.SystemInfoStatus.Statuses.CustomInfo)
	require.Nil(deviceStatus.SystemInfoStatus.Statuses.SystemInfo[common.ArchitectureKey].Message)
	require.Equal(v1beta1.SystemInfoSummaryStatusHealthy, deviceStatus.SystemInfoStatus.Summary.Status)
}

func TestInfoFromCacheCombinesSelectedValuesFromASharedSource(t *testing.T) {
	require := require.New(t)
	productName := systemInfoKeyDefinitions[common.ProductNameKey]
	productSerial := systemInfoKeyDefinitions[common.ProductSerialKey]
	raw := &Info{Hardware: HardwareFacts{System: &SystemInfo{
		ProductName:  "Edge Device",
		SerialNumber: "serial-123",
		UUID:         "unselected-uuid",
	}}}
	manager := &manager{collection: []*collector{{raw: raw, executors: []*cachedExecutor{
		{projectInfo: productName.projectInfo},
		{projectInfo: productSerial.projectInfo},
	}}}}

	info := manager.infoFromCache()
	require.Equal("Edge Device", info.Hardware.System.ProductName)
	require.Equal("serial-123", info.Hardware.System.SerialNumber)
	require.Empty(info.Hardware.System.UUID)
}
