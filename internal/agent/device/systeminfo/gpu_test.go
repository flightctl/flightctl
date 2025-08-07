package systeminfo

import (
	_ "embed"
	"testing"

	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/stretchr/testify/require"
)

const (
	VendorIDNvidia = "0x10de"
	VendorIDAMD    = "0x1002"
	VendorIDIntel  = "0x8086"
)

//go:embed testdata/hardware-map.yaml
var hardwareMapBytes []byte

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
			rw := fileio.NewReadWriter()
			rw.SetRootdir(tmpDir)
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
