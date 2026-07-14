package e2e

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const (
	systemdTestLoadedState     = "loaded"
	systemdTestActiveState     = "active"
	systemdTestRunningSubState = "running"
	systemdTestActiveSubState  = "active"
	systemdTestTargetUnit      = "flightctl-quadlet-app.target"
	systemdTestComputeUnit     = "virt-launcher-test-vm-compute.service"
	systemdTestSubstringOnly   = "launcher-test-vm-compute"
	systemdTestMissingUnit     = "missing.service"
	systemdTestHarnessUnit     = "unit.service"
	systemdTestListUnitsOutput = `test-vm-425604-flightctl-quadlet-app.target loaded active active test-vm target
test-vm-425604-virt-launcher-test-vm-compute.service loaded active running Container compute for pod virt-launcher-test-vm
malformed
`
	systemdTestHarnessNilError = "harness is nil"
	systemdTestVMNilError      = "harness VM is nil"
)

// TestParseSystemdListUnits verifies parsing and tightened unit-name matching for systemd list output.
func TestParseSystemdListUnits(t *testing.T) {
	units := ParseSystemdListUnits(systemdTestListUnitsOutput)

	require.Len(t, units, 2)
	require.True(t, SystemdUnitsContainState(units, systemdTestTargetUnit, systemdTestLoadedState, systemdTestActiveState, systemdTestActiveSubState))
	require.True(t, SystemdUnitsContainState(units, systemdTestComputeUnit, systemdTestLoadedState, systemdTestActiveState, systemdTestRunningSubState))
	require.False(t, SystemdUnitsContainState(units, systemdTestSubstringOnly, systemdTestLoadedState, systemdTestActiveState, systemdTestRunningSubState))
	require.False(t, SystemdUnitsContainState(units, systemdTestMissingUnit, systemdTestLoadedState, systemdTestActiveState, systemdTestRunningSubState))
}

// TestListSystemdUnitsOnVMWithTimeoutValidatesHarness verifies harness validation happens before VM probing.
func TestListSystemdUnitsOnVMWithTimeoutValidatesHarness(t *testing.T) {
	var nilHarness *Harness
	_, _, err := nilHarness.ListSystemdUnitsOnVMWithTimeout(time.Second, systemdTestHarnessUnit)
	require.ErrorContains(t, err, systemdTestHarnessNilError)

	_, _, err = (&Harness{}).ListSystemdUnitsOnVMWithTimeout(time.Second, systemdTestHarnessUnit)
	require.ErrorContains(t, err, systemdTestVMNilError)
}
