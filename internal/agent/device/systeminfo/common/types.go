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

// unionKeySets merges multiple KeySets into a single set.
func unionKeySets(sets ...KeySet) KeySet {
	out := make(KeySet)
	for _, s := range sets {
		for k := range s {
			out[k] = struct{}{}
		}
	}
	return out
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

// builtInKeys are collected unconditionally by built-in collectors.
var builtInKeys = newKeySet(
	ArchitectureKey,
	HostnameKey,
	KernelKey,
	CPUCoresKey,
	CPUProcessorsKey,
	CPUModelKey,
	GPUKey,
	MemoryTotalKbKey,
	NetInterfaceDefaultKey,
	NetIPDefaultKey,
	NetMACDefaultKey,
	BIOSVendorKey,
	BIOSVersionKey,
	ProductNameKey,
	ProductUUIDKey,
	ProductSerialKey,
	DistroNameKey,
	DistroVersionKey,
)

var allKeys = unionKeySets(builtInKeys, runtimeKeys)

// IsKnownKey reports whether the key is supported by the agent.
func IsKnownKey(key string) bool {
	return allKeys.Has(key)
}

// IsBuiltInKey reports whether the key corresponds to a built-in collector.
func IsBuiltInKey(key string) bool {
	return builtInKeys.Has(key)
}

// RuntimeKeys returns the list of runtime / conditional system-info keys.
func RuntimeKeys() []string {
	return runtimeKeys.Strings()
}

// BuiltInKeys returns the list of built-in system-info keys.
func BuiltInKeys() []string {
	return builtInKeys.Strings()
}
