package parametrisabletemplates

import (
	"fmt"

	"github.com/flightctl/flightctl/api/v1alpha1"
	testutil "github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/sirupsen/logrus"
)

var _ = Describe("Template variables in the device configuraion", func() {
	var (
		deviceId string
		testID   string
	)

	BeforeEach(func() {
		deviceId, _ = harness.EnrollAndWaitForOnlineStatus()
		testID = harness.GetTestIDFromContext()
	})

	Context("parametrisable_templates", func() {
		It(`Verifies that Flightctl fleet resource supports parametrisable device
		    templates to configure items that are specific to an individual device
			or a group of devices selected by labels`, Label("75486"), func() {

			By("Create a fleet with template variables in InlineConfigProviderSpec")
			err := configProviderSpec.FromInlineConfigProviderSpec(inlineConfigValidWithFunction)
			Expect(err).ToNot(HaveOccurred())
			fleetTestName := fmt.Sprintf("fleet-test-%s", testID)
			err = harness.CreateTestFleetWithConfig(fleetTestName, testFleetSelector, configProviderSpec)
			Expect(err).ToNot(HaveOccurred())

			By("Check that the device status is Online")
			_, err = harness.CheckDeviceStatus(deviceId, v1alpha1.DeviceSummaryStatusOnline)
			Expect(err).ToNot(HaveOccurred())

			By("Add the fleet selector and the team label to the device")
			nextRenderedVersion, err := harness.PrepareNextDeviceVersion(deviceId)
			Expect(err).ToNot(HaveOccurred())

			err = harness.UpdateDeviceWithRetries(deviceId, func(device *v1alpha1.Device) {
				harness.SetLabelsForDeviceMetadata(&device.Metadata, map[string]string{
					fleetSelectorKey: fleetSelectorValue,
					teamLabelKey:     teamLabelValue,
				})
				logrus.Infof("Updating %s with label %s=%s and %s=%s", deviceId,
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

				By("Check the device status is Online")
				_, err := harness.CheckDeviceStatus(deviceId, v1alpha1.DeviceSummaryStatusOnline)
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

				err = harness.UpdateDeviceWithRetries(deviceId, func(device *v1alpha1.Device) {

					(*device.Metadata.Labels)[fleetSelectorKey] = fleetSelectorValue
					logrus.Infof("Updating %s with label %s=%s", deviceId,
						fleetSelectorKey, fleetSelectorValue)
				})
				Expect(err).ToNot(HaveOccurred())

				By("Check the device will fail to reconcile")
				harness.WaitForDeviceContents(deviceId, `The device could not be updated to the fleet`,
					func(device *v1alpha1.Device) bool {
						return device.Status.Updated.Status == v1alpha1.DeviceUpdatedStatusOutOfDate
					}, testutil.TIMEOUT)
				resp, err := harness.Client.GetDeviceStatusWithResponse(harness.Context, deviceId)
				Expect(err).ToNot(HaveOccurred())
				device := resp.JSON200
				Expect((*device.Metadata.Annotations)["fleet-controller/lastRolloutError"]).NotTo(BeNil())
				Expect(device.Status.Updated.Status).To(Equal(v1alpha1.DeviceUpdatedStatusOutOfDate))
				Expect((*device.Metadata.Annotations)["fleet-controller/lastRolloutError"]).To(ContainSubstring("no entry for key \"team\""))

				By("Add the team label to the device")
				err = harness.UpdateDeviceWithRetries(deviceId, func(device *v1alpha1.Device) {

					(*device.Metadata.Labels)[teamLabelKey] = teamLabelValue
					logrus.Infof("Updating %s with label %s=%s", deviceId,
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

				By("Check the device status")
				_, err := harness.CheckDeviceStatus(deviceId, v1alpha1.DeviceSummaryStatusOnline)
				Expect(err).ToNot(HaveOccurred())

				By("Create a git and a http repository")
				repoTestName := fmt.Sprintf("git-repo-%s", testID)
				gitMetadata.Name = &repoTestName
				err = harness.CreateRepository(gitRepositorySpec, gitMetadata)
				Expect(err).ToNot(HaveOccurred())
				logrus.Infof("Created git repository %s", *gitMetadata.Name)

				httpRepoName := fmt.Sprintf("http-repo-%s", testID)
				httpRepoMetadata.Name = &httpRepoName
				err = harness.CreateRepository(httpRepositoryspec, httpRepoMetadata)
				Expect(err).ToNot(HaveOccurred())
				logrus.Infof("Created http repository %s", *httpRepoMetadata.Name)

				By("Create the device spec adding inline. git, http configs")
				// Create git config with dynamic repository name
				gitConfigWithRepo := gitConfigvalid
				gitConfigWithRepo.GitRef.Repository = repoTestName

				// Create http config with dynamic repository name
				httpConfigWithRepo := httpConfigvalid
				httpConfigWithRepo.HttpRef.Repository = httpRepoName

				httpConfigProviderSpec := v1alpha1.ConfigProviderSpec{}
				err = httpConfigProviderSpec.FromHttpConfigProviderSpec(httpConfigWithRepo)
				Expect(err).ToNot(HaveOccurred())

				gitConfigProviderSpec := v1alpha1.ConfigProviderSpec{}
				err = gitConfigProviderSpec.FromGitConfigProviderSpec(gitConfigWithRepo)
				Expect(err).ToNot(HaveOccurred())

				inlineConfigProviderSpec := v1alpha1.ConfigProviderSpec{}
				err = inlineConfigProviderSpec.FromInlineConfigProviderSpec(inlineConfigValid)
				Expect(err).ToNot(HaveOccurred())

				configProviderSpec := []v1alpha1.ConfigProviderSpec{gitConfigProviderSpec, inlineConfigProviderSpec, httpConfigProviderSpec}

				logrus.Infof("this is the configProviderSpec %s", configProviderSpec)
				deviceImage := fmt.Sprintf("%s/flightctl-device:{{ .metadata.labels.alias }}", harness.RegistryEndpoint())

				var osImageSpec = v1alpha1.DeviceOsSpec{
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

				err = harness.UpdateDeviceWithRetries(deviceId, func(device *v1alpha1.Device) {

					harness.SetLabelsForDeviceMetadata(&device.Metadata, map[string]string{
						fleetSelectorKey: fleetSelectorValue,
						teamLabelKey:     teamLabelValue,
						aliasKey:         deviceAlias,
						configLabelKey:   configLabelValue,
						revisionLabelKey: revisionLabelValue,
						suffixLabelKey:   suffixLabelValue,
					})
					logrus.Infof("Updating %s with labels", deviceId)
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

				err = harness.UpdateDeviceWithRetries(deviceId, func(device *v1alpha1.Device) {

					(*device.Metadata.Labels)[revisionLabelKey] = branchTargetRevision
					logrus.Infof("Updating the device with label %s=%s", revisionLabelKey, branchTargetRevision)
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

				inlineConfigProviderSpec = v1alpha1.ConfigProviderSpec{}
				err = inlineConfigProviderSpec.FromInlineConfigProviderSpec(inlineConfigValid)
				Expect(err).ToNot(HaveOccurred())

				deviceSpec.Config = &[]v1alpha1.ConfigProviderSpec{inlineConfigProviderSpec}

				err = harness.CreateOrUpdateTestFleet(fleetTestName, testFleetSelector, deviceSpec)
				Expect(err).ToNot(HaveOccurred())

				err = harness.WaitForDeviceNewRenderedVersion(deviceId, nextRenderedVersion)
				Expect(err).ToNot(HaveOccurred())

				By("Remove the team label from the device and check that the config variable is replaced by the default")
				nextGeneration, err = harness.PrepareNextDeviceGeneration(deviceId)
				Expect(err).ToNot(HaveOccurred())

				logrus.Infof("Removing %s labels", teamLabelKey)
				err = harness.UpdateDeviceWithRetries(deviceId, func(device *v1alpha1.Device) {
					harness.SetLabelsForDeviceMetadata(&device.Metadata, map[string]string{
						fleetSelectorKey: fleetSelectorValue,
						aliasKey:         deviceAlias,
						configLabelKey:   configLabelValue,
						revisionLabelKey: revisionLabelValue,
						suffixLabelKey:   suffixLabelValue,
					})
					logrus.Infof("Updating %s with labels", deviceId)
				})
				Expect(err).ToNot(HaveOccurred())

				By("Wait for the device to pick the fleet config")
				err = harness.WaitForDeviceNewGeneration(deviceId, nextGeneration)
				Expect(err).ToNot(HaveOccurred())

				By("Check that the default variables are replaced in the config")
				device, err = harness.GetDevice(deviceId)
				Expect(err).ToNot(HaveOccurred())

				logrus.Infof("The device labels are %s", *device.Metadata.Labels)

				inlineConfigResponse, err = harness.GetDeviceInlineConfig(device, inlineConfigName)
				Expect(err).ToNot(HaveOccurred())
				if len(inlineConfigResponse.Inline) > 0 {
					Expect(inlineConfigResponse.Inline[0].Path).To(ContainSubstring(defaultTeamLabelValue))
					Expect(inlineConfigResponse.Inline[0].Content).To(ContainSubstring(defaultTeamLabelValue))
				}

			})
	})
})

var (
	fleetSelectorKey      = "fleet"
	fleetSelectorValue    = "test"
	inlinePath            = "/var/home/user/{{ getOrDefault .metadata.labels \"team\" \"c\" }}.txt"
	inlineContent         = "{{ getOrDefault .metadata.labels \"team\" \"c\" }}"
	teamLabelKey          = "team"
	inlineConfigName      = "inline-config"
	teamLabelValue        = "a"
	defaultTeamLabelValue = "c"
	contentWithFunction   = "{{ replace \"a\" \"c\" .metadata.labels.team }}"
	pathWithFunction      = "/var/home/user/{{ upper .metadata.labels.team | lower }}/test.txt"
	repoTestUrl           = "https://github.com/flightctl/flightctl-demos"
	deviceAlias           = "base"
	branchTargetRevision  = "demo"
	gitRepoConfigPath     = "/demos/basic-nginx-demo/configuration"
	httpConfigPath        = "/var/home/user/{{ .metadata.labels.config }}"
	configLabelKey        = "config"
	configLabelValue      = "fedora-bootc"
	revisionLabelKey      = "revision"
	revisionLabelValue    = "main"
	suffix                = "{{ .metadata.labels.suffix }}"
	gitConfigName         = "git-config"
	httpConfigName        = "http-config"
	revision              = "{{ .metadata.labels.revision }}"
	suffixLabelValue      = ""
	suffixLabelKey        = "suffix"
	aliasKey              = "alias"
)

var mode = 0644
var modePointer = &mode

var inlineConfigSpec = v1alpha1.FileSpec{
	Path:    inlinePath,
	Mode:    modePointer,
	Content: inlineContent,
}

var inlineConfigWithFunctionSpec = v1alpha1.FileSpec{
	Path:    pathWithFunction,
	Mode:    modePointer,
	Content: contentWithFunction,
}

var configProviderSpec v1alpha1.ConfigProviderSpec

var inlineConfigValid = v1alpha1.InlineConfigProviderSpec{
	Inline: []v1alpha1.FileSpec{inlineConfigSpec},
	Name:   inlineConfigName,
}
var inlineConfigValidWithFunction = v1alpha1.InlineConfigProviderSpec{
	Inline: []v1alpha1.FileSpec{inlineConfigWithFunctionSpec},
	Name:   inlineConfigName,
}

var testFleetSelector = v1alpha1.LabelSelector{
	MatchLabels: &map[string]string{fleetSelectorKey: fleetSelectorValue},
}

var deviceSpec v1alpha1.DeviceSpec

var gitRepositorySpec v1alpha1.RepositorySpec
var _ = gitRepositorySpec.FromGenericRepoSpec(v1alpha1.GenericRepoSpec{
	Url:  repoTestUrl,
	Type: v1alpha1.Git,
})

var gitMetadata = v1alpha1.ObjectMeta{
	Name:   nil, // Will be set dynamically in test
	Labels: &map[string]string{},
}

var httpRepoSpec = v1alpha1.HttpRepoSpec{
	Type: v1alpha1.Http,
	Url:  repoTestUrl,
}

var httpRepositoryspec v1alpha1.RepositorySpec

var _ = httpRepositoryspec.FromHttpRepoSpec(httpRepoSpec)

var httpRepoMetadata = v1alpha1.ObjectMeta{
	Name: nil, // Will be set dynamically in test
}

var gitConfigvalid = v1alpha1.GitConfigProviderSpec{
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

var httpConfigvalid = v1alpha1.HttpConfigProviderSpec{
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
