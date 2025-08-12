package systeminfo

import (
	"context"
	"encoding/json"
	"fmt"
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
	readWriter := fileio.NewReadWriter()
	readWriter.SetRootdir(tmpDir)
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

// go test -benchmem -run=^$ -bench ^BenchmarkCollectInfo$ -cpuprofile=cpu.pprof -memprofile=mem.pprof github.com/flightctl/flightctl/internal/agent/device/systeminfo
func BenchmarkCollectInfo(b *testing.B) {
	ctx := context.Background()
	log := log.NewPrefixLogger("test")
	exec := &executer.CommonExecuter{}
	reader := fileio.NewReadWriter()
	hardwareMapPath := "/var/lib/flightctl/hardware_map.json"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		info, err := Collect(ctx, log, exec, reader, nil, hardwareMapPath)
		if err != nil {
			b.Fatalf("CollectInfo failed: %v", err)
		}
		// use value to prevent compiler optimization
		if info == nil {
			b.Fatal("Expected non-nil info")
		}
	}
}

func TestGetCustomInfoMap(t *testing.T) {
	require := require.New(t)

	tests := []struct {
		name           string
		scriptName     string
		scriptContent  []byte
		keys           []string
		lookupKey      string
		expectedValue  string
		expectedExists bool
		wantError      error
	}{
		{
			name:           "script matching exact key with no extension",
			scriptName:     "hostname",
			scriptContent:  generateScriptBytes(0, "hostname_test", 0),
			keys:           []string{"hostname"},
			lookupKey:      "hostname",
			expectedValue:  "hostname_test",
			expectedExists: true,
		},
		{
			name:           "script matching with .sh extension",
			scriptName:     "hostname.sh",
			scriptContent:  generateScriptBytes(0, "hostname_test", 0),
			keys:           []string{"hostname"},
			lookupKey:      "hostname",
			expectedValue:  "hostname_test",
			expectedExists: true,
		},
		{
			name:           "script matching with prefix",
			scriptName:     "01-hostname",
			scriptContent:  generateScriptBytes(0, "hostname_test", 0),
			keys:           []string{"hostname"},
			lookupKey:      "hostname",
			expectedValue:  "hostname_test",
			expectedExists: true,
		},
		{
			name:           "script matching with prefix and extension",
			scriptName:     "01-hostname.sh",
			scriptContent:  generateScriptBytes(0, "hostname_test", 0),
			keys:           []string{"hostname"},
			lookupKey:      "hostname",
			expectedValue:  "hostname_test",
			expectedExists: true,
		},
		{
			name:           "script not matching due to extra suffix",
			scriptName:     "01-hostname_custom.sh",
			scriptContent:  generateScriptBytes(0, "hostname_test", 0),
			keys:           []string{"hostname"},
			lookupKey:      "hostname",
			expectedValue:  "", // no matching script
			expectedExists: true,
		},
		{
			name:           "script exits non-zero",
			scriptName:     "hostname",
			scriptContent:  generateScriptBytes(0, "hostname_test", 1),
			keys:           []string{"hostname"},
			lookupKey:      "hostname",
			expectedValue:  "", // fallback to empty on non-zero exit
			expectedExists: true,
		},
		{
			name:           "timeout handling",
			scriptName:     "hostname",
			scriptContent:  generateScriptBytes(200, "hostname_test", 0),
			keys:           []string{"hostname"},
			lookupKey:      "hostname",
			expectedValue:  "", // fallback to empty on timeout
			expectedExists: true,
		},
		{
			name:           "no script found for custom key",
			scriptName:     "custom_key",
			scriptContent:  generateScriptBytes(0, "custom_value", 0),
			keys:           []string{"hostname"},
			lookupKey:      "hostname",
			expectedValue:  "",
			expectedExists: true,
		},
		{
			name:           "script name is camel case",
			scriptName:     "10-myCustomInfo.sh",
			scriptContent:  generateScriptBytes(0, "custom_value", 0),
			keys:           []string{"myCustomInfo"},
			lookupKey:      "myCustomInfo",
			expectedValue:  "custom_value",
			expectedExists: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			rw := fileio.NewReadWriter()
			rw.SetRootdir(tmpDir)

			err := rw.MkdirAll(config.SystemInfoCustomScriptDir, fileio.DefaultDirectoryPermissions)
			require.NoError(err)

			if tt.scriptName != "" {
				err = rw.WriteFile(
					filepath.Join(config.SystemInfoCustomScriptDir, tt.scriptName),
					tt.scriptContent,
					fileio.DefaultExecutablePermissions,
				)
				require.NoError(err)
			}

			log := log.NewPrefixLogger("test")
			exec := &executer.CommonExecuter{}
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()

			details, err := getCustomInfoMap(ctx, log, tt.keys, rw, exec)
			if tt.wantError != nil {
				require.Error(err)
				require.ErrorIs(tt.wantError, err)
				return
			}
			require.NoError(err)

			value, exists := details[tt.lookupKey]
			require.Equal(tt.expectedExists, exists, "key existence mismatch")
			require.Equal(tt.expectedValue, value, "value mismatch")
		})
	}
}

func generateScriptBytes(sleepms int, output string, exitCode int) []byte {
	var sleepCmd string
	if sleepms > 0 {
		// generate sleep
		sleepCmd = fmt.Sprintf("sleep 0.%03d\n", sleepms)
	}

	content := fmt.Sprintf("#!/bin/bash\n%secho '%s'\nexit %d", sleepCmd, output, exitCode)
	return []byte(content)
}

func TestGetCollectionOptsFromInfoKeys(t *testing.T) {
	tests := []struct {
		name          string
		infoKeys      []string
		expectCPU     bool
		expectGPU     bool
		expectMemory  bool
		expectNetwork bool
		expectBIOS    bool
		expectSystem  bool
		expectKernel  bool
		expectDistro  bool
	}{
		{
			name:     "empty infoKeys",
			infoKeys: []string{},
		},
		{
			name:      "CPU keys",
			infoKeys:  []string{infoKeyCPUCores, infoKeyCPUProcessors, infoKeyCPUModel},
			expectCPU: true,
		},
		{
			name:      "GPU keys",
			infoKeys:  []string{infoKeyGPU},
			expectGPU: true,
		},
		{
			name:         "memory keys",
			infoKeys:     []string{infoKeyMemoryTotalKb},
			expectMemory: true,
		},
		{
			name:          "network keys",
			infoKeys:      []string{infoKeyNetInterfaceDefault, infoKeyNetIPDefault, infoKeyNetMACDefault},
			expectNetwork: true,
		},
		{
			name:       "BIOS keys",
			infoKeys:   []string{infoKeyBIOSVendor, infoKeyBIOSVersion},
			expectBIOS: true,
		},
		{
			name:         "system keys",
			infoKeys:     []string{infoKeyProductName, infoKeyProductSerial, infoKeyProductUUID},
			expectSystem: true,
		},
		{
			name:         "kernel key",
			infoKeys:     []string{infoKeyKernel},
			expectKernel: true,
		},
		{
			name:         "distribution keys",
			infoKeys:     []string{infoKeyDistroName, infoKeyDistroVersion},
			expectDistro: true,
		},
		{
			name:          "mixed keys",
			infoKeys:      []string{infoKeyCPUCores, infoKeyGPU, infoKeyNetIPDefault, infoKeyProductName},
			expectCPU:     true,
			expectGPU:     true,
			expectNetwork: true,
			expectSystem:  true,
		},
		{
			name:     "unknown keys",
			infoKeys: []string{"unknownKey", "anotherUnknown"},
		},
		{
			name:      "mixed known and unknown",
			infoKeys:  []string{infoKeyCPUCores, "unknownKey", infoKeyGPU},
			expectCPU: true,
			expectGPU: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := collectionOptsFromInfoKeys(tt.infoKeys)

			cfg := &collectCfg{}
			for _, opt := range opts {
				opt(cfg)
			}

			require.Equal(t, tt.expectCPU, cfg.hasCollector(collectorCPU), "CPU collection mismatch")
			require.Equal(t, tt.expectGPU, cfg.hasCollector(collectorGPU), "GPU collection mismatch")
			require.Equal(t, tt.expectMemory, cfg.hasCollector(collectorMemory), "Memory collection mismatch")
			require.Equal(t, tt.expectNetwork, cfg.hasCollector(collectorNetwork), "Network collection mismatch")
			require.Equal(t, tt.expectBIOS, cfg.hasCollector(collectorBIOS), "BIOS collection mismatch")
			require.Equal(t, tt.expectSystem, cfg.hasCollector(collectorSystem), "System collection mismatch")
			require.Equal(t, tt.expectKernel, cfg.hasCollector(collectorKernel), "Kernel collection mismatch")
			require.Equal(t, tt.expectDistro, cfg.hasCollector(collectorDistribution), "Distribution collection mismatch")
		})
	}
}

func TestCollectOptFunctions(t *testing.T) {
	t.Run("WithAllDefault enables all collections", func(t *testing.T) {
		cfg := &collectCfg{}
		WithAllDefault()(cfg)

		require.True(t, cfg.hasCollector(collectorCPU))
		require.True(t, cfg.hasCollector(collectorGPU))
		require.True(t, cfg.hasCollector(collectorMemory))
		require.True(t, cfg.hasCollector(collectorNetwork))
		require.True(t, cfg.hasCollector(collectorBIOS))
		require.True(t, cfg.hasCollector(collectorSystem))
		require.True(t, cfg.hasCollector(collectorKernel))
		require.True(t, cfg.hasCollector(collectorDistribution))
		require.False(t, cfg.collectAllCustom) // Should not affect custom
	})

	t.Run("individual collection options", func(t *testing.T) {
		tests := []struct {
			name  string
			opt   CollectOpt
			check collectorType
		}{
			{"WithCPUCollection", WithCPUCollection(), collectorCPU},
			{"WithGPUCollection", WithGPUCollection(), collectorGPU},
			{"WithMemoryCollection", WithMemoryCollection(), collectorMemory},
			{"WithNetworkCollection", WithNetworkCollection(), collectorNetwork},
			{"WithBIOSCollection", WithBIOSCollection(), collectorBIOS},
			{"WithSystemCollection", WithSystemCollection(), collectorSystem},
			{"WithKernelCollection", WithKernelCollection(), collectorKernel},
			{"WithDistributionCollection", WithDistributionCollection(), collectorDistribution},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				cfg := &collectCfg{}
				tt.opt(cfg)
				require.True(t, cfg.hasCollector(tt.check), "Option should enable its corresponding collection")
			})
		}
	})

	t.Run("combining options", func(t *testing.T) {
		tests := []struct {
			name      string
			opts      []CollectOpt
			expected  map[collectorType]bool
			allCustom bool
		}{
			{
				name: "CPU and GPU only",
				opts: []CollectOpt{WithCPUCollection(), WithGPUCollection()},
				expected: map[collectorType]bool{
					collectorCPU: true, collectorGPU: true,
					collectorMemory: false, collectorNetwork: false,
					collectorBIOS: false, collectorSystem: false,
					collectorKernel: false, collectorDistribution: false,
				},
				allCustom: false,
			},
			{
				name: "Hardware collectors",
				opts: []CollectOpt{WithCPUCollection(), WithMemoryCollection(), WithBIOSCollection(), WithSystemCollection()},
				expected: map[collectorType]bool{
					collectorCPU: true, collectorMemory: true, collectorBIOS: true, collectorSystem: true,
					collectorGPU: false, collectorNetwork: false,
					collectorKernel: false, collectorDistribution: false,
				},
				allCustom: false,
			},
			{
				name: "With custom collection",
				opts: []CollectOpt{WithNetworkCollection(), WithAllCustom()},
				expected: map[collectorType]bool{
					collectorNetwork: true,
					collectorCPU:     false, collectorGPU: false, collectorMemory: false,
					collectorBIOS: false, collectorSystem: false,
					collectorKernel: false, collectorDistribution: false,
				},
				allCustom: true,
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				cfg := &collectCfg{}
				for _, opt := range tt.opts {
					opt(cfg)
				}

				for collectorType, expected := range tt.expected {
					require.Equal(t, expected, cfg.hasCollector(collectorType),
						"Collector type %v should be %v", collectorType, expected)
				}
				require.Equal(t, tt.allCustom, cfg.collectAllCustom)
			})
		}
	})
}

func TestCollect_AllDisabled(t *testing.T) {
	tmpDir := t.TempDir()
	readWriter := fileio.NewReadWriter(fileio.WithTestRootDir(tmpDir))

	// Create minimal file structure
	err := readWriter.MkdirAll("/proc/sys/kernel/random", 0755)
	require.NoError(t, err)
	err = readWriter.WriteFile(bootIDPath, []byte("test-boot-id"), 0644)
	require.NoError(t, err)

	ctrl := gomock.NewController(t)
	mockExecuter := executer.NewMockExecuter(ctrl)
	logger := log.NewPrefixLogger("test")
	ctx := context.Background()

	// Boot time collection always happens (needed for reboot detection)
	mockExecuter.EXPECT().ExecuteWithContext(gomock.Any(), "uptime", "-s").Return("2024-12-13 11:01:08", "", 0).Times(1)

	info, err := Collect(ctx, logger, mockExecuter, readWriter, nil, "")
	require.NoError(t, err)
	require.NotNil(t, info)

	// Should have basic info (always collected)
	require.NotEmpty(t, info.OperatingSystem)
	require.NotEmpty(t, info.Architecture)
	require.NotEmpty(t, info.Boot.ID)
	require.NotEmpty(t, info.Boot.Time)

	// Should NOT have hardware info
	require.Nil(t, info.Hardware.CPU)
	require.Len(t, info.Hardware.GPU, 0)
	require.Nil(t, info.Hardware.Memory)
	require.Nil(t, info.Hardware.Network)
	require.Nil(t, info.Hardware.BIOS)
	require.Nil(t, info.Hardware.System)
	require.Nil(t, info.Distribution)
	require.Empty(t, info.Kernel)

}
