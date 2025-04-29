package systeminfo

import (
	"context"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/flightctl/flightctl/internal/agent/config"
	"github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/agent/device/status"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/version"
)

const (
	sysVirtualNetDir = "/sys/devices/virtual/net"
	sysClassNetDir   = "/sys/class/net"
	dmiClassPath     = "/sys/class/dmi/id"
	pciDevicesPath   = "/sys/bus/pci/devices"
	osReleasePath    = "/etc/os-release"
	resolveConfPath  = "/etc/resolv.conf"
	cpuInfoPath      = "/proc/cpuinfo"
	memInfoPath      = "/proc/meminfo"
	ipv4RoutePath    = "/proc/net/route"
	ipv6RoutePath    = "/proc/net/ipv6_route"
	bootIDPath       = "/proc/sys/kernel/random/boot_id"

	// SystemFileName is the name of the file where the system boot status is stored in the data-dir.
	SystemFileName = "system.json"
	// HardwareMapFileName is the name of the file where the hardware map is stored.
	HardwareMapFileName = "hardware-map.yaml"
)

type Manager interface {
	// IsRebooted checks if the system has been rebooted since the last time the agent started
	IsRebooted() bool
	// BootID returns the unique boot ID populated by the kernel
	BootID() string
	// BootTime returns the time the system was booted
	BootTime() string
	// ReloadStatus collects system info and sends a patch status to the management API
	ReloadStatus(ctx context.Context) error
	// RegisterCollector registers a system info collector
	RegisterCollector(ctx context.Context, name string, fn CollectorFn)
	status.Exporter
}

// CollectorFn is a function that collects system information. Collectors are
// best effort and should log any errors.
type CollectorFn func(ctx context.Context) string

type Info struct {
	Hostname        string                 `json:"hostname"`
	Architecture    string                 `json:"architecture"`
	OperatingSystem string                 `json:"operatingSystem"`
	Kernel          string                 `json:"kernel"`
	Distribution    map[string]interface{} `json:"distribution,omitempty"`
	Hardware        HardwareFacts          `json:"hardware"`
	CollectedAt     string                 `json:"collectedAt"`
	Metadata        map[string]interface{} `json:"metadata,omitempty"`
	Boot            Boot                   `json:"boot,omitempty"`
	Custom          map[string]string      `json:"custom,omitempty"`
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
	NumCores          int      `json:"numCores"`
	NumThreads        int      `json:"numThreads"`
	NumThreadsPerCore int      `json:"numThreadsPerCore"`
	Vendor            string   `json:"vendor"`
	Model             string   `json:"model"`
	Capabilities      []string `json:"capabilities,omitempty"`
}

// MemoryInfo represents memory information
type MemoryInfo struct {
	TotalKB uint64                 `json:"totalKb"`
	Details map[string]interface{} `json:"details,omitempty"`
}

// BlockInfo represents block device information
type BlockInfo struct {
	TotalSizeBytes uint64      `json:"totalSizeBytes"`
	TotalSizeGB    float64     `json:"totalSizeGb"`
	Disks          []DiskInfo  `json:"disks"`
	Mounts         []MountInfo `json:"mounts,omitempty"`
}

// DiskInfo contains information about a single disk
type DiskInfo struct {
	Name              string          `json:"name"`
	SizeBytes         uint64          `json:"sizeBytes"`
	SizeGB            float64         `json:"sizeGb"`
	DriveType         string          `json:"driveType"`
	StorageController string          `json:"storageController"`
	Vendor            string          `json:"vendor,omitempty"`
	Model             string          `json:"model,omitempty"`
	SerialNumber      string          `json:"serialNumber,omitempty"`
	WWN               string          `json:"wwn,omitempty"`
	BusType           string          `json:"busType,omitempty"`
	Partitions        []PartitionInfo `json:"partitions,omitempty"`
}

// PartitionInfo contains information about a disk partition
type PartitionInfo struct {
	Name       string  `json:"name"`
	SizeBytes  uint64  `json:"sizeBytes"`
	SizeGB     float64 `json:"sizeGb"`
	MountPoint string  `json:"mountPoint,omitempty"`
	Type       string  `json:"type,omitempty"`
	IsReadOnly bool    `json:"isReadOnly,omitempty"`
}

// MountInfo contains information about a filesystem mount
type MountInfo struct {
	Device     string `json:"device"`
	MountPoint string `json:"mountPoint"`
	FSType     string `json:"fsType"`
	Options    string `json:"options"`
}

// NetworkInfo represents network information
type NetworkInfo struct {
	Interfaces   []InterfaceInfo `json:"interfaces"`
	DefaultRoute *DefaultRoute   `json:"defaultRoute,omitempty"`
	DNSServers   []string        `json:"dnsServers,omitempty"`
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
	MACAddress  string   `json:"macAddress"`
	IsVirtual   bool     `json:"isVirtual"`
	IPAddresses []string `json:"ipAddresses,omitempty"`
	MTU         int      `json:"mtu,omitempty"`
	Status      string   `json:"status,omitempty"`
}

// GPUInfo represents GPU information
type GPUInfo struct {
	GPUs []GPUDeviceInfo `json:"gpus"`
}

// GPUDeviceInfo contains information about a GPU device
type GPUDeviceInfo struct {
	Index       int      `json:"index"`
	Vendor      string   `json:"vendor"`
	Model       string   `json:"model"`
	DeviceID    string   `json:"deviceId,omitempty"`
	PCIAddress  string   `json:"pciAddress,omitempty"`
	RevisionID  string   `json:"revisionId,omitempty"`
	VendorID    string   `json:"vendorId,omitempty"`
	MemoryBytes uint64   `json:"memoryBytes,omitempty"`
	Arch        string   `json:"architecture,omitempty"`
	Features    []string `json:"features,omitempty"`
}

// PCIVendorInfo contains mapping information for vendors and models
type PCIVendorInfo struct {
	Models     []PCIModelInfo `json:"models"`
	VendorID   string         `json:"vendorID"`
	VendorName string         `json:"vendorName"`
}

// PCIModelInfo contains information about a specific GPU model
type PCIModelInfo struct {
	PCIID       string   `json:"pciID"`
	PCIName     string   `json:"pciName"`
	MemoryBytes uint64   `json:"memoryBytes,omitempty"`
	Arch        string   `json:"architecture,omitempty"`
	Features    []string `json:"features,omitempty"`
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
	ProductName  string `json:"productName"`
	SerialNumber string `json:"serialNumber,omitempty"`
	UUID         string `json:"uuid,omitempty"`
	Version      string `json:"version,omitempty"`
	Family       string `json:"family,omitempty"`
	SKU          string `json:"sku,omitempty"`
}

type InfoMap map[string]string

type Boot struct {
	// Time is the time the system was booted.
	Time string `json:"bootTime,omitempty"`
	// ID is the unique boot ID populated by the kernel.
	ID string `json:"bootID,omitempty"`
}

func (b *Boot) IsEmpty() bool {
	return b.Time == "" && b.ID == ""
}

type CollectCfg struct {
	collectAllCustom bool
}

type CollectOpt func(*CollectCfg)

// WithAllCustom sets the allCustom option to true enabling all custom scripts to be collected.
func WithAllCustom() CollectOpt {
	return func(cfg *CollectCfg) {
		cfg.collectAllCustom = true
	}
}

// Collect collects system information and returns it as a map of key-value pairs.
func Collect(ctx context.Context, log *log.PrefixLogger, exec executer.Executer, reader fileio.Reader, customKeys []string, hardwareMapFilePath string, opts ...CollectOpt) (*Info, error) {
	now := time.Now()
	log.Debugf("Collecting system information...")
	defer func() {
		log.Debugf("System information collection took %s", time.Since(now))
	}()

	isContextError := func(err error) bool {
		return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
	}

	info := &Info{
		CollectedAt: time.Now().Format(time.RFC3339),
		Hardware:    HardwareFacts{},
	}

	if err := ctx.Err(); err != nil {
		log.Warningf("Context canceled before collection started: %v", err)
		return nil, err
	}

	// TODO: only collect requested info from fact keys

	// system info
	var err error
	info.Hostname, err = os.Hostname()
	if err != nil {
		log.Warningf("Failed to get hostname: %v", err)
	}
	info.OperatingSystem = runtime.GOOS
	info.Architecture = runtime.GOARCH

	out, err := exec.CommandContext(ctx, "uname", "-r").Output()
	if err != nil {
		if isContextError(err) {
			log.Warningf("Context canceled during kernel info collection: %v", err)
			return info, err
		}
		log.Warningf("Failed to get kernel info: %v", err)
	} else {
		info.Kernel = strings.TrimSpace(string(out))
	}

	// distribution info
	distroInfo, err := collectDistributionInfo(ctx, exec, reader)
	if err != nil {
		if isContextError(err) {
			log.Warningf("Context canceled during distribution info collection: %v", err)
			return info, err
		}
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
	bootTime, err := getBootTime(ctx, exec)
	if err != nil {
		if isContextError(err) {
			log.Warningf("Context canceled during boot time collection: %v", err)
			return info, err
		}
		log.Warningf("Failed to get boot time: %v", err)
	} else {
		info.Boot.Time = bootTime
	}

	// collector version
	info.Metadata = map[string]interface{}{
		"collector_version": version.Get().String(),
		"collector_type":    "flightctl-agent",
	}

	// network info
	netInfo, err := collectNetworkInfo(ctx, log, exec, reader)
	if err != nil {
		if isContextError(err) {
			log.Warningf("Context canceled during network info collection: %v", err)
			return info, err
		}
		log.Warningf("Failed to get network info: %v", err)
	} else {
		info.Hardware.Network = netInfo
	}

	// custom info
	if len(customKeys) > 0 {
		customInfo, err := getCustomInfoMap(ctx, log, customKeys, reader, exec, opts...)
		if err != nil {
			if isContextError(err) {
				log.Warningf("Context canceled during custom info collection: %v", err)
				return info, err
			}
			log.Warningf("Failed to get custom info: %v", err)
		} else {
			info.Custom = customInfo
		}
	} else {
		log.Infof("No custom info keys provided, skipping custom info collection")
	}

	return info, nil
}

// SupportedInfoKeys is a map of supported info keys to their corresponding functions.
var SupportedInfoKeys = map[string]func(info *Info) string{
	"hostname":     func(i *Info) string { return i.Hostname },
	"architecture": func(i *Info) string { return i.Architecture },
	"kernel":       func(i *Info) string { return i.Kernel },
	"distroName": func(i *Info) string {
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
	"distroVersion": func(i *Info) string {
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
	"productName": func(i *Info) string {
		if i.Hardware.System != nil {
			return i.Hardware.System.ProductName
		}
		return ""
	},
	"productSerial": func(i *Info) string {
		if i.Hardware.System != nil {
			return i.Hardware.System.SerialNumber
		}
		return ""
	},
	"productUuid": func(i *Info) string {
		if i.Hardware.System != nil {
			return i.Hardware.System.UUID
		}
		return ""
	},
	"biosVendor": func(i *Info) string {
		if i.Hardware.BIOS != nil {
			return i.Hardware.BIOS.Vendor
		}
		return ""
	},
	"biosVersion": func(i *Info) string {
		if i.Hardware.BIOS != nil {
			return i.Hardware.BIOS.Version
		}
		return ""
	},
	"netInterfaceDefault": func(i *Info) string {
		if i.Hardware.Network != nil && i.Hardware.Network.DefaultRoute != nil {
			return i.Hardware.Network.DefaultRoute.Interface
		}
		return ""
	},
	"netIpDefault": func(i *Info) string {
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
	"netMacDefault": func(i *Info) string {
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
		return strings.Join(parts, ".")
	},
	"memoryTotalKb": func(i *Info) string {
		if i.Hardware.Memory == nil || i.Hardware.Memory.TotalKB <= 0 {
			return ""
		}
		return fmt.Sprintf("%d", i.Hardware.Memory.TotalKB)
	},
	"cpuCores": func(i *Info) string {
		if i.Hardware.CPU != nil {
			return fmt.Sprintf("%d", i.Hardware.CPU.TotalCores)
		}
		return ""
	},
	"cpuProcessors": func(i *Info) string {
		if i.Hardware.CPU != nil {
			return fmt.Sprintf("%d", len(i.Hardware.CPU.Processors))
		}
		return ""
	},
	"cpuModel": func(i *Info) string {
		if i.Hardware.CPU != nil && len(i.Hardware.CPU.Processors) > 0 {
			return i.Hardware.CPU.Processors[0].Model
		}
		return ""
	},
}

// getSystemInfoMap collects system information from the system It executes
// system commands and reads files to gather information about the system.
// It returns a map of key-value pairs representing the system information.
func getSystemInfoMap(ctx context.Context, log *log.PrefixLogger, info *Info, infoKeys []string) InfoMap {
	infoMap := make(InfoMap, len(infoKeys))

	for _, key := range infoKeys {
		if ctx.Err() != nil {
			return infoMap
		}

		formatFn, exists := SupportedInfoKeys[key]
		if !exists {
			log.Errorf("Unsupported system info key: %s", key)
			continue
		}
		val := formatFn(info)
		if val == "" {
			log.Debugf("SystemInfo key returned an empty value: %s", key)
		}
		infoMap[key] = val
	}

	return infoMap
}

// getCustomInfoMap collects custom information from the system It executes
// custom scripts located in the CustomInfoScriptDir directory and returns the
// output as a map of key-value pairs.
func getCustomInfoMap(ctx context.Context, log *log.PrefixLogger, keys []string, reader fileio.Reader, exec executer.Executer, opts ...CollectOpt) (map[string]string, error) {
	cfg := &CollectCfg{}
	for _, opt := range opts {
		opt(cfg)
	}

	exists, err := reader.PathExists(config.SystemInfoCustomScriptDir)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, fmt.Errorf("custom info directory %q does not exist", config.SystemInfoCustomScriptDir)
	}

	entries, err := os.ReadDir(reader.PathFor(config.SystemInfoCustomScriptDir))
	if err != nil {
		return nil, err
	}

	if cfg.collectAllCustom {
		// discover all available scripts dynamically
		keys = discoverKeysFromEntries(entries)
	}

	customInfoinfo := make(map[string]string, len(keys))
	for _, key := range keys {
		if ctx.Err() != nil {
			//  only return ctx errors
			return nil, ctx.Err()
		}

		val, err := getCustomInfoValue(ctx, key, reader, exec, entries)
		if err != nil {
			log.Warnf("Failed to get custom info for key %s: %v", key, err)
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				// only return ctx errors
				return nil, err
			}
		}

		_, ok := customInfoinfo[key]
		if ok {
			// skip if the key already exists
			log.Warnf("Custom info key %s already exists, skipping", key)
			continue
		}

		// empty value is not an error
		customInfoinfo[key] = val
	}

	return customInfoinfo, nil
}

// getCustomInfo takes a
//
// It supports multiple filename patterns based on a hostname:
//   - myCustomInfo.sh
//   - mycustominfo.sh
//   - 01-mycustominfo.sh
//   - 20-mycustominfo.pyp
func getCustomInfoValue(ctx context.Context, key string, reader fileio.Reader, exec executer.Executer, entries []fs.DirEntry) (string, error) {
	var candidates []string
	keyLower := strings.ToLower(key)

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		name := entry.Name()
		base := strings.TrimSuffix(name, filepath.Ext(name))
		baseLower := strings.ToLower(base)

		// match exact key or prefix + "-" + key
		// intentionally not using regex to avoid performance cost
		if base == key || baseLower == keyLower ||
			strings.HasSuffix(base, "-"+key) || strings.HasSuffix(baseLower, "-"+keyLower) {
			candidates = append(candidates, name)
		}
	}

	if len(candidates) == 0 {
		return "", nil
	}

	// lexicographically sort the candidates
	sort.Strings(candidates)

	for _, name := range candidates {
		scriptPath := filepath.Join(reader.PathFor(config.SystemInfoCustomScriptDir), name)

		info, err := os.Stat(scriptPath)
		if err != nil || info.IsDir() || info.Mode()&0111 == 0 {
			continue
		}

		out, err := exec.CommandContext(ctx, scriptPath).Output()
		if err != nil {
			return "", err
		}

		return strings.TrimSpace(string(out)), nil
	}

	return "", nil
}

// discoverCustomInfoKeys discovers all available custom info keys from the
// entries in the CustomInfoScriptDir directory. It returns a slice of keys.
func discoverKeysFromEntries(entries []fs.DirEntry) []string {
	keys := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		name := entry.Name()
		base := strings.TrimSuffix(name, filepath.Ext(name))

		if base != "" {
			keys = append(keys, base)
		}
	}

	return keys
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
		filePath := filepath.Join(dmiClassPath, fileName)
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
		filePath := filepath.Join(dmiClassPath, fileName)
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
