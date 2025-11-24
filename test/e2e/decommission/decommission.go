package decommission_test

import (
	"fmt"

	"github.com/flightctl/flightctl/api/v1beta1"
	"github.com/flightctl/flightctl/test/harness/e2e"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const (
	TIMEOUT = "5m"
)

var _ = Describe("CLI decommission test", func() {

	Context("decommission", func() {

		It("should decommission a device via CLI", Label("decommission", "81782"), func() {
			// Get harness directly - no shared package-level variable
			harness := e2e.GetWorkerHarness()

			// Enroll device and get device ID
			deviceId, _ := harness.EnrollAndWaitForOnlineStatus()
			defer func() {

				_, err := harness.ManageResource("delete", fmt.Sprintf("device/%s", deviceId))
				Expect(err).NotTo(HaveOccurred())
				_, err = harness.ManageResource("delete", fmt.Sprintf("er/%s", deviceId))
				Expect(err).NotTo(HaveOccurred())
			}()

			GinkgoWriter.Printf("decommission device with id: %s\n", deviceId)

			out, err := harness.CLI("decommission", "devices/"+deviceId)
			Expect(err).NotTo(HaveOccurred())
			GinkgoWriter.Printf("%s\n", out)
			Expect(out).To(ContainSubstring("Device scheduled for decommissioning: 200 OK:"))
			harness.WaitForDeviceContents(deviceId, "The device has completed decommissioning and will wipe its management certificate",
				func(device *v1beta1.Device) bool {
					return e2e.ConditionExists(device, "DeviceDecommissioning", "True", string(v1beta1.DecommissionStateComplete))
				}, TIMEOUT)
		})
	})
})
