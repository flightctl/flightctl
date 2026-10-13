package systeminfo

import (
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/samber/lo"
)

const (
	procModulesPath = "/proc/modules"
	sysModulePath   = "/sys/module"
	devKVMPath      = "/dev/kvm"

	// archAMD64 is the runtime.GOARCH value for x86-64 hosts, where a
	// vendor-specific KVM module (kvm_intel or kvm_amd) is required in addition
	// to the base "kvm" module for hardware virtualization to be usable.
	archAMD64 = "amd64"
)

// kvmModules are the KVM kernel modules the agent scans for in /proc/modules and
// /sys/module. "kvm" is the base module and is loaded on every architecture that
// supports KVM, while "kvm_intel" and "kvm_amd" are the x86 (amd64)
// vendor-specific modules. The base module alone is not sufficient to indicate
// usable virtualization on x86 hosts: it is loaded even when neither vendor
// module can be, so full KVM support on amd64 requires kvm_intel or kvm_amd. On
// architectures without vendor modules (for example arm64) the base "kvm" module
// is sufficient. See requiredKVMModules for the per-architecture rules applied
// after scanning.
var kvmModules = []string{"kvm", "kvm_intel", "kvm_amd"}

// requiredKVMModules returns the subset of kvmModules whose presence indicates
// usable KVM support for the given architecture. On amd64 a vendor-specific
// module (kvm_intel or kvm_amd) must be loaded, because the base "kvm" module
// alone does not indicate full hardware virtualization support. On all other
// architectures (for example arm64) the base "kvm" module is sufficient.
func requiredKVMModules(goarch string) []string {
	if goarch == archAMD64 {
		return []string{"kvm_intel", "kvm_amd"}
	}
	return []string{"kvm"}
}

// collectKVMSystemInfo returns whether KVM virtualization is enabled on the
// device. The value is always reported so KVM availability is never omitted.
func collectKVMSystemInfo(log *log.PrefixLogger, reader fileio.Reader) *v1beta1.DeviceKvm {
	enabled := detectKVMEnabled(log, reader, runtime.GOARCH)
	return &v1beta1.DeviceKvm{Enabled: lo.ToPtr(enabled)}
}

// detectKVMEnabled reports whether KVM virtualization is available for the given
// architecture. It requires both a loaded architecture-appropriate KVM kernel
// module and an accessible /dev/kvm device node. Detection is best effort and
// defaults to false when signals are missing.
func detectKVMEnabled(log *log.PrefixLogger, reader fileio.Reader, goarch string) bool {
	if !kvmModuleLoaded(log, reader, goarch) {
		return false
	}
	// WithSkipContentCheck verifies the node exists and can be opened without
	// attempting to read from it, which is the correct accessibility check for
	// the /dev/kvm character device.
	accessible, err := reader.PathExists(devKVMPath, fileio.WithSkipContentCheck())
	if err != nil {
		log.Debugf("Could not access %s: %v", devKVMPath, err)
		return false
	}
	return accessible
}

// kvmModuleLoaded reports whether a KVM kernel module appropriate for the given
// architecture is loaded. On amd64 this requires a vendor-specific module
// (kvm_intel or kvm_amd); on other architectures the base "kvm" module is
// sufficient.
func kvmModuleLoaded(log *log.PrefixLogger, reader fileio.Reader, goarch string) bool {
	loaded := loadedKVMModules(log, reader)
	for _, mod := range requiredKVMModules(goarch) {
		if loaded[mod] {
			return true
		}
	}
	return false
}

// loadedKVMModules returns the set of modules from kvmModules that are currently
// loaded. It prefers /proc/modules and falls back to /sys/module/<name> so it
// works whether a module is loadable or built into the kernel.
func loadedKVMModules(log *log.PrefixLogger, reader fileio.Reader) map[string]bool {
	loaded := make(map[string]bool, len(kvmModules))
	if data, err := reader.ReadFile(procModulesPath); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			// each line begins with the module name followed by a space
			name, _, found := strings.Cut(line, " ")
			if found && slices.Contains(kvmModules, name) {
				loaded[name] = true
			}
		}
	} else {
		log.Debugf("Could not read %s: %v", procModulesPath, err)
	}

	for _, mod := range kvmModules {
		if loaded[mod] {
			continue
		}
		exists, err := reader.PathExists(filepath.Join(sysModulePath, mod), fileio.WithSkipContentCheck())
		if err != nil {
			log.Debugf("Could not check module path for %s: %v", mod, err)
			continue
		}
		if exists {
			loaded[mod] = true
		}
	}
	return loaded
}
