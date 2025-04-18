package systeminfo

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/pkg/log"
	"sigs.k8s.io/yaml"
)

const (
	// PCI class codes for graphics devices
	VGACompatibleController = "0x030000" // VGA compatible controller
	DisplayController       = "0x038000" // Other display controller

	// Class code prefixes for graphics devices
	VGAPrefix     = "0x0300" // Prefix for VGA compatible devices
	DisplayPrefix = "0x0380" // Prefix for other display controllers
)

/* sysfs PCI device structure:
 *
 * /sys/bus/pci/devices/
 * ├── 0000:00:02.0/           # PCI address (BDF format)
 * │   ├── class               # Contains device class (e.g., 0x030000 for VGA)
 * │   ├── vendor              # Contains vendor ID (e.g., 0x8086 for Intel)
 * │   ├── device              # Contains device ID
 * │   ├── revision            # Contains revision ID
 * │   ├── vendor_name         # Contains vendor name (if available)
 * │   └── device_name         # Contains device name (if available)
 * └── ...
 */

// collectGPUInfo gathers information about all GPUs in the system
func collectGPUInfo(log *log.PrefixLogger, reader fileio.Reader, mappingFile string) ([]GPUDeviceInfo, error) {
	gpu := make([]GPUDeviceInfo, 0)
	// load PCI mappings if provided
	var pciMappings map[string]PCIVendorInfo
	if mappingFile != "" {
		var err error
		pciMappings, err = loadPCIMappings(reader, mappingFile)
		if err != nil {
			// collection is best effort
			log.Debugf("Could not load PCI mappings: %v", err)
		}
	}

	// read all PCI devices
	entries, err := reader.ReadDir(pciDevicesPath)
	if err != nil {
		return nil, fmt.Errorf("error reading PCI directory: %v", err)
	}

	index := 0
	for _, entry := range entries {
		devicePath := filepath.Join(pciDevicesPath, entry.Name())

		// ensure GPU/display device by reading class file
		classFile := filepath.Join(devicePath, "class")
		classBytes, err := reader.ReadFile(classFile)
		if err != nil {
			log.Debugf("Could not read class file for device %s: %v", entry.Name(), err)
			continue
		}

		class := strings.TrimSpace(string(classBytes))
		if !strings.HasPrefix(class, VGAPrefix) && !strings.HasPrefix(class, DisplayPrefix) {
			continue
		}

		// vendor ID
		vendorIDFile := filepath.Join(devicePath, "vendor")
		vendorIDBytes, err := reader.ReadFile(vendorIDFile)
		if err != nil {
			log.Debugf("Could not read vendor ID for device %s: %v", entry.Name(), err)
			continue
		}
		vendorID := strings.TrimSpace(string(vendorIDBytes))

		// device ID
		deviceIDFile := filepath.Join(devicePath, "device")
		deviceIDBytes, err := reader.ReadFile(deviceIDFile)
		if err != nil {
			log.Debugf("Could not read device ID for device %s: %v", entry.Name(), err)
			continue
		}
		deviceID := strings.TrimSpace(string(deviceIDBytes))

		// revision ID
		revisionID := ""
		revisionIDFile := filepath.Join(devicePath, "revision")
		revisionIDBytes, err := reader.ReadFile(revisionIDFile)
		if err == nil {
			revisionID = strings.TrimSpace(string(revisionIDBytes))
		}

		// address is the directory name
		pciAddress := entry.Name()

		gpuDevice := GPUDeviceInfo{
			Index:      index,
			VendorID:   vendorID,
			DeviceID:   deviceID,
			PCIAddress: pciAddress,
			RevisionID: revisionID,
		}

		// lookup vendor and model names from mapping best effort
		if pciMappings != nil {
			if vendor, ok := pciMappings[vendorID]; ok {
				gpuDevice.Vendor = vendor.VendorName

				for _, model := range vendor.Models {
					if model.PCIID == deviceID {
						gpuDevice.Model = model.PCIName
						break
					}
				}
			}
		}

		// if no mapping try sysfs
		if gpuDevice.Vendor == "" {
			// read from vendor name file if it exists
			vendorNameFile := filepath.Join(devicePath, "vendor_name")
			vendorNameBytes, err := reader.ReadFile(vendorNameFile)
			if err != nil {
				log.Debugf("Could not read vendor name for device %s: %v", entry.Name(), err)
			}
			gpuDevice.Vendor = strings.TrimSpace(string(vendorNameBytes))
		}

		if gpuDevice.Model == "" {
			// read from device name file if it exists
			deviceNameFile := filepath.Join(devicePath, "device_name")
			deviceNameBytes, err := reader.ReadFile(deviceNameFile)
			if err != nil {
				log.Debugf("Could not read device name for device %s: %v", entry.Name(), err)
			}

			gpuDevice.Model = strings.TrimSpace(string(deviceNameBytes))
		}

		gpu = append(gpu, gpuDevice)
		index++
	}

	return gpu, nil
}

func loadPCIMappings(reader fileio.Reader, mapPath string) (map[string]PCIVendorInfo, error) {
	data, err := reader.ReadFile(mapPath)
	if err != nil {
		return nil, err
	}

	var vendors []PCIVendorInfo
	if err := yaml.Unmarshal(data, &vendors); err != nil {
		return nil, err
	}

	vendorMap := make(map[string]PCIVendorInfo)
	for _, vendor := range vendors {
		vendorMap[vendor.VendorID] = vendor
	}

	return vendorMap, nil
}
