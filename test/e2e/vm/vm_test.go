package vm_test

import (
	"os"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/test/harness/e2e"
	testutil "github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const (
	vmAppName      = "test-vm"
	defaultVMImage = "quay.io/containerdisks/fedora:40"
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

	It("deploys a VM application and reports Running status", Label("vm", "sanity"), func() {
		By("Adding the VM application to the device")
		err := harness.UpdateDeviceAndWaitForVersion(deviceID, func(device *v1beta1.Device) {
			device.Spec.Applications = &[]v1beta1.ApplicationProviderSpec{vmAppSpec}
		})
		Expect(err).ToNot(HaveOccurred())

		By("Verifying the VM application reaches Running status")
		err = harness.WaitForApplicationStatus(deviceID, vmAppName, v1beta1.ApplicationStatusRunning, testutil.LONG_TIMEOUT, testutil.POLLING)
		Expect(err).ToNot(HaveOccurred())

		By("Verifying the applications summary is Healthy")
		err = harness.WaitForApplicationSummary(deviceID, testutil.LONG_TIMEOUT, testutil.POLLING, v1beta1.ApplicationsSummaryStatusHealthy)
		Expect(err).ToNot(HaveOccurred())
	})
})
