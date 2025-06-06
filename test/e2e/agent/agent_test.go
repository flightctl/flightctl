package agent_test

import (
	"context"
	"fmt"
	"strconv"
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
		ctx     context.Context
		harness *e2e.Harness
	)

	BeforeEach(func() {
		ctx = testutil.StartSpecTracerForGinkgo(suiteCtx)
		harness = e2e.NewTestHarness(ctx)
		err := harness.VM.RunAndWaitForSSH()
		Expect(err).ToNot(HaveOccurred())
	})

	AfterEach(func() {
		err := harness.CleanUpAllResources()
		Expect(err).ToNot(HaveOccurred())
		harness.Cleanup(true)
	})

	Context("vm", func() {
		It("Verify VM agent", Label("80455", "sanity"), func() {
			By("should print QR output to console")
			// Wait for the top-most part of the QR output to appear
			Eventually(harness.VM.GetConsoleOutput, TIMEOUT, POLLING).Should(ContainSubstring("████████████████████████████████"))

			fmt.Println("============ Console output ============")
			lines := strings.Split(harness.VM.GetConsoleOutput(), "\n")
			fmt.Println(strings.Join(lines[len(lines)-20:], "\n"))
			fmt.Println("========================================")

			By("should have flightctl-agent running")
			stdout, err := harness.VM.RunSSH([]string{"sudo", "systemctl", "status", "flightctl-agent"}, nil)
			Expect(err).ToNot(HaveOccurred())
			Expect(stdout.String()).To(ContainSubstring("Active: active (running)"))
		})

		It("Verifying generation of enrollment request link", Label("75518", "sanity"), func() {
			By("should be reporting device status on enrollment request")
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

	Context("status", func() {
		It("Device status tests", Label("75991", "sanity"), func() {
			deviceId, device := harness.EnrollAndWaitForOnlineStatus()
			// Get the next expected rendered version
			newRenderedVersion, err := harness.PrepareNextDeviceVersion(deviceId)
			Expect(err).ToNot(HaveOccurred())

			By("should report the correct device status after an inline config is added")

			harness.UpdateDeviceWithRetries(deviceId, func(device *v1alpha1.Device) {

				// Create ConfigProviderSpec.
				var configProviderSpec v1alpha1.ConfigProviderSpec
				err := configProviderSpec.FromInlineConfigProviderSpec(validInlineConfig)
				Expect(err).ToNot(HaveOccurred())

				device.Spec.Config = &[]v1alpha1.ConfigProviderSpec{configProviderSpec}
				logrus.Infof("Updating %s with config %s", deviceId, device.Spec.Config)
			})

			logrus.Infof("Waiting for the device to pick the config")
			harness.WaitForDeviceContents(deviceId, fmt.Sprintf("the device is updated to renderedVersion: %s", strconv.Itoa(newRenderedVersion)),
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

			By("should report the correct device status when trying to upgrade to a not existing image")
			previousRenderedVersion := newRenderedVersion
			nextGeneration, err := harness.PrepareNextDeviceGeneration(deviceId)
			Expect(err).ToNot(HaveOccurred())
			newRenderedVersion, err = harness.PrepareNextDeviceVersion(deviceId)
			Expect(err).ToNot(HaveOccurred())

			var newImageReference string
			harness.UpdateDeviceWithRetries(deviceId, func(device *v1alpha1.Device) {
				currentImage := device.Status.Os.Image
				logrus.Infof("Current image for %s is %s", deviceId, currentImage)
				repo, _ := parseImageReference(currentImage)
				newImageReference = repo + ":not-existing"
				device.Spec.Os = &v1alpha1.DeviceOsSpec{Image: newImageReference}
				logrus.Infof("Updating %s to image %s", deviceId, device.Spec.Os.Image)
			})

			err = harness.WaitForDeviceNewGeneration(deviceId, nextGeneration)
			Expect(err).ToNot(HaveOccurred())

			harness.WaitForDeviceContents(deviceId, fmt.Sprintf("Failed to update to renderedVersion: %s. Error", strconv.Itoa(newRenderedVersion)),
				func(device *v1alpha1.Device) bool {
					// returning true if it is reported an error status or if the device is rolled back to the previous version
					return e2e.ConditionExists(device, "Updating", "False", string(v1alpha1.UpdateStateError)) ||
						(e2e.ConditionExists(device, "Updating", "False", string(v1alpha1.UpdateStateUpdated)) && (device.Status.Config.RenderedVersion == strconv.Itoa(previousRenderedVersion)))
				}, "2m")

			Eventually(harness.GetDeviceWithUpdateStatus, TIMEOUT, POLLING).WithArguments(
				deviceId).Should(Equal(v1alpha1.DeviceUpdatedStatusOutOfDate))

			By(`should show an error when trying to update a device with
				"a reference to a not existing git repo, and report 'Online' status`)

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
					return e2e.ConditionExists(device, "SpecValid", "False", "Invalid")
				}, "2m")

			harness.WaitForDeviceContents(deviceId, fmt.Sprintf("Failed to update to renderedVersion: %s", strconv.Itoa(newRenderedVersion)),
				func(device *v1alpha1.Device) bool {
					return e2e.ConditionExists(device, "Updating", "False", string(v1alpha1.UpdateStateError))
				}, "2m")
			Eventually(harness.GetDeviceWithStatusSummary, TIMEOUT, POLLING).WithArguments(
				deviceId).Should(Equal(v1alpha1.DeviceSummaryStatusType("Online")))

			By(`should show an error when trying to update a device with a httpConfigProviderSpec
			with invalid Path, and report 'Online' status`)
			// Create the http repository.
			_, err = model.NewRepositoryFromApiResource(&httpRepo)
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
					return e2e.ConditionExists(device, "SpecValid", "False", "Invalid")
				}, "2m")

			harness.WaitForDeviceContents(deviceId, fmt.Sprintf("Failed to update to renderedVersion: %s", strconv.Itoa(newRenderedVersion)),
				func(device *v1alpha1.Device) bool {
					return e2e.ConditionExists(device, "Updating", "False", string(v1alpha1.UpdateStateError))
				}, "2m")
			Eventually(harness.GetDeviceWithStatusSummary, TIMEOUT, POLLING).WithArguments(
				deviceId).Should(Equal(v1alpha1.DeviceSummaryStatusOnline))

			By("should report 'Unknown' after the device vm is powered-off")

			// Shutdown the vm.
			err = harness.VM.Shutdown()
			Expect(err).ToNot(HaveOccurred())
			Eventually(harness.GetDeviceWithStatusSummary, LONGTIMEOUT, POLLING).WithArguments(
				deviceId).Should(Equal(v1alpha1.DeviceSummaryStatusUnknown))

		})
	})
})

// mode defines the file permission bits, commonly used in Unix systems for files and directories.
var mode = 0644
var modePointer = &mode

// inlineConfig defines a file specification with content, mode, and path for provisioning system files.
var inlineConfig = v1alpha1.FileSpec{
	Content: "This system is managed by flightctl.",
	Mode:    modePointer,
	Path:    "/etc/motd",
}

// validInlineConfig defines a valid inline configuration provider spec with pre-defined file specs and a name.
var validInlineConfig = v1alpha1.InlineConfigProviderSpec{
	Inline: []v1alpha1.FileSpec{inlineConfig},
	Name:   "valid-inline-config",
}

var name = "flightctl-demos"
var repoMetadata = v1alpha1.ObjectMeta{
	Name: &name,
}

// httpRepoSpec initializes an HttpRepoSpec with an HTTP repository type and URL for clone or access operations.
var httpRepoSpec = v1alpha1.HttpRepoSpec{
	Type: v1alpha1.RepoSpecType("http"),
	Url:  "https://github.com/flightctl/flightctl-demos.git",
}

// spec is a variable of type RepositorySpec used to describe configuration for a repository.
var spec v1alpha1.RepositorySpec
var _ = spec.FromHttpRepoSpec(httpRepoSpec)

// httpRepo represents a v1alpha1.Repository with predefined ApiVersion, Kind, Metadata, and Spec values.
var httpRepo = v1alpha1.Repository{
	ApiVersion: "v1alpha1",
	Kind:       "Repository",
	Metadata:   repoMetadata,
	Spec:       spec,
}

// mountPath specifies the default file system path where the configuration is expected to be mounted.
var mountPath = "/etc/config"

// gitConfigInvalidRepo defines a GitConfigProviderSpec with an invalid repository name ("not-existing-repo") for test purposes.
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

// suffix specifies a default path segment or query parameter that can be appended to a URL in HTTP configuration.
var suffix = "/some/suffix"

// httpConfigInvalidPath defines an invalid HTTP configuration with a non-existent file path for testing scenarios.
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

// parseImageReference splits an image reference string into the repository and tag components.
// It returns the repository and tag as separate strings.
// If no tag is present, the returned tag string will be empty.
func parseImageReference(image string) (string, string) {
	// Split the image string by the colon to separate the repository and the tag.
	parts := strings.Split(image, ":")

	tag := ""
	repo := ""

	// The tag is the last part after the last colon.
	if len(parts) > 1 {
		tag = parts[len(parts)-1]
		// The repository is composed of all parts before the last colon, joined back together with colons.
		repo = strings.Join(parts[:len(parts)-1], ":")
	}

	return repo, tag
}
