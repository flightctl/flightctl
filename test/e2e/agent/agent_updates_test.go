package agent_test

import (
	"strings"

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
		harness.Cleanup(true)
	})

	Context("updates", func() {
		It("should update to the requested image", Label("updates"), func() {

			// Check the device status right after bootstrap
			response := harness.GetDeviceWithStatusSystem(deviceId)
			device := response.JSON200
			Expect(device.Status.Summary.Status).To(Equal(v1alpha1.DeviceSummaryStatusType("Online")))
			Expect(*device.Status.Summary.Info).To(Equal("Bootstrap complete"))
			Expect(device.Status.Updated.Status).To(Equal(v1alpha1.DeviceUpdatedStatusType("Unknown")))

			var newImageReference string

			harness.UpdateDeviceWithRetries(deviceId, func(device *v1alpha1.Device) {
				currentImage := device.Status.Os.Image
				logrus.Infof("current image for %s is %s", deviceId, currentImage)
				repo, _ := parseImageReference(currentImage)
				newImageReference = repo + ":v2"
				device.Spec.Os = &v1alpha1.DeviceOSSpec{Image: newImageReference}
				logrus.Infof("updating %s to image %s", deviceId, device.Spec.Os.Image)
			})

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

// parseImageReference splits the given image string into its repository and tag components.
// The image string is expected to be in the format "repository[:port]/image:tag".
func parseImageReference(image string) (string, string) {
	// Split the image string by the colon to separate the repository and the tag.
	parts := strings.Split(image, ":")

	// The tag is the last part after the last colon.
	tag := parts[len(parts)-1]

	// The repository is composed of all parts before the last colon, joined back together with colons.
	repo := strings.Join(parts[:len(parts)-1], ":")

	return repo, tag
}
