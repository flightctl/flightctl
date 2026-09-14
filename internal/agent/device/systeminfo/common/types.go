package common

import "sort"

const (
	// Info key constants for system information collection
	ArchitectureKey = "architecture"
	HostnameKey     = "hostname"
	KernelKey       = "kernel"

	// CPU info keys
	CPUCoresKey      = "cpuCores"
	CPUProcessorsKey = "cpuProcessors"
	CPUModelKey      = "cpuModel"

	// GPU info keys
	GPUKey = "gpu"

	// Memory info keys
	MemoryTotalKbKey = "memoryTotalKb"

	// Network info keys
	NetInterfaceDefaultKey = "netInterfaceDefault"
	NetIPDefaultKey        = "netIpDefault"
	NetMACDefaultKey       = "netMacDefault"

	// BIOS info keys
	BIOSVendorKey  = "biosVendor"
	BIOSVersionKey = "biosVersion"

	// System info keys
	ProductNameKey   = "productName"
	ProductUUIDKey   = "productUuid"
	ProductSerialKey = "productSerial"

	// Distribution info keys
	DistroNameKey    = "distroName"
	DistroVersionKey = "distroVersion"
	DistroIdKey      = "distroId"

	// Identity / security (runtime/conditional collectors)
	ManagementCertNotAfterKey = "managementCertNotAfter"
	ManagementCertSerialKey   = "managementCertSerial"
	TPMVendorInfoKey          = "tpmVendorInfo"
)

// KeySet represents a set of system-info keys.
type KeySet map[string]struct{}

// newKeySet creates a set from the provided system-info keys.
func newKeySet(keys ...string) KeySet {
	s := make(KeySet, len(keys))
	for _, k := range keys {
		s[k] = struct{}{}
	}
	return s
}

// Has reports whether the given system-info key exists in the set.
func (s KeySet) Has(k string) bool {
	_, ok := s[k]
	return ok
}

// Strings returns the keys as a sorted []string.
func (s KeySet) Strings() []string {
	out := make([]string, 0, len(s))
	for k := range s {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// runtimeKeys are populated by runtime or conditional collectors
// (e.g. management cert, TPM).
var runtimeKeys = newKeySet(
	ManagementCertNotAfterKey,
	ManagementCertSerialKey,
	TPMVendorInfoKey,
)

// IsRuntimeKey reports whether the key is supplied by a runtime collector.
func IsRuntimeKey(key string) bool {
	return runtimeKeys.Has(key)
}

// RuntimeKeys returns the list of runtime / conditional system-info keys.
func RuntimeKeys() []string {
	return runtimeKeys.Strings()
}
