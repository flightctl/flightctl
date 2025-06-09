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

var _ = Describe("VM Agent behaviour during the application lifecycle", func() {
	var (
		ctx      context.Context
		harness  *e2e.Harness
		deviceId string
		device   *v1alpha1.Device
		extIP    string
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
			response := harness.GetDeviceWithStatusSystem(deviceId)
			Expect(response).ToNot(BeNil())
			device = response.JSON200
			Expect(device.Status.Summary.Status).To(Equal(v1alpha1.DeviceSummaryStatusOnline))

			// Get the next expected rendered version
			newRenderedVersion, err := harness.PrepareNextDeviceVersion(deviceId)
			Expect(err).ToNot(HaveOccurred())

			// Get the application url in the local registry and create the application config
			extIP = harness.RegistryEndpoint()
			sleepAppImage := fmt.Sprintf("%s/sleep-app:v1", extIP)
			var applicationConfig = v1alpha1.ImageApplicationProviderSpec{
				Image: sleepAppImage,
			}

			// Update the device with the application config
			harness.UpdateDeviceWithRetries(deviceId, func(device *v1alpha1.Device) {

				// Create applicationSpec.
				var applicationSpec v1alpha1.ApplicationProviderSpec
				err := applicationSpec.FromImageApplicationProviderSpec(applicationConfig)
				Expect(err).ToNot(HaveOccurred())

				device.Spec.Applications = &[]v1alpha1.ApplicationProviderSpec{applicationSpec}
				logrus.Infof("Updating %s with application %s", deviceId, sleepAppImage)
			})

			logrus.Infof("Waiting for the device to pick the config")
			err = harness.WaitForDeviceNewRenderedVersion(deviceId, newRenderedVersion)
			Expect(err).ToNot(HaveOccurred())

			By("Check that the application compose is copied to the device")
			manifestPath := fmt.Sprintf("%s/%s", ComposeManifestPath, sleepAppImage)
			stdout, err := harness.VM.RunSSH([]string{"ls", manifestPath}, nil)
			Expect(err).ToNot(HaveOccurred())
			Expect(stdout.String()).To(ContainSubstring(ComposeFile))

			By("Wait for the reported application running status in the device")
			WaitForApplicationRunningStatus(harness, deviceId, sleepAppImage)

			By("Check the general device application status")
			Expect(device.Status.ApplicationsSummary.Status).To(Equal(v1alpha1.ApplicationsSummaryStatusHealthy))

			By("Check the containers are running in the device")
			output, err := harness.CheckRunningContainers()
			Expect(err).ToNot(HaveOccurred())
			Expect(output).To(ContainSubstring(ExpectedNumSleepAppV1Containers))

			By("Update an application image tag")
			repo, tag := parseImageReference(sleepAppImage)
			Expect(repo).ToNot(Equal(""))
			Expect(tag).ToNot(Equal(""))

			logrus.Infof("Updating from tag %s to v2", tag)

			updateImage := repo + ":v2"
			updateApplicationConfig := v1alpha1.ImageApplicationProviderSpec{
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
				var updateApplicationSpec v1alpha1.ApplicationProviderSpec
				err := updateApplicationSpec.FromImageApplicationProviderSpec(updateApplicationConfig)
				Expect(err).ToNot(HaveOccurred())

				updateApplicationSpec.EnvVars = &applicationVars

				device.Spec.Applications = &[]v1alpha1.ApplicationProviderSpec{updateApplicationSpec}
				logrus.Infof("Updating %s with application %s", deviceId, updateImage)
			})

			By("Check that the device received the new config")
			err = harness.WaitForDeviceNewRenderedVersion(deviceId, newRenderedVersion)
			Expect(err).ToNot(HaveOccurred())

			By("Wait for the application running status")
			WaitForApplicationRunningStatus(harness, deviceId, updateImage)

			By("Check that the new application containers are running")
			out, err := harness.CheckRunningContainers()
			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(ContainSubstring(ExpectedNumSleepAppV2Containers))

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

				device.Spec.Applications = &[]v1alpha1.ApplicationProviderSpec{}
				logrus.Infof("Updating %s removing application %s", deviceId, updateImage)
			})

			By("Check that the device received the new config")
			err = harness.WaitForDeviceNewRenderedVersion(deviceId, newRenderedVersion)
			Expect(err).ToNot(HaveOccurred())

			By("Check all the containers are deleted")
			out, err = harness.CheckRunningContainers()
			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(ContainSubstring(ZeroContainers))

		})

		It("should install an inline compose application and manage its lifecycle with env vars", Label("80990", "sanity"), func() {
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
			harness.UpdateDeviceWithRetries(deviceId, func(device *v1alpha1.Device) {
				device.Spec.Applications = &[]v1alpha1.ApplicationProviderSpec{}
			})

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

		It("Agent pre-update validations should fail the version, and trigger the rollback for various invalid configurations", Label("80998", "sanity"), func() {
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

	// ApplicationRunningStatus represents the status string used to signify that an application is currently running.
	ApplicationRunningStatus = "status: Running"

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

// WaitForApplicationRunningStatus waits for a specific application on a device to reach the "Running" status within a timeout.
func WaitForApplicationRunningStatus(h *e2e.Harness, deviceId string, applicationImage string) {
	logrus.Infof("Waiting for the application ready status")
	h.WaitForDeviceContents(deviceId, ApplicationRunningStatus,
		func(device *v1alpha1.Device) bool {
			for _, application := range device.Status.Applications {
				if application.Name == applicationImage && application.Status == v1alpha1.ApplicationStatusRunning {
					return true
				}
			}
			return false
		}, TIMEOUT)
}

func createInlineApplicationSpec(content string, path string) v1alpha1.InlineApplicationProviderSpec {
	return v1alpha1.InlineApplicationProviderSpec{
		Inline: []v1alpha1.ApplicationContent{
			{
				Content: &content,
				Path:    path,
			},
		},
	}
}

func updateDeviceApplicationFromInline(device *v1alpha1.Device, inlineAppName string, inlineApp v1alpha1.InlineApplicationProviderSpec) error {
	for i, app := range *device.Spec.Applications {
		if app.Name != nil && *app.Name == inlineAppName {
			err := (*device.Spec.Applications)[i].FromInlineApplicationProviderSpec(inlineApp)
			if err != nil {
				return fmt.Errorf("failed to update application %s from inline spec: %w", inlineAppName, err)
			}
			return nil
		}
	}
	return fmt.Errorf("application %s not found in device spec", inlineAppName)
}
