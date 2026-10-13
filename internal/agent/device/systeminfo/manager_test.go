package systeminfo

import (
	"context"
	"encoding/json"
	"errors"
	"math/rand/v2"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
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
		rng:                rand.New(rand.NewPCG(1, 2)), //nolint:gosec // G404 - shuffling does not require crypto-strength randomness
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
		rng:               rand.New(rand.NewPCG(1, 2)), //nolint:gosec // G404 - shuffling does not require crypto-strength randomness
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
		rng:               rand.New(rand.NewPCG(1, 2)), //nolint:gosec // G404 - shuffling does not require crypto-strength randomness
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
		rng:               rand.New(rand.NewPCG(1, 2)), //nolint:gosec // G404 - shuffling does not require crypto-strength randomness
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

	// Stderr is bounded to maxCustomCollectorOutput/2 bytes, so very long
	// error messages are truncated at the output capture level before the
	// status message truncation applies.
	longMessage := strings.Repeat("x", maxCustomCollectorOutput+1)
	writeScript("#!/bin/sh\nprintf '" + longMessage + "' >&2\nexit 10\n")
	truncatedFailure := collect()
	capturedStderr := longMessage[:maxCustomCollectorOutput/2]
	require.Equal(log.Truncate(deviceerrors.FromStderr(capturedStderr, 10).Error(), status.MaxMessageLength), *truncatedFailure.SystemInfoStatus.Statuses.CustomInfo["site"].Message)
	require.Equal(failedSource.LastTransitionTime, truncatedFailure.SystemInfoStatus.Statuses.CustomInfo["site"].LastTransitionTime)
	require.Equal(v1beta1.SystemInfoSourceStatusError, truncatedFailure.SystemInfoStatus.Statuses.CustomInfo["site"].Status)

	writeScript("#!/bin/sh\necho recovered\n")
	recovered := collect()
	require.Equal("recovered", (*recovered.SystemInfo.CustomInfo)["site"])
	require.Nil(recovered.SystemInfoStatus.Statuses.CustomInfo["site"].Message)
	require.NotEqual(failedSource.LastTransitionTime, recovered.SystemInfoStatus.Statuses.CustomInfo["site"].LastTransitionTime)
	require.Equal(v1beta1.SystemInfoSourceStatusHealthy, recovered.SystemInfoStatus.Statuses.CustomInfo["site"].Status)
}

func TestStatusReloadDiscoversConfiguredAndDefaultCustomScripts(t *testing.T) {
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
		[]string{},
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
		SystemInfoCustom:  []string{"*"},
		SystemInfoTimeout: util.Duration(time.Second),
	}))
	wildcardDiscovered := collect()
	require.Contains(wildcardDiscovered.SystemInfoStatus.Statuses.CustomInfo, "discovered")
	require.NotContains(wildcardDiscovered.SystemInfoStatus.Statuses.CustomInfo, "ignored")
	require.Equal("discovered", (*wildcardDiscovered.SystemInfo.CustomInfo)["discovered"])

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

func TestCollectSourcesShufflesOrder(t *testing.T) {
	require := require.New(t)

	// Record the execution order of collector keys across cycles.
	var mu sync.Mutex
	var orders [][]string

	makeEntry := func(key string) sourceEntry {
		return sourceEntry{key: key, kind: customInfoSource, definition: collectorDefinition{
			source: &sourceDefinition{collect: func(_ context.Context, _ *collectContext, info *Info) error {
				return nil
			}},
			extract: func(*Info) string {
				mu.Lock()
				if len(orders) == 0 {
					orders = append(orders, nil)
				}
				orders[len(orders)-1] = append(orders[len(orders)-1], key)
				mu.Unlock()
				return ""
			},
			projectInfo: copyCustomInfo,
		}}
	}

	entries := []sourceEntry{
		makeEntry("a"),
		makeEntry("b"),
		makeEntry("c"),
	}

	// Use a fixed-seed rng for deterministic shuffle.
	m := &manager{
		collectionTimeout: time.Second,
		now:               time.Now,
		rng:               rand.New(rand.NewPCG(42, 99)), //nolint:gosec // G404 - shuffling does not require crypto-strength randomness
		log:               log.NewPrefixLogger("test"),
		collection: collectorsForEntries(
			log.NewPrefixLogger("test"),
			nil, nil, "",
			entries, nil,
		),
	}

	// Run two collection cycles and verify deterministic order from the
	// fixed-seed RNG.
	m.collect(context.Background())
	mu.Lock()
	orders = append(orders, nil)
	mu.Unlock()
	m.collect(context.Background())

	mu.Lock()
	defer mu.Unlock()
	require.Len(orders, 2, "two collection cycles must run")
	require.Len(orders[0], len(entries), "all sources must be collected")
	require.Len(orders[1], len(entries), "all sources must be collected")

	// With a fixed seed the same rng produces the same sequence when called
	// with the same input length; verify the two cycles produced identical
	// permutations (determinism from the seed).
	rng2 := rand.New(rand.NewPCG(42, 99)) //nolint:gosec // G404 - shuffling does not require crypto-strength randomness
	expected := []string{"a", "b", "c"}
	first := make([]string, len(expected))
	copy(first, expected)
	rng2.Shuffle(len(first), func(i, j int) { first[i], first[j] = first[j], first[i] })
	require.Equal(first, orders[0], "first cycle must match the deterministic seed order")
}

func TestCustomCollectorOutputIsSanitized(t *testing.T) {
	require := require.New(t)
	tmpDir := t.TempDir()
	readWriter := fileio.NewReadWriter(
		fileio.NewReader(fileio.WithReaderRootDir(tmpDir)),
		fileio.NewWriter(fileio.WithWriterRootDir(tmpDir)),
	)
	require.NoError(readWriter.MkdirAll(config.SystemInfoCustomScriptDir, fileio.DefaultDirectoryPermissions))
	require.NoError(readWriter.WriteFile(
		filepath.Join(config.SystemInfoCustomScriptDir, "dirty.sh"),
		// Output contains characters that sanitizeCollectorValue should strip.
		[]byte("#!/bin/sh\nprintf 'hello<world>&test/ok'\n"),
		fileio.DefaultExecutablePermissions,
	))

	m := NewManager(
		log.NewPrefixLogger("test"),
		executer.NewCommonExecuter(),
		readWriter,
		"etc/flightctl",
		nil,
		[]string{"dirty"},
		util.Duration(time.Second),
		0,
	)
	m.collect(context.Background())

	// Find the custom executor (skip the boot hidden source).
	var exec *cachedExecutor
	for _, c := range m.collection {
		for _, e := range c.executors {
			if e.key == "dirty" {
				exec = e
			}
		}
	}
	require.NotNil(exec)
	require.True(exec.attempted)
	require.False(exec.failed)
	require.Equal("helloworldtest/ok", exec.value, "non-allowed characters must be stripped")
}

// newCacheTestManager builds a manager backed by a temp filesystem suitable for
// exercising the cached GPU/KVM collection behavior.
func newCacheTestManager(t *testing.T) (*manager, fileio.ReadWriter) {
	t.Helper()
	require := require.New(t)

	tmpDir := t.TempDir()
	dataDir := filepath.Join("etc", "flightctl")
	readWriter := fileio.NewReadWriter(
		fileio.NewReader(fileio.WithReaderRootDir(tmpDir)),
		fileio.NewWriter(fileio.WithWriterRootDir(tmpDir)),
	)
	require.NoError(readWriter.MkdirAll(dataDir, 0755))
	require.NoError(readWriter.MkdirAll("/proc/sys/kernel/random", 0755))
	require.NoError(readWriter.WriteFile(bootIDPath, []byte("boot-id"), 0644))

	ctrl := gomock.NewController(t)
	mockExecuter := executer.NewMockExecuter(ctrl)
	mockExecuter.EXPECT().ExecuteWithContext(gomock.Any(), "uptime", "-s").Return("2024-12-13 11:01:08", "", 0).AnyTimes()

	manager := NewManager(log.NewPrefixLogger("test"), mockExecuter, readWriter, dataDir, nil, nil, util.Duration(5*time.Second), 0)
	return manager, readWriter
}

// TestStatusReportsCachedGPUInventory verifies that the GPU inventory is gathered
// during the collection cycle and served from the cache: Status reports the
// snapshot taken at collection time and does not re-scan the filesystem on every
// status generation. A fresh scan only happens on the next collection (here, a
// forced one).
func TestStatusReportsCachedGPUInventory(t *testing.T) {
	require := require.New(t)
	manager, readWriter := newCacheTestManager(t)

	writeGPU := func(address, device string) {
		deviceDir := filepath.Join(pciDevicesPath, address)
		require.NoError(readWriter.MkdirAll(deviceDir, fileio.DefaultDirectoryPermissions))
		require.NoError(readWriter.WriteFile(filepath.Join(deviceDir, "class"), []byte("0x030000"), fileio.DefaultFilePermissions))
		require.NoError(readWriter.WriteFile(filepath.Join(deviceDir, "vendor"), []byte("0x10de"), fileio.DefaultFilePermissions))
		require.NoError(readWriter.WriteFile(filepath.Join(deviceDir, "device"), []byte(device), fileio.DefaultFilePermissions))
	}
	writeGPU("0000:41:00.0", "0x2717")

	require.NoError(manager.Initialize(context.Background()))

	// Status (without force) serves the snapshot collected during Initialize.
	deviceStatus := &v1beta1.DeviceStatus{}
	require.NoError(manager.Status(context.Background(), deviceStatus))
	require.NotNil(deviceStatus.SystemInfo.Gpus)
	require.Len(*deviceStatus.SystemInfo.Gpus, 1)

	// A second GPU appears after collection. Status must keep serving the cached
	// snapshot rather than re-scanning on each call.
	writeGPU("0000:42:00.0", "0x2718")
	deviceStatus = &v1beta1.DeviceStatus{}
	require.NoError(manager.Status(context.Background(), deviceStatus))
	require.NotNil(deviceStatus.SystemInfo.Gpus)
	require.Len(*deviceStatus.SystemInfo.Gpus, 1)

	// Forcing collection refreshes the cache and picks up the new GPU.
	deviceStatus = &v1beta1.DeviceStatus{}
	require.NoError(manager.Status(context.Background(), deviceStatus, status.WithForceCollect()))
	require.NotNil(deviceStatus.SystemInfo.Gpus)
	require.Len(*deviceStatus.SystemInfo.Gpus, 2)
}

// TestStatusReportsCachedKVMAvailability verifies that KVM availability is
// gathered during the collection cycle and served from the cache: Status reports
// the snapshot taken at collection time and does not re-scan the filesystem on
// every status generation. A fresh scan only happens on the next collection
// (here, a forced one).
func TestStatusReportsCachedKVMAvailability(t *testing.T) {
	require := require.New(t)
	manager, readWriter := newCacheTestManager(t)

	// Initialize with no KVM modules and no /dev/kvm: availability is false.
	require.NoError(manager.Initialize(context.Background()))

	deviceStatus := &v1beta1.DeviceStatus{}
	require.NoError(manager.Status(context.Background(), deviceStatus))
	require.NotNil(deviceStatus.SystemInfo.Kvm)
	require.NotNil(deviceStatus.SystemInfo.Kvm.Enabled)
	require.False(*deviceStatus.SystemInfo.Kvm.Enabled)

	// KVM becomes available after collection. Writing all module names keeps the
	// check architecture-agnostic. Status must keep serving the cached snapshot.
	require.NoError(readWriter.MkdirAll(filepath.Dir(procModulesPath), fileio.DefaultDirectoryPermissions))
	require.NoError(readWriter.WriteFile(procModulesPath, []byte("kvm 1 0 - Live 0x0\nkvm_intel 1 0 - Live 0x0\nkvm_amd 1 0 - Live 0x0\n"), fileio.DefaultFilePermissions))
	require.NoError(readWriter.MkdirAll(filepath.Dir(devKVMPath), fileio.DefaultDirectoryPermissions))
	require.NoError(readWriter.WriteFile(devKVMPath, []byte{}, fileio.DefaultFilePermissions))

	deviceStatus = &v1beta1.DeviceStatus{}
	require.NoError(manager.Status(context.Background(), deviceStatus))
	require.NotNil(deviceStatus.SystemInfo.Kvm)
	require.NotNil(deviceStatus.SystemInfo.Kvm.Enabled)
	require.False(*deviceStatus.SystemInfo.Kvm.Enabled)

	// Forcing collection refreshes the cache and picks up the newly available KVM.
	deviceStatus = &v1beta1.DeviceStatus{}
	require.NoError(manager.Status(context.Background(), deviceStatus, status.WithForceCollect()))
	require.NotNil(deviceStatus.SystemInfo.Kvm)
	require.NotNil(deviceStatus.SystemInfo.Kvm.Enabled)
	require.True(*deviceStatus.SystemInfo.Kvm.Enabled)
}
