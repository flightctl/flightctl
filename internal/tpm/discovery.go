package tpm

import (
	"bytes"
	"fmt"
	"regexp"

	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/pkg/log"
)

const (
	devicePathTemplate   = "/dev/tpm%s"
	deviceRMPathTemplate = "/dev/tpmrm%s"
	versionPathTemplate  = "/sys/class/tpm/%s/tpm_version_major"
	sysClassPath         = "/sys/class/tpm"
	sysFsPathTemplate    = "/sys/class/tpm/%s"
)

var tpmDeviceRegex = regexp.MustCompile(`^tpm(\d+)$`)

func discoverDevices(rw fileio.ReadWriter) ([]Device, error) {
	entries, err := rw.ReadDir(sysClassPath)
	if err != nil {
		return nil, fmt.Errorf("scanning TPM devices: %w", err)
	}

	var devices []Device
	for _, entry := range entries {
		matches := tpmDeviceRegex.FindStringSubmatch(entry.Name())
		if len(matches) != 2 {
			continue
		}
		deviceNum := matches[1]

		device := Device{
			DeviceNumber:    deviceNum,
			DevicePath:      fmt.Sprintf(devicePathTemplate, deviceNum),
			ResourceMgrPath: fmt.Sprintf(deviceRMPathTemplate, deviceNum),
			VersionPath:     fmt.Sprintf(versionPathTemplate, entry.Name()),
			SysfsPath:       fmt.Sprintf(sysFsPathTemplate, entry.Name()),
			rw:              rw,
		}

		devices = append(devices, device)
	}

	return devices, nil
}

func ResolveDevice(rw fileio.ReadWriter, devicePath string) (*Device, error) {
	devices, err := discoverDevices(rw)
	if err != nil {
		return nil, fmt.Errorf("discovering TPM devices: %w", err)
	}

	for _, device := range devices {
		if device.DevicePath == devicePath || device.ResourceMgrPath == devicePath {
			if err := device.ValidateVersion2(); err != nil {
				return nil, fmt.Errorf("invalid TPM device %q: %w", devicePath, err)
			}
			return &device, nil
		}
	}

	return nil, fmt.Errorf("TPM device %q not found", devicePath)
}

// ResolveDefaultDevice finds and returns the first available valid TPM 2.0 device.
// It discovers all TPM devices and returns the first one that exists and is version 2.0.
func ResolveDefaultDevice(rw fileio.ReadWriter, logger *log.PrefixLogger) (*Device, error) {
	devices, err := discoverDevices(rw)
	if err != nil {
		return nil, fmt.Errorf("failed to discover TPM devices: %w", err)
	}

	logger.Debugf("Found %d TPM devices", len(devices))

	for _, device := range devices {
		logger.Debugf("Trying TPM device %s at %s", device.DeviceNumber, device.ResourceMgrPath)
		if device.Exists() {
			logger.Debugf("Device %s exists, validating version", device.DeviceNumber)
			if err := device.ValidateVersion2(); err == nil {
				return &device, nil
			}
			logger.Debugf("Device %s validation failed: %v", device.DeviceNumber, err)
		} else {
			logger.Debugf("Device %s does not exist", device.DeviceNumber)
		}
	}

	return nil, fmt.Errorf("no valid TPM 2.0 devices found")
}

func (d *Device) Exists() bool {
	exists, err := d.rw.PathExists(d.ResourceMgrPath, fileio.WithSkipContentCheck())
	return err == nil && exists
}

func (d *Device) ValidateVersion2() error {
	if !d.Exists() {
		return fmt.Errorf("no TPM detected at %s", d.ResourceMgrPath)
	}
	versionBytes, err := d.rw.ReadFile(d.VersionPath)
	if err != nil {
		return err
	}
	versionStr := string(bytes.TrimSpace(versionBytes))
	if versionStr != "2" {
		return fmt.Errorf("TPM is not version 2.0")
	}
	return nil
}

func (d *Device) Open() (*TPM, error) {
	if d.tpm != nil {
		return d.tpm, nil
	}

	tpm, err := OpenTPM(d.rw, d.ResourceMgrPath)
	if err != nil {
		return nil, err
	}
	d.tpm = tpm
	return tpm, nil
}

func (d *Device) Close() error {
	if d.tpm == nil {
		return nil
	}
	err := d.tpm.Close()
	d.tpm = nil
	return err
}
