package systeminfo

import (
	"context"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"regexp"
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

	// Info key constants for system information collection
	hostnameKey     = "hostname"
	architectureKey = "architecture"
	kernelKey       = "kernel"

	// CPU info keys
	cpuCoresKey      = "cpuCores"
	cpuProcessorsKey = "cpuProcessors"
	cpuModelKey      = "cpuModel"

	// GPU info keys
	gpuKey = "gpu"

	// Memory info keys
	memoryTotalKbKey = "memoryTotalKb"

	// Network info keys
	netInterfaceDefaultKey = "netInterfaceDefault"
	netIPDefaultKey        = "netIpDefault"
	netMACDefaultKey       = "netMacDefault"

	// BIOS info keys
	biosVendorKey  = "biosVendor"
	biosVersionKey = "biosVersion"

	// System info keys
	productNameKey   = "productName"
	productSerialKey = "productSerial"
	productUUIDKey   = "productUuid"

	// Distribution info keys
	distroNameKey    = "distroName"
	distroVersionKey = "distroVersion"
)

type Manager interface {
	// IsRebooted checks if the system has been rebooted since the last time the agent started
	IsRebooted() bool
	// BootID returns the unique boot ID populated by the kernel
	BootID() string
	// BootTime returns the time the system was booted
	BootTime() string
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

type infoMap map[string]string

type Boot struct {
	// Time is the time the system was booted.
	Time string `json:"bootTime,omitempty"`
	// ID is the unique boot ID populated by the kernel.
	ID string `json:"bootID,omitempty"`
}

func (b *Boot) IsEmpty() bool {
	return b.Time == "" && b.ID == ""
}

type collectContext struct {
	log                 *log.PrefixLogger
	exec                executer.Executer
	reader              fileio.Reader
	hardwareMapFilePath string
}

type collectorFunc func(ctx context.Context, collectCtx *collectContext, info *Info) error

type collectorType int

const (
	collectorCPU collectorType = iota
	collectorGPU
	collectorMemory
	collectorNetwork
	collectorBIOS
	collectorSystem
	collectorKernel
	collectorDistribution
	collectorBoot
)

type collectCfg struct {
	collectAllCustom bool
	collectorFuncs   []collectorFunc
	enabledTypes     map[collectorType]bool
}

// addCollector adds a collector function if not already present
func (cfg *collectCfg) addCollector(cType collectorType, fn collectorFunc) {
	if cfg.enabledTypes == nil {
		cfg.enabledTypes = make(map[collectorType]bool)
	}
	if !cfg.enabledTypes[cType] {
		cfg.enabledTypes[cType] = true
		cfg.collectorFuncs = append(cfg.collectorFuncs, fn)
	}
}

// hasCollector checks if a specific collector type is enabled
func (cfg *collectCfg) hasCollector(cType collectorType) bool {
	return cfg.enabledTypes[cType]
}

func collectCPUFunc(ctx context.Context, collectCtx *collectContext, info *Info) error {
	cpuInfo, err := collectCPUInfo(collectCtx.reader)
	if err != nil {
		return fmt.Errorf("CPU collector failed: %w", err)
	}
	info.Hardware.CPU = cpuInfo
	return nil
}

func collectGPUFunc(ctx context.Context, collectCtx *collectContext, info *Info) error {
	gpuInfo, err := collectGPUInfo(collectCtx.log, collectCtx.reader, collectCtx.hardwareMapFilePath)
	if err != nil {
		return fmt.Errorf("GPU collector failed: %w", err)
	}
	info.Hardware.GPU = gpuInfo
	return nil
}

func collectMemoryFunc(ctx context.Context, collectCtx *collectContext, info *Info) error {
	memInfo, err := collectMemoryInfo(collectCtx.log, collectCtx.reader)
	if err != nil {
		return fmt.Errorf("memory collector failed: %w", err)
	}
	info.Hardware.Memory = memInfo
	return nil
}

func collectNetworkFunc(ctx context.Context, collectCtx *collectContext, info *Info) error {
	netInfo, err := collectNetworkInfo(ctx, collectCtx.log, collectCtx.exec, collectCtx.reader)
	if err != nil {
		return fmt.Errorf("network collector failed: %w", err)
	}
	info.Hardware.Network = netInfo
	return nil
}

func collectBIOSFunc(ctx context.Context, collectCtx *collectContext, info *Info) error {
	biosInfo, err := collectBIOSInfo(collectCtx.reader)
	if err != nil {
		return fmt.Errorf("BIOS collector failed: %w", err)
	}
	info.Hardware.BIOS = biosInfo
	return nil
}

func collectSystemFunc(ctx context.Context, collectCtx *collectContext, info *Info) error {
	sysInfo, err := collectSystemInfo(collectCtx.reader)
	if err != nil {
		return fmt.Errorf("system collector failed: %w", err)
	}
	info.Hardware.System = sysInfo
	return nil
}

func collectKernelFunc(ctx context.Context, collectCtx *collectContext, info *Info) error {
	out, err := collectCtx.exec.CommandContext(ctx, "uname", "-r").Output()
	if err != nil {
		return fmt.Errorf("kernel collector failed: %w", err)
	}
	info.Kernel = strings.TrimSpace(string(out))
	return nil
}

func collectDistributionFunc(ctx context.Context, collectCtx *collectContext, info *Info) error {
	distroInfo, err := collectDistributionInfo(ctx, collectCtx.reader)
	if err != nil {
		return fmt.Errorf("distribution collector failed: %w", err)
	}
	info.Distribution = distroInfo
	return nil
}

func collectBootFunc(ctx context.Context, collectCtx *collectContext, info *Info) error {
	bootID, err := getBootID(collectCtx.reader)
	if err != nil {
		return fmt.Errorf("boot collector failed to get boot ID: %w", err)
	}
	info.Boot.ID = bootID

	bootTime, err := getBootTime(ctx, collectCtx.exec)
	if err != nil {
		return fmt.Errorf("boot collector failed to get boot time: %w", err)
	}
	info.Boot.Time = bootTime
	return nil
}

type CollectOpt func(*collectCfg)

// WithAll runs all custom collectors and all flight control defined default collectors.
func WithAll() CollectOpt {
	return func(cfg *collectCfg) {
		cfg.collectAllCustom = true
		cfg.addCollector(collectorCPU, collectCPUFunc)
		cfg.addCollector(collectorGPU, collectGPUFunc)
		cfg.addCollector(collectorMemory, collectMemoryFunc)
		cfg.addCollector(collectorNetwork, collectNetworkFunc)
		cfg.addCollector(collectorBIOS, collectBIOSFunc)
		cfg.addCollector(collectorSystem, collectSystemFunc)
		cfg.addCollector(collectorKernel, collectKernelFunc)
		cfg.addCollector(collectorDistribution, collectDistributionFunc)
	}
}

// WithAllCustom enables all custom collection.
func WithAllCustom() CollectOpt {
	return func(cfg *collectCfg) {
		cfg.collectAllCustom = true
	}
}

// withCollector creates a CollectOpt that adds a specific collector
func withCollector(cType collectorType, fn collectorFunc) CollectOpt {
	return func(cfg *collectCfg) { cfg.addCollector(cType, fn) }
}

// collectionOptsFromInfoKeys returns CollectOpt functions based on the provided infoKeys. An error is returned
// containing any unknown keys that were supplied. All successful Opts that were constructed are always returned
func collectionOptsFromInfoKeys(infoKeys []string) ([]CollectOpt, error) {
	var opts []CollectOpt
	var errs []error

	for _, key := range infoKeys {
		switch key {
		case cpuCoresKey, cpuProcessorsKey, cpuModelKey:
			opts = append(opts, withCollector(collectorCPU, collectCPUFunc))
		case gpuKey:
			opts = append(opts, withCollector(collectorGPU, collectGPUFunc))
		case memoryTotalKbKey:
			opts = append(opts, withCollector(collectorMemory, collectMemoryFunc))
		case netInterfaceDefaultKey, netIPDefaultKey, netMACDefaultKey:
			opts = append(opts, withCollector(collectorNetwork, collectNetworkFunc))
		case biosVendorKey, biosVersionKey:
			opts = append(opts, withCollector(collectorBIOS, collectBIOSFunc))
		case productNameKey, productSerialKey, productUUIDKey:
			opts = append(opts, withCollector(collectorSystem, collectSystemFunc))
		case kernelKey:
			opts = append(opts, withCollector(collectorKernel, collectKernelFunc))
		case distroNameKey, distroVersionKey:
			opts = append(opts, withCollector(collectorDistribution, collectDistributionFunc))
		case hostnameKey, architectureKey:
			// No specific collector needed - hostname and architecture are always collected
		default:
			errs = append(errs, fmt.Errorf("unknown key: %q", key))
		}
	}

	// Always return the opts that we successfully handled so that collection isn't fully blocked
	// by invalid keys. errors.Join returns nil if there are no errors
	return opts, errors.Join(errs...)
}

// Collect collects system information and returns it as a map of key-value pairs.
func Collect(ctx context.Context, log *log.PrefixLogger, exec executer.Executer, reader fileio.Reader, customKeys []string, hardwareMapFilePath string, opts ...CollectOpt) (*Info, error) {
	now := time.Now()
	log.Debugf("Collecting system information...")
	defer func() {
		log.Debugf("System information collection took %s", time.Since(now))
	}()

	cfg := &collectCfg{}
	for _, opt := range opts {
		opt(cfg)
	}

	info := &Info{
		CollectedAt: time.Now().Format(time.RFC3339),
		Hardware:    HardwareFacts{},
	}

	if err := ctx.Err(); err != nil {
		log.Warningf("Context canceled before collection started: %v", err)
		return nil, err
	}

	// Always collect basic system info (hostname, OS, arch)
	var err error
	info.Hostname, err = os.Hostname()
	if err != nil {
		log.Warningf("Failed to get hostname: %v", err)
	}
	info.OperatingSystem = runtime.GOOS
	info.Architecture = runtime.GOARCH

	collectCtx := &collectContext{
		log:                 log,
		exec:                exec,
		reader:              reader,
		hardwareMapFilePath: hardwareMapFilePath,
	}

	// always collect boot info (needed for reboot detection)
	cfg.addCollector(collectorBoot, collectBootFunc)

	// collector version
	info.Metadata = map[string]interface{}{
		"collector_version": version.Get().String(),
		"collector_type":    "flightctl-agent",
	}

	for _, collectorFn := range cfg.collectorFuncs {
		if ctx.Err() != nil {
			log.Warningf("Context canceled during collection: %v", ctx.Err())
			return info, ctx.Err()
		}

		if err = collectorFn(ctx, collectCtx, info); err != nil {
			log.Warningf("Collector failed: %v", err)

			// If it's a context error, allow early exit
			if errors.IsContext(err) {
				return info, err
			}
			// Otherwise continue with other collectors (best effort)
		}
	}

	// custom info
	if len(customKeys) > 0 || cfg.collectAllCustom {
		customInfo, err := getCustomInfoMap(ctx, log, customKeys, reader, exec, opts...)
		if err != nil {
			if errors.IsContext(err) {
				log.Warningf("Context canceled during custom info collection: %v", err)
				return info, err
			}
			log.Warningf("Failed to get custom info: %v", err)
		} else {
			info.Custom = customInfo
		}
	} else {
		log.Debugf("No custom info keys provided, skipping custom info collection")
	}

	return info, nil
}

// SupportedInfoKeys is a map of supported info keys to their corresponding functions.
var SupportedInfoKeys = map[string]func(info *Info) string{
	hostnameKey:     func(i *Info) string { return i.Hostname },
	architectureKey: func(i *Info) string { return i.Architecture },
	kernelKey:       func(i *Info) string { return i.Kernel },
	distroNameKey: func(i *Info) string {
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
	distroVersionKey: func(i *Info) string {
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
	productNameKey: func(i *Info) string {
		if i.Hardware.System != nil {
			return i.Hardware.System.ProductName
		}
		return ""
	},
	productSerialKey: func(i *Info) string {
		if i.Hardware.System != nil {
			return i.Hardware.System.SerialNumber
		}
		return ""
	},
	productUUIDKey: func(i *Info) string {
		if i.Hardware.System != nil {
			return i.Hardware.System.UUID
		}
		return ""
	},
	biosVendorKey: func(i *Info) string {
		if i.Hardware.BIOS != nil {
			return i.Hardware.BIOS.Vendor
		}
		return ""
	},
	biosVersionKey: func(i *Info) string {
		if i.Hardware.BIOS != nil {
			return i.Hardware.BIOS.Version
		}
		return ""
	},
	netInterfaceDefaultKey: func(i *Info) string {
		if i.Hardware.Network != nil && i.Hardware.Network.DefaultRoute != nil {
			return i.Hardware.Network.DefaultRoute.Interface
		}
		return ""
	},
	netIPDefaultKey: func(i *Info) string {
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
	netMACDefaultKey: func(i *Info) string {
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
	gpuKey: func(i *Info) string {
		if len(i.Hardware.GPU) == 0 {
			return ""
		}

		var parts []string
		for idx, gpu := range i.Hardware.GPU {
			parts = append(parts, fmt.Sprintf("[%d] %s %s", idx, gpu.Vendor, gpu.Model))
		}
		return strings.Join(parts, ".")
	},
	memoryTotalKbKey: func(i *Info) string {
		if i.Hardware.Memory == nil || i.Hardware.Memory.TotalKB <= 0 {
			return ""
		}
		return fmt.Sprintf("%d", i.Hardware.Memory.TotalKB)
	},
	cpuCoresKey: func(i *Info) string {
		if i.Hardware.CPU != nil {
			return fmt.Sprintf("%d", i.Hardware.CPU.TotalCores)
		}
		return ""
	},
	cpuProcessorsKey: func(i *Info) string {
		if i.Hardware.CPU != nil {
			return fmt.Sprintf("%d", len(i.Hardware.CPU.Processors))
		}
		return ""
	},
	cpuModelKey: func(i *Info) string {
		if i.Hardware.CPU != nil && len(i.Hardware.CPU.Processors) > 0 {
			return i.Hardware.CPU.Processors[0].Model
		}
		return ""
	},
}

// getSystemInfoMap collects system information from the system It executes
// system commands and reads files to gather information about the system.
// It returns a map of key-value pairs representing the system information.
func getSystemInfoMap(ctx context.Context, log *log.PrefixLogger, info *Info, infoKeys []string, collectors map[string]CollectorFn) infoMap {
	infoMap := make(infoMap, len(infoKeys)+len(collectors))

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

	for key, collectorfn := range collectors {
		_, alreadyExists := infoMap[key]
		if alreadyExists {
			log.Warnf("SystemInfo collector already populated: %s is %s", key, infoMap[key])
		} else {
			val := collectorfn(ctx)
			trimmed := strings.TrimSpace(val)
			reg, _ := regexp.Compile("[^a-zA-Z0-9]+")
			sanitizedval := reg.ReplaceAllString(trimmed, "")
			infoMap[key] = sanitizedval
		}
	}

	return infoMap
}

// getCustomInfoMap collects custom information from the system It executes
// custom scripts located in the CustomInfoScriptDir directory and returns the
// output as a map of key-value pairs.
func getCustomInfoMap(ctx context.Context, log *log.PrefixLogger, keys []string, reader fileio.Reader, exec executer.Executer, opts ...CollectOpt) (map[string]string, error) {
	cfg := &collectCfg{}
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
			if errors.IsContext(err) {
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
func collectDistributionInfo(ctx context.Context, reader fileio.Reader) (map[string]interface{}, error) {
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
