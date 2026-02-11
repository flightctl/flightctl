package agent_test

import (
	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/test/harness/e2e"
	"github.com/flightctl/flightctl/test/util"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Agent Quadlet-based Applications", func() {
	var (
		deviceId string
		device   *v1beta1.Device
	)

	BeforeEach(func() {
		harness := e2e.GetWorkerHarness()
		deviceId, device = harness.EnrollAndWaitForOnlineStatus()

		// Make sure the device status right after bootstrap is Online
		response, err := harness.GetDeviceWithStatusSystem(deviceId)
		Expect(err).ToNot(HaveOccurred())
		Expect(response).ToNot(BeNil())
		device = response.JSON200
		Expect(device.Status.Summary.Status).To(Equal(v1beta1.DeviceSummaryStatusOnline))
	})

	Context("container apps", func() {
		It("Should install and run a simple container application", Label("sanity", "agent"), func() {
			harness := e2e.GetWorkerHarness()

			imageName := util.NewSleepAppImageReference(util.SleepAppTags.V1).String()

			err := harness.UpdateDeviceAndWait(deviceId, func(device *v1beta1.Device) {
				app := v1beta1.ContainerApplication{
					AppType: v1beta1.AppTypeContainer,
					Image:   imageName,
					RunAs:   "flightctl",
				}

				var appSpec v1beta1.ApplicationProviderSpec
				err := appSpec.FromContainerApplication(app)
				Expect(err).ToNot(HaveOccurred())

				device.Spec.Applications = &[]v1beta1.ApplicationProviderSpec{appSpec}
				GinkgoWriter.Printf("Updating %s with application %s\n", deviceId, imageName)
			})
			Expect(err).ToNot(HaveOccurred())

			By("Wait for the reported application running status in the device")
			harness.WaitForApplicationRunningStatus(deviceId, imageName)
		})
	})
})
