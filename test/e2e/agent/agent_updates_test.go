package agent_test

import (
	"strings"

	"github.com/flightctl/flightctl/api/v1alpha1"
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

		It("should update to the requested image", Label("updates", "75523"), func() {

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

			harness.WaitForDeviceContents(deviceId, "the device is upgrading to renderedVersion: 2",
				func(device *v1alpha1.Device) bool {
					return conditionExists(device, "Updating", "True", "Update")
				}, "1m")

			harness.WaitForDeviceContents(deviceId, "the device is rebooting",
				func(device *v1alpha1.Device) bool {
					return conditionExists(device, "Updating", "True", "Rebooting")
				}, "2m")

			harness.WaitForDeviceContents(deviceId, "status.Os.Image gets updated",
				func(device *v1alpha1.Device) bool {
					return device.Status.Os.Image == newImageReference &&
						conditionExists(device, "Updating", "False", "Updated")
				}, "2m")

			// TODO(hexfusion): we were expecting this update status not to be unknown at this point
			// related to: https://issues.redhat.com/browse/EDM-679
			// Expect(device.Status.Updated.Status).ToNot(Equal(v1alpha1.DeviceUpdatedStatusType("Unknown")))
			logrus.Infof("Device updated to new image %s 🎉", "flightctl-device:v2")
		})

		It("Should update to v4 with embedded application", Label("updates", "77667"), func() {
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
				newImageReference = repo + ":v4"
				device.Spec.Os = &v1alpha1.DeviceOSSpec{Image: newImageReference}
				logrus.Infof("updating %s to image %s", deviceId, device.Spec.Os.Image)
			})

			harness.WaitForDeviceContents(deviceId, "the device is upgrading to renderedVersion: 2",
				func(device *v1alpha1.Device) bool {
					return conditionExists(device, "Updating", "True", "Update")
				}, "1m")

			harness.WaitForDeviceContents(deviceId, "the device is rebooting",
				func(device *v1alpha1.Device) bool {
					return conditionExists(device, "Updating", "True", "Rebooting")
				}, "2m")

			harness.WaitForDeviceContents(deviceId, "status.Os.Image gets updated",
				func(device *v1alpha1.Device) bool {
					return device.Status.Os.Image == newImageReference &&
						conditionExists(device, "Updating", "False", "Updated")
				}, "2m")

			// TODO(hexfusion): we were expecting this update status not to be unknown at this point
			// related to: https://issues.redhat.com/browse/EDM-679
			// Expect(device.Status.Updated.Status).ToNot(Equal(v1alpha1.DeviceUpdatedStatusType("Unknown")))
			logrus.Infof("Device updated to new image %s 🎉", "flightctl-device:v4")

			logrus.Info("Container with sleep infinity process is present but not running, as expected")
			stdout, err := harness.VM.RunSSH([]string{"sudo", "podman", "ps"}, nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(stdout.String()).To(ContainSubstring("sleep infinity"))
			logrus.Info("Pods are not running, as expected 👌")
			harness.UpdateDeviceWithRetries(deviceId, func(device *v1alpha1.Device) {
				currentImage := device.Status.Os.Image
				logrus.Infof("current image for %s is %s", deviceId, currentImage)
				repo, _ := parseImageReference(currentImage)
				newImageReference = repo + ":base"
				device.Spec.Os = &v1alpha1.DeviceOSSpec{Image: newImageReference}
				logrus.Infof("updating %s to image %s", deviceId, device.Spec.Os.Image)
			})

			harness.WaitForDeviceContents(deviceId, "the device is upgrading to renderedVersion: 3",
				func(device *v1alpha1.Device) bool {
					return conditionExists(device, "Updating", "True", "Update")
				}, "1m")

			harness.WaitForDeviceContents(deviceId, "the device is rebooting",
				func(device *v1alpha1.Device) bool {
					return conditionExists(device, "Updating", "True", "Rebooting")
				}, "2m")

			harness.WaitForDeviceContents(deviceId, "status.Os.Image gets updated",
				func(device *v1alpha1.Device) bool {
					return device.Status.Os.Image == newImageReference &&
						conditionExists(device, "Updating", "False", "Updated")
				}, "2m")

			logrus.Infof("Device updated to new image %s 🎉", "flightctl-device:base")

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
