package agent_test

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/test/harness/e2e"
	testutil "github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
)

const (
	fileName      = "podman-compose.yaml"
	inlineAppName = "my-app"
)

func sleepAppImageName(harness *e2e.Harness, tag string) string {
	extIP := harness.RegistryEndpoint()
	return fmt.Sprintf("%s/sleep-app:%s", extIP, tag)
}

var _ = Describe("VM Agent behaviour during the application lifecycle", func() {
	var (
		ctx      context.Context
		harness  *e2e.Harness
		deviceId string
		device   *v1alpha1.Device
	)

	BeforeEach(func() {
		ctx = testutil.StartSpecTracerForGinkgo(suiteCtx)
		harness = e2e.NewTestHarness(ctx)
		deviceId = harness.StartVMAndEnroll()
	})

	AfterEach(func() {
		harness.Cleanup(true)
	})

	Context("application", func() {
		It("should install an application image package and report its status", Label("76800", "sanity"), func() {
			By("Add the application spec to the device")

			// Make sure the device status right after bootstrap is Online
			response, err := harness.GetDeviceWithStatusSystem(deviceId)
			Expect(err).ToNot(HaveOccurred())
			Expect(response).ToNot(BeNil())
			device = response.JSON200
			Expect(device.Status.Summary.Status).To(Equal(v1alpha1.DeviceSummaryStatusOnline))

			imageName := sleepAppImageName(harness, "v1")

			updateDevice(harness, deviceId, func(device *v1alpha1.Device) {
				var applicationConfig = v1alpha1.ImageApplicationProviderSpec{
					Image: imageName,
				}

				var appSpec v1alpha1.ApplicationProviderSpec
				err := appSpec.FromImageApplicationProviderSpec(applicationConfig)
				Expect(err).ToNot(HaveOccurred())

				device.Spec.Applications = &[]v1alpha1.ApplicationProviderSpec{appSpec}
				logrus.Infof("Updating %s with application %s", deviceId, imageName)
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
			Expect(device.Status.ApplicationsSummary.Status).To(Equal(v1alpha1.ApplicationsSummaryStatusHealthy))

			By("Check the containers are running in the device")
			output, err := harness.CheckRunningContainers()
			Expect(err).ToNot(HaveOccurred())
			Expect(output).To(ContainSubstring(ExpectedNumSleepAppV1Containers))

			By("Update an application image tag")

			imageName = sleepAppImageName(harness, "v2")

			updateDevice(harness, deviceId, func(device *v1alpha1.Device) {
				applicationVars := map[string]string{
					"FFO":      "FFO",
					"SIMPLE":   "SIMPLE",
					"SOME_KEY": "SOME_KEY",
				}

				applicationConfig := v1alpha1.ImageApplicationProviderSpec{
					Image: imageName,
				}

				var appSpec v1alpha1.ApplicationProviderSpec
				err := appSpec.FromImageApplicationProviderSpec(applicationConfig)
				Expect(err).ToNot(HaveOccurred())

				appSpec.EnvVars = &applicationVars

				device.Spec.Applications = &[]v1alpha1.ApplicationProviderSpec{appSpec}
				logrus.Infof("Updating %s with application %s", deviceId, imageName)
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
			logrus.Infof("Removing all the applications from %s", deviceId)

			updateDevice(harness, deviceId, func(device *v1alpha1.Device) {
				device.Spec.Applications = &[]v1alpha1.ApplicationProviderSpec{}
				logrus.Infof("Updating %s removing application %s", deviceId, imageName)
			})
			Expect(err).ToNot(HaveOccurred())

			By("Check all the containers are deleted")
			verifyContainerCount(harness, 0)
		})

		It("Should handle application volumes from images correctly", Label("83000"), func() {
			By("Update the application to include artifact volumes")

			imageName := sleepAppImageName(harness, "v3")

			updateDevice(harness, deviceId, func(device *v1alpha1.Device) {
				volumeConfig := v1alpha1.ApplicationVolume{
					Name: "testvol",
				}
				err := volumeConfig.FromImageVolumeProviderSpec(
					v1alpha1.ImageVolumeProviderSpec{
						Image: v1alpha1.ImageVolumeSource{
							// This contains a single tar.gz file layer called sqlite--3.50.2.x86_64_linux.bottle.tar.gz
							Reference:  "ghcr.io/homebrew/core/sqlite:3.50.2",
							PullPolicy: lo.ToPtr(v1alpha1.PullIfNotPresent),
						},
					})
				Expect(err).ToNot(HaveOccurred())

				appConfig := v1alpha1.ImageApplicationProviderSpec{
					Image:   imageName,
					Volumes: &[]v1alpha1.ApplicationVolume{volumeConfig},
				}

				var appSpec v1alpha1.ApplicationProviderSpec
				err = appSpec.FromImageApplicationProviderSpec(appConfig)
				Expect(err).ToNot(HaveOccurred())

				device.Spec.Applications = &[]v1alpha1.ApplicationProviderSpec{appSpec}
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
			imageName = sleepAppImageName(harness, "v2")

			updateDevice(harness, deviceId, func(device *v1alpha1.Device) {
				appConfig := v1alpha1.ImageApplicationProviderSpec{
					Image: imageName,
				}

				var appSpec v1alpha1.ApplicationProviderSpec
				err := appSpec.FromImageApplicationProviderSpec(appConfig)
				Expect(err).ToNot(HaveOccurred())

				device.Spec.Applications = &[]v1alpha1.ApplicationProviderSpec{appSpec}
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
			logrus.Infof("Updated Device Resource: %+v", updatedDevice)

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
			Eventually(func() v1alpha1.ApplicationStatusType {
				status, err := harness.CheckApplicationStatus(deviceId, inlineAppName)
				Expect(err).ToNot(HaveOccurred())
				return status
			}, TIMEOUT).Should(Equal(v1alpha1.ApplicationStatusRunning))
			newRenderedVersion, err = harness.PrepareNextDeviceVersion(deviceId)
			Expect(err).ToNot(HaveOccurred())
			inlineAppComposeYaml = fmt.Sprintf(inlineAppComposeYamlUpdated, NginxImage)
			inlineApp = createInlineApplicationSpec(inlineAppComposeYaml, fileName)

			By("Update the application with the new compose file")
			err = harness.UpdateDevice(deviceId, func(d *v1alpha1.Device) {
				err := updateDeviceApplicationFromInline(d, inlineAppName, inlineApp)
				if err != nil {
					logrus.Errorf("Failed to update application %s on device %s: %v", inlineAppName, deviceId, err)
				} else {
					logrus.Infof("Successfully updated application %s on device %s", inlineAppName, deviceId)
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
			err = harness.UpdateDeviceWithRetries(deviceId, func(device *v1alpha1.Device) {
				device.Spec.Applications = &[]v1alpha1.ApplicationProviderSpec{}
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
			inlineApp = v1alpha1.InlineApplicationProviderSpec{
				Inline: []v1alpha1.ApplicationContent{
					{
						Content:         &invalidContent,
						Path:            "Chart.yaml",
						ContentEncoding: lo.ToPtr(v1alpha1.EncodingType("base64")),
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
