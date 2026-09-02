package systeminfo

import (
	_ "embed"
	"path/filepath"
	"testing"

	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/stretchr/testify/require"
)

const (
	VendorIDNvidia = "0x10de"
	VendorIDAMD    = "0x1002"
	VendorIDIntel  = "0x8086"
)

//go:embed testdata/hardware-map.yaml
var hardwareMapBytes []byte

//go:embed testdata/uevent_jetson_orin
var ueventJetsonOrin []byte

func TestLoadPCIMappings(t *testing.T) {
	require := require.New(t)
	testCases := []struct {
		name               string
		vendorToCheck      string
		vendorName         string
		deviceToCheck      string
		expectedDeviceName string
	}{
		{
			name:               "NVIDIA RTX 4090",
			vendorToCheck:      VendorIDNvidia,
			vendorName:         "NVIDIA",
			deviceToCheck:      "0x2717",
			expectedDeviceName: "RTX_4090",
		},
		{
			name:               "NVIDIA A100",
			vendorToCheck:      VendorIDNvidia,
			vendorName:         "NVIDIA",
			deviceToCheck:      "0x20b5",
			expectedDeviceName: "A100",
		},
		{
			name:               "AMD Instinct MI100",
			vendorToCheck:      VendorIDAMD,
			vendorName:         "AMD",
			deviceToCheck:      "0x744c",
			expectedDeviceName: "Instinct MI100",
		},
		{
			name:               "Intel Arc A770",
			vendorToCheck:      VendorIDIntel,
			vendorName:         "Intel",
			deviceToCheck:      "0x4906",
			expectedDeviceName: "Arc A770",
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			rw := fileio.NewReadWriter(
				fileio.NewReader(fileio.WithReaderRootDir(tmpDir)),
				fileio.NewWriter(fileio.WithWriterRootDir(tmpDir)),
			)
			err := rw.WriteFile(HardwareMapFileName, hardwareMapBytes, fileio.DefaultFilePermissions)
			require.NoError(err)

			mappings, err := loadPCIMappings(rw, HardwareMapFileName)
			require.NoError(err)

			vendor, exists := mappings[tc.vendorToCheck]
			require.True(exists, "Vendor %s should exist in mappings", tc.vendorToCheck)

			require.Equal(tc.vendorName, vendor.VendorName, "Vendor name should match")

			var deviceFound bool
			var deviceName string

			for _, model := range vendor.Models {
				if model.PCIID == tc.deviceToCheck {
					deviceFound = true
					deviceName = model.PCIName
					break
				}
			}

			require.True(deviceFound, "Device %s should exist for vendor %s", tc.deviceToCheck, tc.vendorName)
			require.Equal(tc.expectedDeviceName, deviceName, "Device name should match expected")
		})
	}
}

func TestParseUevent(t *testing.T) {
	require := require.New(t)

	tests := []struct {
		name string
		data string
		want map[string]string
	}{
		{
			name: "When uevent has multiple KEY=VALUE pairs it should parse all of them",
			data: "DRIVER=nvidia\nOF_NAME=gpu\nOF_COMPATIBLE_0=nvidia,ga10b\n",
			want: map[string]string{
				"DRIVER":          "nvidia",
				"OF_NAME":         "gpu",
				"OF_COMPATIBLE_0": "nvidia,ga10b",
			},
		},
		{
			name: "When uevent is empty it should return an empty map",
			data: "",
			want: map[string]string{},
		},
		{
			name: "When a line has no equals sign it should be skipped",
			data: "DRIVER=nvidia\nmalformed-line\nOF_NAME=gpu\n",
			want: map[string]string{
				"DRIVER":  "nvidia",
				"OF_NAME": "gpu",
			},
		},
		{
			name: "When a value contains an equals sign it should preserve the full value",
			data: "KEY=val=ue\n",
			want: map[string]string{
				"KEY": "val=ue",
			},
		},
		{
			name: "When lines have trailing whitespace it should trim them",
			data: "  DRIVER=nvidia  \n  OF_NAME=gpu  \n",
			want: map[string]string{
				"DRIVER":  "nvidia",
				"OF_NAME": "gpu",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseUevent([]byte(tt.data))
			require.Equal(tt.want, got)
		})
	}
}

func TestCollectPlatformGPUs(t *testing.T) {
	require := require.New(t)
	logger := log.NewPrefixLogger("test")

	tests := []struct {
		name       string
		setup      func(rw fileio.ReadWriter)
		startIndex int
		wantCount  int
		wantFirst  *GPUDeviceInfo
	}{
		{
			name: "When a Jetson Orin GPU is present it should return one GPU with correct metadata",
			setup: func(rw fileio.ReadWriter) {
				err := rw.MkdirAll(filepath.Join(platformDevicesPath, "17000000.gpu"), fileio.DefaultDirectoryPermissions)
				require.NoError(err)
				err = rw.WriteFile(
					filepath.Join(platformDevicesPath, "17000000.gpu", "uevent"),
					ueventJetsonOrin,
					fileio.DefaultFilePermissions,
				)
				require.NoError(err)
			},
			startIndex: 0,
			wantCount:  1,
			wantFirst: &GPUDeviceInfo{
				Index:    0,
				Vendor:   "NVIDIA",
				Model:    "GA10B",
				Arch:     "Ampere",
				DeviceID: "nvidia,ga10b",
			},
		},
		{
			name:       "When the platform devices directory does not exist it should return nil",
			setup:      func(rw fileio.ReadWriter) {},
			startIndex: 0,
			wantCount:  0,
		},
		{
			name: "When a platform device has OF_NAME=gpu but an unknown compatible string it should be skipped",
			setup: func(rw fileio.ReadWriter) {
				err := rw.MkdirAll(filepath.Join(platformDevicesPath, "17000000.gpu"), fileio.DefaultDirectoryPermissions)
				require.NoError(err)
				uevent := []byte("OF_NAME=gpu\nOF_COMPATIBLE_0=unknown,gpu-chip\n")
				err = rw.WriteFile(
					filepath.Join(platformDevicesPath, "17000000.gpu", "uevent"),
					uevent,
					fileio.DefaultFilePermissions,
				)
				require.NoError(err)
			},
			startIndex: 0,
			wantCount:  0,
		},
		{
			name: "When a platform device is not a GPU it should be ignored",
			setup: func(rw fileio.ReadWriter) {
				err := rw.MkdirAll(filepath.Join(platformDevicesPath, "serial0"), fileio.DefaultDirectoryPermissions)
				require.NoError(err)
				uevent := []byte("OF_NAME=serial\nOF_COMPATIBLE_0=ns16550a\n")
				err = rw.WriteFile(
					filepath.Join(platformDevicesPath, "serial0", "uevent"),
					uevent,
					fileio.DefaultFilePermissions,
				)
				require.NoError(err)
			},
			startIndex: 0,
			wantCount:  0,
		},
		{
			name: "When the known compatible string is OF_COMPATIBLE_1 it should still match",
			setup: func(rw fileio.ReadWriter) {
				err := rw.MkdirAll(filepath.Join(platformDevicesPath, "17000000.gpu"), fileio.DefaultDirectoryPermissions)
				require.NoError(err)
				uevent := []byte("OF_NAME=gpu\nOF_COMPATIBLE_0=nvidia,tegra234-gpu\nOF_COMPATIBLE_1=nvidia,ga10b\nOF_COMPATIBLE_N=2\n")
				err = rw.WriteFile(
					filepath.Join(platformDevicesPath, "17000000.gpu", "uevent"),
					uevent,
					fileio.DefaultFilePermissions,
				)
				require.NoError(err)
			},
			startIndex: 0,
			wantCount:  1,
			wantFirst: &GPUDeviceInfo{
				Index:    0,
				Vendor:   "NVIDIA",
				Model:    "GA10B",
				Arch:     "Ampere",
				DeviceID: "nvidia,tegra234-gpu",
			},
		},
		{
			name: "When a Jetson Xavier GPU is present it should return Volta metadata",
			setup: func(rw fileio.ReadWriter) {
				err := rw.MkdirAll(filepath.Join(platformDevicesPath, "17000000.gpu"), fileio.DefaultDirectoryPermissions)
				require.NoError(err)
				uevent := []byte("OF_NAME=gpu\nOF_COMPATIBLE_0=nvidia,gv11b\nOF_COMPATIBLE_N=1\n")
				err = rw.WriteFile(
					filepath.Join(platformDevicesPath, "17000000.gpu", "uevent"),
					uevent,
					fileio.DefaultFilePermissions,
				)
				require.NoError(err)
			},
			startIndex: 0,
			wantCount:  1,
			wantFirst: &GPUDeviceInfo{
				Index:    0,
				Vendor:   "NVIDIA",
				Model:    "GV11B",
				Arch:     "Volta",
				DeviceID: "nvidia,gv11b",
			},
		},
		{
			name: "When a platform GPU has memory info it should populate MemoryBytes",
			setup: func(rw fileio.ReadWriter) {
				gpuDir := filepath.Join(platformDevicesPath, "17000000.gpu")
				err := rw.MkdirAll(gpuDir, fileio.DefaultDirectoryPermissions)
				require.NoError(err)
				err = rw.WriteFile(
					filepath.Join(gpuDir, "uevent"),
					ueventJetsonOrin,
					fileio.DefaultFilePermissions,
				)
				require.NoError(err)
				err = rw.MkdirAll(filepath.Join(gpuDir, "driver"), fileio.DefaultDirectoryPermissions)
				require.NoError(err)
				err = rw.WriteFile(
					filepath.Join(gpuDir, "driver", "vram_size_MB"),
					[]byte("8192"),
					fileio.DefaultFilePermissions,
				)
				require.NoError(err)
			},
			startIndex: 0,
			wantCount:  1,
			wantFirst: &GPUDeviceInfo{
				Index:       0,
				Vendor:      "NVIDIA",
				Model:       "GA10B",
				Arch:        "Ampere",
				DeviceID:    "nvidia,ga10b",
				MemoryBytes: 8192 * 1024 * 1024,
			},
		},
		{
			name: "When startIndex is offset it should assign indices starting from that value",
			setup: func(rw fileio.ReadWriter) {
				err := rw.MkdirAll(filepath.Join(platformDevicesPath, "17000000.gpu"), fileio.DefaultDirectoryPermissions)
				require.NoError(err)
				err = rw.WriteFile(
					filepath.Join(platformDevicesPath, "17000000.gpu", "uevent"),
					ueventJetsonOrin,
					fileio.DefaultFilePermissions,
				)
				require.NoError(err)
			},
			startIndex: 2,
			wantCount:  1,
			wantFirst: &GPUDeviceInfo{
				Index:    2,
				Vendor:   "NVIDIA",
				Model:    "GA10B",
				Arch:     "Ampere",
				DeviceID: "nvidia,ga10b",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			rw := fileio.NewReadWriter(
				fileio.NewReader(fileio.WithReaderRootDir(tmpDir)),
				fileio.NewWriter(fileio.WithWriterRootDir(tmpDir)),
			)
			tt.setup(rw)

			gpus := collectPlatformGPUs(logger, rw, tt.startIndex)
			require.Len(gpus, tt.wantCount)
			if tt.wantFirst != nil && len(gpus) > 0 {
				require.Equal(tt.wantFirst.Index, gpus[0].Index)
				require.Equal(tt.wantFirst.Vendor, gpus[0].Vendor)
				require.Equal(tt.wantFirst.Model, gpus[0].Model)
				require.Equal(tt.wantFirst.Arch, gpus[0].Arch)
				require.Equal(tt.wantFirst.DeviceID, gpus[0].DeviceID)
				require.Equal(tt.wantFirst.MemoryBytes, gpus[0].MemoryBytes)
			}
		})
	}
}

func TestCollectGPUInfo_PCI(t *testing.T) {
	require := require.New(t)
	logger := log.NewPrefixLogger("test")

	tmpDir := t.TempDir()
	rw := fileio.NewReadWriter(
		fileio.NewReader(fileio.WithReaderRootDir(tmpDir)),
		fileio.NewWriter(fileio.WithWriterRootDir(tmpDir)),
	)

	deviceDir := filepath.Join(pciDevicesPath, "0000:41:00.0")
	require.NoError(rw.MkdirAll(deviceDir, fileio.DefaultDirectoryPermissions))
	require.NoError(rw.WriteFile(filepath.Join(deviceDir, "class"), []byte("0x030000"), fileio.DefaultFilePermissions))
	require.NoError(rw.WriteFile(filepath.Join(deviceDir, "vendor"), []byte("0x10de"), fileio.DefaultFilePermissions))
	require.NoError(rw.WriteFile(filepath.Join(deviceDir, "device"), []byte("0x2717"), fileio.DefaultFilePermissions))
	require.NoError(rw.WriteFile(filepath.Join(deviceDir, "revision"), []byte("0xa1"), fileio.DefaultFilePermissions))

	gpus, err := collectGPUInfo(logger, rw, "")
	require.NoError(err)
	require.Len(gpus, 1)
	require.Equal(0, gpus[0].Index)
	require.Equal("0000:41:00.0", gpus[0].PCIAddress)
	require.Equal("0x10de", gpus[0].VendorID)
	require.Equal("0x2717", gpus[0].DeviceID)
}

func TestCollectGPUInfo_PlatformOnly(t *testing.T) {
	require := require.New(t)
	logger := log.NewPrefixLogger("test")

	tmpDir := t.TempDir()
	rw := fileio.NewReadWriter(
		fileio.NewReader(fileio.WithReaderRootDir(tmpDir)),
		fileio.NewWriter(fileio.WithWriterRootDir(tmpDir)),
	)

	require.NoError(rw.MkdirAll(filepath.Join(platformDevicesPath, "17000000.gpu"), fileio.DefaultDirectoryPermissions))
	require.NoError(rw.WriteFile(
		filepath.Join(platformDevicesPath, "17000000.gpu", "uevent"),
		ueventJetsonOrin,
		fileio.DefaultFilePermissions,
	))

	gpus, err := collectGPUInfo(logger, rw, "")
	require.NoError(err)
	require.Len(gpus, 1)
	require.Equal(0, gpus[0].Index)
	require.Equal("NVIDIA", gpus[0].Vendor)
	require.Equal("GA10B", gpus[0].Model)
	require.Equal("Ampere", gpus[0].Arch)
	require.Empty(gpus[0].PCIAddress)
}

func TestCollectGPUInfo_Mixed(t *testing.T) {
	require := require.New(t)
	logger := log.NewPrefixLogger("test")

	tmpDir := t.TempDir()
	rw := fileio.NewReadWriter(
		fileio.NewReader(fileio.WithReaderRootDir(tmpDir)),
		fileio.NewWriter(fileio.WithWriterRootDir(tmpDir)),
	)

	pciDir := filepath.Join(pciDevicesPath, "0000:41:00.0")
	require.NoError(rw.MkdirAll(pciDir, fileio.DefaultDirectoryPermissions))
	require.NoError(rw.WriteFile(filepath.Join(pciDir, "class"), []byte("0x030000"), fileio.DefaultFilePermissions))
	require.NoError(rw.WriteFile(filepath.Join(pciDir, "vendor"), []byte("0x10de"), fileio.DefaultFilePermissions))
	require.NoError(rw.WriteFile(filepath.Join(pciDir, "device"), []byte("0x2717"), fileio.DefaultFilePermissions))

	require.NoError(rw.MkdirAll(filepath.Join(platformDevicesPath, "17000000.gpu"), fileio.DefaultDirectoryPermissions))
	require.NoError(rw.WriteFile(
		filepath.Join(platformDevicesPath, "17000000.gpu", "uevent"),
		ueventJetsonOrin,
		fileio.DefaultFilePermissions,
	))

	gpus, err := collectGPUInfo(logger, rw, "")
	require.NoError(err)
	require.Len(gpus, 2)

	require.Equal(0, gpus[0].Index)
	require.Equal("0000:41:00.0", gpus[0].PCIAddress)

	require.Equal(1, gpus[1].Index)
	require.Equal("NVIDIA", gpus[1].Vendor)
	require.Equal("GA10B", gpus[1].Model)
	require.Empty(gpus[1].PCIAddress)
}

func TestCollectGPUInfo_Empty(t *testing.T) {
	require := require.New(t)
	logger := log.NewPrefixLogger("test")

	tmpDir := t.TempDir()
	rw := fileio.NewReadWriter(
		fileio.NewReader(fileio.WithReaderRootDir(tmpDir)),
		fileio.NewWriter(fileio.WithWriterRootDir(tmpDir)),
	)

	gpus, err := collectGPUInfo(logger, rw, "")
	require.NoError(err)
	require.Empty(gpus)
}
