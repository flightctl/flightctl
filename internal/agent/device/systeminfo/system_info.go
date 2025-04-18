package systeminfo

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/agent/device/status"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/version"
)

const (
	sysVirtualNetDir = "/sys/devices/virtual/net"
	sysClassNetDir   = "/sys/class/net"
	dmiDirClass      = "/sys/class/dmi/id"
	pciDevicesPath   = "/sys/bus/pci/devices"
	osReleasePath    = "/etc/os-release"
	resolveConfPath  = "/etc/resolv.conf"
	cpuInfoPath      = "/proc/cpuinfo"
	memInfoPath      = "/proc/meminfo"
	ipv4RoutePath    = "/proc/net/route"
	ipv6RoutePath    = "/proc/net/ipv6_route"

	// ScriptOverrideDir is the directory where custom/overide scripts are stored
	ScriptOverrideDir = "collect.d"
)

type Manager interface {
	// IsRebooted checks if the system has been rebooted since the last time the agent started
	IsRebooted() bool
	// BootID returns the unique boot ID populated by the kernel
	BootID() string
	// BootTime returns the time the system was booted
	BootTime() string
	// ReloadStatus collects system info and sends a patch status to the management API
	ReloadStatus() error
	status.Exporter
}

type Info struct {
	Hostname     string                 `json:"hostname"`
	Architecture string                 `json:"architecture"`
	Kernel       string                 `json:"kernel"`
	Distribution map[string]interface{} `json:"distribution,omitempty"`
	Hardware     HardwareFacts          `json:"hardware"`
	CollectedAt  string                 `json:"collected_at"`
	Metadata     map[string]interface{} `json:"metadata,omitempty"`
	Boot         Boot                   `json:"boot,omitempty"`
}

// HardwareFacts contains hardware information gathered by ghw
type HardwareFacts struct {
	CPU     *CPUInfo        `json:"cpu,omitempty"`
	Memory  *MemoryInfo     `json:"memory,omitempty"`
	Block   *BlockInfo      `json:"block,omitempty"`
	Network *NetworkInfo    `json:"network,omitempty"`
	GPU     []GPUDeviceInfo `json:"gpu,omitempty"`
	BIOS    *BIOSInfo       `json:"bios,omitempty"`
	System  *SystemInfo     `json:"system,omitempty"`
}

// CPUInfo represents CPU information
type CPUInfo struct {
	TotalCores   int             `json:"total_cores"`
	TotalThreads int             `json:"total_threads"`
	Architecture string          `json:"architecture"`
	Processors   []ProcessorInfo `json:"processors"`
}

// ProcessorInfo contains information about a single processor
type ProcessorInfo struct {
	ID                int      `json:"id"`
	NumCores          int      `json:"num_cores"`
	NumThreads        int      `json:"num_threads"`
	NumThreadsPerCore int      `json:"num_threads_per_core"`
	Vendor            string   `json:"vendor"`
	Model             string   `json:"model"`
	Capabilities      []string `json:"capabilities,omitempty"`
}

// MemoryInfo represents memory information
type MemoryInfo struct {
	TotalKB uint64                 `json:"total_kb"`
	Details map[string]interface{} `json:"details,omitempty"`
}

// BlockInfo represents block device information
type BlockInfo struct {
	TotalSizeBytes uint64      `json:"total_size_bytes"`
	TotalSizeGB    float64     `json:"total_size_gb"`
	Disks          []DiskInfo  `json:"disks"`
	Mounts         []MountInfo `json:"mounts,omitempty"`
}

// DiskInfo contains information about a single disk
type DiskInfo struct {
	Name              string          `json:"name"`
	SizeBytes         uint64          `json:"size_bytes"`
	SizeGB            float64         `json:"size_gb"`
	DriveType         string          `json:"drive_type"`
	StorageController string          `json:"storage_controller"`
	Vendor            string          `json:"vendor,omitempty"`
	Model             string          `json:"model,omitempty"`
	SerialNumber      string          `json:"serial_number,omitempty"`
	WWN               string          `json:"wwn,omitempty"`
	BusType           string          `json:"bus_type,omitempty"`
	Partitions        []PartitionInfo `json:"partitions,omitempty"`
}

// PartitionInfo contains information about a disk partition
type PartitionInfo struct {
	Name       string  `json:"name"`
	SizeBytes  uint64  `json:"size_bytes"`
	SizeGB     float64 `json:"size_gb"`
	MountPoint string  `json:"mount_point,omitempty"`
	Type       string  `json:"type,omitempty"`
	IsReadOnly bool    `json:"is_read_only,omitempty"`
}

// MountInfo contains information about a filesystem mount
type MountInfo struct {
	Device     string `json:"device"`
	MountPoint string `json:"mount_point"`
	FSType     string `json:"fs_type"`
	Options    string `json:"options"`
}

// NetworkInfo represents network information
type NetworkInfo struct {
	Interfaces   []InterfaceInfo `json:"interfaces"`
	DefaultRoute *DefaultRoute   `json:"default_route,omitempty"`
	DNSServers   []string        `json:"dns_servers,omitempty"`
	FQDN         string          `json:"fqdn,omitempty"`
}

// DefaultRoute represents the default network route for IPv4 or IPv6.
type DefaultRoute struct {
	Interface string `json:"interface"`
	Gateway   string `json:"gateway"`
	Family    string `json:"family"` // "ipv4" or "ipv6"
}

// InterfaceInfo contains information about a network interface
type InterfaceInfo struct {
	Name        string   `json:"name"`
	MACAddress  string   `json:"mac_address"`
	IsVirtual   bool     `json:"is_virtual"`
	IPAddresses []string `json:"ip_addresses,omitempty"`
	MTU         int      `json:"mtu,omitempty"`
	Status      string   `json:"status,omitempty"`
}

// GPUInfo represents GPU information
type GPUInfo struct {
	GPUs []GPUDeviceInfo `json:"gpus"`
}

// GPUDeviceInfo contains information about a GPU device
type GPUDeviceInfo struct {
	Index       int    `json:"index"`
	Vendor      string `json:"vendor"`
	Model       string `json:"model"`
	DeviceID    string `json:"device_id,omitempty"`
	PCIAddress  string `json:"pci_address,omitempty"`
	RevisionID  string `json:"revision_id,omitempty"`
	VendorID    string `json:"vendor_id,omitempty"`
	MemoryBytes uint64 `json:"memory_bytes,omitempty"`
}

// PCIVendorInfo contains mapping information for vendors and models
type PCIVendorInfo struct {
	Models     []PCIModelInfo `yaml:"models"`
	VendorID   string         `yaml:"vendorID"`
	VendorName string         `yaml:"vendorName"`
}

// PCIModelInfo contains information about a specific GPU model
type PCIModelInfo struct {
	PCIID   string `yaml:"pciID"`
	PCIName string `yaml:"pciName"`
}

// BIOSInfo represents BIOS information
type BIOSInfo struct {
	Vendor  string `json:"vendor"`
	Version string `json:"version"`
	Date    string `json:"date,omitempty"`
}

// SystemInfo represents system information
type SystemInfo struct {
	Manufacturer string `json:"manufacturer"`
	ProductName  string `json:"product_name"`
	SerialNumber string `json:"serial_number,omitempty"`
	UUID         string `json:"uuid,omitempty"`
	Version      string `json:"version,omitempty"`
	Family       string `json:"family,omitempty"`
	SKU          string `json:"sku,omitempty"`
}

type Boot struct {
	// Time is the time the system was booted.
	Time string `json:"bootTime,omitempty"`
	// ID is the unique boot ID populated by the kernel.
	ID string `json:"bootID,omitempty"`
}

func (b *Boot) IsEmpty() bool {
	return b.Time == "" && b.ID == ""
}

// CollectInfo collects system information and returns a SystemInfoReport
func CollectInfo(ctx context.Context, log *log.PrefixLogger, exec executer.Executer, reader fileio.Reader, hardwareMapFilePath string) (*Info, error) {
	now := time.Now()
	log.Debugf("Collecting system information...")
	defer func() {
		log.Debugf("System information collection took %s", time.Since(now))
	}()

	info := &Info{
		CollectedAt: time.Now().Format(time.RFC3339),
		Hardware:    HardwareFacts{},
	}

	// TODO: only collect requested info from fact keys

	// system info
	var err error
	info.Hostname, err = os.Hostname()
	if err != nil {
		log.Warningf("Failed to get hostname: %v", err)
	}

	info.Architecture = runtime.GOARCH
	if out, err := exec.CommandContext(ctx, "uname", "-r").Output(); err == nil {
		info.Kernel = strings.TrimSpace(string(out))
	} else {
		info.Kernel = runtime.GOOS
	}

	// distribution info
	distroInfo, err := collectDistributionInfo(ctx, exec, reader)
	if err != nil {
		log.Warningf("Failed to get distribution info: %v", err)
	} else {
		info.Distribution = distroInfo
	}

	// cpu info
	cpuInfo, err := collectCPUInfo(reader)
	if err != nil {
		log.Warningf("Failed to get CPU info: %v", err)
	} else {
		info.Hardware.CPU = cpuInfo
	}

	// network info
	netInfo, err := collectNetworkInfo(ctx, log, exec, reader)
	if err != nil {
		log.Warningf("Failed to get network info: %v", err)
	} else {
		info.Hardware.Network = netInfo
	}

	// gpu info
	gpuInfo, err := collectGPUInfo(log, reader, hardwareMapFilePath)
	if err != nil {
		log.Warningf("Failed to get GPU info: %v", err)
	} else {
		info.Hardware.GPU = gpuInfo
	}

	// memory info
	memInfo, err := collectMemoryInfo(log, reader)
	if err != nil {
		log.Warningf("Failed to get memory info: %v", err)
	} else {
		info.Hardware.Memory = memInfo
	}

	// bios info
	biosInfo, err := collectBIOSInfo(reader)
	if err != nil {
		log.Warningf("Failed to get BIOS info: %v", err)
	} else {
		info.Hardware.BIOS = biosInfo
	}

	// system info
	sysInfo, err := collectSystemInfo(reader)
	if err != nil {
		log.Warningf("Failed to get system info: %v", err)
	} else {
		info.Hardware.System = sysInfo
	}

	// boot info
	bootID, err := getBootID(reader)
	if err != nil {
		log.Warningf("Failed to get boot ID: %v", err)
	} else {
		info.Boot.ID = bootID
	}
	bootTime, err := getBootTime(exec)
	if err != nil {
		log.Warningf("Failed to get boot time: %v", err)
	} else {
		info.Boot.Time = bootTime
	}

	// collector version
	info.Metadata = map[string]interface{}{
		"collector_version": version.Get().String(),
		"collector_type":    "flightctl-agent",
	}

	return info, nil
}

// SupportedInfoKeys is a map of supported info keys to their corresponding functions.
var SupportedInfoKeys = map[string]func(info *Info) string{
	"hostname":     func(i *Info) string { return i.Hostname },
	"architecture": func(i *Info) string { return i.Architecture },
	"kernel":       func(i *Info) string { return i.Kernel },
	"distro_name": func(i *Info) string {
		if i.Distribution == nil {
			return ""
		}
		if val, ok := i.Distribution["name"]; ok {
			if s, ok := val.(string); ok {
				return s
			}
			return fmt.Sprint(val)
		}
		return ""
	},
	"distro_version": func(i *Info) string {
		if i.Distribution == nil {
			return ""
		}
		if val, ok := i.Distribution["version"]; ok {
			if str, ok := val.(string); ok {
				return str
			}
			return fmt.Sprint(val)
		}
		return ""
	},
	"product_name": func(i *Info) string {
		if i.Hardware.System != nil {
			return i.Hardware.System.ProductName
		}
		return ""
	},
	"product_serial": func(i *Info) string {
		if i.Hardware.System != nil {
			return i.Hardware.System.SerialNumber
		}
		return ""
	},
	"product_uuid": func(i *Info) string {
		if i.Hardware.System != nil {
			return i.Hardware.System.UUID
		}
		return ""
	},
	"bios_vendor": func(i *Info) string {
		if i.Hardware.BIOS != nil {
			return i.Hardware.BIOS.Vendor
		}
		return ""
	},
	"bios_version": func(i *Info) string {
		if i.Hardware.BIOS != nil {
			return i.Hardware.BIOS.Version
		}
		return ""
	},
	"default_interface": func(i *Info) string {
		if i.Hardware.Network != nil && i.Hardware.Network.DefaultRoute != nil {
			return i.Hardware.Network.DefaultRoute.Interface
		}
		return ""
	},
	"default_ip_address": func(i *Info) string {
		if i.Hardware.Network == nil || i.Hardware.Network.DefaultRoute == nil {
			return ""
		}
		dr := i.Hardware.Network.DefaultRoute
		for _, iface := range i.Hardware.Network.Interfaces {
			if iface.Name == dr.Interface {
				if len(iface.IPAddresses) == 0 {
					return ""
				}
				// prio non-link-local address
				for _, addr := range iface.IPAddresses {
					ip := net.ParseIP(strings.Split(addr, "/")[0])
					if ip != nil && !ip.IsLinkLocalUnicast() {
						return addr
					}
				}

				// fallback to the first address
				return iface.IPAddresses[0]
			}
		}
		return ""
	},
	"default_mac_address": func(i *Info) string {
		if i.Hardware.Network == nil || i.Hardware.Network.DefaultRoute == nil {
			return ""
		}
		dr := i.Hardware.Network.DefaultRoute
		for _, iface := range i.Hardware.Network.Interfaces {
			if iface.Name == dr.Interface {
				return iface.MACAddress
			}
		}
		return ""
	},
	"gpu": func(i *Info) string {
		if len(i.Hardware.GPU) == 0 {
			return ""
		}

		var parts []string
		for idx, gpu := range i.Hardware.GPU {
			parts = append(parts, fmt.Sprintf("[%d] %s %s", idx, gpu.Vendor, gpu.Model))
		}
		return strings.Join(parts, "; ")
	},
	"total_memory_mb": func(i *Info) string {
		if i.Hardware.Memory == nil || i.Hardware.Memory.TotalKB <= 0 {
			return ""
		}
		return fmt.Sprintf("%d", i.Hardware.Memory.TotalKB/1024)
	},
	"cpu_cores": func(i *Info) string {
		if i.Hardware.CPU != nil {
			return fmt.Sprintf("%d", i.Hardware.CPU.TotalCores)
		}
		return ""
	},
	"cpu_processors": func(i *Info) string {
		if i.Hardware.CPU != nil {
			return fmt.Sprintf("%d", len(i.Hardware.CPU.Processors))
		}
		return ""
	},
	"cpu_model": func(i *Info) string {
		if i.Hardware.CPU != nil && len(i.Hardware.CPU.Processors) > 0 {
			return i.Hardware.CPU.Processors[0].Model
		}
		return ""
	},
}

// DefaultInfoKeys is a list of default keys to be used when generating facts
var DefaultInfoKeys = []string{
	"hostname",
	"architecture",
	"kernel",
	"distro_name",
	"distro_version",
	"product_name",
	"product_uuid",
	"default_interface",
	"default_ip_address",
	"default_mac_address",
}

// GenerateFacts generates a map of facts based on the provided keys and system information
func GenerateFacts(ctx context.Context, log *log.PrefixLogger, reader fileio.Reader, exec executer.Executer, info *Info, keys []string, dataDir string) map[string]string {
	labels := make(map[string]string)

	for _, key := range keys {
		if ctx.Err() != nil {
			log.Warningf("Context error while generating facts for key %s: %v", key, ctx.Err())
			return labels
		}
		// eval override/custom
		if val, ok := getOverrideValue(ctx, key, reader, exec, dataDir); ok {
			labels[key] = val
			continue
		}

		if fn, ok := SupportedInfoKeys[key]; ok {
			val := fn(info)
			if val != "" {
				labels[key] = val
			}
		}
	}

	return labels
}

// getOverrideValue checks if a script exists in the override directory and executes it
func getOverrideValue(ctx context.Context, key string, reader fileio.Reader, exec executer.Executer, dataDir string) (string, bool) {
	scriptPath := filepath.Join(dataDir, ScriptOverrideDir, key)
	info, err := os.Stat(reader.PathFor(scriptPath))
	if err != nil || info.IsDir() {
		return "", false
	}

	// TODO do we need to have a timeout for each we do have a global timeout.
	out, err := exec.CommandContext(ctx, reader.PathFor(scriptPath)).Output()
	if err != nil {
		return "", false
	}

	return strings.TrimSpace(string(out)), true
}

// collectSystemInfo gathers system information
func collectSystemInfo(reader fileio.Reader) (*SystemInfo, error) {
	sysInfo := &SystemInfo{}

	fileFieldMap := map[string]*string{
		"sys_vendor":      &sysInfo.Manufacturer,
		"product_name":    &sysInfo.ProductName,
		"product_serial":  &sysInfo.SerialNumber, // requires root
		"product_uuid":    &sysInfo.UUID,         // requires root
		"product_version": &sysInfo.Version,
		"product_family":  &sysInfo.Family,
		"product_sku":     &sysInfo.SKU,
	}

	for fileName, fieldPtr := range fileFieldMap {
		filePath := filepath.Join(dmiDirClass, fileName)
		content, err := reader.ReadFile(filePath)
		if err == nil {
			*fieldPtr = strings.TrimSpace(string(content))
		}
		// best effort: ignore errors for missing files and permissions
	}

	return sysInfo, nil
}

// collectDistributionInfo gathers OS distribution information
func collectDistributionInfo(ctx context.Context, exec executer.Executer, reader fileio.Reader) (map[string]interface{}, error) {
	distro := make(map[string]interface{})

	if _, err := os.Stat(reader.PathFor(osReleasePath)); err == nil {
		data, err := reader.ReadFile(osReleasePath)
		if err != nil {
			return nil, err
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
			lines := strings.Split(string(data), "\n")
			for _, line := range lines {
				line = strings.TrimSpace(line)
				if line == "" {
					continue
				}

				parts := strings.SplitN(line, "=", 2)
				if len(parts) != 2 {
					continue
				}

				key := parts[0]
				value := strings.Trim(parts[1], "\"")

				switch key {
				case "NAME":
					distro["name"] = value
				case "VERSION":
					distro["version"] = value
				case "ID":
					distro["id"] = value
				case "VERSION_ID":
					distro["version_id"] = value
				case "PRETTY_NAME":
					distro["pretty_name"] = value
				}
			}
		}
	}

	// kernel version
	args := []string{"-r"}
	out, err := exec.CommandContext(ctx, "uname", args...).Output()
	if err == nil {
		distro["kernel"] = strings.TrimSpace(string(out))
	}

	return distro, nil
}

// collectBIOSInfo gathers BIOS information
func collectBIOSInfo(reader fileio.Reader) (*BIOSInfo, error) {
	biosInfo := &BIOSInfo{}

	fileFieldMap := map[string]*string{
		"bios_vendor":  &biosInfo.Vendor,
		"bios_version": &biosInfo.Version,
		"bios_date":    &biosInfo.Date,
	}

	for fileName, fieldPtr := range fileFieldMap {
		filePath := filepath.Join(dmiDirClass, fileName)
		content, err := reader.ReadFile(filePath)
		if err != nil {
			// best effort: ignore errors for missing files and permissions
			continue
		}
		*fieldPtr = strings.TrimSpace(string(content))
	}

	if biosInfo.Vendor == "" && biosInfo.Version == "" && biosInfo.Date == "" {
		return nil, fmt.Errorf("unable to retrieve BIOS information")
	}

	return biosInfo, nil
}
