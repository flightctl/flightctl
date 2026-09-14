package systeminfo

import (
	"context"
	"fmt"
	"net"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/agent/device/status"
	"github.com/flightctl/flightctl/internal/agent/device/systeminfo/common"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
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
	// RegisterCollector registers a system info collector
	RegisterCollector(ctx context.Context, key string, fn CollectorFn)
	// RefreshRuntimeCollectors updates cached runtime collector values without running built-in collectors.
	RefreshRuntimeCollectors(ctx context.Context)
	// Run starts periodic system info collection. Blocks until ctx is cancelled.
	Run(ctx context.Context)
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

type collectCfg struct {
	collectAllCustom bool
	infoKeys         []string
}

func (cfg *collectCfg) addInfoKey(key string) {
	if !slices.Contains(cfg.infoKeys, key) {
		cfg.infoKeys = append(cfg.infoKeys, key)
	}
}

func (cfg *collectCfg) hasInfoKey(key string) bool {
	return slices.Contains(cfg.infoKeys, key)
}

type CollectOpt func(*collectCfg)

// WithAll runs all custom collectors and all flight control defined default collectors.
func WithAll() CollectOpt {
	return func(cfg *collectCfg) {
		cfg.collectAllCustom = true
		for _, key := range builtInInfoKeys() {
			cfg.addInfoKey(key)
		}
	}
}

// WithAllCustom enables all custom collection.
func WithAllCustom() CollectOpt {
	return func(cfg *collectCfg) {
		cfg.collectAllCustom = true
	}
}

func withInfoKey(key string) CollectOpt {
	return func(cfg *collectCfg) { cfg.addInfoKey(key) }
}

// collectionOptsFromInfoKeys returns CollectOpt functions based on the provided infoKeys. An error is returned
// containing any unknown keys that were supplied. All successful Opts that were constructed are always returned
func collectionOptsFromInfoKeys(infoKeys []string) ([]CollectOpt, error) {
	var opts []CollectOpt
	var errs []error

	for _, key := range infoKeys {
		if _, ok := collectorForInfoKey(key); ok {
			opts = append(opts, withInfoKey(key))
			continue
		}
		if !common.IsRuntimeKey(key) {
			errs = append(errs, fmt.Errorf("unknown key: %q", key))
		}
	}

	// Always return the opts that we successfully handled so that collection isn't fully blocked
	// by invalid keys. errors.Join returns nil if there are no errors
	return opts, errors.Join(errs...)
}

var (
	hostnameSource     = &sourceDefinition{collect: collectHostnameFunc}
	architectureSource = &sourceDefinition{collect: collectArchitectureFunc}
	cpuSource          = &sourceDefinition{collect: collectCPUFunc}
	gpuSource          = &sourceDefinition{collect: collectGPUFunc}
	memorySource       = &sourceDefinition{collect: collectMemoryFunc}
	networkSource      = &sourceDefinition{collect: collectNetworkFunc}
	biosSource         = &sourceDefinition{collect: collectBIOSFunc}
	systemSource       = &sourceDefinition{collect: collectSystemFunc}
	kernelSource       = &sourceDefinition{collect: collectKernelFunc}
	distributionSource = &sourceDefinition{collect: collectDistributionFunc}
)

// systemInfoKeyDefinitions is the single source of truth for built-in system
// information keys. Keys that use the same source are fetched together, but
// each key controls its own cached value and Info projection.
var systemInfoKeyDefinitions = map[string]collectorDefinition{
	common.HostnameKey: {
		source:  hostnameSource,
		extract: func(info *Info) string { return info.Hostname },
		projectInfo: func(destination, source *Info) {
			destination.Hostname = source.Hostname
		},
	},
	common.ArchitectureKey: {
		source:  architectureSource,
		extract: func(info *Info) string { return info.Architecture },
		projectInfo: func(destination, source *Info) {
			destination.Architecture = source.Architecture
			destination.OperatingSystem = source.OperatingSystem
		},
	},
	common.CPUCoresKey: {
		source: cpuSource,
		extract: func(info *Info) string {
			if info.Hardware.CPU == nil {
				return ""
			}
			return fmt.Sprintf("%d", info.Hardware.CPU.TotalCores)
		},
		projectInfo: func(destination, source *Info) {
			if source.Hardware.CPU != nil {
				ensure(&destination.Hardware.CPU).TotalCores = source.Hardware.CPU.TotalCores
			}
		},
	},
	common.CPUProcessorsKey: {
		source: cpuSource,
		extract: func(info *Info) string {
			if info.Hardware.CPU == nil {
				return ""
			}
			return fmt.Sprintf("%d", len(info.Hardware.CPU.Processors))
		},
		projectInfo: func(destination, source *Info) {
			if source.Hardware.CPU != nil {
				ensure(&destination.Hardware.CPU).Processors = slices.Clone(source.Hardware.CPU.Processors)
			}
		},
	},
	common.CPUModelKey: {
		source: cpuSource,
		extract: func(info *Info) string {
			if info.Hardware.CPU == nil || len(info.Hardware.CPU.Processors) == 0 {
				return ""
			}
			return info.Hardware.CPU.Processors[0].Model
		},
		projectInfo: func(destination, source *Info) {
			if source.Hardware.CPU != nil && len(source.Hardware.CPU.Processors) > 0 {
				cpu := ensure(&destination.Hardware.CPU)
				if len(cpu.Processors) == 0 {
					cpu.Processors = []ProcessorInfo{{}}
				}
				cpu.Processors[0].Model = source.Hardware.CPU.Processors[0].Model
			}
		},
	},
	common.GPUKey: {
		source: gpuSource,
		extract: func(info *Info) string {
			if len(info.Hardware.GPU) == 0 {
				return ""
			}
			parts := make([]string, 0, len(info.Hardware.GPU))
			for index, gpu := range info.Hardware.GPU {
				parts = append(parts, fmt.Sprintf("[%d] %s %s", index, gpu.Vendor, gpu.Model))
			}
			return strings.Join(parts, ".")
		},
		projectInfo: func(destination, source *Info) {
			destination.Hardware.GPU = slices.Clone(source.Hardware.GPU)
		},
	},
	common.MemoryTotalKbKey: {
		source: memorySource,
		extract: func(info *Info) string {
			if info.Hardware.Memory == nil || info.Hardware.Memory.TotalKB <= 0 {
				return ""
			}
			return fmt.Sprintf("%d", info.Hardware.Memory.TotalKB)
		},
		projectInfo: func(destination, source *Info) {
			if source.Hardware.Memory != nil {
				ensure(&destination.Hardware.Memory).TotalKB = source.Hardware.Memory.TotalKB
			}
		},
	},
	common.NetInterfaceDefaultKey: {
		source: networkSource,
		extract: func(info *Info) string {
			if info.Hardware.Network == nil || info.Hardware.Network.DefaultRoute == nil {
				return ""
			}
			return info.Hardware.Network.DefaultRoute.Interface
		},
		projectInfo: func(destination, source *Info) {
			projectDefaultRoute(destination, source, func(*NetworkInfo, *DefaultRoute, InterfaceInfo) {})
		},
	},
	common.NetIPDefaultKey: {
		source:  networkSource,
		extract: extractDefaultIP,
		projectInfo: func(destination, source *Info) {
			projectDefaultRoute(destination, source, func(network *NetworkInfo, _ *DefaultRoute, iface InterfaceInfo) {
				ensureNetworkInterface(network, iface.Name).IPAddresses = slices.Clone(iface.IPAddresses)
			})
		},
	},
	common.NetMACDefaultKey: {
		source:  networkSource,
		extract: extractDefaultMAC,
		projectInfo: func(destination, source *Info) {
			projectDefaultRoute(destination, source, func(network *NetworkInfo, _ *DefaultRoute, iface InterfaceInfo) {
				ensureNetworkInterface(network, iface.Name).MACAddress = iface.MACAddress
			})
		},
	},
	common.BIOSVendorKey: {
		source: biosSource,
		extract: func(info *Info) string {
			if info.Hardware.BIOS == nil {
				return ""
			}
			return info.Hardware.BIOS.Vendor
		},
		projectInfo: func(destination, source *Info) {
			if source.Hardware.BIOS != nil {
				ensure(&destination.Hardware.BIOS).Vendor = source.Hardware.BIOS.Vendor
			}
		},
	},
	common.BIOSVersionKey: {
		source: biosSource,
		extract: func(info *Info) string {
			if info.Hardware.BIOS == nil {
				return ""
			}
			return info.Hardware.BIOS.Version
		},
		projectInfo: func(destination, source *Info) {
			if source.Hardware.BIOS != nil {
				ensure(&destination.Hardware.BIOS).Version = source.Hardware.BIOS.Version
			}
		},
	},
	common.ProductNameKey: {
		source: systemSource,
		extract: func(info *Info) string {
			return systemInfoValue(info, func(system *SystemInfo) string { return system.ProductName })
		},
		projectInfo: func(destination, source *Info) {
			if source.Hardware.System != nil {
				ensure(&destination.Hardware.System).ProductName = source.Hardware.System.ProductName
			}
		},
	},
	common.ProductSerialKey: {
		source: systemSource,
		extract: func(info *Info) string {
			return systemInfoValue(info, func(system *SystemInfo) string { return system.SerialNumber })
		},
		projectInfo: func(destination, source *Info) {
			if source.Hardware.System != nil {
				ensure(&destination.Hardware.System).SerialNumber = source.Hardware.System.SerialNumber
			}
		},
	},
	common.ProductUUIDKey: {
		source: systemSource,
		extract: func(info *Info) string {
			return systemInfoValue(info, func(system *SystemInfo) string { return system.UUID })
		},
		projectInfo: func(destination, source *Info) {
			if source.Hardware.System != nil {
				ensure(&destination.Hardware.System).UUID = source.Hardware.System.UUID
			}
		},
	},
	common.KernelKey: {
		source:  kernelSource,
		extract: func(info *Info) string { return info.Kernel },
		projectInfo: func(destination, source *Info) {
			destination.Kernel = source.Kernel
		},
	},
	common.DistroNameKey:    distributionKeyDefinition("name"),
	common.DistroVersionKey: distributionKeyDefinition("version"),
	common.DistroIdKey:      distributionKeyDefinition("id"),
}

func collectorForInfoKey(key string) (collectorDefinition, bool) {
	definition, ok := systemInfoKeyDefinitions[key]
	return definition, ok
}

func builtInInfoKeys() []string {
	keys := make([]string, 0, len(systemInfoKeyDefinitions))
	for key := range systemInfoKeyDefinitions {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
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

	custom := customCollectionRequest{mode: customCollectionDisabled}
	if cfg.collectAllCustom {
		custom.mode = customCollectionDiscover
	} else if len(customKeys) > 0 {
		custom = customCollectionRequest{mode: customCollectionConfigured, keys: customKeys}
	}

	m := &manager{
		log: log,
		collection: buildCollectors(
			log,
			exec,
			reader,
			hardwareMapFilePath,
			collectionRequest{
				infoKeys: append([]string{common.HostnameKey, common.ArchitectureKey}, cfg.infoKeys...),
				custom:   custom,
			},
			nil,
			nil,
		),
		now: time.Now,
	}
	m.collectAndCache(ctx)
	return m.infoFromCache(), nil
}

func distributionKeyDefinition(field string) collectorDefinition {
	return collectorDefinition{
		source: distributionSource,
		extract: func(info *Info) string {
			if info.Distribution == nil {
				return ""
			}
			value, ok := info.Distribution[field]
			if !ok {
				return ""
			}
			if stringValue, ok := value.(string); ok {
				return stringValue
			}
			return fmt.Sprint(value)
		},
		projectInfo: func(destination, source *Info) {
			if value, ok := source.Distribution[field]; ok {
				if destination.Distribution == nil {
					destination.Distribution = make(map[string]interface{})
				}
				destination.Distribution[field] = value
			}
		},
	}
}

func systemInfoValue(info *Info, value func(*SystemInfo) string) string {
	if info.Hardware.System == nil {
		return ""
	}
	return value(info.Hardware.System)
}

func extractDefaultIP(info *Info) string {
	if info.Hardware.Network == nil || info.Hardware.Network.DefaultRoute == nil {
		return ""
	}
	for _, iface := range info.Hardware.Network.Interfaces {
		if iface.Name != info.Hardware.Network.DefaultRoute.Interface || len(iface.IPAddresses) == 0 {
			continue
		}
		for _, address := range iface.IPAddresses {
			ip := net.ParseIP(strings.Split(address, "/")[0])
			if ip != nil && !ip.IsLinkLocalUnicast() {
				return address
			}
		}
		return iface.IPAddresses[0]
	}
	return ""
}

func extractDefaultMAC(info *Info) string {
	if info.Hardware.Network == nil || info.Hardware.Network.DefaultRoute == nil {
		return ""
	}
	for _, iface := range info.Hardware.Network.Interfaces {
		if iface.Name == info.Hardware.Network.DefaultRoute.Interface {
			return iface.MACAddress
		}
	}
	return ""
}

func ensure[T any](value **T) *T {
	if *value == nil {
		*value = new(T)
	}
	return *value
}

func ensureNetworkInterface(network *NetworkInfo, name string) *InterfaceInfo {
	for index := range network.Interfaces {
		if network.Interfaces[index].Name == name {
			return &network.Interfaces[index]
		}
	}
	network.Interfaces = append(network.Interfaces, InterfaceInfo{Name: name})
	return &network.Interfaces[len(network.Interfaces)-1]
}

func projectDefaultRoute(destination, source *Info, projectInterface func(*NetworkInfo, *DefaultRoute, InterfaceInfo)) {
	if source.Hardware.Network == nil || source.Hardware.Network.DefaultRoute == nil {
		return
	}
	network := ensure(&destination.Hardware.Network)
	if network.DefaultRoute == nil {
		network.DefaultRoute = &DefaultRoute{}
	}
	route := network.DefaultRoute
	route.Interface = source.Hardware.Network.DefaultRoute.Interface
	for _, iface := range source.Hardware.Network.Interfaces {
		if iface.Name == route.Interface {
			projectInterface(network, route, iface)
			return
		}
	}
	projectInterface(network, route, InterfaceInfo{Name: route.Interface})
}
