package configuration_test

import (
	"fmt"
	"testing"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/test/harness/e2e"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/sirupsen/logrus"
)

func TestConfigurations(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Inline configuration E2E Suite")
}

var _ = Describe("Inline configuration tests", Ordered, func() {
	var (
		harness  *e2e.Harness
		deviceId string
	)
	// Setup for the suite
	BeforeEach(func() {
		harness = e2e.NewTestHarness()
		deviceId = harness.StartVMAndEnroll()
	})

	AfterEach(func() {
		err := harness.CleanUpAllResources()
		Expect(err).ToNot(HaveOccurred())
	})

	Context("Inline config tests", func() {

		It("flighctl support inlineconfig with path, owner, permission and content", Label("78316"), func() {

			By("Update device with inline config, set path of the config (the fields that have defaults - don't set (mode,user, group)")
			validConfigs, err := getConfigurationFromInlineConfig(validInlineConfig)
			Expect(err).ToNot(HaveOccurred())
			err = harness.WaitForDeviceConfigUpdate(deviceId, validConfigs)
			Expect(err).ToNot(HaveOccurred())

			logrus.Infof("The device should have the online config.")
			stdout, err := harness.VM.RunSSH([]string{"cat", inlinePath}, nil)
			Expect(err).ToNot(HaveOccurred())
			Expect(stdout.String()).To(ContainSubstring(""))

			By("Update device with inline config, set file mode")
			validConfigsWithMode, err := getConfigurationFromInlineConfig(validInlineConfigWithMode)
			Expect(err).ToNot(HaveOccurred())
			err = harness.WaitForDeviceConfigUpdate(deviceId, validConfigsWithMode)
			Expect(err).ToNot(HaveOccurred())

			logrus.Infof("The device should have the correct permissions.")
			mode, err := harness.VM.RunSSH([]string{"stat -c %A", inlinePath}, nil)
			Expect(err).ToNot(HaveOccurred())
			Expect(mode.String()).To(ContainSubstring(inlineNotationMode))

			By("Update device with inline config, set the owner")
			validConfigsWithUser, err := getConfigurationFromInlineConfig(validInlineConfigWithUser)
			Expect(err).ToNot(HaveOccurred())
			err = harness.WaitForDeviceConfigUpdate(deviceId, validConfigsWithUser)
			Expect(err).ToNot(HaveOccurred())

			logrus.Infof("The device should have the updated owner permissions.")
			owner, err := harness.VM.RunSSH([]string{"stat --format='%U %G'", inlinePath}, nil)
			Expect(err).ToNot(HaveOccurred())
			Expect(owner.String()).To(ContainSubstring(fmt.Sprintf("%s %s", inlineUser, inlineGroup)))

			By("Update device with inline config, set the content")
			validConfigsWithContent, err := getConfigurationFromInlineConfig(validInlineConfigWithContent)
			Expect(err).ToNot(HaveOccurred())
			err = harness.WaitForDeviceConfigUpdate(deviceId, validConfigsWithContent)
			Expect(err).ToNot(HaveOccurred())

			logrus.Infof("The device should have the updated content.")
			stdout, err = harness.VM.RunSSH([]string{"cat", inlinePath}, nil)
			Expect(err).ToNot(HaveOccurred())
			Expect(stdout.String()).To(ContainSubstring(inlineContent))

			By("Update device with inline config, change the path")
			validConfigsWithPath2, err := getConfigurationFromInlineConfig(validInlineConfigWithPath2)
			Expect(err).ToNot(HaveOccurred())
			err = harness.WaitForDeviceConfigUpdate(deviceId, validConfigsWithPath2)
			Expect(err).ToNot(HaveOccurred())

			logrus.Infof("The device should have the updated content.")
			stdout, err = harness.VM.RunSSH([]string{"cat", inlinePath2}, nil)
			Expect(err).ToNot(HaveOccurred())
			Expect(stdout.String()).To(ContainSubstring(inlineContent))

			By("Update device with inline config, change the inline config name")
			validConfigsWithName2, err := getConfigurationFromInlineConfig(validInlineConfigWithName2)
			Expect(err).ToNot(HaveOccurred())
			err = harness.WaitForDeviceConfigUpdate(deviceId, validConfigsWithName2)
			Expect(err).ToNot(HaveOccurred())

			By("Update device with inline config, add another file to inline config")
			validConfigsWith2Files, err := getConfigurationFromInlineConfig(validInlineConfigWith2Files)
			Expect(err).ToNot(HaveOccurred())
			err = harness.WaitForDeviceConfigUpdate(deviceId, validConfigsWith2Files)
			Expect(err).ToNot(HaveOccurred())

			logrus.Infof("The device should have the updated content.")
			stdout, err = harness.VM.RunSSH([]string{"cat", inlinePath}, nil)
			Expect(err).ToNot(HaveOccurred())
			Expect(stdout.String()).To(ContainSubstring(inlineContent))

			By("Update device with inline config, add another inline config")
			combinedConfigs := &[]v1alpha1.ConfigProviderSpec{validConfigsWithContent[0], validConfigsWithName2[0]}
			err = harness.WaitForDeviceConfigUpdate(deviceId, *combinedConfigs)
			Expect(err).ToNot(HaveOccurred())

			logrus.Infof("The device should have the updated content.")
			stdout, err = harness.VM.RunSSH([]string{"cat", inlinePath}, nil)
			Expect(err).ToNot(HaveOccurred())
			Expect(stdout.String()).To(ContainSubstring(inlineContent))
		})
	})
})

func getConfigurationFromInlineConfig(inlineConfig v1alpha1.InlineConfigProviderSpec) ([]v1alpha1.ConfigProviderSpec, error) {
	var configItem = v1alpha1.ConfigProviderSpec{}
	err := configItem.FromInlineConfigProviderSpec(inlineConfig)
	if err != nil {
		return nil, err
	}
	validConfigs := &[]v1alpha1.ConfigProviderSpec{configItem}
	return *validConfigs, nil
}

var (
	inlineMode         = 0666
	inlineNotationMode = "-rw-rw-rw-"
	inlineModePointer  = &inlineMode
	inlineContent      = "This system is managed by flightctl."
	inlinePath         = "/etc/inline"
	inlinePath2        = "/etc/inline2"
	inlineUser         = "user"
	inlineGroup        = "user"
	inlineName1        = "valid-inline-config"
	inlineName2        = "valid-inline-config-2"
	inlineName2files   = "valid-inline-config-2-files"
)

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

// Create reusable FileSpecs
var (
	inlineConfig        = newFileSpec(inlinePath, nil, nil, nil, "")
	inlineConfigMode    = newFileSpec(inlinePath, inlineModePointer, nil, nil, "")
	inlineConfigUser    = newFileSpec(inlinePath, inlineModePointer, &inlineUser, &inlineGroup, "")
	inlineConfigContent = newFileSpec(inlinePath, inlineModePointer, &inlineUser, &inlineGroup, inlineContent)
	inlineConfigPath2   = newFileSpec(inlinePath2, inlineModePointer, &inlineUser, &inlineGroup, inlineContent)
)

// Create InlineConfigProviderSpecs
var (
	validInlineConfig            = newInlineConfigProviderSpec(inlineName1, []v1alpha1.FileSpec{inlineConfig})
	validInlineConfigWithMode    = newInlineConfigProviderSpec(inlineName1, []v1alpha1.FileSpec{inlineConfigMode})
	validInlineConfigWithUser    = newInlineConfigProviderSpec(inlineName1, []v1alpha1.FileSpec{inlineConfigUser})
	validInlineConfigWithContent = newInlineConfigProviderSpec(inlineName1, []v1alpha1.FileSpec{inlineConfigContent})
	validInlineConfigWithPath2   = newInlineConfigProviderSpec(inlineName1, []v1alpha1.FileSpec{inlineConfigPath2})
	validInlineConfigWithName2   = newInlineConfigProviderSpec(inlineName2, []v1alpha1.FileSpec{inlineConfigPath2})
	validInlineConfigWith2Files  = newInlineConfigProviderSpec(inlineName2files, []v1alpha1.FileSpec{inlineConfigPath2, inlineConfigContent})
)
