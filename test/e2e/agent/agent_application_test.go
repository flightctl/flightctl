package agent_test

import (
	"fmt"
	"strings"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/test/harness/e2e"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/sirupsen/logrus"
)

var _ = Describe("VM Agent behaviour during the application lifecycle", func() {
	var (
		harness  *e2e.Harness
		deviceId string
		device   *v1alpha1.Device
		extIP    string
	)

	BeforeEach(func() {
		harness = e2e.NewTestHarness()
		deviceId = harness.StartVMAndEnroll()
	})

	AfterEach(func() {
		harness.Cleanup(true)
	})

	Context("application", func() {
		It("should install an application image package and report its status", Label("76800"), func() {
			By("Add the application spec to the device")

			// Make sure the device status right after bootstrap is Online
			response := harness.GetDeviceWithStatusSystem(deviceId)
			Expect(response).ToNot(BeNil())
			device = response.JSON200
			Expect(device.Status.Summary.Status).To(Equal(v1alpha1.DeviceSummaryStatusOnline))

			// Get the next expected rendered version
			newRenderedVersion, err := harness.PrepareNextDeviceVersion(deviceId)
			Expect(err).ToNot(HaveOccurred())

			// Get the application url in the local registryand create the application config
			extIP = harness.RegistryEndpoint()
			sleepAppImage := fmt.Sprintf("%s/sleep-app:v1", extIP)
			var applicationConfig = v1alpha1.ImageApplicationProvider{
				Image: sleepAppImage,
			}

			// Update the device with the application config
			harness.UpdateDeviceWithRetries(deviceId, func(device *v1alpha1.Device) {

				// Create applicationSpec.
				var applicationSpec v1alpha1.ApplicationSpec
				err := applicationSpec.FromImageApplicationProvider(applicationConfig)
				Expect(err).ToNot(HaveOccurred())

				device.Spec.Applications = &[]v1alpha1.ApplicationSpec{applicationSpec}
				logrus.Infof("Updating %s with application %s", deviceId, sleepAppImage)
			})

			logrus.Infof("Waiting for the device to pick the config")
			err = harness.WaitForDeviceNewRenderedVersion(deviceId, newRenderedVersion)
			Expect(err).ToNot(HaveOccurred())

			By("Check that the application compose is copied to the device")
			manifestPath := fmt.Sprintf("/etc/compose/manifests/%s", sleepAppImage)
			stdout, err := harness.VM.RunSSH([]string{"ls", manifestPath}, nil)
			Expect(err).ToNot(HaveOccurred())
			Expect(stdout.String()).To(ContainSubstring(ComposeFile))

			By("Wait for the reported application running status in the device")
			WaitForApplicationRunningStatus(harness, deviceId, sleepAppImage)

			By("Check the general device application status")
			Expect(device.Status.ApplicationsSummary.Status).To(Equal(v1alpha1.ApplicationsSummaryStatusHealthy))

			By("Check the containers are running in the device")
			stdout, err = harness.VM.RunSSH([]string{"sudo", "podman", "ps", "|", "grep", "Up", "|", "wc", "-l"}, nil)
			Expect(err).ToNot(HaveOccurred())
			Expect(stdout.String()).To(ContainSubstring(ExpectedNumSleepAppV1Containers))

			By("Update an application image tag")
			repo, tag := parseImageReference(sleepAppImage)
			Expect(repo).ToNot(Equal(""))
			Expect(tag).ToNot(Equal(""))

			logrus.Infof("Updating from tag %s to v2", tag)

			updateImage := repo + ":v2"
			updateApplicationConfig := v1alpha1.ImageApplicationProvider{
				Image: updateImage,
			}

			// Get the next expected rendered version before the update
			newRenderedVersion, err = harness.PrepareNextDeviceVersion(deviceId)
			Expect(err).ToNot(HaveOccurred())

			applicationVars := map[string]string{
				"FFO":      "FFO",
				"SIMPLE":   "SIMPLE",
				"SOME_KEY": "SOME_KEY",
			}

			harness.UpdateDeviceWithRetries(deviceId, func(device *v1alpha1.Device) {

				// Create applicationSpec.
				var updateApplicationSpec v1alpha1.ApplicationSpec
				err := updateApplicationSpec.FromImageApplicationProvider(updateApplicationConfig)
				Expect(err).ToNot(HaveOccurred())

				updateApplicationSpec.EnvVars = &applicationVars

				device.Spec.Applications = &[]v1alpha1.ApplicationSpec{updateApplicationSpec}
				logrus.Infof("Updating %s with application %s", deviceId, updateImage)
			})

			By("Check that the device received the new config")
			err = harness.WaitForDeviceNewRenderedVersion(deviceId, newRenderedVersion)
			Expect(err).ToNot(HaveOccurred())

			By("Wait for the application running status")
			WaitForApplicationRunningStatus(harness, deviceId, updateImage)

			By("Check that the new application containers are running")
			stdout, err = harness.VM.RunSSH([]string{"sudo", "podman", "ps", "|", "grep", "Up", "|", "wc", "-l"}, nil)
			Expect(err).ToNot(HaveOccurred())
			Expect(stdout.String()).To(ContainSubstring(ExpectedNumSleepAppV2Containers))

			By("Check that the envs of v2 app are present in the containers")
			containerName, err := harness.VM.RunSSH([]string{"sudo", "podman", "ps", "--format", "\"{{.Names}} {{.Names}}\"", "|", "head", "-n", "1", "|", "awk", "'{print $1}'"}, nil)
			containerNameString := strings.Trim(containerName.String(), "\n")
			Expect(err).ToNot(HaveOccurred())

			stdout, err = harness.VM.RunSSH([]string{"sudo", "podman", "exec", containerNameString, "printenv"}, nil)
			Expect(err).ToNot(HaveOccurred())
			Expect(stdout.String()).To(ContainSubstring("SIMPLE"))

			newRenderedVersion, err = harness.PrepareNextDeviceVersion(deviceId)
			Expect(err).ToNot(HaveOccurred())

			By("Delete the application from the fleet configuration")
			logrus.Infof("Removing all the applications from %s", deviceId)

			harness.UpdateDeviceWithRetries(deviceId, func(device *v1alpha1.Device) {

				device.Spec.Applications = &[]v1alpha1.ApplicationSpec{}
				logrus.Infof("Updating %s removing application %s", deviceId, updateImage)
			})

			By("Check that the device received the new config")
			err = harness.WaitForDeviceNewRenderedVersion(deviceId, newRenderedVersion)
			Expect(err).ToNot(HaveOccurred())

			By("Check all the containers are deleted")
			stdout, err = harness.VM.RunSSH([]string{"sudo", "podman", "ps", "|", "grep", "Up", "|", "wc", "-l"}, nil)
			Expect(err).ToNot(HaveOccurred())
			Expect(stdout.String()).To(ContainSubstring(ZeroContainers))
		})
	})
})

func WaitForApplicationRunningStatus(h *e2e.Harness, deviceId string, applicationImage string) {
	logrus.Infof("Waiting for the application ready status")
	h.WaitForDeviceContents(deviceId, "status: Running",
		func(device *v1alpha1.Device) bool {
			for _, application := range device.Status.Applications {
				if application.Name == applicationImage && application.Status == v1alpha1.ApplicationStatusRunning {
					return true
				}
			}
			return false
		}, TIMEOUT)
}
