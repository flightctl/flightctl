package configuration_test

import (
	"fmt"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/test/harness/e2e"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/sirupsen/logrus"
)

var _ = Describe("Inline configuration tests", func() {
	var (
		deviceId string
	)
	// Setup for the suite
	BeforeEach(func() {
		deviceId, _ = harness.EnrollAndWaitForOnlineStatus()
	})

	Context("Inline config tests", func() {

		It("flighctl support inlineconfig with path, owner, permission and content", Label("78316", "sanity"), func() {

			By("Update device with inline config, set path of the config (the fields that have defaults - don't set (mode,user, group)")
			validConfigs, err := getConfigurationFromInlineConfig(validInlineConfig)
			Expect(err).ToNot(HaveOccurred())

			newRenderedVersion, err := harness.PrepareNextDeviceVersion(deviceId)
			Expect(err).ToNot(HaveOccurred())

			err = harness.UpdateDeviceConfigWithRetries(deviceId, validConfigs, newRenderedVersion)
			Expect(err).ToNot(HaveOccurred())

			logrus.Infof("The configuration file should have the online config, the content is empty.")
			stdout, err := harness.VM.RunSSH([]string{"cat", inlinePath}, nil)
			Expect(err).ToNot(HaveOccurred())
			Expect(stdout.String()).To(ContainSubstring(""))

			logrus.Infof("The deconfiguration file should have the default owner permissions:root.")
			owner, err := harness.VM.RunSSH([]string{"stat --format='%U %G'", inlinePath}, nil)
			Expect(err).ToNot(HaveOccurred())
			Expect(owner.String()).To(ContainSubstring(fmt.Sprintf("%s %s", "root", "root")))

			logrus.Infof("The configuration file should have the default permissions: 0644.")
			mode, err := harness.VM.RunSSH([]string{"stat -c %A", inlinePath}, nil)
			Expect(err).ToNot(HaveOccurred())
			Expect(mode.String()).To(ContainSubstring(inlineDefaultNotationMode))

			By("Update device with inline config, set file mode")
			validConfigsWithMode, err := getConfigurationFromInlineConfig(validInlineConfigWithMode)
			Expect(err).ToNot(HaveOccurred())

			newRenderedVersion, err = harness.PrepareNextDeviceVersion(deviceId)
			Expect(err).ToNot(HaveOccurred())

			err = harness.UpdateDeviceConfigWithRetries(deviceId, validConfigsWithMode, newRenderedVersion)
			Expect(err).ToNot(HaveOccurred())

			logrus.Infof("The configuration file should have the correct permissions.")
			mode, err = harness.VM.RunSSH([]string{"stat -c %A", inlinePath}, nil)
			Expect(err).ToNot(HaveOccurred())
			Expect(mode.String()).To(ContainSubstring(inlineNotationMode))

			By("Update device with inline config, set the owner")
			validConfigsWithUser, err := getConfigurationFromInlineConfig(validInlineConfigWithUser)
			Expect(err).ToNot(HaveOccurred())

			newRenderedVersion, err = harness.PrepareNextDeviceVersion(deviceId)
			Expect(err).ToNot(HaveOccurred())

			err = harness.UpdateDeviceConfigWithRetries(deviceId, validConfigsWithUser, newRenderedVersion)
			Expect(err).ToNot(HaveOccurred())

			logrus.Infof("The configuration file should have the updated owner permissions.")
			owner, err = harness.VM.RunSSH([]string{"stat --format='%U %G'", inlinePath}, nil)
			Expect(err).ToNot(HaveOccurred())
			Expect(owner.String()).To(ContainSubstring(fmt.Sprintf("%s %s", inlineUser, inlineGroup)))

			By("Update device with inline config, set the content")
			validConfigsWithContent, err := getConfigurationFromInlineConfig(validInlineConfigWithContent)
			Expect(err).ToNot(HaveOccurred())

			newRenderedVersion, err = harness.PrepareNextDeviceVersion(deviceId)
			Expect(err).ToNot(HaveOccurred())

			err = harness.UpdateDeviceConfigWithRetries(deviceId, validConfigsWithContent, newRenderedVersion)
			Expect(err).ToNot(HaveOccurred())

			logrus.Infof("The configuration file should have the updated content")
			stdout1, err := harness.VM.RunSSH([]string{"cat", inlinePath1}, nil)
			Expect(err).ToNot(HaveOccurred())
			Expect(stdout1.String()).To(ContainSubstring(inlineContent))

			By("Update device with inline config, change the path")
			validConfigsWithPath2, err := getConfigurationFromInlineConfig(validInlineConfigWithPath2)
			Expect(err).ToNot(HaveOccurred())

			newRenderedVersion, err = harness.PrepareNextDeviceVersion(deviceId)
			Expect(err).ToNot(HaveOccurred())

			err = harness.UpdateDeviceConfigWithRetries(deviceId, validConfigsWithPath2, newRenderedVersion)
			Expect(err).ToNot(HaveOccurred())

			logrus.Infof("The configuration file should have the updated content.")
			stdout, err = harness.VM.RunSSH([]string{"cat", inlinePath2}, nil)
			Expect(err).ToNot(HaveOccurred())
			Expect(stdout.String()).To(ContainSubstring(inlineContent))

			By("Update device with inline config, change the inline config name")
			validConfigsWithName2, err := getConfigurationFromInlineConfig(validInlineConfigWithName2)
			Expect(err).ToNot(HaveOccurred())

			newRenderedVersion, err = harness.PrepareNextDeviceVersion(deviceId)
			Expect(err).ToNot(HaveOccurred())
			err = harness.UpdateDeviceConfigWithRetries(deviceId, validConfigsWithName2, newRenderedVersion)
			Expect(err).ToNot(HaveOccurred())

			By("Update device with inline config, add another file to inline config")
			validConfigsWith2Files, err := getConfigurationFromInlineConfig(validInlineConfigWith2Files)
			Expect(err).ToNot(HaveOccurred())

			newRenderedVersion, err = harness.PrepareNextDeviceVersion(deviceId)
			Expect(err).ToNot(HaveOccurred())
			err = harness.UpdateDeviceConfigWithRetries(deviceId, validConfigsWith2Files, newRenderedVersion)
			Expect(err).ToNot(HaveOccurred())

			logrus.Infof("The configuration file should have the updated content.")
			stdout, err = harness.VM.RunSSH([]string{"cat", inlinePath2}, nil)
			Expect(err).ToNot(HaveOccurred())
			Expect(stdout.String()).To(ContainSubstring(inlineContent))

			By("Update device with inline config, add another inline config")
			combinedConfigs := &[]v1alpha1.ConfigProviderSpec{validConfigsWithContent[0], validConfigsWithName2[0]}

			newRenderedVersion, err = harness.PrepareNextDeviceVersion(deviceId)
			Expect(err).ToNot(HaveOccurred())

			err = harness.UpdateDeviceConfigWithRetries(deviceId, *combinedConfigs, newRenderedVersion)
			Expect(err).ToNot(HaveOccurred())

			logrus.Infof("The configuration file should have the updated content.")
			stdout, err = harness.VM.RunSSH([]string{"cat", inlinePath2}, nil)
			Expect(err).ToNot(HaveOccurred())
			Expect(stdout.String()).To(ContainSubstring(inlineContent))
		})
		It("Validations for flighctl inlineconfigs", Label("78364", "sanity"), func() {
			currentVersion1, err := harness.GetCurrentDeviceRenderedVersion(deviceId)
			Expect(err).ToNot(HaveOccurred())

			By("Try to update device with inline config without mandatory fields: path")
			invalidInlineConfigsNoPath, err := getConfigurationFromInlineConfig(invalidInlineConfigWithoutPath)
			Expect(err).ToNot(HaveOccurred())

			err = UpdateDeviceConfig(harness, deviceId, invalidInlineConfigsNoPath)

			Expect(err).To(HaveOccurred(), "Expected an error when updating device with missing path")
			Expect(err.Error()).To(ContainSubstring("path: Invalid value"))

			currentVersion2, err := harness.GetCurrentDeviceRenderedVersion(deviceId)
			Expect(err).ToNot(HaveOccurred())
			Expect(currentVersion1).To(Equal(currentVersion2))

			By("Try to update device with inline config without mandatory fields: name")
			invalidInlineConfigsNoName, err := getConfigurationFromInlineConfig(invalidInlineConfigNoName)
			Expect(err).ToNot(HaveOccurred())

			err = UpdateDeviceConfig(harness, deviceId, invalidInlineConfigsNoName)

			Expect(err).To(HaveOccurred(), "Expected an error when updating device with missing name")
			Expect(err.Error()).To(ContainSubstring("name: Invalid value"))

			currentVersion2, err = harness.GetCurrentDeviceRenderedVersion(deviceId)
			Expect(err).ToNot(HaveOccurred())
			Expect(currentVersion1).To(Equal(currentVersion2))

			By("Try to update device with inline config wit not absolute path")
			invalidInlineConfigsRelativePath, err := getConfigurationFromInlineConfig(invalidInlineConfigRelativePath)
			Expect(err).ToNot(HaveOccurred())

			err = UpdateDeviceConfig(harness, deviceId, invalidInlineConfigsRelativePath)
			Expect(err).To(HaveOccurred(), "Expected an error when updating device with relative path")
			Expect(err.Error()).To(ContainSubstring("must be an absolute path"))

			currentVersion2, err = harness.GetCurrentDeviceRenderedVersion(deviceId)
			Expect(err).ToNot(HaveOccurred())
			Expect(currentVersion1).To(Equal(currentVersion2))

			By("Try to update device with inline config with invalid file mode")
			invalidInlineConfigsInvalidMode, err := getConfigurationFromInlineConfig(invalidInlineConfigWithInvalidMode)
			Expect(err).ToNot(HaveOccurred())

			err = UpdateDeviceConfig(harness, deviceId, invalidInlineConfigsInvalidMode)

			Expect(err).To(HaveOccurred(), "Expected an error when updating device with invalid permission mode")
			Expect(err.Error()).To(ContainSubstring("mode: Invalid value"))

			By("Verify the rendered version wasn't upgraded")
			currentVersion2, err = harness.GetCurrentDeviceRenderedVersion(deviceId)
			Expect(err).ToNot(HaveOccurred())
			Expect(currentVersion1).To(Equal(currentVersion2))
		})
	})
})

var (
	inlineMode                = 0666
	inlineNotationMode        = "-rw-rw-rw-"
	inlineDefaultNotationMode = "-rw-r--r--"
	inlineModePointer         = &inlineMode
	invalidInlineMode         = 9999
	inlineContent             = "This system is managed by flightctl"
	inlinePath                = "/etc/inline"
	inlinePath1               = "/etc/inline1"
	inlinePath2               = "/etc/inline2"
	relativePath              = "etc/inline3"
	inlineUser                = "user"
	inlineGroup               = "user"
	inlineName1               = "valid-inline-config"
	inlineName2               = "valid-inline-config-2"
	inlineName2files          = "valid-inline-config-2-files"
	invalidInlineName1        = "invalid-inline-config"
)

// Create reusable FileSpecs
var (
	inlineConfig                    = newFileSpec(inlinePath, nil, nil, nil, "")
	inlineConfigMode                = newFileSpec(inlinePath, inlineModePointer, nil, nil, "")
	inlineConfigUser                = newFileSpec(inlinePath, inlineModePointer, &inlineUser, &inlineGroup, "")
	inlineConfigContent             = newFileSpec(inlinePath1, inlineModePointer, &inlineUser, &inlineGroup, inlineContent)
	inlineConfigPath2               = newFileSpec(inlinePath2, inlineModePointer, &inlineUser, &inlineGroup, inlineContent)
	invalidnlineConfigNoPath        = newFileSpec("", inlineModePointer, &inlineUser, &inlineGroup, inlineContent)
	invalidinlineConfigRelativePath = newFileSpec(relativePath, nil, nil, nil, "")
	invalidInlineConfigInvalidMode  = newFileSpec(inlinePath, &invalidInlineMode, nil, nil, "")
)

// Create InlineConfigProviderSpecs
var (
	validInlineConfig                  = newInlineConfigProviderSpec(inlineName1, []v1alpha1.FileSpec{inlineConfig})
	validInlineConfigWithMode          = newInlineConfigProviderSpec(inlineName1, []v1alpha1.FileSpec{inlineConfigMode})
	validInlineConfigWithUser          = newInlineConfigProviderSpec(inlineName1, []v1alpha1.FileSpec{inlineConfigUser})
	validInlineConfigWithContent       = newInlineConfigProviderSpec(inlineName1, []v1alpha1.FileSpec{inlineConfigContent})
	validInlineConfigWithPath2         = newInlineConfigProviderSpec(inlineName1, []v1alpha1.FileSpec{inlineConfigPath2})
	validInlineConfigWithName2         = newInlineConfigProviderSpec(inlineName2, []v1alpha1.FileSpec{inlineConfigPath2})
	validInlineConfigWith2Files        = newInlineConfigProviderSpec(inlineName2files, []v1alpha1.FileSpec{inlineConfigPath2, inlineConfigContent})
	invalidInlineConfigWithoutPath     = newInlineConfigProviderSpec(invalidInlineName1, []v1alpha1.FileSpec{invalidnlineConfigNoPath})
	invalidInlineConfigNoName          = newInlineConfigProviderSpec("", []v1alpha1.FileSpec{inlineConfig})
	invalidInlineConfigRelativePath    = newInlineConfigProviderSpec(invalidInlineName1, []v1alpha1.FileSpec{invalidinlineConfigRelativePath})
	invalidInlineConfigWithInvalidMode = newInlineConfigProviderSpec(invalidInlineName1, []v1alpha1.FileSpec{invalidInlineConfigInvalidMode})
)

func UpdateDeviceConfig(harness *e2e.Harness, deviceId string, configs []v1alpha1.ConfigProviderSpec) error {
	err := harness.UpdateDevice(deviceId, func(device *v1alpha1.Device) {
		device.Spec.Config = &configs
		logrus.WithFields(logrus.Fields{
			"deviceId": deviceId,
			"config":   fmt.Sprintf("%+v", &device.Spec.Config),
		}).Info("Updating device with new config")
	})
	return err
}

func getConfigurationFromInlineConfig(inlineConfig v1alpha1.InlineConfigProviderSpec) ([]v1alpha1.ConfigProviderSpec, error) {
	var configItem = v1alpha1.ConfigProviderSpec{}
	err := configItem.FromInlineConfigProviderSpec(inlineConfig)
	if err != nil {
		return nil, err
	}
	validConfigs := &[]v1alpha1.ConfigProviderSpec{configItem}
	return *validConfigs, nil
}

// Helper function to generate a FileSpec
func newFileSpec(path string, mode *int, user *string, group *string, content string) v1alpha1.FileSpec {
	return v1alpha1.FileSpec{
		Path:    path,
		Mode:    mode,
		User:    user,
		Group:   group,
		Content: content,
	}
}

// Helper function to generate InlineConfigProviderSpec
func newInlineConfigProviderSpec(name string, files []v1alpha1.FileSpec) v1alpha1.InlineConfigProviderSpec {
	return v1alpha1.InlineConfigProviderSpec{
		Inline: files,
		Name:   name,
	}
}
