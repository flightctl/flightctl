package decommission_test

import (
	"testing"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/test/harness/e2e"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/sirupsen/logrus"
)

const TIMEOUT = "1m"
const POLLING = "250ms"

func TestDecommission(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Decommission E2E Suite")
}

var _ = Describe("CLI decommission test", func() {
	var (
		harness  *e2e.Harness
		deviceId string
	)

	BeforeEach(func() {
		harness = e2e.NewTestHarness()
		deviceId = harness.StartVMAndEnroll()
	})

	AfterEach(func() {
		harness.Cleanup(false)
	})

	Context("decommission", func() {
		It("should decommission a device via CLI", Label("decommission", "rh-799"), func() {
			logrus.Infof("decommission device with id: %s", deviceId)

			out, err := harness.CLI("decommission", "devices/"+deviceId)
			Expect(err).NotTo(HaveOccurred())
			logrus.Info(out)
			Expect(out).To(ContainSubstring("Device scheduled for decommissioning: 200 OK:"))
			harness.WaitForDeviceContents(deviceId, "The device has completed decommissioning and will wipe its management certificate",
				func(device *v1alpha1.Device) bool {
					return harness.ConditionExists(device, "DeviceDecommissioning", "True", string(v1alpha1.DecommissionStateComplete))
				}, "2m")
		})
	})
})
