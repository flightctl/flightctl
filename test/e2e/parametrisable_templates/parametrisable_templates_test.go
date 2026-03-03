package parametrisabletemplates

import (
	"fmt"
	"strings"
	"time"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/test/harness/e2e"
	testutil "github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Template variables in the device configuration", func() {
	var (
		deviceId string
		testID   string
	)

	BeforeEach(func() {
		// Get harness directly - no shared package-level variable
		harness := e2e.GetWorkerHarness()
		deviceId, _ = harness.EnrollAndWaitForOnlineStatus()
		testID = harness.GetTestIDFromContext()
	})

	Context("parametrisable_templates", func() {
		It(`Verifies that Flightctl fleet resource supports parameterizable device
		    templates to configure items that are specific to an individual device
			or a group of devices selected by labels`, Label("75486"), func() {
			// Get harness directly - no shared package-level variable
			harness := e2e.GetWorkerHarness()

			By("Create a fleet with template variables in InlineConfigProviderSpec")
			err := configProviderSpec.FromInlineConfigProviderSpec(inlineConfigValidWithFunction)
			Expect(err).ToNot(HaveOccurred())
			fleetTestName := fmt.Sprintf("fleet-test-%s", testID)
			err = harness.CreateTestFleetWithConfig(fleetTestName, testFleetSelector, configProviderSpec)
			Expect(err).ToNot(HaveOccurred())

			By("Check that the device status is Online")
			_, err = harness.CheckDeviceStatus(deviceId, v1beta1.DeviceSummaryStatusOnline)
			Expect(err).ToNot(HaveOccurred())

			By("Add the fleet selector and the team label to the device")
			nextRenderedVersion, err := harness.PrepareNextDeviceVersion(deviceId)
			Expect(err).ToNot(HaveOccurred())

			err = harness.UpdateDeviceWithRetries(deviceId, func(device *v1beta1.Device) {
				harness.SetLabelsForDeviceMetadata(&device.Metadata, map[string]string{
					fleetSelectorKey: fleetSelectorValue,
					teamLabelKey:     teamLabelValue,
				})
				GinkgoWriter.Printf("Updating %s with label %s=%s and %s=%s\n", deviceId,
					fleetSelectorKey, fleetSelectorValue, teamLabelKey, teamLabelValue)
			})
			Expect(err).ToNot(HaveOccurred())

			By("Verify the Device is updated with the labels")
			device, err := harness.GetDevice(deviceId)
			Expect(err).ToNot(HaveOccurred())
			responseSelectorLabelValue := (*device.Metadata.Labels)[fleetSelectorKey]
			Expect(responseSelectorLabelValue).To(ContainSubstring(fleetSelectorValue))
			responseTeamLabelValue := (*device.Metadata.Labels)[teamLabelKey]
			Expect(responseTeamLabelValue).To(ContainSubstring(teamLabelValue))

			By("Wait for the device to get the fleet configuration")
			err = harness.WaitForDeviceNewRenderedVersion(deviceId, nextRenderedVersion)
			Expect(err).ToNot(HaveOccurred())

			By("Verify that the template variable is replaced in the configuration update")
			device, err = harness.GetDevice(deviceId)
			Expect(err).ToNot(HaveOccurred())

			inlineConfigPathResponse, err := harness.GetDeviceInlineConfig(device, inlineConfigName)
			Expect(err).ToNot(HaveOccurred())
			if len(inlineConfigPathResponse.Inline) > 0 {
				Expect(inlineConfigPathResponse.Inline[0].Path).To(ContainSubstring(teamLabelValue))
				Expect(inlineConfigPathResponse.Inline[0].Content).To(ContainSubstring(defaultTeamLabelValue))
			}
		})

		It(`Verifies that if a device is missing a parametrisable device label
		    an error is generated, but it will reconcile if the label is provided`,
			Label("75600", "sanity"), func() {
				// Get harness directly - no shared package-level variable
				harness := e2e.GetWorkerHarness()

				By("Check the device status is Online")
				_, err := harness.CheckDeviceStatus(deviceId, v1beta1.DeviceSummaryStatusOnline)
				Expect(err).ToNot(HaveOccurred())

				By("Create a fleet with a template variable")
				err = configProviderSpec.FromInlineConfigProviderSpec(inlineConfigValidWithFunction)
				Expect(err).ToNot(HaveOccurred())
				fleetTestName := fmt.Sprintf("fleet-test-%s", testID)
				err = harness.CreateTestFleetWithConfig(fleetTestName, testFleetSelector, configProviderSpec)
				Expect(err).ToNot(HaveOccurred())

				By("Associate the device to the fleet without adding the team label")
				nextRenderedVersion, err := harness.PrepareNextDeviceVersion(deviceId)
				Expect(err).ToNot(HaveOccurred())

				err = harness.UpdateDeviceWithRetries(deviceId, func(device *v1beta1.Device) {

					(*device.Metadata.Labels)[fleetSelectorKey] = fleetSelectorValue
					GinkgoWriter.Printf("Updating %s with label %s=%s\n", deviceId,
						fleetSelectorKey, fleetSelectorValue)
				})
				Expect(err).ToNot(HaveOccurred())

				By("Check the device will fail to reconcile")
				harness.WaitForDeviceContents(deviceId, deviceCouldNotBeUpdatedToFleetMsg, isDeviceUpdatedStatusOutOfDate, testutil.TIMEOUT_5M)

				By("Verify fleet controller error annotation is set")
				Eventually(func() error {
					resp, err := harness.Client.GetDeviceStatusWithResponse(harness.Context, deviceId)
					if err != nil {
						return err
					}
					device := resp.JSON200
					if device.Status.Updated.Status != v1beta1.DeviceUpdatedStatusOutOfDate {
						return fmt.Errorf("device status is not OutOfDate")
					}
					if device.Metadata.Annotations == nil {
						return fmt.Errorf("device annotations are nil")
					}
					errorAnnotation, exists := (*device.Metadata.Annotations)["fleet-controller/lastRolloutError"]
					if !exists || errorAnnotation == "" {
						return fmt.Errorf("fleet-controller/lastRolloutError annotation not set")
					}
					if !strings.Contains(errorAnnotation, "no entry for key \"team\"") {
						return fmt.Errorf("fleet-controller/lastRolloutError annotation does not contain expected error message")
					}
					return nil
				}, 30*time.Second, 1*time.Second).Should(BeNil(), "Fleet controller error annotation should be set with correct error message")

				resp, err := harness.Client.GetDeviceStatusWithResponse(harness.Context, deviceId)
				Expect(err).ToNot(HaveOccurred())
				device := resp.JSON200
				Expect((*device.Metadata.Annotations)["fleet-controller/lastRolloutError"]).NotTo(BeNil())
				Expect(device.Status.Updated.Status).To(Equal(v1beta1.DeviceUpdatedStatusOutOfDate))
				Expect((*device.Metadata.Annotations)["fleet-controller/lastRolloutError"]).To(ContainSubstring("no entry for key \"team\""))

				By("Add the team label to the device")
				err = harness.UpdateDeviceWithRetries(deviceId, func(device *v1beta1.Device) {

					(*device.Metadata.Labels)[teamLabelKey] = teamLabelValue
					GinkgoWriter.Printf("Updating %s with label %s=%s\n", deviceId,
						teamLabelKey, teamLabelValue)
				})
				Expect(err).ToNot(HaveOccurred())
				resp, err = harness.Client.GetDeviceStatusWithResponse(harness.Context, deviceId)
				Expect(err).ToNot(HaveOccurred())
				device = resp.JSON200
				Expect((*device.Metadata.Labels)[teamLabelKey]).To(ContainSubstring(teamLabelValue))

				By("Check the device now reconciles")
				err = harness.WaitForDeviceNewRenderedVersion(deviceId, nextRenderedVersion)
				Expect(err).ToNot(HaveOccurred())

				By("Verify that the template variable is replaced in the configuration update")
				device, err = harness.GetDevice(deviceId)
				Expect(err).ToNot(HaveOccurred())
				inlineConfigResponse, err := harness.GetDeviceInlineConfig(device, inlineConfigName)
				Expect(err).ToNot(HaveOccurred())
				if len(inlineConfigResponse.Inline) > 0 {
					Expect(inlineConfigResponse.Inline[0].Path).To(ContainSubstring(teamLabelValue))
					Expect(inlineConfigResponse.Inline[0].Content).To(ContainSubstring(defaultTeamLabelValue))
				}
			})

		It(`Verifies that the template variables are replaced in the different configurations
		    and work with the helper functions`,
			Label("78684", "sanity"), func() {
				// Get harness directly - no shared package-level variable
				harness := e2e.GetWorkerHarness()

				By("Check the device status")
				_, err := harness.CheckDeviceStatus(deviceId, v1beta1.DeviceSummaryStatusOnline)
				Expect(err).ToNot(HaveOccurred())

				By("Create a git and a http repository")
				repoTestName := fmt.Sprintf("git-repo-%s", testID)
				gitMetadata.Name = &repoTestName
				err = harness.CreateRepository(gitRepositorySpec, gitMetadata)
				Expect(err).ToNot(HaveOccurred())
				GinkgoWriter.Printf("Created git repository %s\n", *gitMetadata.Name)

				httpRepoName := fmt.Sprintf("http-repo-%s", testID)
				httpRepoMetadata.Name = &httpRepoName
				err = harness.CreateRepository(httpRepositoryspec, httpRepoMetadata)
				Expect(err).ToNot(HaveOccurred())
				GinkgoWriter.Printf("Created http repository %s\n", *httpRepoMetadata.Name)

				By("Create the device spec adding inline. git, http configs")
				// Create git config with dynamic repository name
				gitConfigWithRepo := gitConfigvalid
				gitConfigWithRepo.GitRef.Repository = repoTestName

				// Create http config with dynamic repository name
				httpConfigWithRepo := httpConfigvalid
				httpConfigWithRepo.HttpRef.Repository = httpRepoName

				httpConfigProviderSpec := v1beta1.ConfigProviderSpec{}
				err = httpConfigProviderSpec.FromHttpConfigProviderSpec(httpConfigWithRepo)
				Expect(err).ToNot(HaveOccurred())

				gitConfigProviderSpec := v1beta1.ConfigProviderSpec{}
				err = gitConfigProviderSpec.FromGitConfigProviderSpec(gitConfigWithRepo)
				Expect(err).ToNot(HaveOccurred())

				inlineConfigProviderSpec := v1beta1.ConfigProviderSpec{}
				err = inlineConfigProviderSpec.FromInlineConfigProviderSpec(inlineConfigValid)
				Expect(err).ToNot(HaveOccurred())

				configProviderSpec := []v1beta1.ConfigProviderSpec{gitConfigProviderSpec, inlineConfigProviderSpec, httpConfigProviderSpec}

				GinkgoWriter.Printf("this is the configProviderSpec %s\n", configProviderSpec)
				deviceImage := fmt.Sprintf("%s:{{ .metadata.labels.alias }}", testutil.NewDeviceImageReference("").String())

				var osImageSpec = v1beta1.DeviceOsSpec{
					Image: deviceImage,
				}

				deviceSpec.Os = &osImageSpec
				deviceSpec.Config = &configProviderSpec

				By("Create a fleet with parametrisable templates in the os image, inlineconfig, gitconfig")
				fleetTestName := fmt.Sprintf("fleet-test-%s", testID)
				err = harness.CreateOrUpdateTestFleet(fleetTestName, testFleetSelector, deviceSpec)
				Expect(err).ToNot(HaveOccurred())

				By("Add all the labels to the device")
				nextRenderedVersion, err := harness.PrepareNextDeviceVersion(deviceId)
				Expect(err).ToNot(HaveOccurred())

				err = harness.UpdateDeviceWithRetries(deviceId, func(device *v1beta1.Device) {

					harness.SetLabelsForDeviceMetadata(&device.Metadata, map[string]string{
						fleetSelectorKey: fleetSelectorValue,
						teamLabelKey:     teamLabelValue,
						aliasKey:         deviceAlias,
						configLabelKey:   configLabelValue,
						revisionLabelKey: revisionLabelValue,
						suffixLabelKey:   suffixLabelValue,
					})
					GinkgoWriter.Printf("Updating %s with labels\n", deviceId)
				})
				Expect(err).ToNot(HaveOccurred())

				By("Wait for the device to pick the fleet config")
				err = harness.WaitForDeviceNewRenderedVersion(deviceId, nextRenderedVersion)
				Expect(err).ToNot(HaveOccurred())

				By("Check that the template variables are replaced in the device configurations")
				device, err := harness.GetDevice(deviceId)
				Expect(err).ToNot(HaveOccurred())

				inlineConfigResponse, err := harness.GetDeviceInlineConfig(device, inlineConfigName)
				Expect(err).ToNot(HaveOccurred())
				if len(inlineConfigResponse.Inline) > 0 {
					Expect(inlineConfigResponse.Inline[0].Path).To(ContainSubstring(teamLabelValue))
					Expect(inlineConfigResponse.Inline[0].Content).To(ContainSubstring(teamLabelValue))
				}

				gitConfigResponse, err := harness.GetDeviceGitConfig(device, gitConfigName)
				Expect(err).ToNot(HaveOccurred())
				Expect(gitConfigResponse.GitRef.TargetRevision).To(ContainSubstring(revisionLabelValue))
				httpConfigResponse, err := harness.GetDeviceHttpConfig(device, httpConfigName)
				Expect(err).ToNot(HaveOccurred())
				Expect(httpConfigResponse.HttpRef.FilePath).To(ContainSubstring(configLabelValue))
				Expect(*httpConfigResponse.HttpRef.Suffix).To(ContainSubstring(suffixLabelValue))

				By("Check that the template variable is replaced in the device os image")
				deviceOsImage, err := harness.GetDeviceOsImage(device)
				Expect(err).ToNot(HaveOccurred())
				Expect(deviceOsImage).To(ContainSubstring(deviceAlias))

				By("Test that the template variable is replaced in target-revision parameter")
				nextGeneration, err := harness.PrepareNextDeviceGeneration(deviceId)
				Expect(err).ToNot(HaveOccurred())

				err = harness.UpdateDeviceWithRetries(deviceId, func(device *v1beta1.Device) {
					(*device.Metadata.Labels)[revisionLabelKey] = branchTargetRevision
					GinkgoWriter.Printf("Updating the device with label %s=%s\n", revisionLabelKey, branchTargetRevision)
				})
				Expect(err).ToNot(HaveOccurred())

				err = harness.WaitForDeviceNewGeneration(deviceId, nextGeneration)
				Expect(err).ToNot(HaveOccurred())

				gitConfigResponse, err = harness.GetDeviceGitConfig(device, gitConfigName)
				Expect(err).ToNot(HaveOccurred())
				Expect(gitConfigResponse.GitRef.TargetRevision).To(ContainSubstring(revisionLabelValue))

				By("Update the fleet inline config with a template variable with getOrDefault function")
				nextRenderedVersion, err = harness.PrepareNextDeviceVersion(deviceId)
				Expect(err).ToNot(HaveOccurred())

				inlineConfigProviderSpec = v1beta1.ConfigProviderSpec{}
				err = inlineConfigProviderSpec.FromInlineConfigProviderSpec(inlineConfigValid)
				Expect(err).ToNot(HaveOccurred())

				deviceSpec.Config = &[]v1beta1.ConfigProviderSpec{inlineConfigProviderSpec}

				err = harness.CreateOrUpdateTestFleet(fleetTestName, testFleetSelector, deviceSpec)
				Expect(err).ToNot(HaveOccurred())

				err = harness.WaitForDeviceNewRenderedVersion(deviceId, nextRenderedVersion)
				Expect(err).ToNot(HaveOccurred())

				By("Remove the team label from the device and check that the config variable is replaced by the default")
				nextGeneration, err = harness.PrepareNextDeviceGeneration(deviceId)
				Expect(err).ToNot(HaveOccurred())

				GinkgoWriter.Printf("Removing %s labels\n", teamLabelKey)
				err = harness.UpdateDeviceWithRetries(deviceId, func(device *v1beta1.Device) {
					harness.SetLabelsForDeviceMetadata(&device.Metadata, map[string]string{
						fleetSelectorKey: fleetSelectorValue,
						aliasKey:         deviceAlias,
						configLabelKey:   configLabelValue,
						revisionLabelKey: revisionLabelValue,
						suffixLabelKey:   suffixLabelValue,
					})
					GinkgoWriter.Printf("Updating %s with labels\n", deviceId)
				})
				Expect(err).ToNot(HaveOccurred())

				By("Wait for the device to pick the fleet config")
				err = harness.WaitForDeviceNewGeneration(deviceId, nextGeneration)
				Expect(err).ToNot(HaveOccurred())

				By("Check that the default variables are replaced in the config")
				device, err = harness.GetDevice(deviceId)
				Expect(err).ToNot(HaveOccurred())

				GinkgoWriter.Printf("The device labels are %s\n", *device.Metadata.Labels)

				inlineConfigResponse, err = harness.GetDeviceInlineConfig(device, inlineConfigName)
				Expect(err).ToNot(HaveOccurred())
				if len(inlineConfigResponse.Inline) > 0 {
					Expect(inlineConfigResponse.Inline[0].Path).To(ContainSubstring(defaultTeamLabelValue))
					Expect(inlineConfigResponse.Inline[0].Content).To(ContainSubstring(defaultTeamLabelValue))
				}

			})

		It(`Verifies that we can add parametrizable templates variables in the fleets device's application configuration`,
			Label("87803", "sanity"), func() {
				harness := e2e.GetWorkerHarness()
				fleetTestName := fmt.Sprintf("templated-app-fleet-%s", testID)

				By("Check that the device status is Online")
				_, err := harness.CheckDeviceStatus(deviceId, v1beta1.DeviceSummaryStatusOnline)
				Expect(err).ToNot(HaveOccurred())

				By("Create a fleet with parametrisable application templates")
				err = harness.CreateOrUpdateTestFleet(fleetTestName, appFleetSelector, templatedDeviceSpec)
				Expect(err).ToNot(HaveOccurred())

				By("Add labels to the device to associate it with the fleet and provide template values")
				nextRenderedVersion, err := harness.PrepareNextDeviceVersion(deviceId)
				Expect(err).ToNot(HaveOccurred())

				err = harness.UpdateDeviceWithRetries(deviceId, func(device *v1beta1.Device) {
					harness.SetLabelsForDeviceMetadata(&device.Metadata, map[string]string{
						appFleetSelectorKey: appFleetSelectorValue,
						nginxLabelKey:       nginxTag,
						inlineLabelKey:      inlineTag,
						volrefLabelKey:      volrefTag,
					})
					GinkgoWriter.Printf("Updating %s with labels app=%s nginx=%s inline=%s volref=%s\n",
						deviceId, appFleetSelectorValue, nginxTag, inlineTag, volrefTag)
				})
				Expect(err).ToNot(HaveOccurred())

				By("Wait for the device to receive the fleet configuration")
				err = harness.WaitForDeviceNewRenderedVersion(deviceId, nextRenderedVersion)
				Expect(err).ToNot(HaveOccurred())

				By("Verify that template variables are rendered in the device applications")
				containerImage, volumeRef, inlineImage, err := getRenderedAppRefs(harness, deviceId)
				Expect(err).ToNot(HaveOccurred())
				Expect(containerImage).To(Equal(fmt.Sprintf("%s:%s", nginxImage, nginxTag)))
				Expect(volumeRef).To(Equal(fmt.Sprintf("%s:%s", sqliteImage, volrefTag)))
				Expect(inlineImage).To(ContainSubstring(fmt.Sprintf("%s:%s", alpineImage, inlineTag)))
				Expect(containerImage).ToNot(ContainSubstring("{{"))
				Expect(volumeRef).ToNot(ContainSubstring("{{"))
				Expect(inlineImage).ToNot(ContainSubstring("{{"))

				By("Ensure that all applications start properly")
				harness.WaitForApplicationRunningStatus(deviceId, containerAppName)
				harness.WaitForApplicationRunningStatus(deviceId, inlineAppName)
				harness.WaitForRunningApplicationsCount(deviceId, 2)
				harness.WaitForApplicationsSummaryStatus(deviceId, v1beta1.ApplicationsSummaryStatusHealthy)

				By("Update the fleet template removing templated image references")
				nextRenderedVersion, err = harness.PrepareNextDeviceVersion(deviceId)
				Expect(err).ToNot(HaveOccurred())

				harness.UpdateFleetWithRetries(fleetTestName, func(fleet *v1beta1.Fleet) {
					fleet.Spec.Template.Spec = nonTemplatedDeviceSpec
				})

				By("Wait for the device to pick up the updated fleet configuration")
				err = harness.WaitForDeviceNewRenderedVersion(deviceId, nextRenderedVersion)
				Expect(err).ToNot(HaveOccurred())

				By("Ensure the device is updated without any templating occurring")
				containerImage, volumeRef, inlineImage, err = getRenderedAppRefs(harness, deviceId)
				Expect(err).ToNot(HaveOccurred())
				Expect(containerImage).To(Equal(fmt.Sprintf("%s:%s", nginxImage, nginxTagFixed)))
				Expect(volumeRef).To(Equal(fmt.Sprintf("%s:%s", sqliteImage, volrefTagFixed)))
				Expect(inlineImage).To(ContainSubstring(fmt.Sprintf("%s:%s", alpineImageFixed, inlineTagFixed)))
				Expect(containerImage).ToNot(ContainSubstring("{{"))
				Expect(volumeRef).ToNot(ContainSubstring("{{"))
				Expect(inlineImage).ToNot(ContainSubstring("{{"))

				By("Ensure that all applications are running after the update")
				harness.WaitForApplicationRunningStatus(deviceId, containerAppName)
				harness.WaitForApplicationRunningStatus(deviceId, inlineAppName)
				harness.WaitForRunningApplicationsCount(deviceId, 2)
				harness.WaitForApplicationsSummaryStatus(deviceId, v1beta1.ApplicationsSummaryStatusHealthy)
			})
	})
})

var (
	fleetSelectorKey          = "fleet"
	fleetSelectorValue        = "test"
	inlinePath                = "/var/home/user/{{ getOrDefault .metadata.labels \"team\" \"c\" }}.txt"
	inlineContent             = "{{ getOrDefault .metadata.labels \"team\" \"c\" }}"
	teamLabelKey              = "team"
	inlineConfigName          = "inline-config"
	teamLabelValue            = "a"
	defaultTeamLabelValue     = "c"
	contentWithFunction       = "{{ replace \"a\" \"c\" .metadata.labels.team }}"
	pathWithFunction          = "/var/home/user/{{ upper .metadata.labels.team | lower }}/test.txt"
	repoTestUrl               = "https://github.com/flightctl/flightctl-demos"
	deviceAlias               = "base"
	branchTargetRevision      = "demo"
	gitRepoConfigPath         = "/demos/basic-nginx-demo/configuration"
	httpConfigPath            = "/var/home/user/{{ .metadata.labels.config }}"
	configLabelKey            = "config"
	configLabelValue          = "fedora-bootc"
	revisionLabelKey          = "revision"
	revisionLabelValue        = "c5ff21b9a8116bb5daf72c8f07b67449c221b596"
	suffix                    = "{{ .metadata.labels.suffix }}"
	gitConfigName             = "git-config"
	httpConfigName            = "http-config"
	revision                  = "{{ .metadata.labels.revision }}"
	suffixLabelValue          = ""
	suffixLabelKey            = "suffix"
	aliasKey                  = "alias"
	appFleetSelectorKey       = "app"
	appFleetSelectorValue     = "my-templated-app"
	containerAppName          = "my-app"
	inlineAppName             = "inline-app"
	nginxLabelKey             = "nginx"
	nginxTag                  = "alpine"
	nginxTagFixed             = "latest"
	inlineLabelKey            = "inline"
	inlineTag                 = "v1"
	volrefLabelKey            = "volref"
	volrefTag                 = "3.50.2"
	inlineTagFixed            = "3.19"
	volrefTagFixed            = "3.46.0"
	nginxImage                = "docker.io/library/nginx"
	alpineImage               = "quay.io/flightctl-tests/alpine"
	alpineImageFixed          = "docker.io/library/alpine"
	sqliteImage               = "ghcr.io/homebrew/core/sqlite"
	volumeName                = "data"
	volumeMountPath           = "/mnt/data"
	containerCPU              = "0.5"
	containerMemory           = "256m"
	inlineAppEnvVars          = map[string]string{"LOG_MESSAGE": "Hello from FlightControl (Inline Ref)"}
	pullPolicy                = v1beta1.PullIfNotPresent
	templatedNginxImage       = nginxImage + ":{{ .metadata.labels." + nginxLabelKey + " }}"
	templatedSqliteRef        = sqliteImage + ":{{ .metadata.labels." + volrefLabelKey + " }}"
	templatedAlpineImage      = alpineImage + ":{{ .metadata.labels." + inlineLabelKey + " }}"
	fixedNginxImage           = nginxImage + ":" + nginxTagFixed
	fixedSqliteRef            = sqliteImage + ":" + volrefTagFixed
	fixedAlpineImage          = alpineImageFixed + ":" + inlineTagFixed
	templatedInlineContent    = "[Container]\nImage=" + templatedAlpineImage + "\nExec=sleep infinity\n\n[Install]\nWantedBy=default.target\n"
	nonTemplatedInlineContent = "[Container]\nImage=" + fixedAlpineImage + "\nExec=sleep infinity\n\n[Install]\nWantedBy=default.target\n"

	deviceCouldNotBeUpdatedToFleetMsg = "The device could not be updated to the fleet"
)

var mode = 0644
var modePointer = &mode

var inlineConfigSpec = v1beta1.FileSpec{
	Path:    inlinePath,
	Mode:    modePointer,
	Content: inlineContent,
}

var inlineConfigWithFunctionSpec = v1beta1.FileSpec{
	Path:    pathWithFunction,
	Mode:    modePointer,
	Content: contentWithFunction,
}

var configProviderSpec v1beta1.ConfigProviderSpec

var inlineConfigValid = v1beta1.InlineConfigProviderSpec{
	Inline: []v1beta1.FileSpec{inlineConfigSpec},
	Name:   inlineConfigName,
}
var inlineConfigValidWithFunction = v1beta1.InlineConfigProviderSpec{
	Inline: []v1beta1.FileSpec{inlineConfigWithFunctionSpec},
	Name:   inlineConfigName,
}

var testFleetSelector = v1beta1.LabelSelector{
	MatchLabels: &map[string]string{fleetSelectorKey: fleetSelectorValue},
}

var deviceSpec v1beta1.DeviceSpec

var gitRepositorySpec v1beta1.RepositorySpec
var _ = gitRepositorySpec.FromGitRepoSpec(v1beta1.GitRepoSpec{
	Url:  repoTestUrl,
	Type: v1beta1.GitRepoSpecTypeGit,
})

var gitMetadata = v1beta1.ObjectMeta{
	Name:   nil, // Will be set dynamically in test
	Labels: &map[string]string{},
}

var httpRepoSpec = v1beta1.HttpRepoSpec{
	Type: v1beta1.HttpRepoSpecTypeHttp,
	Url:  repoTestUrl,
}

var httpRepositoryspec v1beta1.RepositorySpec

var _ = httpRepositoryspec.FromHttpRepoSpec(httpRepoSpec)

var httpRepoMetadata = v1beta1.ObjectMeta{
	Name: nil, // Will be set dynamically in test
}

var gitConfigvalid = v1beta1.GitConfigProviderSpec{
	GitRef: struct {
		Path           string `json:"path"`
		Repository     string `json:"repository"`
		TargetRevision string `json:"targetRevision"`
	}{
		Path:           gitRepoConfigPath,
		Repository:     "", // Will be set dynamically in test
		TargetRevision: revision,
	},
	Name: gitConfigName,
}

var httpConfigvalid = v1beta1.HttpConfigProviderSpec{
	HttpRef: struct {
		FilePath   string  `json:"filePath"`
		Repository string  `json:"repository"`
		Suffix     *string `json:"suffix,omitempty"`
	}{
		FilePath:   httpConfigPath,
		Repository: "", // Will be set dynamically in test
		Suffix:     &suffix,
	},
	Name: httpConfigName,
}

var appFleetSelector = v1beta1.LabelSelector{
	MatchLabels: &map[string]string{appFleetSelectorKey: appFleetSelectorValue},
}

// Templated volume
var templatedVol v1beta1.ApplicationVolume
var _ = func() v1beta1.ApplicationVolume {
	templatedVol = v1beta1.ApplicationVolume{Name: volumeName}
	_ = templatedVol.FromImageMountVolumeProviderSpec(v1beta1.ImageMountVolumeProviderSpec{
		Image: v1beta1.ImageVolumeSource{Reference: templatedSqliteRef, PullPolicy: &pullPolicy},
		Mount: v1beta1.VolumeMount{Path: volumeMountPath},
	})
	return templatedVol
}()

// Non-templated volume
var nonTemplatedVol v1beta1.ApplicationVolume
var _ = func() v1beta1.ApplicationVolume {
	nonTemplatedVol = v1beta1.ApplicationVolume{Name: volumeName}
	_ = nonTemplatedVol.FromImageMountVolumeProviderSpec(v1beta1.ImageMountVolumeProviderSpec{
		Image: v1beta1.ImageVolumeSource{Reference: fixedSqliteRef, PullPolicy: &pullPolicy},
		Mount: v1beta1.VolumeMount{Path: volumeMountPath},
	})
	return nonTemplatedVol
}()

// Templated container app
var templatedContainerApp v1beta1.ApplicationProviderSpec
var _ = func() v1beta1.ApplicationProviderSpec {
	vols := []v1beta1.ApplicationVolume{templatedVol}
	templatedContainerApp, _ = e2e.NewContainerApplicationSpec(
		containerAppName, templatedNginxImage,
		[]v1beta1.ApplicationPort{"8080:80"},
		&containerCPU, &containerMemory,
		&vols,
	)
	return templatedContainerApp
}()

// Non-templated container app
var nonTemplatedContainerApp v1beta1.ApplicationProviderSpec
var _ = func() v1beta1.ApplicationProviderSpec {
	vols := []v1beta1.ApplicationVolume{nonTemplatedVol}
	nonTemplatedContainerApp, _ = e2e.NewContainerApplicationSpec(
		containerAppName, fixedNginxImage,
		[]v1beta1.ApplicationPort{"8080:80"},
		&containerCPU, &containerMemory,
		&vols,
	)
	return nonTemplatedContainerApp
}()

// Templated inline quadlet app
var templatedInlineQuadletApp v1beta1.QuadletApplication
var _ = templatedInlineQuadletApp.FromInlineApplicationProviderSpec(v1beta1.InlineApplicationProviderSpec{
	Inline: []v1beta1.ApplicationContent{{Path: "app.container", Content: &templatedInlineContent}},
})

var templatedInlineApp v1beta1.ApplicationProviderSpec
var _ = func() v1beta1.ApplicationProviderSpec {
	templatedInlineQuadletApp.Name = &inlineAppName
	templatedInlineQuadletApp.AppType = v1beta1.AppTypeQuadlet
	templatedInlineQuadletApp.EnvVars = &inlineAppEnvVars
	_ = templatedInlineApp.FromQuadletApplication(templatedInlineQuadletApp)
	return templatedInlineApp
}()

// Non-templated inline quadlet app
var nonTemplatedInlineQuadletApp v1beta1.QuadletApplication
var _ = nonTemplatedInlineQuadletApp.FromInlineApplicationProviderSpec(v1beta1.InlineApplicationProviderSpec{
	Inline: []v1beta1.ApplicationContent{{Path: "app.container", Content: &nonTemplatedInlineContent}},
})

var nonTemplatedInlineApp v1beta1.ApplicationProviderSpec
var _ = func() v1beta1.ApplicationProviderSpec {
	nonTemplatedInlineQuadletApp.Name = &inlineAppName
	nonTemplatedInlineQuadletApp.AppType = v1beta1.AppTypeQuadlet
	nonTemplatedInlineQuadletApp.EnvVars = &inlineAppEnvVars
	_ = nonTemplatedInlineApp.FromQuadletApplication(nonTemplatedInlineQuadletApp)
	return nonTemplatedInlineApp
}()

var templatedDeviceSpec = v1beta1.DeviceSpec{
	Applications: &[]v1beta1.ApplicationProviderSpec{templatedContainerApp, templatedInlineApp},
}

var nonTemplatedDeviceSpec = v1beta1.DeviceSpec{
	Applications: &[]v1beta1.ApplicationProviderSpec{nonTemplatedContainerApp, nonTemplatedInlineApp},
}

// isDeviceUpdatedStatusOutOfDate returns true when the device status indicates it could not be updated to the fleet.
func isDeviceUpdatedStatusOutOfDate(device *v1beta1.Device) bool {
	if device == nil || device.Status == nil || device.Status.Updated.Status == "" {
		return false
	}
	return device.Status.Updated.Status == v1beta1.DeviceUpdatedStatusOutOfDate
}

// getRenderedAppRefs returns the container image, volume image ref, and inline app content from the device spec.
// It uses harness application helpers and expects exactly two apps: containerAppName and inlineAppName.
func getRenderedAppRefs(harness *e2e.Harness, deviceId string) (containerImage, volumeRef, inlineContent string, err error) {
	device, err := harness.GetDevice(deviceId)
	if err != nil {
		GinkgoWriter.Printf("getRenderedAppRefs: failed to get device %s: %v\n", deviceId, err)
		return "", "", "", err
	}
	if device == nil || device.Spec == nil || device.Spec.Applications == nil {
		GinkgoWriter.Printf("getRenderedAppRefs: device %s has nil Spec or Applications\n", deviceId)
		return "", "", "", fmt.Errorf("device %s has nil spec or applications", deviceId)
	}
	apps := *device.Spec.Applications
	if len(apps) != 2 {
		GinkgoWriter.Printf("getRenderedAppRefs: device %s has %d applications, expected 2\n", deviceId, len(apps))
		return "", "", "", fmt.Errorf("device %s has %d applications, expected 2", deviceId, len(apps))
	}

	var gotContainer, gotVolume, gotInline string
	for _, appSpec := range apps {
		name, nameErr := appSpec.GetName()
		if nameErr != nil {
			GinkgoWriter.Printf("getRenderedAppRefs: GetName failed: %v\n", nameErr)
			return "", "", "", nameErr
		}
		if name == nil || *name == "" {
			GinkgoWriter.Printf("getRenderedAppRefs: application has nil or empty name\n")
			return "", "", "", fmt.Errorf("application has nil or empty name")
		}

		switch *name {
		case containerAppName:
			gotContainer, err = e2e.GetContainerApplicationImage(appSpec)
			if err != nil {
				GinkgoWriter.Printf("getRenderedAppRefs: GetContainerApplicationImage failed: %v\n", err)
				return "", "", "", err
			}
			gotVolume, err = e2e.GetContainerApplicationVolumeImageRef(appSpec, volumeName)
			if err != nil {
				GinkgoWriter.Printf("getRenderedAppRefs: GetContainerApplicationVolumeImageRef failed: %v\n", err)
				return "", "", "", err
			}
		case inlineAppName:
			gotInline, err = e2e.GetQuadletApplicationInlineContent(appSpec)
			if err != nil {
				GinkgoWriter.Printf("getRenderedAppRefs: GetQuadletApplicationInlineContent failed: %v\n", err)
				return "", "", "", err
			}
		default:
			GinkgoWriter.Printf("getRenderedAppRefs: unexpected application name %q\n", *name)
			return "", "", "", fmt.Errorf("unexpected application name: %s", *name)
		}
	}

	if gotContainer == "" || gotVolume == "" || gotInline == "" {
		GinkgoWriter.Printf("getRenderedAppRefs: missing refs container=%q volume=%q inlineLen=%d\n",
			gotContainer, gotVolume, len(gotInline))
		return "", "", "", fmt.Errorf("missing one or more refs (container=%q volume=%q)", gotContainer, gotVolume)
	}
	GinkgoWriter.Printf("getRenderedAppRefs: container=%s volume=%s inline contains image ref\n", gotContainer, gotVolume)
	return gotContainer, gotVolume, gotInline, nil
}
