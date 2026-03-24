package parametrisabletemplates

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/test/e2e/infra/auxiliary"
	"github.com/flightctl/flightctl/test/harness/e2e"
	testutil "github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const fleetControllerErrorAnnotation = "fleet-controller/lastRolloutError"

var (
	fleetSelectorKey                  = "fleet"
	fleetSelectorValue                = "test"
	inlinePath                        = "/var/home/user/{{ getOrDefault .metadata.labels \"team\" \"c\" }}.txt"
	inlineContent                     = "{{ getOrDefault .metadata.labels \"team\" \"c\" }}"
	teamLabelKey                      = "team"
	inlineConfigName                  = "inline-config"
	teamLabelValue                    = "a"
	defaultTeamLabelValue             = "c"
	contentWithFunction               = "{{ replace \"a\" \"c\" .metadata.labels.team }}"
	pathWithFunction                  = "/var/home/user/{{ upper .metadata.labels.team | lower }}/test.txt"
	repoTestUrl                       = "https://github.com/flightctl/flightctl-demos"
	deviceAlias                       = "base"
	branchTargetRevision              = "demo"
	gitRepoConfigPath                 = "/demos/basic-nginx-demo/configuration"
	httpConfigPath                    = "/var/home/user/{{ .metadata.labels.config }}"
	configLabelKey                    = "config"
	configLabelValue                  = "fedora-bootc"
	revisionLabelKey                  = "revision"
	revisionLabelValue                = "c5ff21b9a8116bb5daf72c8f07b67449c221b596"
	suffix                            = "{{ .metadata.labels.suffix }}"
	gitConfigName                     = "git-config"
	httpConfigName                    = "http-config"
	revision                          = "{{ .metadata.labels.revision }}"
	suffixLabelValue                  = ""
	suffixLabelKey                    = "suffix"
	aliasKey                          = "alias"
	sizeLabelKey                      = "size"
	sizeLabelSmallValue               = "small"
	sizeLabelBigValue                 = "big"
	motdConfigName                    = "motd-config"
	motdPath                          = "/etc/motd"
	smallContent                      = "I'm small\n"
	bigContent                        = "I'm big\n"
	appFleetSelectorKey               = "app"
	appFleetSelectorValue             = "my-templated-app"
	containerAppName                  = "my-app"
	inlineAppName                     = "inline-app"
	inlineLabelKey                    = "inline"
	inlineTag                         = "v1"
	nginxImage                        = "docker.io/library/nginx"
	alpineImage                       = "quay.io/flightctl-tests/alpine"
	containerCPU                      = "0.5"
	containerMemory                   = "256m"
	inlineAppEnvVars                  = map[string]string{"LOG_MESSAGE": "Hello from FlightControl (Inline Ref)"}
	pullPolicy                        = v1beta1.PullIfNotPresent
	fixedContainerTag                 = "alpine"
	fixedQuadletTag                   = "with-image-ref"
	fixedArtifactTag                  = "latest"
	fixedInlineTag                    = "v1"
	deviceCouldNotBeUpdatedToFleetMsg = "The device could not be updated to the fleet"
	containerLabelKey                 = "container"
	containerLabelValue               = "alpine"
	quadletLabelKey                   = "quadlet"
	quadletLabelValue                 = "with-image-ref"
	artifactLabelKey                  = "artifact"
	artifactLabelValue                = "latest"
	quadletImageAppName               = "my-app-2"
	quadletArtifactImage              = "quay.io/flightctl-tests/quadlet-app-artifact"
	modelArtifactImage                = "quay.io/flightctl-tests/model-artifact"
	nginxConfigArtifact               = "quay.io/flightctl-tests/nginx-config-artifact:latest"
	nginxHtmlArtifact                 = "quay.io/flightctl-tests/nginx-html-artifact-image:latest"
	negTemplatedNginxImage            = nginxImage + ":{{ .metadata.labels." + containerLabelKey + " }}"
	negInlineContent                  = "[Unit]\nDescription=Primary application container\n\n[Container]\n" +
		"Image=" + alpineImage + ":{{ .metadata.labels." + inlineLabelKey + " }}\n" +
		"Exec=sh -c \"echo 'Primary container started.' && echo 'LOG_MESSAGE:' $LOG_MESSAGE && sleep infinity\"\n\n" +
		"[Install]\nWantedBy=default.target\n"
	nonTemplatedNegInlineContent = "[Unit]\nDescription=Primary application container\n\n[Container]\n" +
		"Image=" + alpineImage + ":" + fixedInlineTag + "\n" +
		"Exec=sh -c \"echo 'Primary container started.' && echo 'LOG_MESSAGE:' $LOG_MESSAGE && sleep infinity\"\n\n" +
		"[Install]\nWantedBy=default.target\n"
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

var motdGitConfigSpec = v1beta1.GitConfigProviderSpec{
	GitRef: struct {
		Path           string `json:"path"`
		Repository     string `json:"repository"`
		TargetRevision string `json:"targetRevision"`
	}{
		Path:           fmt.Sprintf("/contents/{{ .metadata.labels.%s }}", sizeLabelKey),
		Repository:     "", // Will be set dynamically in test
		TargetRevision: "main",
	},
	Name: motdConfigName,
}

var appFleetSelector = v1beta1.LabelSelector{
	MatchLabels: &map[string]string{appFleetSelectorKey: appFleetSelectorValue},
}

var negNginxConfigVol v1beta1.ApplicationVolume
var _ = func() v1beta1.ApplicationVolume {
	negNginxConfigVol = v1beta1.ApplicationVolume{Name: "nginx-config"}
	_ = negNginxConfigVol.FromImageMountVolumeProviderSpec(v1beta1.ImageMountVolumeProviderSpec{
		Image: v1beta1.ImageVolumeSource{Reference: nginxConfigArtifact},
		Mount: v1beta1.VolumeMount{Path: "/etc/nginx/conf.d"},
	})
	return negNginxConfigVol
}()

var negNginxHtmlVol v1beta1.ApplicationVolume
var _ = func() v1beta1.ApplicationVolume {
	negNginxHtmlVol = v1beta1.ApplicationVolume{Name: "nginx-html"}
	_ = negNginxHtmlVol.FromImageMountVolumeProviderSpec(v1beta1.ImageMountVolumeProviderSpec{
		Image: v1beta1.ImageVolumeSource{Reference: nginxHtmlArtifact},
		Mount: v1beta1.VolumeMount{Path: "/usr/share/nginx/html"},
	})
	return negNginxHtmlVol
}()

var negNginxLogsVol v1beta1.ApplicationVolume
var _ = func() v1beta1.ApplicationVolume {
	negNginxLogsVol = v1beta1.ApplicationVolume{Name: "nginx-logs"}
	_ = negNginxLogsVol.FromMountVolumeProviderSpec(v1beta1.MountVolumeProviderSpec{
		Mount: v1beta1.VolumeMount{Path: "/var/log/nginx"},
	})
	return negNginxLogsVol
}()

var negModelDataVol v1beta1.ApplicationVolume
var _ = func() v1beta1.ApplicationVolume {
	negModelDataVol = v1beta1.ApplicationVolume{Name: "model-data"}
	_ = negModelDataVol.FromImageVolumeProviderSpec(v1beta1.ImageVolumeProviderSpec{
		Image: v1beta1.ImageVolumeSource{
			Reference:  modelArtifactImage + ":{{ .metadata.labels." + artifactLabelKey + " }}",
			PullPolicy: &pullPolicy,
		},
	})
	return negModelDataVol
}()

var negContainerApp v1beta1.ApplicationProviderSpec
var _ = func() v1beta1.ApplicationProviderSpec {
	vols := []v1beta1.ApplicationVolume{negNginxConfigVol, negNginxHtmlVol, negNginxLogsVol}
	negContainerApp, _ = e2e.NewContainerApplicationSpec(
		containerAppName, negTemplatedNginxImage,
		[]v1beta1.ApplicationPort{"8081:80", "8080:8080"},
		&containerCPU, &containerMemory,
		&vols,
	)
	return negContainerApp
}()

var negQuadletImageApp v1beta1.ApplicationProviderSpec
var _ = func() v1beta1.ApplicationProviderSpec {
	negQuadletImageApp, _ = e2e.NewQuadletApplicationSpec(
		quadletImageAppName,
		quadletArtifactImage+":{{ .metadata.labels."+quadletLabelKey+" }}",
		"",
		map[string]string{"LOG_MESSAGE": "Multi-file artifact (with .image ref)"},
		negModelDataVol,
	)
	return negQuadletImageApp
}()

var negInlineQuadletApp v1beta1.QuadletApplication
var _ = negInlineQuadletApp.FromInlineApplicationProviderSpec(v1beta1.InlineApplicationProviderSpec{
	Inline: []v1beta1.ApplicationContent{{Path: "app.container", Content: &negInlineContent}},
})

var negInlineApp v1beta1.ApplicationProviderSpec
var _ = func() v1beta1.ApplicationProviderSpec {
	negInlineQuadletApp.Name = &inlineAppName
	negInlineQuadletApp.AppType = v1beta1.AppTypeQuadlet
	negInlineQuadletApp.EnvVars = &inlineAppEnvVars
	_ = negInlineApp.FromQuadletApplication(negInlineQuadletApp)
	return negInlineApp
}()

var negTestDeviceSpec = v1beta1.DeviceSpec{
	Applications: &[]v1beta1.ApplicationProviderSpec{negContainerApp, negQuadletImageApp, negInlineApp},
}

var nonTemplatedModelDataVol v1beta1.ApplicationVolume
var _ = func() v1beta1.ApplicationVolume {
	nonTemplatedModelDataVol = v1beta1.ApplicationVolume{Name: "model-data"}
	_ = nonTemplatedModelDataVol.FromImageVolumeProviderSpec(v1beta1.ImageVolumeProviderSpec{
		Image: v1beta1.ImageVolumeSource{
			Reference:  modelArtifactImage + ":" + fixedArtifactTag,
			PullPolicy: &pullPolicy,
		},
	})
	return nonTemplatedModelDataVol
}()

var nonTemplatedFullContainerApp v1beta1.ApplicationProviderSpec
var _ = func() v1beta1.ApplicationProviderSpec {
	vols := []v1beta1.ApplicationVolume{negNginxConfigVol, negNginxHtmlVol, negNginxLogsVol}
	nonTemplatedFullContainerApp, _ = e2e.NewContainerApplicationSpec(
		containerAppName, nginxImage+":"+fixedContainerTag,
		[]v1beta1.ApplicationPort{"8081:80", "8080:8080"},
		&containerCPU, &containerMemory,
		&vols,
	)
	return nonTemplatedFullContainerApp
}()

var nonTemplatedFullQuadletApp v1beta1.ApplicationProviderSpec
var _ = func() v1beta1.ApplicationProviderSpec {
	nonTemplatedFullQuadletApp, _ = e2e.NewQuadletApplicationSpec(
		quadletImageAppName,
		quadletArtifactImage+":"+fixedQuadletTag,
		"",
		map[string]string{"LOG_MESSAGE": "Multi-file artifact (with .image ref)"},
		nonTemplatedModelDataVol,
	)
	return nonTemplatedFullQuadletApp
}()

var nonTemplatedFullInlineQuadletApp v1beta1.QuadletApplication
var _ = nonTemplatedFullInlineQuadletApp.FromInlineApplicationProviderSpec(v1beta1.InlineApplicationProviderSpec{
	Inline: []v1beta1.ApplicationContent{{Path: "app.container", Content: &nonTemplatedNegInlineContent}},
})

var nonTemplatedFullInlineApp v1beta1.ApplicationProviderSpec
var _ = func() v1beta1.ApplicationProviderSpec {
	nonTemplatedFullInlineQuadletApp.Name = &inlineAppName
	nonTemplatedFullInlineQuadletApp.AppType = v1beta1.AppTypeQuadlet
	nonTemplatedFullInlineQuadletApp.EnvVars = &inlineAppEnvVars
	_ = nonTemplatedFullInlineApp.FromQuadletApplication(nonTemplatedFullInlineQuadletApp)
	return nonTemplatedFullInlineApp
}()

var nonTemplatedFullDeviceSpec = v1beta1.DeviceSpec{
	Applications: &[]v1beta1.ApplicationProviderSpec{nonTemplatedFullContainerApp, nonTemplatedFullQuadletApp, nonTemplatedFullInlineApp},
}

var _ = Describe("Template variables in the device configuration", func() {
	var (
		harness      *e2e.Harness
		deviceId     string
		testID       string
		registryHost string
		registryPort string
	)

	BeforeEach(func() {
		harness = e2e.GetWorkerHarness()
		registryHost, registryPort = auxSvcs.Registry.Host, auxSvcs.Registry.Port
		deviceId, _ = harness.EnrollAndWaitForOnlineStatus()
		testID = harness.GetTestIDFromContext()
	})

	Context("parametrisable_templates", func() {
		It(`Verifies that Flightctl fleet resource supports parameterizable device
		    templates to configure items that are specific to an individual device
			or a group of devices selected by labels`, Label("75486"), func() {
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
					return checkFleetControllerErrorAnnotation(harness, deviceId, "no entry for key \"team\"")
				}, 30*time.Second, testutil.POLLING).Should(BeNil(), "Fleet controller error annotation should be set with correct error message")

				resp, err := harness.Client.GetDeviceStatusWithResponse(harness.Context, deviceId)
				Expect(err).ToNot(HaveOccurred())
				Expect(resp.JSON200).ToNot(BeNil(), "expected 200 response, got %d", resp.StatusCode())
				device := resp.JSON200
				Expect((*device.Metadata.Annotations)[fleetControllerErrorAnnotation]).NotTo(BeNil())
				Expect(device.Status.Updated.Status).To(Equal(v1beta1.DeviceUpdatedStatusOutOfDate))
				Expect((*device.Metadata.Annotations)[fleetControllerErrorAnnotation]).To(ContainSubstring("no entry for key \"team\""))

				By("Add the team label to the device")
				err = harness.UpdateDeviceWithRetries(deviceId, func(device *v1beta1.Device) {

					(*device.Metadata.Labels)[teamLabelKey] = teamLabelValue
					GinkgoWriter.Printf("Updating %s with label %s=%s\n", deviceId,
						teamLabelKey, teamLabelValue)
				})
				Expect(err).ToNot(HaveOccurred())
				resp, err = harness.Client.GetDeviceStatusWithResponse(harness.Context, deviceId)
				Expect(err).ToNot(HaveOccurred())
				Expect(resp.JSON200).ToNot(BeNil(), "expected 200 response, got %d", resp.StatusCode())
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
				gitConfigWithRepo := gitConfigvalid
				gitConfigWithRepo.GitRef.Repository = repoTestName

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
				deviceImage := fmt.Sprintf("%s:{{ .metadata.labels.alias }}", harness.GetDeviceImageRefForFleet(registryHost, registryPort, ""))

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

		It(`Verifies that changing a device label updates git config file content on the device`,
			Label("88262", "sanity"), func() {
				gitConfig, gitInternalHost, gitInternalPort, sshKeyPath, sshKeyContent, gitErr := getGitEnv(harness.Context)
				Expect(gitErr).ToNot(HaveOccurred())

				repoName := fmt.Sprintf("git-label-repo-%s", testID)
				fleetTestName := fmt.Sprintf("fleet-git-label-%s", testID)

				By("Create a git repository on the e2e git server")
				err := harness.CreateGitRepositoryOnServer(gitConfig, sshKeyPath, repoName)
				Expect(err).ToNot(HaveOccurred())

				By("Push 'small' content to the git repo")
				err = harness.PushContentToGitServerRepo(
					gitConfig, sshKeyPath,
					repoName,
					fmt.Sprintf("contents/%s%s", sizeLabelSmallValue, motdPath),
					smallContent,
					"Add small content",
				)
				Expect(err).ToNot(HaveOccurred())

				By("Push 'big' content to the git repo")
				err = harness.PushContentToGitServerRepo(
					gitConfig, sshKeyPath,
					repoName,
					fmt.Sprintf("contents/%s%s", sizeLabelBigValue, motdPath),
					bigContent,
					"Add big content",
				)
				Expect(err).ToNot(HaveOccurred())

				By("Create a Repository resource with SSH credentials")
				err = harness.CreateRepositoryWithValidE2ECredentials(gitInternalHost, gitInternalPort, repoName, sshKeyContent)
				Expect(err).ToNot(HaveOccurred())

				By("Create a fleet with git config using templated path")
				motdGitConfig := motdGitConfigSpec
				motdGitConfig.GitRef.Repository = repoName
				gitConfigProviderSpec := v1beta1.ConfigProviderSpec{}
				err = gitConfigProviderSpec.FromGitConfigProviderSpec(motdGitConfig)
				Expect(err).ToNot(HaveOccurred())

				err = harness.CreateTestFleetWithConfig(fleetTestName, testFleetSelector, gitConfigProviderSpec)
				Expect(err).ToNot(HaveOccurred())

				By("Verify device is online")
				_, err = harness.CheckDeviceStatus(deviceId, v1beta1.DeviceSummaryStatusOnline)
				Expect(err).ToNot(HaveOccurred())

				By("Add fleet selector and size=small labels to device")
				nextRenderedVersion, err := harness.PrepareNextDeviceVersion(deviceId)
				Expect(err).ToNot(HaveOccurred())

				err = harness.UpdateDeviceWithRetries(deviceId, func(device *v1beta1.Device) {
					harness.SetLabelsForDeviceMetadata(&device.Metadata, map[string]string{
						fleetSelectorKey: fleetSelectorValue,
						sizeLabelKey:     sizeLabelSmallValue,
					})
					GinkgoWriter.Printf("Updating %s with label %s=%s and %s=%s\n", deviceId,
						fleetSelectorKey, fleetSelectorValue, sizeLabelKey, sizeLabelSmallValue)
				})
				Expect(err).ToNot(HaveOccurred())

				By("Wait for the device to get the fleet configuration")
				err = harness.WaitForDeviceNewRenderedVersion(deviceId, nextRenderedVersion)
				Expect(err).ToNot(HaveOccurred())

				By("Verify rendered git config path for size=small")
				err = harness.VerifyDeviceGitConfigPath(deviceId, motdConfigName, "/contents/"+sizeLabelSmallValue)
				Expect(err).ToNot(HaveOccurred())

				By("Verify /etc/motd on the device contains the small content")
				Eventually(func() string {
					content, err := harness.ReadFileFromDevice(motdPath)
					if err != nil {
						GinkgoWriter.Printf("Error reading %s: %v\n", motdPath, err)
						return ""
					}
					return content
				}, testutil.TIMEOUT, testutil.LONG_POLLING).Should(ContainSubstring(smallContent))

				By("Change device label to size=big")
				nextRenderedVersion, err = harness.PrepareNextDeviceVersion(deviceId)
				Expect(err).ToNot(HaveOccurred())

				err = harness.UpdateDeviceWithRetries(deviceId, func(device *v1beta1.Device) {
					(*device.Metadata.Labels)[sizeLabelKey] = sizeLabelBigValue
					GinkgoWriter.Printf("Updating %s with label %s=%s\n", deviceId,
						sizeLabelKey, sizeLabelBigValue)
				})
				Expect(err).ToNot(HaveOccurred())

				By("Wait for the device to pick up the new configuration")
				err = harness.WaitForDeviceNewRenderedVersion(deviceId, nextRenderedVersion)
				Expect(err).ToNot(HaveOccurred())

				By("Verify rendered git config path for size=big")
				err = harness.VerifyDeviceGitConfigPath(deviceId, motdConfigName, "/contents/"+sizeLabelBigValue)
				Expect(err).ToNot(HaveOccurred())

				By("Verify /etc/motd on the device contains the big content")
				Eventually(func() string {
					content, err := harness.ReadFileFromDevice(motdPath)
					if err != nil {
						GinkgoWriter.Printf("Error reading %s: %v\n", motdPath, err)
						return ""
					}
					return content
				}, testutil.TIMEOUT, testutil.LONG_POLLING).Should(ContainSubstring(bigContent))
			})

		It(`Verifies that we can add parametrizable templates variables in the fleets device's application configuration`,
			Label("87803", "sanity"), func() {
				fleetTestName := fmt.Sprintf("templated-app-fleet-%s", testID)

				By("Check that the device status is Online")
				_, err := harness.CheckDeviceStatus(deviceId, v1beta1.DeviceSummaryStatusOnline)
				Expect(err).ToNot(HaveOccurred())

				By("Create a fleet with parametrisable application templates")
				err = harness.CreateOrUpdateTestFleet(fleetTestName, appFleetSelector, negTestDeviceSpec)
				Expect(err).ToNot(HaveOccurred())

				By("Add labels to the device to associate it with the fleet and provide template values")
				nextRenderedVersion, err := harness.PrepareNextDeviceVersion(deviceId)
				Expect(err).ToNot(HaveOccurred())

				err = harness.UpdateDeviceWithRetries(deviceId, func(device *v1beta1.Device) {
					harness.SetLabelsForDeviceMetadata(&device.Metadata, map[string]string{
						appFleetSelectorKey: appFleetSelectorValue,
						containerLabelKey:   containerLabelValue,
						quadletLabelKey:     quadletLabelValue,
						artifactLabelKey:    artifactLabelValue,
						inlineLabelKey:      inlineTag,
					})
					GinkgoWriter.Printf("Updating %s with labels app=%s container=%s quadlet=%s artifact=%s inline=%s\n",
						deviceId, appFleetSelectorValue, containerLabelValue, quadletLabelValue, artifactLabelValue, inlineTag)
				})
				Expect(err).ToNot(HaveOccurred())

				By("Wait for the device to receive the fleet configuration")
				err = harness.WaitForDeviceNewRenderedVersion(deviceId, nextRenderedVersion)
				Expect(err).ToNot(HaveOccurred())

				By("Verify that template variables are rendered in the device applications")
				refs, err := harness.GetDeviceRenderedAppRefs(deviceId, containerAppName, quadletImageAppName, inlineAppName)
				Expect(err).ToNot(HaveOccurred())
				Expect(refs.ContainerImage).To(Equal(fmt.Sprintf("%s:%s", nginxImage, containerLabelValue)))
				Expect(refs.QuadletImage).To(Equal(fmt.Sprintf("%s:%s", quadletArtifactImage, quadletLabelValue)))
				Expect(refs.QuadletVolRef).To(Equal(fmt.Sprintf("%s:%s", modelArtifactImage, artifactLabelValue)))
				Expect(refs.InlineContent).To(ContainSubstring(fmt.Sprintf("%s:%s", alpineImage, inlineTag)))
				Expect(refs.ContainerImage).ToNot(ContainSubstring("{{"))
				Expect(refs.QuadletImage).ToNot(ContainSubstring("{{"))
				Expect(refs.QuadletVolRef).ToNot(ContainSubstring("{{"))
				Expect(refs.InlineContent).ToNot(ContainSubstring("{{"))

				By("Ensure that all applications start properly")
				harness.WaitForApplicationRunningStatus(deviceId, containerAppName)
				harness.WaitForApplicationRunningStatus(deviceId, quadletImageAppName)
				harness.WaitForApplicationRunningStatus(deviceId, inlineAppName)
				harness.WaitForRunningApplicationsCount(deviceId, 3)
				harness.WaitForApplicationsSummaryStatus(deviceId, v1beta1.ApplicationsSummaryStatusHealthy)

				By("Update the fleet template removing templated image references")
				nextRenderedVersion, err = harness.PrepareNextDeviceVersion(deviceId)
				Expect(err).ToNot(HaveOccurred())

				harness.UpdateFleetWithRetries(fleetTestName, func(fleet *v1beta1.Fleet) {
					fleet.Spec.Template.Spec = nonTemplatedFullDeviceSpec
				})

				By("Wait for the device to pick up the updated fleet configuration")
				err = harness.WaitForDeviceNewRenderedVersion(deviceId, nextRenderedVersion)
				Expect(err).ToNot(HaveOccurred())

				By("Ensure the device is updated without any templating occurring")
				refs, err = harness.GetDeviceRenderedAppRefs(deviceId, containerAppName, quadletImageAppName, inlineAppName)
				Expect(err).ToNot(HaveOccurred())
				Expect(refs.ContainerImage).To(Equal(fmt.Sprintf("%s:%s", nginxImage, fixedContainerTag)))
				Expect(refs.QuadletImage).To(Equal(fmt.Sprintf("%s:%s", quadletArtifactImage, fixedQuadletTag)))
				Expect(refs.QuadletVolRef).To(Equal(fmt.Sprintf("%s:%s", modelArtifactImage, fixedArtifactTag)))
				Expect(refs.InlineContent).To(ContainSubstring(fmt.Sprintf("%s:%s", alpineImage, fixedInlineTag)))
				Expect(refs.ContainerImage).ToNot(ContainSubstring("{{"))
				Expect(refs.QuadletImage).ToNot(ContainSubstring("{{"))
				Expect(refs.QuadletVolRef).ToNot(ContainSubstring("{{"))
				Expect(refs.InlineContent).ToNot(ContainSubstring("{{"))

				By("Ensure that all applications are running after the update")
				harness.WaitForApplicationRunningStatus(deviceId, containerAppName)
				harness.WaitForApplicationRunningStatus(deviceId, quadletImageAppName)
				harness.WaitForApplicationRunningStatus(deviceId, inlineAppName)
				harness.WaitForRunningApplicationsCount(deviceId, 3)
				harness.WaitForApplicationsSummaryStatus(deviceId, v1beta1.ApplicationsSummaryStatusHealthy)
			})

		It(`Verifies that a missing template label in fleet applications causes device rollout failure,
		    and adding the label allows the device to reconcile and applications to become healthy`,
			Label("88385", "sanity"), func() {
				fleetTestName := fmt.Sprintf("templated-neg-app-fleet-%s", testID)

				By("Check that the device status is Online")
				_, err := harness.CheckDeviceStatus(deviceId, v1beta1.DeviceSummaryStatusOnline)
				Expect(err).ToNot(HaveOccurred())

				By("Create a fleet with parametrisable application templates using container, quadlet, and inline apps")
				err = harness.CreateOrUpdateTestFleet(fleetTestName, appFleetSelector, negTestDeviceSpec)
				Expect(err).ToNot(HaveOccurred())

				By("Add labels to the device WITHOUT the artifact label")
				nextRenderedVersion, err := harness.PrepareNextDeviceVersion(deviceId)
				Expect(err).ToNot(HaveOccurred())

				err = harness.UpdateDeviceWithRetries(deviceId, func(device *v1beta1.Device) {
					harness.SetLabelsForDeviceMetadata(&device.Metadata, map[string]string{
						appFleetSelectorKey: appFleetSelectorValue,
						containerLabelKey:   containerLabelValue,
						quadletLabelKey:     quadletLabelValue,
						inlineLabelKey:      inlineTag,
					})
					GinkgoWriter.Printf("Updating %s with labels (missing artifact)\n", deviceId)
				})
				Expect(err).ToNot(HaveOccurred())

				By("Check the device fails to reconcile due to missing artifact label")
				harness.WaitForDeviceContents(deviceId, deviceCouldNotBeUpdatedToFleetMsg, isDeviceUpdatedStatusOutOfDate, testutil.TIMEOUT_5M)

				By("Verify fleet controller error annotation references the missing artifact label")
				Eventually(func() error {
					return checkFleetControllerErrorAnnotation(harness, deviceId, "no entry for key \"artifact\"")
				}, 30*time.Second, testutil.POLLING).Should(BeNil(), "Fleet controller error annotation should reference missing artifact label")

				resp, err := harness.Client.GetDeviceStatusWithResponse(harness.Context, deviceId)
				Expect(err).ToNot(HaveOccurred())
				Expect(resp.JSON200).ToNot(BeNil(), "expected 200 response, got %d", resp.StatusCode())
				device := resp.JSON200
				Expect(device.Status.Updated.Status).To(Equal(v1beta1.DeviceUpdatedStatusOutOfDate))
				Expect((*device.Metadata.Annotations)[fleetControllerErrorAnnotation]).To(ContainSubstring("no entry for key \"artifact\""))

				By("Add the missing artifact label to the device")
				err = harness.UpdateDeviceWithRetries(deviceId, func(device *v1beta1.Device) {
					(*device.Metadata.Labels)[artifactLabelKey] = artifactLabelValue
					GinkgoWriter.Printf("Updating %s with label %s=%s\n", deviceId, artifactLabelKey, artifactLabelValue)
				})
				Expect(err).ToNot(HaveOccurred())

				By("Verify the device now has the artifact label")
				resp, err = harness.Client.GetDeviceStatusWithResponse(harness.Context, deviceId)
				Expect(err).ToNot(HaveOccurred())
				Expect(resp.JSON200).ToNot(BeNil(), "expected 200 response, got %d", resp.StatusCode())
				device = resp.JSON200
				Expect((*device.Metadata.Labels)[artifactLabelKey]).To(Equal(artifactLabelValue))

				By("Wait for the device to reconcile with the fleet")
				err = harness.WaitForDeviceNewRenderedVersion(deviceId, nextRenderedVersion)
				Expect(err).ToNot(HaveOccurred())

				By("Verify that all template variables are resolved in the rendered device spec")
				refs, err := harness.GetDeviceRenderedAppRefs(deviceId, containerAppName, quadletImageAppName, inlineAppName)
				Expect(err).ToNot(HaveOccurred())
				Expect(refs.ContainerImage).To(Equal(fmt.Sprintf("%s:%s", nginxImage, containerLabelValue)))
				Expect(refs.QuadletImage).To(Equal(fmt.Sprintf("%s:%s", quadletArtifactImage, quadletLabelValue)))
				Expect(refs.QuadletVolRef).To(Equal(fmt.Sprintf("%s:%s", modelArtifactImage, artifactLabelValue)))
				Expect(refs.InlineContent).To(ContainSubstring(fmt.Sprintf("%s:%s", alpineImage, inlineTag)))
				Expect(refs.ContainerImage).ToNot(ContainSubstring("{{"))
				Expect(refs.QuadletImage).ToNot(ContainSubstring("{{"))
				Expect(refs.QuadletVolRef).ToNot(ContainSubstring("{{"))
				Expect(refs.InlineContent).ToNot(ContainSubstring("{{"))

				By("Ensure that all applications start and become healthy")
				harness.WaitForApplicationRunningStatus(deviceId, containerAppName)
				harness.WaitForApplicationRunningStatus(deviceId, quadletImageAppName)
				harness.WaitForApplicationRunningStatus(deviceId, inlineAppName)
				harness.WaitForRunningApplicationsCount(deviceId, 3)
				harness.WaitForApplicationsSummaryStatus(deviceId, v1beta1.ApplicationsSummaryStatusHealthy)
			})
	})
})

func isDeviceUpdatedStatusOutOfDate(device *v1beta1.Device) bool {
	if device == nil || device.Status == nil || device.Status.Updated.Status == "" {
		return false
	}
	return device.Status.Updated.Status == v1beta1.DeviceUpdatedStatusOutOfDate
}

// checkFleetControllerErrorAnnotation verifies that the device has the fleet controller
// error annotation set and that it contains the expected key substring. Returns an error
// suitable for use inside Eventually blocks for retryable polling.
func checkFleetControllerErrorAnnotation(harness *e2e.Harness, deviceId, expectedKeySubstring string) error {
	resp, err := harness.Client.GetDeviceStatusWithResponse(harness.Context, deviceId)
	if err != nil {
		return fmt.Errorf("GetDeviceStatusWithResponse failed: %w", err)
	}
	if resp.JSON200 == nil {
		return fmt.Errorf("expected 200 response, got %d", resp.StatusCode())
	}
	device := resp.JSON200
	if device.Status.Updated.Status != v1beta1.DeviceUpdatedStatusOutOfDate {
		return fmt.Errorf("device status is not OutOfDate, got %s", device.Status.Updated.Status)
	}
	if device.Metadata.Annotations == nil {
		return fmt.Errorf("device annotations are nil")
	}
	errorAnnotation, exists := (*device.Metadata.Annotations)[fleetControllerErrorAnnotation]
	if !exists || errorAnnotation == "" {
		return fmt.Errorf("%s annotation not set", fleetControllerErrorAnnotation)
	}
	if !strings.Contains(errorAnnotation, expectedKeySubstring) {
		return fmt.Errorf("%s does not contain expected substring %q, got: %s",
			fleetControllerErrorAnnotation, expectedKeySubstring, errorAnnotation)
	}
	GinkgoWriter.Printf("checkFleetControllerErrorAnnotation: device %s has annotation with expected substring %q\n",
		deviceId, expectedKeySubstring)
	return nil
}

// getGitEnv retrieves git server configuration and SSH credentials from the auxiliary services.
func getGitEnv(ctx context.Context) (e2e.GitServerConfig, string, int, testutil.SSHPrivateKeyPath, testutil.SSHPrivateKeyContent, error) {
	svc := auxiliary.Get(ctx)
	if svc == nil {
		return e2e.GitServerConfig{}, "", 0, "", "", fmt.Errorf("auxiliary services not initialized")
	}
	config := e2e.GitServerConfig{
		Host: svc.GitServer.Host,
		Port: svc.GitServer.Port,
		User: "user",
	}
	keyPath, err := svc.GetGitSSHPrivateKeyPath()
	if err != nil {
		return config, "", 0, "", "", fmt.Errorf("failed to get git SSH private key path: %w", err)
	}
	keyContent, err := svc.GetGitSSHPrivateKey()
	if err != nil {
		return config, "", 0, "", "", fmt.Errorf("failed to get git SSH private key content: %w", err)
	}
	GinkgoWriter.Printf("getGitEnv: host=%s internalHost=%s internalPort=%d\n",
		config.Host, svc.GitServer.InternalHost, svc.GitServer.InternalPort)
	return config, svc.GitServer.InternalHost, svc.GitServer.InternalPort, keyPath, keyContent, nil
}
