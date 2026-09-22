package systeminfo

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/flightctl/flightctl/internal/agent/config"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/agent/device/systeminfo/common"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

// go test -benchmem -run=^$ -bench ^BenchmarkCollectInfo$ -cpuprofile=cpu.pprof -memprofile=mem.pprof github.com/flightctl/flightctl/internal/agent/device/systeminfo
func BenchmarkCollectInfo(b *testing.B) {
	ctx := context.Background()
	log := log.NewPrefixLogger("test")
	exec := executer.NewCommonExecuter()
	reader := fileio.NewReadWriter(fileio.NewReader(), fileio.NewWriter())
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

func TestCollectCustomInfo(t *testing.T) {
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
			rw := fileio.NewReadWriter(
				fileio.NewReader(fileio.WithReaderRootDir(tmpDir)),
				fileio.NewWriter(fileio.WithWriterRootDir(tmpDir)),
			)

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
			exec := executer.NewCommonExecuter()
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()

			info, err := Collect(ctx, log, exec, rw, tt.keys, "")
			if tt.wantError != nil {
				require.Error(err)
				require.ErrorIs(err, tt.wantError)
				return
			}
			require.NoError(err)

			value, exists := info.Custom[tt.lookupKey]
			require.Equal(tt.expectedExists, exists, "key existence mismatch")
			require.Equal(tt.expectedValue, value, "value mismatch")
		})
	}
}

func TestCollectDiscoversExecutableCustomScripts(t *testing.T) {
	require := require.New(t)
	tmpDir := t.TempDir()
	readWriter := fileio.NewReadWriter(
		fileio.NewReader(fileio.WithReaderRootDir(tmpDir)),
		fileio.NewWriter(fileio.WithWriterRootDir(tmpDir)),
	)
	require.NoError(readWriter.MkdirAll(config.SystemInfoCustomScriptDir, fileio.DefaultDirectoryPermissions))
	require.NoError(readWriter.WriteFile(
		filepath.Join(config.SystemInfoCustomScriptDir, "site.sh"),
		[]byte("#!/bin/sh\necho site\n"),
		fileio.DefaultExecutablePermissions,
	))
	require.NoError(readWriter.WriteFile(
		filepath.Join(config.SystemInfoCustomScriptDir, "ignored.sh"),
		[]byte("#!/bin/sh\necho ignored\n"),
		0644,
	))

	ctrl := gomock.NewController(t)
	exec := executer.NewMockExecuter(ctrl)
	exec.EXPECT().ExecuteWithContext(gomock.Any(), "uptime", "-s").Return("2024-12-13 11:01:08", "", 0)
	exec.EXPECT().ExecuteWithContext(
		gomock.Any(),
		filepath.Join(readWriter.PathFor(config.SystemInfoCustomScriptDir), "site.sh"),
	).Return("site\n", "", 0)

	info, err := Collect(context.Background(), log.NewPrefixLogger("test"), exec, readWriter, nil, "", WithAllCustom())
	require.NoError(err)
	require.Equal(map[string]string{"site": "site"}, info.Custom)
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
		name      string
		infoKeys  []string
		expected  []string
		expectErr bool
	}{
		{name: "empty infoKeys"},
		{
			name:     "selected keys are retained without source expansion",
			infoKeys: []string{common.ProductNameKey, common.ProductNameKey, common.NetIPDefaultKey},
			expected: []string{common.ProductNameKey, common.NetIPDefaultKey},
		},
		{
			name:      "unknown keys are rejected",
			infoKeys:  []string{"unknownKey", "anotherUnknown"},
			expectErr: true,
		},
		{
			name:      "known keys survive unknown keys",
			infoKeys:  []string{common.CPUCoresKey, "unknownKey", common.GPUKey},
			expected:  []string{common.CPUCoresKey, common.GPUKey},
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts, err := collectionOptsFromInfoKeys(tt.infoKeys)

			if tt.expectErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}

			cfg := &collectCfg{}
			for _, opt := range opts {
				opt(cfg)
			}

			require.Equal(t, tt.expected, cfg.infoKeys)
		})
	}
}

func TestWithAllSelectsEveryBuiltInKey(t *testing.T) {
	cfg := &collectCfg{}
	WithAll()(cfg)

	require.True(t, cfg.collectAllCustom)
	require.Len(t, cfg.infoKeys, len(systemInfoKeyDefinitions))
	for key := range systemInfoKeyDefinitions {
		require.True(t, cfg.hasInfoKey(key))
	}
}

func TestBuildCollectorsGroupsSelectedKeysBySource(t *testing.T) {
	require := require.New(t)
	collectors := buildCollectors(
		log.NewPrefixLogger("test"),
		nil,
		nil,
		"",
		collectionRequest{infoKeys: []string{common.ProductNameKey, common.ProductSerialKey}},
		nil,
		nil,
	)

	require.Len(collectors, 2, "boot and the one selected shared system source")
	for _, source := range collectors {
		if source.source != systemSource {
			continue
		}
		require.Len(source.executors, 2)
		require.Equal(common.ProductNameKey, source.executors[0].key)
		require.Equal(common.ProductSerialKey, source.executors[1].key)
		return
	}
	t.Fatal("system source was not constructed")
}

func TestCollect_AllDisabled(t *testing.T) {
	tmpDir := t.TempDir()
	readWriter := fileio.NewReadWriter(
		fileio.NewReader(fileio.WithReaderRootDir(tmpDir)),
		fileio.NewWriter(fileio.WithWriterRootDir(tmpDir)),
	)

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

func TestGetCustomInfoContextTimeout(t *testing.T) {
	tests := []struct {
		name          string
		scriptContent string
		description   string
	}{
		{
			name:          "timeout with exec single process in group",
			scriptContent: "#!/bin/bash\nexec sleep 10\n",
		},
		{
			name:          "timeout without exec two processes in group",
			scriptContent: "#!/bin/bash\nsleep 10\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)

			tmpDir := t.TempDir()
			rw := fileio.NewReadWriter(
				fileio.NewReader(fileio.WithReaderRootDir(tmpDir)),
				fileio.NewWriter(fileio.WithWriterRootDir(tmpDir)),
			)

			err := rw.MkdirAll(config.SystemInfoCustomScriptDir, fileio.DefaultDirectoryPermissions)
			require.NoError(err)

			scriptName := "slowScript.sh"
			err = rw.WriteFile(
				filepath.Join(config.SystemInfoCustomScriptDir, scriptName),
				[]byte(tt.scriptContent),
				fileio.DefaultExecutablePermissions,
			)
			require.NoError(err)

			exec := executer.NewCommonExecuter()
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()

			start := time.Now()
			info, err := Collect(ctx, log.NewPrefixLogger("test"), exec, rw, []string{"slowScript"}, "")
			elapsed := time.Since(start)

			require.Less(elapsed, 200*time.Millisecond, "timeout quickly")
			require.NoError(err)
			require.Empty(info.Custom["slowScript"], "empty on timeout")
		})
	}
}
