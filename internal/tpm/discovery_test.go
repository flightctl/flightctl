//go:build amd64 || arm64

package tpm

import (
	"testing"

	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/stretchr/testify/require"
)

func TestTPMDeviceExists(t *testing.T) {
	tempDir := t.TempDir()
	rw := fileio.NewReadWriter(fileio.WithTestRootDir(tempDir))

	// Create mock resource manager file using ReadWriter
	err := rw.WriteFile("/dev/tpmrm0", []byte{}, 0600)
	require.NoError(t, err)

	tests := []struct {
		name     string
		device   Device
		expected bool
	}{
		{
			name: "device exists at resource manager path",
			device: Device{
				DevicePath:      "/dev/tpm0",
				ResourceMgrPath: "/dev/tpmrm0",
				rw:              rw,
			},
			expected: true,
		},
		{
			name: "device does not exist at resource manager path",
			device: Device{
				DevicePath:      "/dev/tpm0",
				ResourceMgrPath: "/dev/nonexistent",
				rw:              rw,
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.device.Exists()
			require.Equal(t, tt.expected, result)
		})
	}
}

func TestTPMDeviceOpenClose(t *testing.T) {
	require := require.New(t)
	tempDir := t.TempDir()
	rw := fileio.NewReadWriter(fileio.WithTestRootDir(tempDir))

	device := Device{
		DeviceNumber:    "0",
		DevicePath:      "/dev/tpm0",
		ResourceMgrPath: "/dev/tpmrm0",
		VersionPath:     "/sys/class/tpm/tpm0/tpm_version_major",
		SysfsPath:       "/sys/class/tpm/tpm0",
		rw:              rw,
	}

	// Test that device starts with nil tpm
	require.Nil(device.tpm)

	// Test Close on device that was never opened
	err := device.Close()
	require.NoError(err)
	require.Nil(device.tpm)
}

func TestDiscoverTPMDevices(t *testing.T) {
	// Create a temporary directory structure to simulate /sys/class/tpm
	tempDir := t.TempDir()
	rw := fileio.NewReadWriter(fileio.WithTestRootDir(tempDir))

	// Create mock TPM directories using ReadWriter
	tpmDirs := []string{"tpm0", "tpm1", "tpm2"}
	for _, dir := range tpmDirs {
		err := rw.MkdirAll("/sys/class/tpm/"+dir, 0755)
		require.NoError(t, err)
	}

	// Create a non-TPM directory that should be ignored
	err := rw.MkdirAll("/sys/class/tpm/other", 0755)
	require.NoError(t, err)

	// Create a file that should be ignored
	err = rw.WriteFile("/sys/class/tpm/file.txt", []byte{}, 0600)
	require.NoError(t, err)

	devices, err := discoverDevices(rw)
	require.NoError(t, err)
	require.Len(t, devices, 3)

	expectedDevices := []Device{
		{
			DeviceNumber:    "0",
			DevicePath:      "/dev/tpm0",
			ResourceMgrPath: "/dev/tpmrm0",
			VersionPath:     "/sys/class/tpm/tpm0/tpm_version_major",
			SysfsPath:       "/sys/class/tpm/tpm0",
		},
		{
			DeviceNumber:    "1",
			DevicePath:      "/dev/tpm1",
			ResourceMgrPath: "/dev/tpmrm1",
			VersionPath:     "/sys/class/tpm/tpm1/tpm_version_major",
			SysfsPath:       "/sys/class/tpm/tpm1",
		},
		{
			DeviceNumber:    "2",
			DevicePath:      "/dev/tpm2",
			ResourceMgrPath: "/dev/tpmrm2",
			VersionPath:     "/sys/class/tpm/tpm2/tpm_version_major",
			SysfsPath:       "/sys/class/tpm/tpm2",
		},
	}

	for i, expected := range expectedDevices {
		require.Equal(t, expected.DeviceNumber, devices[i].DeviceNumber)
		require.Equal(t, expected.DevicePath, devices[i].DevicePath)
		require.Equal(t, expected.ResourceMgrPath, devices[i].ResourceMgrPath)
		require.Equal(t, expected.VersionPath, devices[i].VersionPath)
		require.Equal(t, expected.SysfsPath, devices[i].SysfsPath)
		require.Equal(t, rw, devices[i].rw)
	}
}

func TestDiscoverTPMDevicesError(t *testing.T) {
	// Test with a ReadWriter that points to a non-existent directory
	tempDir := t.TempDir()
	rw := fileio.NewReadWriter(fileio.WithTestRootDir(tempDir))

	// Try to discover TPM devices from a directory that doesn't exist
	devices, err := discoverDevices(rw)

	// The ReadWriter with test root dir will return empty results instead of error
	// when the directory doesn't exist, so we should get an empty slice
	require.NoError(t, err)
	require.Empty(t, devices)
}

func TestTPMDeviceValidateVersion2(t *testing.T) {
	tempDir := t.TempDir()
	rw := fileio.NewReadWriter(fileio.WithTestRootDir(tempDir))

	// Create mock resource manager and version files using ReadWriter
	err := rw.WriteFile("/dev/tpmrm0", []byte{}, 0600)
	require.NoError(t, err)
	err = rw.WriteFile("/sys/class/tpm/tpm0/tpm_version_major", []byte("2"), 0600)
	require.NoError(t, err)

	tests := []struct {
		name        string
		device      Device
		expectError bool
	}{
		{
			name: "valid TPM 2.0 device",
			device: Device{
				DevicePath:      "/dev/tpm0",
				ResourceMgrPath: "/dev/tpmrm0",
				VersionPath:     "/sys/class/tpm/tpm0/tpm_version_major",
				rw:              rw,
			},
			expectError: false,
		},
		{
			name: "resource manager does not exist",
			device: Device{
				DevicePath:      "/dev/tpm0",
				ResourceMgrPath: "/dev/nonexistent",
				VersionPath:     "/sys/class/tpm/tpm0/tpm_version_major",
				rw:              rw,
			},
			expectError: true,
		},
		{
			name: "version file does not exist",
			device: Device{
				DevicePath:      "/dev/tpm0",
				ResourceMgrPath: "/dev/tpmrm0",
				VersionPath:     "/sys/class/tpm/tpm0/nonexistent",
				rw:              rw,
			},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.device.ValidateVersion2()
			if tt.expectError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestGetDefaultTPMDevice(t *testing.T) {
	tests := []struct {
		name           string
		setup          func(rw fileio.ReadWriter)
		expectError    bool
		expectedDevice *Device
	}{
		{
			name: "returns first valid TPM device when available",
			setup: func(rw fileio.ReadWriter) {
				// Create multiple TPM devices
				err := rw.MkdirAll("/sys/class/tpm/tpm0", 0755)
				require.NoError(t, err)
				err = rw.MkdirAll("/sys/class/tpm/tpm1", 0755)
				require.NoError(t, err)

				// Create resource manager files for both
				err = rw.WriteFile("/dev/tpmrm0", []byte{}, 0600)
				require.NoError(t, err)
				err = rw.WriteFile("/dev/tpmrm1", []byte{}, 0600)
				require.NoError(t, err)

				// Create version files for both (TPM 2.0)
				err = rw.WriteFile("/sys/class/tpm/tpm0/tpm_version_major", []byte("2"), 0600)
				require.NoError(t, err)
				err = rw.WriteFile("/sys/class/tpm/tpm1/tpm_version_major", []byte("2"), 0600)
				require.NoError(t, err)
			},
			expectError: false,
			expectedDevice: &Device{
				DeviceNumber:    "0",
				DevicePath:      "/dev/tpm0",
				ResourceMgrPath: "/dev/tpmrm0",
				VersionPath:     "/sys/class/tpm/tpm0/tpm_version_major",
				SysfsPath:       "/sys/class/tpm/tpm0",
			},
		},
		{
			name: "returns error when no TPM devices exist",
			setup: func(rw fileio.ReadWriter) {
				// Don't create any TPM devices
			},
			expectError: true,
		},
		{
			name: "returns error when TPM devices exist but are not version 2.0",
			setup: func(rw fileio.ReadWriter) {
				// Create TPM device directory
				err := rw.MkdirAll("/sys/class/tpm/tpm0", 0755)
				require.NoError(t, err)

				// Create resource manager file
				err = rw.WriteFile("/dev/tpmrm0", []byte{}, 0600)
				require.NoError(t, err)

				// Create version file with non-2.0 version
				err = rw.WriteFile("/sys/class/tpm/tpm0/tpm_version_major", []byte("1"), 0600)
				require.NoError(t, err)
			},
			expectError: true,
		},
		{
			name: "returns error when resource manager files don't exist",
			setup: func(rw fileio.ReadWriter) {
				// Create TPM device directory
				err := rw.MkdirAll("/sys/class/tpm/tpm0", 0755)
				require.NoError(t, err)

				// Create version file but not resource manager files
				err = rw.WriteFile("/sys/class/tpm/tpm0/tpm_version_major", []byte("2"), 0600)
				require.NoError(t, err)
			},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tempDir := t.TempDir()
			rw := fileio.NewReadWriter(fileio.WithTestRootDir(tempDir))

			tt.setup(rw)

			device, err := ResolveDefaultDevice(rw, log.NewPrefixLogger("test"))

			if tt.expectError {
				require.Error(t, err)
				require.Nil(t, device)
			} else {
				require.NoError(t, err)
				require.NotNil(t, device)
				require.Equal(t, tt.expectedDevice.DeviceNumber, device.DeviceNumber)
				require.Equal(t, tt.expectedDevice.DevicePath, device.DevicePath)
				require.Equal(t, tt.expectedDevice.ResourceMgrPath, device.ResourceMgrPath)
				require.Equal(t, tt.expectedDevice.VersionPath, device.VersionPath)
				require.Equal(t, tt.expectedDevice.SysfsPath, device.SysfsPath)
				require.Equal(t, rw, device.rw)
			}
		})
	}
}

func TestResolveTPMDevice(t *testing.T) {
	tests := []struct {
		name           string
		setup          func(rw fileio.ReadWriter)
		devicePath     string
		expectError    bool
		expectedDevice *Device
	}{
		{
			name: "resolves device by resource manager path",
			setup: func(rw fileio.ReadWriter) {
				err := rw.MkdirAll("/sys/class/tpm/tpm0", 0755)
				require.NoError(t, err)
				err = rw.WriteFile("/dev/tpmrm0", []byte{}, 0600)
				require.NoError(t, err)
				err = rw.WriteFile("/sys/class/tpm/tpm0/tpm_version_major", []byte("2"), 0600)
				require.NoError(t, err)
			},
			devicePath:  "/dev/tpmrm0",
			expectError: false,
			expectedDevice: &Device{
				DeviceNumber:    "0",
				DevicePath:      "/dev/tpm0",
				ResourceMgrPath: "/dev/tpmrm0",
				VersionPath:     "/sys/class/tpm/tpm0/tpm_version_major",
				SysfsPath:       "/sys/class/tpm/tpm0",
			},
		},
		{
			name: "resolves device by direct device path",
			setup: func(rw fileio.ReadWriter) {
				err := rw.MkdirAll("/sys/class/tpm/tpm1", 0755)
				require.NoError(t, err)
				err = rw.WriteFile("/dev/tpmrm1", []byte{}, 0600)
				require.NoError(t, err)
				err = rw.WriteFile("/sys/class/tpm/tpm1/tpm_version_major", []byte("2"), 0600)
				require.NoError(t, err)
			},
			devicePath:  "/dev/tpm1",
			expectError: false,
			expectedDevice: &Device{
				DeviceNumber:    "1",
				DevicePath:      "/dev/tpm1",
				ResourceMgrPath: "/dev/tpmrm1",
				VersionPath:     "/sys/class/tpm/tpm1/tpm_version_major",
				SysfsPath:       "/sys/class/tpm/tpm1",
			},
		},
		{
			name: "returns error for non-existent device",
			setup: func(rw fileio.ReadWriter) {
			},
			devicePath:  "/dev/tpmrm99",
			expectError: true,
		},
		{
			name: "returns error for non-TPM2 device",
			setup: func(rw fileio.ReadWriter) {
				err := rw.MkdirAll("/sys/class/tpm/tpm0", 0755)
				require.NoError(t, err)
				err = rw.WriteFile("/dev/tpmrm0", []byte{}, 0600)
				require.NoError(t, err)
				err = rw.WriteFile("/sys/class/tpm/tpm0/tpm_version_major", []byte("1"), 0600)
				require.NoError(t, err)
			},
			devicePath:  "/dev/tpmrm0",
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tempDir := t.TempDir()
			rw := fileio.NewReadWriter(fileio.WithTestRootDir(tempDir))

			tt.setup(rw)

			device, err := ResolveDevice(rw, tt.devicePath)

			if tt.expectError {
				require.Error(t, err)
				require.Nil(t, device)
			} else {
				require.NoError(t, err)
				require.NotNil(t, device)
				require.Equal(t, tt.expectedDevice.DeviceNumber, device.DeviceNumber)
				require.Equal(t, tt.expectedDevice.DevicePath, device.DevicePath)
				require.Equal(t, tt.expectedDevice.ResourceMgrPath, device.ResourceMgrPath)
				require.Equal(t, tt.expectedDevice.VersionPath, device.VersionPath)
				require.Equal(t, tt.expectedDevice.SysfsPath, device.SysfsPath)
				require.Equal(t, rw, device.rw)
			}
		})
	}
}
