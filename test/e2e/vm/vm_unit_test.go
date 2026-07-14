package vm_test

import (
	"testing"

	"github.com/flightctl/flightctl/test/harness/e2e"
	"github.com/stretchr/testify/require"
)

// TestVMApplicationUnitsRunning verifies VM readiness requires both target and compute units.
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
	}

	for _, tt := range tests {
		require.Equal(t, tt.want, vmApplicationUnitsRunning(tt.units, vmAppName), tt.name)
	}
}

// TestVMApplicationUnitPatternsUseExactTarget verifies systemd unit lookups use exact VM unit names.
func TestVMApplicationUnitPatternsUseExactTarget(t *testing.T) {
	patterns := vmApplicationUnitPatterns(vmAppName)

	require.Len(t, patterns, 2)
	require.Equal(t, vmApplicationTargetUnitName(vmAppName), patterns[0])
	require.Equal(t, vmApplicationComputeServiceName(vmAppName), patterns[1])
}
