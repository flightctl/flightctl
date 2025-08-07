package systeminfo

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/pkg/log"
	"sigs.k8s.io/yaml"
)

// ref, https://admin.pci-ids.ucw.cz/read/PD
const (
	// PCI class codes for graphics devices
	VGACompatibleController = "0x030000" // VGA compatible controller
	DisplayController       = "0x038000" // Other display controller
	ThreeDController        = "0x030200" // 3D controller

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
			log.Tracef("Could not load PCI mappings: %v", err)
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
		// address is the directory name
		pciAddress := entry.Name()

		gpuDevice := GPUDeviceInfo{
			Index:      index,
			PCIAddress: pciAddress,
		}

		// ensure GPU/display device by reading class file
		classFile := filepath.Join(devicePath, "class")
		classBytes, err := reader.ReadFile(classFile)
		if err != nil {
			log.Debugf("Could not read class file for device %s: %v", entry.Name(), err)
			continue
		}

		class := strings.TrimSpace(string(classBytes))
		if !isGPU(class) {
			log.Tracef("Device %s is not a GPU: %s", entry.Name(), class)
			continue
		}

		// vendor ID
		vendorIDFile := filepath.Join(devicePath, "vendor")
		vendorIDBytes, err := reader.ReadFile(vendorIDFile)
		if err != nil {
			log.Debugf("Could not read vendor ID for device %s: %v", entry.Name(), err)
		} else {
			gpuDevice.VendorID = strings.TrimSpace(string(vendorIDBytes))
		}

		// device ID
		deviceIDFile := filepath.Join(devicePath, "device")
		deviceIDBytes, err := reader.ReadFile(deviceIDFile)
		if err != nil {
			log.Debugf("Could not read device ID for device %s: %v", entry.Name(), err)
		} else {
			gpuDevice.DeviceID = strings.TrimSpace(string(deviceIDBytes))
		}

		// revision ID
		revisionIDFile := filepath.Join(devicePath, "revision")
		revisionIDBytes, err := reader.ReadFile(revisionIDFile)
		if err != nil {
			log.Debugf("Could not read revision ID for device %s: %v", entry.Name(), err)
		} else {
			gpuDevice.RevisionID = strings.TrimSpace(string(revisionIDBytes))
		}

		// use mapping information if available
		if pciMappings != nil && gpuDevice.VendorID != "" {
			if vendor, ok := pciMappings[gpuDevice.VendorID]; ok {
				gpuDevice.Vendor = vendor.VendorName

				if gpuDevice.DeviceID != "" {
					for _, model := range vendor.Models {
						if model.PCIID == gpuDevice.DeviceID {
							gpuDevice.Model = model.PCIName

							if model.MemoryBytes > 0 {
								gpuDevice.MemoryBytes = model.MemoryBytes
							}
							if model.Arch != "" {
								gpuDevice.Arch = model.Arch
							}
							if len(model.Features) > 0 {
								gpuDevice.Features = model.Features
							}

							break
						}
					}
				}
			}
		}

		if gpuDevice.Vendor == "" {
			vendorNameFile := filepath.Join(devicePath, "vendor_name")
			vendorNameBytes, err := reader.ReadFile(vendorNameFile)
			if err != nil {
				log.Debugf("Could not read vendor name for device %s: %v", entry.Name(), err)
			} else {
				gpuDevice.Vendor = strings.TrimSpace(string(vendorNameBytes))
			}
		}

		if gpuDevice.Model == "" {
			// read from device name file if it exists
			deviceNameFile := filepath.Join(devicePath, "device_name")
			deviceNameBytes, err := reader.ReadFile(deviceNameFile)
			if err != nil {
				log.Debugf("Could not read device name for device %s: %v", entry.Name(), err)
			} else {
				gpuDevice.Model = strings.TrimSpace(string(deviceNameBytes))
			}
		}

		if gpuDevice.MemoryBytes == 0 {
			gpuDevice.MemoryBytes = getGPUMemory(devicePath, reader, log)
		}

		gpu = append(gpu, gpuDevice)
		index++
	}

	return gpu, nil
}

// loadPCIMappings loads the PCI mappings from the specified file
// and returns a map of vendor IDs to PCIVendorInfo structs.
func loadPCIMappings(reader fileio.Reader, mapPath string) (map[string]PCIVendorInfo, error) {
	data, err := reader.ReadFile(mapPath)
	if err != nil {
		return nil, err
	}

	var vendors []PCIVendorInfo
	if err := yaml.Unmarshal(data, &vendors); err != nil {
		return nil, err
	}

	vendorMap := make(map[string]PCIVendorInfo, len(vendors))
	for _, vendor := range vendors {
		vendorMap[vendor.VendorID] = vendor
	}

	return vendorMap, nil
}

func isGPU(classCode string) bool {
	// convert to lowercase for consistent comparison
	classCode = strings.TrimSpace(strings.ToLower(classCode))

	// check for exact known GPU classes
	if classCode == VGACompatibleController ||
		classCode == DisplayController ||
		classCode == ThreeDController {
		return true
	}

	// prefixes for various GPU classes
	if strings.HasPrefix(classCode, VGAPrefix) || strings.HasPrefix(classCode, DisplayPrefix) {
		return true
	}

	return false
}

// getGPUMemory attempts to retrieve GPU memory information using simple, vendor-agnostic approaches best effort.
//
// refs:
// - https://docs.nvidia.com/jetson/archives/l4t-archived/l4t-3231/index.html
// - https://www.kernel.org/doc/html/latest/gpu/amdgpu.html
// - https://intel.github.io/intel-gpu-memory-management/
// - https://www.ics.uci.edu/~harris/ics216/pci/PCI_22.pdf
func getGPUMemory(devicePath string, reader fileio.Reader, log *log.PrefixLogger) uint64 {
	pathsToCheck := []struct {
		path       string
		multiplier uint64
	}{
		{"resource2_size", 1},                // PCI BAR memory (bytes)
		{"driver/vram_size_MB", 1024 * 1024}, // NVIDIA (MB to bytes)
		{"driver/mem_info_vram_total", 1},    // Common format (bytes)
		{"driver/mem_info/vram_size", 1},     // AMD format (bytes)
	}

	// Try all paths
	for _, p := range pathsToCheck {
		fullPath := filepath.Join(devicePath, p.path)
		data, err := reader.ReadFile(fullPath)
		if err != nil {
			// best-effort try next path
			continue
		}

		memStr := strings.TrimSpace(string(data))

		// Try decimal parse
		if value, err := strconv.ParseUint(memStr, 10, 64); err == nil {
			return value * p.multiplier
		}

		// Try hex parse (with potential 0x prefix)
		if strings.HasPrefix(memStr, "0x") {
			if value, err := strconv.ParseUint(memStr[2:], 16, 64); err == nil {
				return value * p.multiplier
			}
		}

		// Log that we found a file but couldn't parse it
		log.Debugf("Found memory info at %s but couldn't parse value: %s", fullPath, memStr)
	}

	return 0 // No memory info found
}
