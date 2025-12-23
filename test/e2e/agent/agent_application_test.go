package agent_test

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/flightctl/flightctl/api/v1beta1"
	"github.com/flightctl/flightctl/test/harness/e2e"
	"github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
)

const (
	fileName      = "podman-compose.yaml"
	inlineAppName = "my-app"
)

var _ = Describe("VM Agent behaviour during the application lifecycle", func() {
	var (
		deviceId string
		device   *v1beta1.Device
	)

	BeforeEach(func() {
		// Get harness directly - no shared package-level variable
		harness := e2e.GetWorkerHarness()

		// Use the shared harness from the suite test
		// The harness is already set up with VM from pool and agent started
		// We just need to enroll the device
		deviceId, device = harness.EnrollAndWaitForOnlineStatus()
	})

	Context("application", func() {
		It("should install an application image package and report its status", Label("76800", "sanity"), func() {
			// Get harness directly - no shared package-level variable
			harness := e2e.GetWorkerHarness()

			By("Add the application spec to the device")

			// Make sure the device status right after bootstrap is Online
			response, err := harness.GetDeviceWithStatusSystem(deviceId)
			Expect(err).ToNot(HaveOccurred())
			Expect(response).ToNot(BeNil())
			device = response.JSON200
			Expect(device.Status.Summary.Status).To(Equal(v1beta1.DeviceSummaryStatusOnline))

			imageName := util.NewSleepAppImageReference(util.SleepAppTags.V1).String()

			updateDevice(harness, deviceId, func(device *v1beta1.Device) {
				var applicationConfig = v1beta1.ImageApplicationProviderSpec{
					Image: imageName,
				}

				appSpec := v1beta1.ApplicationProviderSpec{
					AppType: v1beta1.AppTypeCompose,
				}
				err := appSpec.FromImageApplicationProviderSpec(applicationConfig)
				Expect(err).ToNot(HaveOccurred())

				device.Spec.Applications = &[]v1beta1.ApplicationProviderSpec{appSpec}
				GinkgoWriter.Printf("Updating %s with application %s\n", deviceId, imageName)
			})
			Expect(err).ToNot(HaveOccurred())

			By("Check that the application compose is copied to the device")
			manifestPath := fmt.Sprintf("%s/%s", ComposeManifestPath, imageName)
			verifyCommandOutputsSubstring(
				harness,
				[]string{"ls", manifestPath},
				ComposeFile)

			By("Wait for the reported application running status in the device")
			WaitForApplicationRunningStatus(harness, deviceId, imageName)

			By("Check the general device application status")
			// Re-fetch the device to get the current status after the application is running
			response, err = harness.GetDeviceWithStatusSystem(deviceId)
			Expect(err).ToNot(HaveOccurred())
			device = response.JSON200
			Expect(device.Status.ApplicationsSummary.Status).To(Equal(v1beta1.ApplicationsSummaryStatusHealthy))

			By("Check the containers are running in the device")
			output, err := harness.CheckRunningContainers()
			Expect(err).ToNot(HaveOccurred())
			Expect(output).To(ContainSubstring(ExpectedNumSleepAppV1Containers))

			By("Update an application image tag")

			imageName = util.NewSleepAppImageReference(util.SleepAppTags.V2).String()

			updateDevice(harness, deviceId, func(device *v1beta1.Device) {
				applicationVars := map[string]string{
					"FFO":      "FFO",
					"SIMPLE":   "SIMPLE",
					"SOME_KEY": "SOME_KEY",
				}

				applicationConfig := v1beta1.ImageApplicationProviderSpec{
					Image: imageName,
				}

				appSpec := v1beta1.ApplicationProviderSpec{
					AppType: v1beta1.AppTypeCompose,
				}
				err := appSpec.FromImageApplicationProviderSpec(applicationConfig)
				Expect(err).ToNot(HaveOccurred())

				appSpec.EnvVars = &applicationVars

				device.Spec.Applications = &[]v1beta1.ApplicationProviderSpec{appSpec}
				GinkgoWriter.Printf("Updating %s with application %s\n", deviceId, imageName)
			})
			Expect(err).ToNot(HaveOccurred())

			By("Wait for the application running status")
			WaitForApplicationRunningStatus(harness, deviceId, imageName)

			By("Check that the new application containers are running")
			verifyContainerCount(harness, ExpectedNumSleepAppV2V3Containers)

			By("Check that the envs of v2 app are present in the containers")
			containerName := extractSingleContainerNameFromVM(harness)

			verifyCommandOutputsSubstring(harness, []string{"sudo", "podman", "exec", containerName, "printenv"}, "SIMPLE")

			By("Delete the application from the fleet configuration")
			GinkgoWriter.Printf("Removing all the applications from %s\n", deviceId)

			updateDevice(harness, deviceId, func(device *v1beta1.Device) {
				device.Spec.Applications = &[]v1beta1.ApplicationProviderSpec{}
				GinkgoWriter.Printf("Updating %s removing application %s\n", deviceId, imageName)
			})
			Expect(err).ToNot(HaveOccurred())

			By("Check all the containers are deleted")
			verifyContainerCount(harness, 0)
		})

		It("Should handle application volumes from images correctly", Label("83000"), func() {
			// Get harness directly - no shared package-level variable
			harness := e2e.GetWorkerHarness()

			By("Update the application to include artifact volumes")

			imageName := util.NewSleepAppImageReference(util.SleepAppTags.V3).String()

			updateDevice(harness, deviceId, func(device *v1beta1.Device) {
				volumeConfig := v1beta1.ApplicationVolume{
					Name: "testvol",
				}
				err := volumeConfig.FromImageVolumeProviderSpec(
					v1beta1.ImageVolumeProviderSpec{
						Image: v1beta1.ImageVolumeSource{
							// This contains a single tar.gz file layer called sqlite--3.50.2.x86_64_linux.bottle.tar.gz
							Reference:  "ghcr.io/homebrew/core/sqlite:3.50.2",
							PullPolicy: lo.ToPtr(v1beta1.PullIfNotPresent),
						},
					})
				Expect(err).ToNot(HaveOccurred())

				appConfig := v1beta1.ImageApplicationProviderSpec{
					Image:   imageName,
					Volumes: &[]v1beta1.ApplicationVolume{volumeConfig},
				}

				appSpec := v1beta1.ApplicationProviderSpec{
					AppType: v1beta1.AppTypeCompose,
				}
				err = appSpec.FromImageApplicationProviderSpec(appConfig)
				Expect(err).ToNot(HaveOccurred())

				device.Spec.Applications = &[]v1beta1.ApplicationProviderSpec{appSpec}
			})

			By("Wait for the application running status")
			WaitForApplicationRunningStatus(harness, deviceId, imageName)

			By("Check that the new application containers are running")
			verifyContainerCount(harness, ExpectedNumSleepAppV2V3Containers)
			containerName := extractSingleContainerNameFromVM(harness)

			verifyCommandOutputsSubstring(
				harness,
				[]string{"sudo", "podman", "inspect", "--format", `"{{.Mounts}}"`, containerName},
				"testvol")

			By("downgrading to v2 we should not have the mount anymore")
			imageName = util.NewSleepAppImageReference(util.SleepAppTags.V2).String()

			updateDevice(harness, deviceId, func(device *v1beta1.Device) {
				appConfig := v1beta1.ImageApplicationProviderSpec{
					Image: imageName,
				}

				appSpec := v1beta1.ApplicationProviderSpec{
					AppType: v1beta1.AppTypeCompose,
				}
				err := appSpec.FromImageApplicationProviderSpec(appConfig)
				Expect(err).ToNot(HaveOccurred())

				device.Spec.Applications = &[]v1beta1.ApplicationProviderSpec{appSpec}
			})
			WaitForApplicationRunningStatus(harness, deviceId, imageName)

			verifyContainerCount(harness, ExpectedNumSleepAppV2V3Containers)
			containerName = extractSingleContainerNameFromVM(harness)

			verifyCommandLacksSubstring(
				harness,
				[]string{"sudo", "podman", "inspect", "--format", `"{{.Mounts}}"`, containerName},
				"testvol")
		})

		It("should install an inline compose application and manage its lifecycle with env vars", Label("80990"), func() {
			// Get harness directly - no shared package-level variable
			harness := e2e.GetWorkerHarness()

			By("Creating the first application")
			newRenderedVersion, err := harness.PrepareNextDeviceVersion(deviceId)
			Expect(err).ToNot(HaveOccurred())
			containerAmount := 3
			inlineAppComposeYaml := fmt.Sprintf(inlineAppComposeYamlInitial, AlpineImage, AlpineImage, AlpineImage)
			inlineApp := createInlineApplicationSpec(inlineAppComposeYaml, fileName)
			err = harness.UpdateApplication(true, deviceId, inlineAppName, inlineApp, nil)
			Expect(err).ToNot(HaveOccurred())

			By("Verify the Device resource after update")
			updatedDevice, err := harness.GetDevice(deviceId)
			Expect(err).ToNot(HaveOccurred())
			GinkgoWriter.Printf("Updated Device Resource: %+v\n", updatedDevice)

			By("Wait for the device to receive the initial inline app configuration")
			err = harness.WaitForDeviceNewRenderedVersion(deviceId, newRenderedVersion)
			Expect(err).ToNot(HaveOccurred())

			By("Check if the application directory exists on the device")
			err = harness.CheckApplicationDirectoryExist(inlineAppName)
			Expect(err).ToNot(HaveOccurred())

			By("Check that the compose file is copied to the device")
			err = harness.CheckApplicationComposeFileExist(inlineAppName, filepath.Join("/", fileName))
			Expect(err).ToNot(HaveOccurred())

			By("Read the compose file content to verify")
			stdout, err := harness.VM.RunSSH([]string{"cat", filepath.Join(ComposeManifestPath, inlineAppName, fileName)}, nil)
			Expect(err).ToNot(HaveOccurred())
			Expect(stdout.String()).To(ContainSubstring(inlineAppComposeYaml))

			By("Wait for the inline app to report running status")
			WaitForApplicationRunningStatus(harness, deviceId, inlineAppName)

			By(fmt.Sprintf("Ensure %d/%d containers are up", containerAmount, containerAmount))
			stdout, err = harness.VM.RunSSH([]string{"sudo", "podman", "ps", "--format", "\"{{.Image}}\""}, nil)
			Expect(err).ToNot(HaveOccurred())
			Expect(stdout.String()).To(ContainSubstring(AlpineImage))
			Expect(strings.Count(stdout.String(), AlpineImage)).To(Equal(containerAmount))

			By("Check the application status in the device spec")
			Eventually(func() v1beta1.ApplicationStatusType {
				status, err := harness.CheckApplicationStatus(deviceId, inlineAppName)
				Expect(err).ToNot(HaveOccurred())
				return status
			}, TIMEOUT).Should(Equal(v1beta1.ApplicationStatusRunning))
			newRenderedVersion, err = harness.PrepareNextDeviceVersion(deviceId)
			Expect(err).ToNot(HaveOccurred())
			inlineAppComposeYaml = fmt.Sprintf(inlineAppComposeYamlUpdated, NginxImage)
			inlineApp = createInlineApplicationSpec(inlineAppComposeYaml, fileName)

			By("Update the application with the new compose file")
			err = harness.UpdateDevice(deviceId, func(d *v1beta1.Device) {
				err := updateDeviceApplicationFromInline(d, inlineAppName, inlineApp)
				if err != nil {
					GinkgoWriter.Printf("Failed to update application %s on device %s: %v\n", inlineAppName, deviceId, err)
				} else {
					GinkgoWriter.Printf("Successfully updated application %s on device %s\n", inlineAppName, deviceId)
				}
			})
			Expect(err).NotTo(HaveOccurred())

			By("Wait for the device to apply the updated configuration")
			err = harness.WaitForDeviceNewRenderedVersion(deviceId, newRenderedVersion)
			Expect(err).ToNot(HaveOccurred())

			By("Ensure the application is updated with the new image")
			Eventually(func() string {
				stdout, err := harness.VM.RunSSH([]string{"sudo", "podman", "ps", "--format", "\"{{.Image}}\""}, nil)
				Expect(err).ToNot(HaveOccurred())
				return stdout.String()
			}, TIMEOUT).Should(ContainSubstring(NginxImage))
			newRenderedVersion, err = harness.PrepareNextDeviceVersion(deviceId)
			Expect(err).ToNot(HaveOccurred())

			By("Remove the application from the spec")
			err = harness.UpdateDeviceWithRetries(deviceId, func(device *v1beta1.Device) {
				device.Spec.Applications = &[]v1beta1.ApplicationProviderSpec{}
			})
			Expect(err).ToNot(HaveOccurred())

			By("Wait for device to pick up the removal config")
			err = harness.WaitForDeviceNewRenderedVersion(deviceId, newRenderedVersion)
			Expect(err).ToNot(HaveOccurred())

			By("Ensure all containers are stopped")
			output, err := harness.CheckRunningContainers()
			Expect(err).ToNot(HaveOccurred())
			Expect(strings.TrimSpace(output)).To(Equal("0"))

			By("Ensure the application folder is deleted")
			err = harness.CheckApplicationDirectoryExist(inlineAppName)
			Expect(err).To(HaveOccurred()) // Expect an error because the directory should be gone
			newRenderedVersion, err = harness.PrepareNextDeviceVersion(deviceId)
			Expect(err).ToNot(HaveOccurred())

			By("Add the first application again , now with an environment variable")
			envVarName := "MY_ENV_VAR"
			envVarValue := "my-value"
			inlineAppComposeYaml = fmt.Sprintf(inlineAppComposeYamlWithEnv, AlpineImage, envVarName, envVarValue, AlpineImage, AlpineImage)
			inlineApp = createInlineApplicationSpec(inlineAppComposeYaml, fileName)
			err = harness.UpdateApplication(true, deviceId, inlineAppName, inlineApp, nil)
			Expect(err).ToNot(HaveOccurred())

			By("Wait for the device to receive the application with the env var")
			err = harness.WaitForDeviceNewRenderedVersion(deviceId, newRenderedVersion)
			Expect(err).ToNot(HaveOccurred())

			By("Check that the env var is injected into the app container")
			Eventually(func() string {
				value, err := harness.CheckEnvInjectedToApplication(envVarName, AlpineImage)
				Expect(err).ToNot(HaveOccurred())
				return value
			}, TIMEOUT).Should(Equal(envVarValue))
		})

		It("Agent pre-update validations should fail the version, and trigger the rollback for various invalid configurations", Label("80998"), func() {
			// Get harness directly - no shared package-level variable
			harness := e2e.GetWorkerHarness()

			By("Create initial application")
			initialRenderedVersion, err := harness.PrepareNextDeviceVersion(deviceId)
			Expect(err).ToNot(HaveOccurred())
			inlineAppComposeYaml := fmt.Sprintf(inlineAppComposeYamlInitial, AlpineImage, AlpineImage, AlpineImage)
			inlineApp := createInlineApplicationSpec(inlineAppComposeYaml, fileName)
			err = harness.UpdateApplication(true, deviceId, inlineAppName, inlineApp, nil)
			Expect(err).ToNot(HaveOccurred())
			err = harness.WaitForDeviceNewRenderedVersion(deviceId, initialRenderedVersion)
			Expect(err).ToNot(HaveOccurred())

			By("Update application with duplicate names")
			inlineAppComposeYaml = fmt.Sprintf(inlineAppComposeYamlWithDuplicateNames, AlpineImage, AlpineImage)
			inlineApp = createInlineApplicationSpec(inlineAppComposeYaml, fileName)
			apiError := harness.UpdateApplication(false, deviceId, inlineAppName, inlineApp, nil)
			Expect(apiError).To(HaveOccurred())
			Expect(apiError.Error()).To(ContainSubstring("invalid compose YAML"))

			By("Update application with bad path")
			inlineAppComposeYaml = fmt.Sprintf(inlineAppComposeYamlBadPath, AlpineImage)
			inlineApp = createInlineApplicationSpec(inlineAppComposeYaml, "test-app")
			apiError = harness.UpdateApplication(false, deviceId, inlineAppName, inlineApp, nil)
			Expect(apiError).To(HaveOccurred())
			Expect(apiError.Error()).To(ContainSubstring("compose path must have .yaml or .yml extension"))

			By("Update application with bad YAML structure")
			inlineAppComposeYaml = fmt.Sprintf(inlineAppComposeYamlBadStructure, NginxImage)
			inlineApp = createInlineApplicationSpec(inlineAppComposeYaml, fileName)
			apiError = harness.UpdateApplication(false, deviceId, inlineAppName, inlineApp, nil)
			Expect(apiError).To(HaveOccurred())
			Expect(apiError.Error()).To(ContainSubstring("compose spec has no services"))

			By("Update application with invalid environment variables")
			inlineAppComposeYaml = fmt.Sprintf(inlineAppComposeYamlInitial, AlpineImage, AlpineImage, AlpineImage)
			inlineApp = createInlineApplicationSpec(inlineAppComposeYaml, fileName)
			apiError = harness.UpdateApplication(false, deviceId, inlineAppName, inlineApp, map[string]string{"-1": "test", "!": "test"})
			Expect(apiError).To(HaveOccurred())
			Expect(apiError.Error()).To(ContainSubstring("envVars: Invalid value"))

			By("Attempt to create an application with non-base64 content and contentEncoding: base64")
			invalidContent := "Not encoded string"
			inlineApp = v1beta1.InlineApplicationProviderSpec{
				Inline: []v1beta1.ApplicationContent{
					{
						Content:         &invalidContent,
						Path:            "Chart.yaml",
						ContentEncoding: lo.ToPtr(v1beta1.EncodingType("base64")),
					},
				},
			}
			apiError = harness.UpdateApplication(false, deviceId, inlineAppName, inlineApp, nil)
			Expect(apiError).To(HaveOccurred())
			Expect(apiError.Error()).To(ContainSubstring("decode base64 content: illegal base64 data"))
		})
	})
})

const (

	// ComposeManifestPath defines the file system path where compose manifests are stored, typically used in deployment setups.
	ComposeManifestPath = "/etc/compose/manifests"

	AlpineImage = "quay.io/flightctl-tests/alpine:v1"

	NginxImage = "quay.io/flightctl-tests/nginx:v1"

	inlineAppComposeYamlInitial = `
version: "3.8"
services:
  service1:
    image: %s
    command: ["sleep", "infinity"]
  service2:
    image: %s
    command: ["sleep", "infinity"]
  service3:
    image: %s
    command: ["sleep", "infinity"]
`

	inlineAppComposeYamlUpdated = `
version: "3.8"
services:
  app:
    image: %s
    ports:
      - "80:80"
`

	inlineAppComposeYamlWithEnv = `
version: "3.8"
services:
  service1:
    image: %s
    command: ["sleep", "infinity"]
    environment:
      - %s=%s
  service2:
    image: %s
    command: ["sleep", "infinity"]
  service3:
    image: %s
    command: ["sleep", "infinity"]
`

	inlineAppComposeYamlWithDuplicateNames = `
version: "3.8"
services:
  service1:
    image: %s
    command: ["sleep", "infinity"]
	name: test1
  service2:
    image: %s
    command: ["sleep", "infinity"]
	name: test2
`

	inlineAppComposeYamlBadPath = `
version: "3.8"
services:
  service1:
    image: %s
    command: ["sleep", "infinity"]
`

	inlineAppComposeYamlBadStructure = `
version: "3.8"
services:
app:
image: %s
ports:
- "80:80"
`
)
