package systeminfo

import (
	"testing"

	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/stretchr/testify/require"
)

func TestDetectKVMEnabled(t *testing.T) {
	const intelModules = "kvm 1114112 1 kvm_intel, Live 0x0000000000000000\n" +
		"kvm_intel 380928 0 - Live 0x0000000000000000\n"
	const amdModules = "kvm 1114112 1 kvm_amd, Live 0x0000000000000000\n" +
		"kvm_amd 176128 0 - Live 0x0000000000000000\n"
	// baseOnlyModules mirrors a host where only the architecture-independent kvm
	// module is loaded with no vendor-specific kvm_intel/kvm_amd module. This is
	// the normal state on ARM, but on x86 it means KVM is not usable.
	const baseOnlyModules = "kvm 1077248 0 - Live 0x0000000000000000\n"
	const noKVMModules = "ext4 987136 1 - Live 0x0000000000000000\n"

	// Architectures under test. archAMD64 is defined in kvm.go; arm64 stands in
	// for any architecture without vendor-specific KVM modules.
	const arm64 = "arm64"

	testCases := []struct {
		name        string
		goarch      string
		procModules string
		sysModules  []string
		devKVM      bool
		expected    bool
	}{
		{
			name:        "When kvm_intel is loaded on amd64 and /dev/kvm exists it should be enabled",
			goarch:      archAMD64,
			procModules: intelModules,
			devKVM:      true,
			expected:    true,
		},
		{
			name:        "When kvm_amd is loaded on amd64 and /dev/kvm exists it should be enabled",
			goarch:      archAMD64,
			procModules: amdModules,
			devKVM:      true,
			expected:    true,
		},
		{
			name:        "When only the base kvm module is loaded on amd64 it should be disabled",
			goarch:      archAMD64,
			procModules: baseOnlyModules,
			devKVM:      true,
			expected:    false,
		},
		{
			name:        "When only the base kvm module is loaded on arm64 and /dev/kvm exists it should be enabled",
			goarch:      arm64,
			procModules: baseOnlyModules,
			devKVM:      true,
			expected:    true,
		},
		{
			name:       "When kvm_intel is only visible under /sys/module on amd64 it should be enabled",
			goarch:     archAMD64,
			sysModules: []string{"kvm_intel"},
			devKVM:     true,
			expected:   true,
		},
		{
			name:       "When only the base kvm module is visible under /sys/module on amd64 it should be disabled",
			goarch:     archAMD64,
			sysModules: []string{"kvm"},
			devKVM:     true,
			expected:   false,
		},
		{
			name:       "When only the base kvm module is visible under /sys/module on arm64 it should be enabled",
			goarch:     arm64,
			sysModules: []string{"kvm"},
			devKVM:     true,
			expected:   true,
		},
		{
			name:        "When only the base kvm module is loaded on arm64 but /dev/kvm is missing it should be disabled",
			goarch:      arm64,
			procModules: baseOnlyModules,
			devKVM:      false,
			expected:    false,
		},
		{
			name:        "When a KVM module is loaded on amd64 but /dev/kvm is missing it should be disabled",
			goarch:      archAMD64,
			procModules: intelModules,
			devKVM:      false,
			expected:    false,
		},
		{
			name:     "When /dev/kvm exists on amd64 but no module is loaded it should be disabled",
			goarch:   archAMD64,
			devKVM:   true,
			expected: false,
		},
		{
			name:        "When no KVM module is loaded on amd64 and /dev/kvm is missing it should be disabled",
			goarch:      archAMD64,
			procModules: noKVMModules,
			devKVM:      false,
			expected:    false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			tmpDir := t.TempDir()
			rw := fileio.NewReadWriter(
				fileio.NewReader(fileio.WithReaderRootDir(tmpDir)),
				fileio.NewWriter(fileio.WithWriterRootDir(tmpDir)),
			)

			if tc.procModules != "" {
				require.NoError(rw.MkdirAll("proc", fileio.DefaultDirectoryPermissions))
				require.NoError(rw.WriteFile(procModulesPath, []byte(tc.procModules), fileio.DefaultFilePermissions))
			}
			for _, mod := range tc.sysModules {
				require.NoError(rw.MkdirAll(sysModulePath+"/"+mod, fileio.DefaultDirectoryPermissions))
			}
			if tc.devKVM {
				require.NoError(rw.MkdirAll("dev", fileio.DefaultDirectoryPermissions))
				require.NoError(rw.WriteFile(devKVMPath, []byte{}, fileio.DefaultFilePermissions))
			}

			logger := log.NewPrefixLogger("test")
			require.Equal(tc.expected, detectKVMEnabled(logger, rw, tc.goarch))
		})
	}
}

func TestCollectKVMSystemInfo(t *testing.T) {
	require := require.New(t)
	tmpDir := t.TempDir()
	rw := fileio.NewReadWriter(
		fileio.NewReader(fileio.WithReaderRootDir(tmpDir)),
		fileio.NewWriter(fileio.WithWriterRootDir(tmpDir)),
	)
	logger := log.NewPrefixLogger("test")

	// No modules and no /dev/kvm: the field must still be reported as false.
	kvm := collectKVMSystemInfo(logger, rw)
	require.NotNil(kvm)
	require.NotNil(kvm.Enabled)
	require.False(*kvm.Enabled)
}
