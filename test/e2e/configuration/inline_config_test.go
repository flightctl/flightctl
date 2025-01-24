package configuration_test

import (
	"fmt"
	"testing"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/test/harness/e2e"
	"github.com/flightctl/flightctl/test/login"
	"github.com/flightctl/flightctl/test/util"
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
		harness *e2e.Harness
	)
	// Setup for the suite
	BeforeEach(func() {
		harness = e2e.NewTestHarness()

		err := harness.VM.RunAndWaitForSSH()
		Expect(err).ToNot(HaveOccurred())
		login.LoginToAPIWithToken(harness)
		logrus.Infof("=0=")
	})

	AfterEach(func() {
		harness.Cleanup(true)
	})

	Context("Inline config tests", func() {
		It("flighctl support inlineconfig with path, owner, permission and content", Label("78316"), func() {
			// Wait for the top-most part of the QR output to appear
			Eventually(harness.VM.GetConsoleOutput, util.TIMEOUT, util.POLLING).Should(ContainSubstring("████████████████████████████████"))
			logrus.Infof("=1=")
			deviceId, _ := harness.EnrollAndWaitForOnlineStatus()
			logrus.Infof("=2=")

			By("Update device with inline config, set path of the config (the fields that have defaults - don't set (mode,user, group)")
			err := harness.UpdateAndWaitForDeviceConfigChange(deviceId, validInlineConfig)
			Expect(err).ToNot(HaveOccurred())

			logrus.Infof("The device should have the online config.")
			stdout, err := harness.VM.RunSSH([]string{"cat", inlinePath}, nil)
			Expect(err).ToNot(HaveOccurred())
			Expect(stdout.String()).To(ContainSubstring(""))

			By("Update device with inline config, set file mode")
			err = harness.UpdateAndWaitForDeviceConfigChange(deviceId, validInlineConfigWithMode)
			Expect(err).ToNot(HaveOccurred())

			logrus.Infof("The device should have the correct permissions.")
			_, err = harness.VM.RunSSH([]string{"stat --format='%a'", inlinePath}, nil)
			Expect(err).ToNot(HaveOccurred())
			//Expect(perm).Equal(inlineMode) //?

			By("Update device with inline config, set the owner")
			err = harness.UpdateAndWaitForDeviceConfigChange(deviceId, validInlineConfigWithUser)
			Expect(err).ToNot(HaveOccurred())

			logrus.Infof("The device should have the updated owner permissions.")
			owner, err := harness.VM.RunSSH([]string{"stat --format='%U %G'", inlinePath}, nil)
			Expect(err).ToNot(HaveOccurred())
			Expect(owner.String()).To(ContainSubstring(fmt.Sprintf("%s %s", inlineUser, inlineGroup)))

			By("Update device with inline config, set the content")
			err = harness.UpdateAndWaitForDeviceConfigChange(deviceId, validInlineConfigWithUser)
			Expect(err).ToNot(HaveOccurred())

			logrus.Infof("The device should have the updated content.")
			stdout, err = harness.VM.RunSSH([]string{"cat", inlinePath}, nil)
			Expect(err).ToNot(HaveOccurred())
			Expect(stdout.String()).To(ContainSubstring(inlineContent))

			By("Update device with inline config, change the path")
			err = harness.UpdateAndWaitForDeviceConfigChange(deviceId, validInlineConfigWithPath2)
			Expect(err).ToNot(HaveOccurred())

			logrus.Infof("The device should have the updated content.")
			stdout, err = harness.VM.RunSSH([]string{"cat", inlinePath2}, nil)
			Expect(err).ToNot(HaveOccurred())
			Expect(stdout.String()).To(ContainSubstring(inlineContent))

			By("Update device with inline config, change the inline config name")
			err = harness.UpdateAndWaitForDeviceConfigChange(deviceId, validInlineConfigWithName2)
			Expect(err).ToNot(HaveOccurred())

			By("Update device with inline config, add another file to inline config")
			err = harness.UpdateAndWaitForDeviceConfigChange(deviceId, validInlineConfig2Files)
			Expect(err).ToNot(HaveOccurred())

			logrus.Infof("The device should have the updated content.")
			stdout, err = harness.VM.RunSSH([]string{"cat", inlinePath}, nil)
			Expect(err).ToNot(HaveOccurred())
			Expect(stdout.String()).To(ContainSubstring(inlineContent))

			By("Update device with inline config, add another inline config")
			err = harness.UpdateAndWaitForDeviceConfigChange(deviceId, validInlineConfig2configs)
			Expect(err).ToNot(HaveOccurred())

			logrus.Infof("The device should have the updated content.")
			stdout, err = harness.VM.RunSSH([]string{"cat", inlinePath}, nil)
			Expect(err).ToNot(HaveOccurred())
			Expect(stdout.String()).To(ContainSubstring(inlineContent))
		})
	})
})

var (
	inlineMode        = 0777
	inlineModePointer = &inlineMode
	inlineContent     = "This system is managed by flightctl."
	inlinePath        = "/etc/inline"
	inlinePath2       = "/etc/inline2"
	inlineUser        = "user"
	inlineGroup       = "user"
)

var inlineConfig = v1alpha1.FileSpec{
	Path: inlinePath,
}

var inlineConfigMode = v1alpha1.FileSpec{
	Path: inlinePath,
	Mode: inlineModePointer,
}

var inlineConfigUser = v1alpha1.FileSpec{
	Path:  inlinePath,
	Mode:  inlineModePointer,
	User:  &inlineUser,
	Group: &inlineGroup,
}

var inlineConfigContent = v1alpha1.FileSpec{
	Path:    inlinePath,
	Mode:    inlineModePointer,
	User:    &inlineUser,
	Group:   &inlineGroup,
	Content: inlineContent,
}

var inlineConfigPath2 = v1alpha1.FileSpec{
	Path:    inlinePath2,
	Mode:    inlineModePointer,
	User:    &inlineUser,
	Group:   &inlineGroup,
	Content: inlineContent,
}

var validInlineConfig = []v1alpha1.InlineConfigProviderSpec{
	{
		Inline: []v1alpha1.FileSpec{inlineConfig},
		Name:   "valid-inline-config",
	},
}

var validInlineConfigWithMode = []v1alpha1.InlineConfigProviderSpec{
	{
		Inline: []v1alpha1.FileSpec{inlineConfigMode},
		Name:   "valid-inline-config",
	},
}

var validInlineConfigWithUser = []v1alpha1.InlineConfigProviderSpec{
	{
		Inline: []v1alpha1.FileSpec{inlineConfigUser},
		Name:   "valid-inline-config",
	},
}

var validInlineConfigWithContent = []v1alpha1.InlineConfigProviderSpec{
	{
		Inline: []v1alpha1.FileSpec{inlineConfigContent},
		Name:   "valid-inline-config",
	},
}

var validInlineConfigWithPath2 = []v1alpha1.InlineConfigProviderSpec{
	{
		Inline: []v1alpha1.FileSpec{inlineConfigPath2},
		Name:   "valid-inline-config",
	},
}

var validInlineConfigWithName2 = []v1alpha1.InlineConfigProviderSpec{
	{
		Inline: []v1alpha1.FileSpec{inlineConfigPath2},
		Name:   "valid-inline-config-2",
	},
}

var validInlineConfig2Files = []v1alpha1.InlineConfigProviderSpec{
	{
		Inline: []v1alpha1.FileSpec{inlineConfigPath2, inlineConfigContent},
		Name:   "valid-inline-config-2-files",
	},
}

var validInlineConfig2configs = []v1alpha1.InlineConfigProviderSpec{
	{
		Inline: []v1alpha1.FileSpec{inlineConfigPath2},
		Name:   "valid-inline-config",
	},
	{
		Inline: []v1alpha1.FileSpec{inlineConfig},
		Name:   "valid-inline-config-2",
	},
}
