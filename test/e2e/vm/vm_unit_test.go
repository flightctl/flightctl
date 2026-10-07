package vm_test

import (
	"testing"

	"github.com/flightctl/flightctl/test/harness/e2e"
	"github.com/stretchr/testify/require"
)

func TestParseLsblkDisks(t *testing.T) {
	tests := []struct {
		name string
		out  string
		want []string
	}{
		{
			name: "When paste-joined names it should keep each disk",
			out:  "vda,vdb,vdc,vdd",
			want: []string{"vda", "vdb", "vdc", "vdd"},
		},
		{
			name: "When names have surrounding space it should trim fields",
			out:  " vda , vdb ",
			want: []string{"vda", "vdb"},
		},
		{
			name: "When output is empty it should return no disks",
			out:  "",
			want: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, parseLsblkDisks(tt.out))
		})
	}
}

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
