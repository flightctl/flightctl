package agent_test

import (
	"fmt"
	"strings"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/test/harness/e2e"
	testutil "github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/sirupsen/logrus"
)

var _ = Describe("VM Agent behavior", func() {
	var (
		harness *e2e.Harness
	)

	BeforeEach(func() {
		harness = e2e.NewTestHarness()
		err := harness.VM.RunAndWaitForSSH()
		Expect(err).ToNot(HaveOccurred())
	})

	AfterEach(func() {
		harness.Cleanup(true)
	})

	Context("vm", func() {
		It("should print QR output to console", func() {
			// Wait for the top-most part of the QR output to appear
			Eventually(harness.VM.GetConsoleOutput, TIMEOUT, POLLING).Should(ContainSubstring("████████████████████████████████"))

			fmt.Println("============ Console output ============")
			lines := strings.Split(harness.VM.GetConsoleOutput(), "\n")
			fmt.Println(strings.Join(lines[len(lines)-20:], "\n"))
			fmt.Println("========================================")
		})

		It("should have flightctl-agent running", func() {
			stdout, err := harness.VM.RunSSH([]string{"sudo", "systemctl", "status", "flightctl-agent"}, nil)
			Expect(err).ToNot(HaveOccurred())
			Expect(stdout.String()).To(ContainSubstring("Active: active (running)"))
		})

		It("should be reporting device status on enrollment request", func() {
			// Get the enrollment Request ID from the console output
			enrollmentID := harness.GetEnrollmentIDFromConsole()
			logrus.Infof("Enrollment ID found in VM console output: %s", enrollmentID)

			// Wait for the device to create the enrollment request, and check the TPM details
			enrollmentRequest := harness.WaitForEnrollmentRequest(enrollmentID)
			Expect(enrollmentRequest.Spec).ToNot(BeNil())
			Expect(enrollmentRequest.Spec.DeviceStatus).ToNot(BeNil())
			Expect(enrollmentRequest.Spec.DeviceStatus.SystemInfo.IsEmpty()).NotTo(BeTrue())

			// Approve the enrollment and wait for the device details to be populated by the agent
			harness.ApproveEnrollment(enrollmentID, testutil.TestEnrollmentApproval())
			logrus.Infof("Waiting for device %s to report status", enrollmentID)

			// wait for the device to pickup enrollment and report measurements on device status
			Eventually(harness.GetDeviceWithStatusSystem, TIMEOUT, POLLING).WithArguments(
				enrollmentID).ShouldNot(BeNil())
		})
	})

	Context("status", Label("75991"), func() {
		It("should report the correct device status after an inline config is added", func() {
			deviceId, device := harness.EnrollAndWaitForOnlineStatus()

			harness.UpdateDeviceWithRetries(deviceId, func(device *v1alpha1.Device) {

				// Create ConfigProviderSpec.
				var configProviderSpec v1alpha1.ConfigProviderSpec
				err := configProviderSpec.FromInlineConfigProviderSpec(validInlineConfig)
				Expect(err).ToNot(HaveOccurred())

				device.Spec.Config = &[]v1alpha1.ConfigProviderSpec{configProviderSpec}
				logrus.Infof("Updating %s with config %s", deviceId, device.Spec.Config)
			})

			logrus.Infof("Waiting for the device to pick the config")
			harness.WaitForDeviceContents(deviceId, "the device is upgrading to renderedVersion: 2",
				func(device *v1alpha1.Device) bool {
					for _, condition := range device.Status.Conditions {
						if condition.Type == "Updating" && condition.Reason == "Updated" && condition.Status == "False" &&
							condition.Message == UpdateRenderedVersionSuccess.String() {
							return true
						}
					}
					return false
				}, TIMEOUT)

			// The device should have the online config.
			stdout, err := harness.VM.RunSSH([]string{"cat", "/etc/motd"}, nil)
			Expect(err).ToNot(HaveOccurred())
			Expect(stdout.String()).To(ContainSubstring("This system is managed by flightctl."))

			// The status should be "Online"
			logrus.Infof("The device has the config %s", device.Spec.Config)
			Eventually(harness.GetDeviceWithStatusSummary, TIMEOUT, POLLING).WithArguments(
				deviceId).Should(Equal(v1alpha1.DeviceSummaryStatusType("Online")))
		})

		It("should report the correct device status when trying to upgrade to a not existing image", func() {
			deviceId, _ := harness.EnrollAndWaitForOnlineStatus()

			var newImageReference string
			harness.UpdateDeviceWithRetries(deviceId, func(device *v1alpha1.Device) {
				currentImage := device.Status.Os.Image
				logrus.Infof("Current image for %s is %s", deviceId, currentImage)
				repo, _ := parseImageReference(currentImage)
				newImageReference = repo + ":not-existing"
				device.Spec.Os = &v1alpha1.DeviceOSSpec{Image: newImageReference}
				logrus.Infof("Updating %s to image %s", deviceId, device.Spec.Os.Image)
			})

			harness.WaitForDeviceContents(deviceId, "Failed to update to renderedVersion: 2. Retrying",
				func(device *v1alpha1.Device) bool {
					return conditionExists(device, "Updating", "False", string(v1alpha1.UpdateStateError))
				}, "2m")

			Eventually(harness.GetDeviceWithUpdateStatus, TIMEOUT, POLLING).WithArguments(
				deviceId).Should(Equal(v1alpha1.DeviceUpdatedStatusOutOfDate))
		})

		It(`should show an error when trying to update a device with
			"a reference to a not existing git repo, and report 'Online' status`, Label("git"), func() {
			deviceId, _ := harness.EnrollAndWaitForOnlineStatus()

			harness.UpdateDeviceWithRetries(deviceId, func(device *v1alpha1.Device) {
				logrus.Infof("Current device is %s", deviceId)

				// Create ConfigProviderSpec.
				var configProviderSpec v1alpha1.ConfigProviderSpec
				err := configProviderSpec.FromGitConfigProviderSpec(gitConfigInvalidRepo)
				Expect(err).ToNot(HaveOccurred())

				device.Spec.Config = &[]v1alpha1.ConfigProviderSpec{configProviderSpec}
				logrus.Infof("Updating %s with config %s", deviceId, device.Spec.Config)
			})

			// Check the http config error is detected.
			harness.WaitForDeviceContents(deviceId, `Error: failed fetching specified Repository definition`,
				func(device *v1alpha1.Device) bool {
					return conditionExists(device, "SpecValid", "False", "Invalid")
				}, "2m")

			// The behaviour will change after EDM-418.
			harness.WaitForDeviceContents(deviceId, "the device is updated to renderedVersion: 1",
				func(device *v1alpha1.Device) bool {
					return conditionExists(device, "Updating", "False", "Updated")
				}, "2m")
			Eventually(harness.GetDeviceWithStatusSummary, TIMEOUT, POLLING).WithArguments(
				deviceId).Should(Equal(v1alpha1.DeviceSummaryStatusType("Online")))
		})

		It(`should show an error when trying to update a device with a httpConfigProviderSpec
			with invalid Path, and report 'Online' status`, func() {
			deviceId, _ := harness.EnrollAndWaitForOnlineStatus()

			// Create the http repository.
			_, err := model.NewRepositoryFromApiResource(&httpRepo)
			Expect(err).ToNot(HaveOccurred())

			// Update the device with the http invalid config.
			harness.UpdateDeviceWithRetries(deviceId, func(device *v1alpha1.Device) {
				logrus.Infof("current device is %s", deviceId)
				// Create ConfigProviderSpec.
				var configProviderSpec v1alpha1.ConfigProviderSpec
				err := configProviderSpec.FromHttpConfigProviderSpec(httpConfigInvalidPath)
				Expect(err).ToNot(HaveOccurred())

				device.Spec.Config = &[]v1alpha1.ConfigProviderSpec{configProviderSpec}
				logrus.Infof("updating %s with config %s", deviceId, device.Spec.Config)
			})

			// Check the http config error is detected.
			harness.WaitForDeviceContents(deviceId, "Error: sending HTTP Request",
				func(device *v1alpha1.Device) bool {
					return conditionExists(device, "SpecValid", "False", "Invalid")
				}, "2m")

			// The behaviour will change after EDM-418.
			harness.WaitForDeviceContents(deviceId, "the device is updated to renderedVersion: 1",
				func(device *v1alpha1.Device) bool {
					return conditionExists(device, "Updating", "False", "Updated")
				}, "2m")
			Eventually(harness.GetDeviceWithStatusSummary, TIMEOUT, POLLING).WithArguments(
				deviceId).Should(Equal(v1alpha1.DeviceSummaryStatusOnline))
		})

		It("should report 'Unknown' after the device vm is powered-off", func() {
			deviceId, _ := harness.EnrollAndWaitForOnlineStatus()

			// Shutdown the vm.
			err := harness.VM.Shutdown()
			Expect(err).ToNot(HaveOccurred())
			Eventually(harness.GetDeviceWithStatusSummary, LONGTIMEOUT, POLLING).WithArguments(
				deviceId).Should(Equal(v1alpha1.DeviceSummaryStatusUnknown))
		})
	})
})

var mode = 0644
var modePointer = &mode

var inlineConfig = v1alpha1.FileSpec{
	Content: "This system is managed by flightctl.",
	Mode:    modePointer,
	Path:    "/etc/motd",
}

var validInlineConfig = v1alpha1.InlineConfigProviderSpec{
	Inline: []v1alpha1.FileSpec{inlineConfig},
	Name:   "valid-inline-config",
}

var name = "flightctl-demos"
var repoMetadata = v1alpha1.ObjectMeta{
	Name: &name,
}

var httpRepoSpec = v1alpha1.HttpRepoSpec{
	Type: v1alpha1.RepoSpecType("http"),
	Url:  "https://github.com/flightctl/flightctl-demos.git",
}

var spec v1alpha1.RepositorySpec
var _ = spec.FromHttpRepoSpec(httpRepoSpec)

var httpRepo = v1alpha1.Repository{
	ApiVersion: "v1alpha1",
	Kind:       "Repository",
	Metadata:   repoMetadata,
	Spec:       spec,
}

var mountPath = "/etc/config"
var gitConfigInvalidRepo = v1alpha1.GitConfigProviderSpec{
	GitRef: struct {
		MountPath      *string `json:"mountPath,omitempty"`
		Path           string  `json:"path"`
		Repository     string  `json:"repository"`
		TargetRevision string  `json:"targetRevision"`
	}{
		MountPath:      &mountPath,
		Path:           "/configs/repo/config.yaml",
		Repository:     "not-existing-repo",
		TargetRevision: "main",
	},
	Name: "example-git-config-provider",
}

var suffix = "/some/suffix"
var httpConfigInvalidPath = v1alpha1.HttpConfigProviderSpec{
	HttpRef: struct {
		FilePath   string  `json:"filePath"`
		Repository string  `json:"repository"`
		Suffix     *string `json:"suffix,omitempty"`
	}{
		FilePath:   "/invalid/path",
		Repository: "flightctl-demos",
		Suffix:     &suffix,
	},
	Name: "example-http-config-provider",
}
