package vm_test

import (
	"strconv"
	"testing"

	"github.com/flightctl/flightctl/test/harness/e2e"
	"github.com/stretchr/testify/require"
)

const (
	expectedVMAppTargetUnitName     = "test-vm-425604-flightctl-quadlet-app.target"
	expectedVMAppComputeServiceName = "test-vm-425604-virt-launcher-test-vm-compute.service"
)

// TestVMApplicationUnitsRunning verifies VM readiness requires exact target and compute unit states.
func TestVMApplicationUnitsRunning(t *testing.T) {
	targetUnitName := vmApplicationTargetUnitName(vmAppName)
	computeServiceName := vmApplicationComputeServiceName(vmAppName)
	targetUnit := e2e.SystemdUnitState{
		Unit:        targetUnitName,
		LoadState:   systemdLoadStateLoadedString,
		ActiveState: systemdActiveStateActive,
		SubState:    systemdSubStateActive,
	}
	computeUnit := e2e.SystemdUnitState{
		Unit:        computeServiceName,
		LoadState:   systemdLoadStateLoadedString,
		ActiveState: systemdActiveStateActive,
		SubState:    systemdSubStateRunning,
	}
	collisionComputeUnit := e2e.SystemdUnitState{
		Unit:        "other-" + computeServiceName,
		LoadState:   systemdLoadStateLoadedString,
		ActiveState: systemdActiveStateActive,
		SubState:    systemdSubStateRunning,
	}
	wrongStateComputeUnit := e2e.SystemdUnitState{
		Unit:        computeServiceName,
		LoadState:   systemdLoadStateLoadedString,
		ActiveState: systemdActiveStateActive,
		SubState:    systemdSubStateActive,
	}

	tests := []struct {
		name  string
		units []e2e.SystemdUnitState
		want  bool
	}{
		{
			name:  "running state",
			units: []e2e.SystemdUnitState{targetUnit, computeUnit},
			want:  true,
		},
		{
			name:  "target-only state",
			units: []e2e.SystemdUnitState{targetUnit},
			want:  false,
		},
		{
			name:  "compute-only state",
			units: []e2e.SystemdUnitState{computeUnit},
			want:  false,
		},
		{
			name:  "compute service collision",
			units: []e2e.SystemdUnitState{targetUnit, collisionComputeUnit},
			want:  false,
		},
		{
			name:  "compute service wrong state",
			units: []e2e.SystemdUnitState{targetUnit, wrongStateComputeUnit},
			want:  false,
		},
	}

	for _, tt := range tests {
		require.Equal(t, tt.want, vmApplicationUnitsRunning(tt.units, vmAppName), tt.name)
	}
}

// TestVMApplicationUnitPatternsUseProductionNames pins generated unit names used for VM diagnostics.
func TestVMApplicationUnitPatternsUseProductionNames(t *testing.T) {
	patterns := vmApplicationUnitPatterns(vmAppName)

	require.Len(t, patterns, 2)
	require.Equal(t, expectedVMAppTargetUnitName, patterns[0])
	require.Equal(t, expectedVMAppComputeServiceName, patterns[1])
}

func TestConfigDriveCloudUserDataWhenEnablingPasswordSSHItShouldResetFaillock(t *testing.T) {
	sshPublicKey := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAItest"
	password := t.Name()
	got := configDriveCloudUserData(sshPublicKey, password)

	require.Contains(t, got, "ssh_pwauth: true")
	require.Contains(t, got, "name: "+vmCloudUser)
	require.Contains(t, got, "password: "+strconv.Quote(password))
	require.Contains(t, got, strconv.Quote(sshPublicKey))
	require.Contains(t, got, "path: /etc/security/faillock.conf")
	require.Contains(t, got, "deny = 0")
	require.Contains(t, got, "authselect disable-feature with-faillock")
	require.Contains(t, got, "faillock --user "+vmCloudUser+" --reset")
	require.NotContains(t, got, "hello-http.service")
}

func TestConfigDriveCloudUserDataWithServicesWhenRequestedItShouldIncludeHTTPAndUDPUnits(t *testing.T) {
	got := configDriveCloudUserDataWithServices("ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAItest", t.Name())

	require.Contains(t, got, "path: /etc/security/faillock.conf")
	require.Contains(t, got, "hello-http.service")
	require.Contains(t, got, "hello-udp.service")
	require.Contains(t, got, "PORT = "+strconv.Itoa(vmBPublishedUDPPort))
	require.Contains(t, got, strconv.Quote(configDriveIndexHTMLContent))
	require.Contains(t, got, "systemctl enable --now hello-http.service")
}
