package systeminfo

import (
	_ "embed"
	"testing"

	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/stretchr/testify/require"
)

//go:embed testdata/cpuinfo_arm64
var cpuinfoARM []byte

//go:embed testdata/cpuinfo_x86
var cpuinfoX86 []byte

func TestCollectCPUInfoARM(t *testing.T) {
	require := require.New(t)
	tmpDir := t.TempDir()
	rw := fileio.NewReadWriter()
	rw.SetRootdir(tmpDir)

	err := rw.MkdirAll("proc", fileio.DefaultDirectoryPermissions)
	require.NoError(err)

	err = rw.WriteFile("/proc/cpuinfo", cpuinfoARM, fileio.DefaultFilePermissions)
	require.NoError(err)

	cpuInfo, err := collectCPUInfo(rw)
	require.NoError(err)
	require.NotNil(cpuInfo)

	require.Equal(4, cpuInfo.TotalCores)
	require.Equal(4, cpuInfo.TotalThreads)
	require.Len(cpuInfo.Processors, 4)

	t.Logf("Total Cores: %d", cpuInfo.TotalCores)
	t.Logf("Total Threads: %d", cpuInfo.TotalThreads)
	t.Logf("Processors: %+v", cpuInfo.Processors)
}

func TestCollectCPUInfoX86(t *testing.T) {
	require := require.New(t)
	tmpDir := t.TempDir()
	rw := fileio.NewReadWriter()
	rw.SetRootdir(tmpDir)

	err := rw.MkdirAll("proc", fileio.DefaultDirectoryPermissions)
	require.NoError(err)

	err = rw.WriteFile("/proc/cpuinfo", cpuinfoX86, fileio.DefaultFilePermissions)
	require.NoError(err)

	cpuInfo, err := collectCPUInfo(rw)
	require.NoError(err)
	require.NotNil(cpuInfo)

	require.Equal(8, cpuInfo.TotalCores)
	require.Equal(16, cpuInfo.TotalThreads)
	require.Len(cpuInfo.Processors, 1)

	t.Logf("Total Cores: %d", cpuInfo.TotalCores)
	t.Logf("Total Threads: %d", cpuInfo.TotalThreads)
	t.Logf("Processors: %+v", cpuInfo.Processors)
}
