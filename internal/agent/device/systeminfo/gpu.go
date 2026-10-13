package systeminfo

import (
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/ccoveille/go-safecast"
	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/samber/lo"
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
			gpuDevice.PCIVendorID = strings.TrimSpace(string(vendorIDBytes))
		}

		// device ID
		deviceIDFile := filepath.Join(devicePath, "device")
		deviceIDBytes, err := reader.ReadFile(deviceIDFile)
		if err != nil {
			log.Debugf("Could not read device ID for device %s: %v", entry.Name(), err)
		} else {
			gpuDevice.PCIDeviceID = strings.TrimSpace(string(deviceIDBytes))
		}

		// revision ID
		revisionIDFile := filepath.Join(devicePath, "revision")
		revisionIDBytes, err := reader.ReadFile(revisionIDFile)
		if err != nil {
			log.Debugf("Could not read revision ID for device %s: %v", entry.Name(), err)
		} else {
			gpuDevice.PCIRevisionID = strings.TrimSpace(string(revisionIDBytes))
		}

		// use mapping information if available
		if pciMappings != nil && gpuDevice.PCIVendorID != "" {
			if vendor, ok := pciMappings[gpuDevice.PCIVendorID]; ok {
				gpuDevice.Vendor = vendor.VendorName

				if gpuDevice.PCIDeviceID != "" {
					for _, model := range vendor.Models {
						if model.PCIID == gpuDevice.PCIDeviceID {
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

	platformGPUs := collectPlatformGPUs(log, reader, index)
	gpu = append(gpu, platformGPUs...)

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

type platformGPUInfo struct {
	Vendor string
	Model  string
	Arch   string
}

func lookupPlatformGPU(compatible string) (platformGPUInfo, bool) {
	switch compatible {
	case "nvidia,gm20b":
		return platformGPUInfo{Vendor: "NVIDIA", Model: "GM20B", Arch: "Maxwell"}, true
	case "nvidia,gp10b":
		return platformGPUInfo{Vendor: "NVIDIA", Model: "GP10B", Arch: "Pascal"}, true
	case "nvidia,gv11b":
		return platformGPUInfo{Vendor: "NVIDIA", Model: "GV11B", Arch: "Volta"}, true
	case "nvidia,ga10b":
		return platformGPUInfo{Vendor: "NVIDIA", Model: "GA10B", Arch: "Ampere"}, true
	default:
		return platformGPUInfo{}, false
	}
}

// collectPlatformGPUs scans platform bus devices for integrated GPUs identified
// by Device Tree compatible strings (e.g. NVIDIA Jetson SoC GPUs).
func collectPlatformGPUs(log *log.PrefixLogger, reader fileio.Reader, startIndex int) []GPUDeviceInfo {
	entries, err := reader.ReadDir(platformDevicesPath)
	if err != nil {
		log.Tracef("Could not read platform devices directory: %v", err)
		return nil
	}

	var gpus []GPUDeviceInfo
	for _, entry := range entries {
		devicePath := filepath.Join(platformDevicesPath, entry.Name())
		ueventPath := filepath.Join(devicePath, "uevent")

		ueventBytes, err := reader.ReadFile(ueventPath)
		if err != nil {
			continue
		}

		uevent := parseUevent(ueventBytes)
		if uevent["OF_NAME"] != "gpu" {
			continue
		}

		var matched platformGPUInfo
		var matchedCompat string
		for i := 0; ; i++ {
			key := fmt.Sprintf("OF_COMPATIBLE_%d", i)
			compat, ok := uevent[key]
			if !ok {
				break
			}
			if info, found := lookupPlatformGPU(compat); found {
				matched = info
				matchedCompat = compat
				break
			}
		}

		gpu := GPUDeviceInfo{
			Index: startIndex,
		}

		if matchedCompat != "" {
			gpu.Vendor = matched.Vendor
			gpu.Model = matched.Model
			gpu.Arch = matched.Arch
			gpu.PCIDeviceID = matchedCompat
		} else {
			log.Tracef("Platform device %s has OF_NAME=gpu but no known compatible string", entry.Name())
			compat := uevent["OF_COMPATIBLE_0"]
			gpu.PCIDeviceID = compat
			// Per devicetree spec, compatible strings use "manufacturer,model" format
			if vendor, model, ok := strings.Cut(compat, ","); ok {
				gpu.Vendor = vendor
				gpu.Model = model
			}
		}
		gpu.MemoryBytes = getGPUMemory(devicePath, reader, log)

		gpus = append(gpus, gpu)
		startIndex++
	}

	return gpus
}

func parseUevent(data []byte) map[string]string {
	result := make(map[string]string)
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		result[parts[0]] = parts[1]
	}
	return result
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

// collectGPUSystemInfo returns the structured GPU inventory for the system info.
// GPUs are enumerated even when the "gpu" info key is not configured so the GPU
// inventory is always reported. When the GPU collector already ran (the "gpu"
// info key is configured) its cached result is reused instead of re-scanning.
//
// It returns nil on scan failure so the caller can preserve the previously
// cached inventory: reporting an empty list would be indistinguishable from a
// successful scan that found zero GPUs and would incorrectly clear the reported
// GPU inventory.
func collectGPUSystemInfo(log *log.PrefixLogger, reader fileio.Reader, hardwareMapPath string, info *Info) *[]v1beta1.DeviceGpu {
	gpus := info.Hardware.GPU
	if gpus == nil {
		// The GPU collector did not run (the "gpu" info key is not configured),
		// so enumerate GPUs directly. Collection is best effort.
		collected, err := collectGPUInfo(log, reader, hardwareMapPath)
		if err != nil {
			log.Warnf("Failed to collect GPU info for system info: %v", err)
			return nil
		}
		gpus = collected
	}
	return toDeviceSystemInfoGPUs(gpus)
}

// toDeviceSystemInfoGPUs converts the collected GPU inventory into the API GPU
// list. The returned pointer is always non-nil (possibly an empty list) so the
// GPU inventory is reported deterministically.
func toDeviceSystemInfoGPUs(gpus []GPUDeviceInfo) *[]v1beta1.DeviceGpu {
	out := make([]v1beta1.DeviceGpu, 0, len(gpus))
	for _, gpu := range gpus {
		entry := v1beta1.DeviceGpu{Index: int32(gpu.Index)}
		if gpu.PCIAddress != "" {
			entry.PciAddress = lo.ToPtr(gpu.PCIAddress)
		}
		if gpu.Vendor != "" {
			entry.Vendor = lo.ToPtr(gpu.Vendor)
		}
		if gpu.Model != "" {
			entry.Model = lo.ToPtr(gpu.Model)
		}
		if gpu.PCIDeviceID != "" {
			entry.PciDeviceId = lo.ToPtr(gpu.PCIDeviceID)
		}
		if gpu.PCIVendorID != "" {
			entry.PciVendorId = lo.ToPtr(gpu.PCIVendorID)
		}
		if gpu.PCIRevisionID != "" {
			entry.PciRevisionId = lo.ToPtr(gpu.PCIRevisionID)
		}
		if gpu.Arch != "" {
			entry.Arch = lo.ToPtr(gpu.Arch)
		}
		if len(gpu.Features) > 0 {
			entry.Features = lo.ToPtr(slices.Clone(gpu.Features))
		}
		if gpu.MemoryBytes > 0 {
			// Guard the uint64 -> int64 conversion against overflow.
			if mem, err := safecast.ToInt64(gpu.MemoryBytes); err == nil {
				entry.MemoryBytes = lo.ToPtr(mem)
			}
		}
		out = append(out, entry)
	}
	return &out
}
