package agent_test

import (
	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/agent/device/status"
	"github.com/flightctl/flightctl/test/harness/e2e"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/sirupsen/logrus"
)

var _ = Describe("VM Agent behavior during updates", func() {
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

	Context("updates", func() {
		It("should update to the requested image", Label("updates", "rh-75523"), func() {

			device, newImageReference := harness.WaitForBootstrapAndUpdateToVersion(deviceId, ":v2")

			currentImage := device.Status.Os.Image
			logrus.Infof("Current image is: %s", currentImage)
			logrus.Infof("New image is: %s", newImageReference)

			harness.WaitForDeviceContents(deviceId, "the device is applying update renderedVersion: 2",
				func(device *v1alpha1.Device) bool {
					return conditionExists(device, "Updating", "True", string(status.UpdateStateApplyingUpdate))
				}, "1m")

			Expect(device.Status.Summary.Status).To(Equal(v1alpha1.DeviceSummaryStatusType("Online")))

			harness.WaitForDeviceContents(deviceId, "the device is rebooting",
				func(device *v1alpha1.Device) bool {
					return conditionExists(device, "Updating", "True", string(status.UpdateStateRebooting))
				}, "2m")

			Eventually(harness.GetDeviceWithStatusSummary, LONGTIMEOUT, POLLING).WithArguments(
				deviceId).Should(Equal(v1alpha1.DeviceSummaryStatusType("Rebooting")))

			harness.WaitForDeviceContents(deviceId, "status.Os.Image gets updated",
				func(device *v1alpha1.Device) bool {
					return device.Status.Os.Image == newImageReference &&
						conditionExists(device, "Updating", "False", string(status.UpdateStateUpdated))
				}, "2m")

			Eventually(harness.GetDeviceWithStatusSummary, LONGTIMEOUT, POLLING).WithArguments(
				deviceId).Should(Equal(v1alpha1.DeviceSummaryStatusType("Online")))

			// TODO(hexfusion): we were expecting this update status not to be unknown at this point
			// related to: https://issues.redhat.com/browse/EDM-679
			// Expect(device.Status.Updated.Status).ToNot(Equal(v1alpha1.DeviceUpdatedStatusType("Unknown")))
			logrus.Info("Device updated to new image 🎉")
		})

		It("Should update to v4 with embedded application", Label("updates", "rh-77667"), func() {

			device, newImageReference := harness.WaitForBootstrapAndUpdateToVersion(deviceId, ":v4")

			currentImage := device.Status.Os.Image
			logrus.Infof("Current image is: %s", currentImage)
			logrus.Infof("New image is: %s", newImageReference)

			harness.WaitForDeviceContents(deviceId, "the device is applying update renderedVersion: 2",
				func(device *v1alpha1.Device) bool {
					return conditionExists(device, "Updating", "True", string(status.UpdateStateApplyingUpdate))
				}, "1m")

			Expect(device.Status.Summary.Status).To(Equal(v1alpha1.DeviceSummaryStatusType("Online")))

			harness.WaitForDeviceContents(deviceId, "the device is rebooting",
				func(device *v1alpha1.Device) bool {
					return conditionExists(device, "Updating", "True", string(status.UpdateStateRebooting))
				}, "2m")

			Eventually(harness.GetDeviceWithStatusSummary, LONGTIMEOUT, POLLING).WithArguments(
				deviceId).Should(Equal(v1alpha1.DeviceSummaryStatusType("Rebooting")))

			harness.WaitForDeviceContents(deviceId, "status.Os.Image gets updated",
				func(device *v1alpha1.Device) bool {
					return device.Status.Os.Image == newImageReference &&
						conditionExists(device, "Updating", "False", string(status.UpdateStateUpdated))
				}, "2m")

			Eventually(harness.GetDeviceWithStatusSummary, LONGTIMEOUT, POLLING).WithArguments(
				deviceId).Should(Equal(v1alpha1.DeviceSummaryStatusType("Online")))

			// TODO(hexfusion): we were expecting this update status not to be unknown at this point
			// related to: https://issues.redhat.com/browse/EDM-679
			// Expect(device.Status.Updated.Status).ToNot(Equal(v1alpha1.DeviceUpdatedStatusType("Unknown")))

			logrus.Infof("Device updated to new image %s 🎉", "flightctl-device:v4")
			logrus.Info("We expect containers with sleep infinity process to be present but not running")
			stdout, err := harness.VM.RunSSH([]string{"sudo", "podman", "ps"}, nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(stdout.String()).To(ContainSubstring("sleep infinity"))

			logrus.Info("We expect podman containers with sleep infinity process to be present but not running 👌")

			device, newImageReference = harness.WaitForBootstrapAndUpdateToVersion(deviceId, ":base")

			currentImage = device.Status.Os.Image
			logrus.Infof("Current image is: %s", currentImage)
			logrus.Infof("New image is: %s", newImageReference)

			harness.WaitForDeviceContents(deviceId, "the device is applying update renderedVersion: 3",
				func(device *v1alpha1.Device) bool {
					return conditionExists(device, "Updating", "True", string(status.UpdateStateApplyingUpdate))
				}, "1m")

			Expect(device.Status.Summary.Status).To(Equal(v1alpha1.DeviceSummaryStatusType("Online")))

			harness.WaitForDeviceContents(deviceId, "the device is rebooting",
				func(device *v1alpha1.Device) bool {
					return conditionExists(device, "Updating", "True", string(status.UpdateStateRebooting))
				}, "2m")

			Eventually(harness.GetDeviceWithStatusSummary, LONGTIMEOUT, POLLING).WithArguments(
				deviceId).Should(Equal(v1alpha1.DeviceSummaryStatusType("Rebooting")))

			harness.WaitForDeviceContents(deviceId, "status.Os.Image gets updated",
				func(device *v1alpha1.Device) bool {
					return device.Status.Os.Image == newImageReference &&
						conditionExists(device, "Updating", "False", string(status.UpdateStateUpdated))
				}, "2m")

			Eventually(harness.GetDeviceWithStatusSummary, LONGTIMEOUT, POLLING).WithArguments(
				deviceId).Should(Equal(v1alpha1.DeviceSummaryStatusType("Online")))

			// TODO(hexfusion): we were expecting this update status not to be unknown at this point
			// related to: https://issues.redhat.com/browse/EDM-679
			// Expect(device.Status.Updated.Status).ToNot(Equal(v1alpha1.DeviceUpdatedStatusType("Unknown"))))
			logrus.Infof("Device updated to new image %s 🎉", "flightctl-device:base")
			Expect(device.Spec.Applications).To(BeNil())
			logrus.Info("Application demo_embedded_app is not present in new image 🌞")

			stdout1, err1 := harness.VM.RunSSH([]string{"sudo", "podman", "ps"}, nil)
			Expect(err1).NotTo(HaveOccurred())
			Expect(stdout1.String()).NotTo(ContainSubstring("sleep infinity"))

			logrus.Info("Went back to base image and checked that there is no application now👌")
		})
	})
})

// conditionExists checks if a specific condition exists for the device with the given type, status, and reason.
func conditionExists(device *v1alpha1.Device, conditionType, conditionStatus, conditionReason string) bool {
	for _, condition := range device.Status.Conditions {
		if string(condition.Type) == conditionType &&
			condition.Reason == conditionReason &&
			string(condition.Status) == conditionStatus {
			return true
		}
	}
	return false
}
