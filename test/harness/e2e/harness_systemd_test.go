package e2e

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const (
	systemdTestTargetUnitName  = "test-vm-425604-flightctl-quadlet-app.target"
	systemdTestComputeUnitName = "test-vm-425604-virt-launcher-test-vm-compute.service"
	systemdTestLoadedState     = "loaded"
	systemdTestActiveState     = "active"
	systemdTestRunningSubState = "running"
	systemdTestActiveSubState  = "active"
	systemdTestHarnessUnit     = "unit.service"
	systemdTestListUnitsOutput = `test-vm-425604-flightctl-quadlet-app.target loaded active active test-vm target
test-vm-425604-virt-launcher-test-vm-compute.service loaded active running Container compute for pod virt-launcher-test-vm
malformed
`
	systemdTestHarnessNilError = "harness is nil"
	systemdTestVMNilError      = "harness VM is nil"
)

// TestParseSystemdListUnits verifies parsing stable fields from systemd list output.
func TestParseSystemdListUnits(t *testing.T) {
	units := ParseSystemdListUnits(systemdTestListUnitsOutput)

	require.Len(t, units, 2)
	require.Equal(t, systemdTestTargetUnitName, units[0].Unit)
	require.Equal(t, systemdTestLoadedState, units[0].LoadState)
	require.Equal(t, systemdTestActiveState, units[0].ActiveState)
	require.Equal(t, systemdTestActiveSubState, units[0].SubState)
	require.Equal(t, systemdTestComputeUnitName, units[1].Unit)
	require.Equal(t, systemdTestLoadedState, units[1].LoadState)
	require.Equal(t, systemdTestActiveState, units[1].ActiveState)
	require.Equal(t, systemdTestRunningSubState, units[1].SubState)
}

// TestListSystemdUnitsOnVMWithTimeoutValidatesHarness verifies harness validation happens before VM probing.
func TestListSystemdUnitsOnVMWithTimeoutValidatesHarness(t *testing.T) {
	var nilHarness *Harness
	_, _, err := nilHarness.ListSystemdUnitsOnVMWithTimeout(time.Second, systemdTestHarnessUnit)
	require.ErrorContains(t, err, systemdTestHarnessNilError)

	_, _, err = (&Harness{}).ListSystemdUnitsOnVMWithTimeout(time.Second, systemdTestHarnessUnit)
	require.ErrorContains(t, err, systemdTestVMNilError)
}
