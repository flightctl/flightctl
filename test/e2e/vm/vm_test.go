package vm_test

import (
	"fmt"
	"os"
	"strings"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/device/applications/lifecycle"
	"github.com/flightctl/flightctl/internal/quadlet"
	"github.com/flightctl/flightctl/test/harness/e2e"
	testutil "github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const (
	vmAppName                    = "test-vm"
	defaultVMImage               = "quay.io/containerdisks/fedora:40"
	systemdSubStateActive        = "active"
	systemdSubStateRunning       = "running"
	systemdLoadStateLoadedString = string(v1beta1.SystemdLoadStateLoaded)
	systemdActiveStateActive     = string(v1beta1.SystemdActiveStateActive)
)

func getVMImage() string {
	if image := os.Getenv("FLIGHTCTL_E2E_VM_IMAGE"); image != "" {
		return image
	}
	return defaultVMImage
}

var _ = Describe("VM Applications", Ordered, func() {
	var (
		deviceID  string
		harness   *e2e.Harness
		vmAppSpec v1beta1.ApplicationProviderSpec
	)

	BeforeAll(func() {
		var err error
		vmAppSpec, err = e2e.NewVmApplicationSpec(
			vmAppName,
			getVMImage(),
		)
		Expect(err).ToNot(HaveOccurred())
	})

	BeforeEach(func() {
		harness = e2e.GetWorkerHarness()
		deviceID, _ = harness.EnrollAndWaitForOnlineStatus()
	})

	It("deploys a VM application and reports Running status", Label("vm"), func() {
		By("Adding the VM application to the device")
		err := harness.UpdateDeviceAndWaitForVersion(deviceID, func(device *v1beta1.Device) {
			device.Spec.Applications = &[]v1beta1.ApplicationProviderSpec{vmAppSpec}
		})
		Expect(err).ToNot(HaveOccurred())

		By("Verifying the VM application reports Running status")
		err = harness.WaitForApplicationStatus(deviceID, vmAppName, v1beta1.ApplicationStatusRunning, testutil.LONG_TIMEOUT, testutil.POLLING)
		if err != nil {
			logVMApplicationUnitStatus(harness, vmAppName)
		}
		Expect(err).ToNot(HaveOccurred())

		By("Verifying the applications summary is Healthy")
		err = harness.WaitForApplicationSummary(deviceID, testutil.LONG_TIMEOUT, testutil.POLLING, v1beta1.ApplicationsSummaryStatusHealthy)
		if err != nil {
			logVMApplicationUnitStatus(harness, vmAppName)
		}
		Expect(err).ToNot(HaveOccurred())
	})
})

// logVMApplicationUnitStatus logs generated systemd unit state when product-level VM app status checks fail.
func logVMApplicationUnitStatus(h *e2e.Harness, appName string) {
	if appName == "" {
		GinkgoWriter.Println("VM application unit diagnostics skipped: app name is empty")
		return
	}
	units, err := vmApplicationUnitStatus(h, appName)
	if err != nil {
		GinkgoWriter.Printf("VM application %s unit diagnostics failed: %v\n", appName, err)
		return
	}
	if len(units) == 0 {
		GinkgoWriter.Printf("VM application %s unit diagnostics found no matching systemd units\n", appName)
		return
	}
	running, details := vmApplicationUnitsRunningStatus(units, appName)
	GinkgoWriter.Printf("VM application %s unit readiness=%t: %s\n", appName, running, details)
}

// vmApplicationUnitPatterns returns the generated systemd unit patterns for a VM app.
func vmApplicationUnitPatterns(appName string) []string {
	if appName == "" {
		return nil
	}
	return []string{
		vmApplicationTargetUnitName(appName),
		vmApplicationComputeServiceName(appName),
	}
}

// vmApplicationUnitStatus returns the live systemd units generated for a VM app.
func vmApplicationUnitStatus(h *e2e.Harness, appName string) ([]e2e.SystemdUnitState, error) {
	patterns := vmApplicationUnitPatterns(appName)
	if len(patterns) == 0 {
		return nil, fmt.Errorf("VM application unit patterns are empty for app %q", appName)
	}
	units, _, err := h.ListSystemdUnitsOnVM(patterns...)
	if err != nil {
		return nil, err
	}
	return units, nil
}

// vmApplicationUnitsRunning reports whether the VM app target is active and the compute service is running.
func vmApplicationUnitsRunning(units []e2e.SystemdUnitState, appName string) bool {
	running, _ := vmApplicationUnitsRunningStatus(units, appName)
	return running
}

// vmApplicationUnitsRunningStatus reports VM app readiness and explains missing or mismatched unit state.
func vmApplicationUnitsRunningStatus(units []e2e.SystemdUnitState, appName string) (bool, string) {
	targetRunning, targetDetails := vmApplicationUnitHasState(units, vmApplicationTargetUnitName(appName), systemdLoadStateLoadedString, systemdActiveStateActive, systemdSubStateActive)
	computeRunning, computeDetails := vmApplicationUnitHasState(units, vmApplicationComputeServiceName(appName), systemdLoadStateLoadedString, systemdActiveStateActive, systemdSubStateRunning)
	if targetRunning && computeRunning {
		return true, "target and compute service have required states"
	}
	return false, fmt.Sprintf("%s; %s; matching units: %s", targetDetails, computeDetails, vmApplicationFormatUnits(units))
}

// vmApplicationUnitHasState reports whether a named unit has the expected load, active, and sub states.
func vmApplicationUnitHasState(units []e2e.SystemdUnitState, unitName string, loadState string, activeState string, subState string) (bool, string) {
	for _, unit := range units {
		if unit.Unit != unitName {
			continue
		}
		if unit.LoadState == loadState &&
			unit.ActiveState == activeState &&
			unit.SubState == subState {
			return true, fmt.Sprintf("%s has required state", unitName)
		}
		return false, fmt.Sprintf("%s has load=%q active=%q sub=%q, want load=%q active=%q sub=%q", unitName, unit.LoadState, unit.ActiveState, unit.SubState, loadState, activeState, subState)
	}
	return false, fmt.Sprintf("%s is missing, want load=%q active=%q sub=%q", unitName, loadState, activeState, subState)
}

// vmApplicationFormatUnits returns compact diagnostic text for matching systemd units.
func vmApplicationFormatUnits(units []e2e.SystemdUnitState) string {
	if len(units) == 0 {
		return "none"
	}
	formatted := make([]string, 0, len(units))
	for _, unit := range units {
		formatted = append(formatted, fmt.Sprintf("%s(load=%s active=%s sub=%s)", unit.Unit, unit.LoadState, unit.ActiveState, unit.SubState))
	}
	return strings.Join(formatted, ", ")
}

// vmApplicationTargetUnitName returns the exact generated Flight Control target unit name for a VM app.
func vmApplicationTargetUnitName(appName string) string {
	return quadlet.NamespaceResource(vmApplicationID(appName), lifecycle.QuadletTargetName)
}

// vmApplicationComputeServiceName returns the generated virt-launcher compute service name for a VM app.
func vmApplicationComputeServiceName(appName string) string {
	return quadlet.NamespaceResource(vmApplicationID(appName), fmt.Sprintf("virt-launcher-%s-compute.service", appName))
}

// vmApplicationID returns the production app ID used to namespace generated VM units.
func vmApplicationID(appName string) string {
	return lifecycle.GenerateAppID(appName, v1beta1.CurrentProcessUsername)
}
