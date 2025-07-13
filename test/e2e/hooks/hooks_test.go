package hooks

import (
	"fmt"

	"github.com/flightctl/flightctl/api/v1alpha1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/sirupsen/logrus"
)

var _ = Describe("Device lifecycles and embedded hooks tests", func() {
	var (
		deviceId string
	)

	BeforeEach(func() {
		deviceId, _ = harness.EnrollAndWaitForOnlineStatus()
	})

	Context("hooks", func() {
		It(`Verifies that lifecycles hooks are triggered after the device and agent events`, Label("78753", "sanity"), func() {

			By("Update the device image to one containing an embedded hook")
			_, err := harness.CheckDeviceStatus(deviceId, v1alpha1.DeviceSummaryStatusOnline)
			Expect(err).ToNot(HaveOccurred())

			nextRenderedVersion, err := harness.PrepareNextDeviceVersion(deviceId)
			Expect(err).ToNot(HaveOccurred())

			deviceImage := fmt.Sprintf("%s/flightctl-device:v6", harness.RegistryEndpoint())

			var osImageSpec = v1alpha1.DeviceOsSpec{
				Image: deviceImage,
			}

			err = harness.UpdateDeviceWithRetries(deviceId, func(device *v1alpha1.Device) {
				device.Spec.Os = &osImageSpec

				logrus.Infof("Updating %s with Os image", osImageSpec)
			})
			Expect(err).ToNot(HaveOccurred())

			err = harness.WaitForDeviceNewRenderedVersion(deviceId, nextRenderedVersion)
			Expect(err).ToNot(HaveOccurred())

			By("Add an inline configuration for sshd")
			nextRenderedVersion, err = harness.PrepareNextDeviceVersion(deviceId)
			Expect(err).ToNot(HaveOccurred())

			inlineConfigProviderSpec := v1alpha1.ConfigProviderSpec{}
			err = inlineConfigProviderSpec.FromInlineConfigProviderSpec(inlineConfigValid)
			Expect(err).ToNot(HaveOccurred())

			deviceSpecConfig := []v1alpha1.ConfigProviderSpec{inlineConfigProviderSpec}

			err = harness.UpdateDeviceConfigWithRetries(deviceId, deviceSpecConfig, nextRenderedVersion)
			Expect(err).ToNot(HaveOccurred())

			stdout, err := harness.VM.RunSSH([]string{"sudo", "cat", inlinePath}, nil)
			Expect(err).ToNot(HaveOccurred())
			Expect(stdout.String()).To(ContainSubstring(sshdConfigurationContent))
			logrus.Infof("the configuration %s was found in the device", inlineConfigName)

			By("Check that the embedded sshd hook is triggered and sshd config reloaded trying to login with user and password")
			_, err = harness.VM.RunSSHWithUser([]string{"pwd"}, nil, rootUser)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring(tooManyAuthFailuresError))

			By("Update the sshd config")
			nextRenderedVersion, err = harness.PrepareNextDeviceVersion(deviceId)
			Expect(err).ToNot(HaveOccurred())

			err = inlineConfigProviderSpec.FromInlineConfigProviderSpec(inlineConfigValid2)
			Expect(err).ToNot(HaveOccurred())

			deviceSpecConfig = []v1alpha1.ConfigProviderSpec{inlineConfigProviderSpec}

			err = harness.UpdateDeviceConfigWithRetries(deviceId, deviceSpecConfig, nextRenderedVersion)
			Expect(err).ToNot(HaveOccurred())

			By("Check that the embedded sshd hook is triggered and sshd config reloaded by trying to ssh with any user")
			_, err = harness.VM.RunSSH([]string{"pwd"}, nil)

			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring(noPasswordLoginError))

			By("Verify that an embedded hook precedes an inline config hook with the same name")
			nextRenderedVersion, err = harness.PrepareNextDeviceVersion(deviceId)
			Expect(err).ToNot(HaveOccurred())

			err = inlineConfigProviderSpec.FromInlineConfigProviderSpec(inlineConfigValid3)
			Expect(err).ToNot(HaveOccurred())

			deviceSpecConfig = []v1alpha1.ConfigProviderSpec{inlineConfigProviderSpec}

			err = harness.UpdateDeviceConfigWithRetries(deviceId, deviceSpecConfig, nextRenderedVersion)
			Expect(err).ToNot(HaveOccurred())

			By("Update the sshd config")
			nextRenderedVersion, err = harness.PrepareNextDeviceVersion(deviceId)
			Expect(err).ToNot(HaveOccurred())

			inlineConfigProviderSpec1 := v1alpha1.ConfigProviderSpec{}

			err = inlineConfigProviderSpec1.FromInlineConfigProviderSpec(inlineConfigValid)
			Expect(err).ToNot(HaveOccurred())

			configProviderSpec := []v1alpha1.ConfigProviderSpec{inlineConfigProviderSpec, inlineConfigProviderSpec1}

			err = harness.UpdateDeviceConfigWithRetries(deviceId, configProviderSpec, nextRenderedVersion)
			Expect(err).ToNot(HaveOccurred())

			_, err = harness.VM.RunSSH([]string{"pwd"}, nil)
			Expect(err).ToNot(HaveOccurred())

			_, err = harness.VM.RunSSHWithUser([]string{"pwd"}, nil, rootUser)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring(tooManyAuthFailuresError))

			By("Remove the inline hook")
			nextRenderedVersion, err = harness.PrepareNextDeviceVersion(deviceId)
			Expect(err).ToNot(HaveOccurred())

			deviceSpecConfig = []v1alpha1.ConfigProviderSpec{}

			err = harness.UpdateDeviceConfigWithRetries(deviceId, deviceSpecConfig, nextRenderedVersion)
			Expect(err).ToNot(HaveOccurred())

			_, err = harness.VM.RunSSH([]string{"pwd"}, nil)
			Expect(err).ToNot(HaveOccurred())

			By("Check pre/after update and pre/after reboot hooks from inline config works")
			nextRenderedVersion, err = harness.PrepareNextDeviceVersion(deviceId)
			Expect(err).ToNot(HaveOccurred())
			deviceImage = fmt.Sprintf("%s/flightctl-device:base", harness.RegistryEndpoint())

			osImageSpec.Image = deviceImage
			err = inlineConfigProviderSpec.FromInlineConfigProviderSpec(inlineConfigValidLifecycle)
			Expect(err).ToNot(HaveOccurred())

			deviceSpecConfig = []v1alpha1.ConfigProviderSpec{inlineConfigProviderSpec}

			deviceSpec.Os = &osImageSpec
			deviceSpec.Config = &deviceSpecConfig

			err = harness.UpdateDeviceWithRetries(deviceId, func(device *v1alpha1.Device) {
				device.Spec = &deviceSpec
				logrus.Infof("Updating %s with a new image and configuration %s", deviceId, inlineConfigLifecycleName)
			})
			Expect(err).ToNot(HaveOccurred())

			err = harness.WaitForDeviceNewRenderedVersion(deviceId, nextRenderedVersion)
			Expect(err).ToNot(HaveOccurred())

			By("Check that in the device logs the hooks were triggered")
			logs, err := harness.ReadPrimaryVMAgentLogs("")
			Expect(err).NotTo(HaveOccurred())
			Expect(logs).To(ContainSubstring("this is a test message from afterupdating hook"))
			Expect(logs).To(ContainSubstring("this is a test message from afterrebooting hook"))
			Expect(logs).To(ContainSubstring("this is a test message from beforerebooting hook"))
			Expect(logs).To(ContainSubstring("this is a test message from beforeupdating hook"))
		})
		It("Verifies that lifecycle hooks can be defined with template variables", Label("80022"), func() {
			const (
				firstFileContents        = "this is a test message from afteradding hook"
				firstFilePath            = "temp-dir/file1.txt"
				firstFileName            = "first-create"
				secondFileContents       = "this is a second test message from afteradding hook"
				secondFilePath           = "secondary-dir/file2.txt"
				secondFileName           = "secondary-create"
				firstFileUpdatedContents = "this has been updated"
			)

			By("Adding a template hook that prints the contents of all created/updated files")
			nextRenderedVersion, err := harness.PrepareNextDeviceVersion(deviceId)
			Expect(err).ToNot(HaveOccurred())

			configSpec, err := newHookSpec()
			Expect(err).ToNot(HaveOccurred())
			err = harness.UpdateDeviceWithRetries(deviceId, func(device *v1alpha1.Device) {
				device.Spec.Config = &configSpec
			})
			Expect(err).ToNot(HaveOccurred())
			err = harness.WaitForDeviceNewRenderedVersion(deviceId, nextRenderedVersion)
			Expect(err).ToNot(HaveOccurred())

			By("adding a new file to the hooks watch directory")
			nextRenderedVersion, err = harness.PrepareNextDeviceVersion(deviceId)
			Expect(err).ToNot(HaveOccurred())
			configSpec, err = newHookSpec(templateSpecProviderArgs{
				name:    firstFileName,
				path:    firstFilePath,
				content: firstFileContents,
			})
			Expect(err).ToNot(HaveOccurred())
			err = harness.UpdateDeviceWithRetries(deviceId, func(device *v1alpha1.Device) {
				device.Spec.Config = &configSpec
			})
			Expect(err).ToNot(HaveOccurred())
			err = harness.WaitForDeviceNewRenderedVersion(deviceId, nextRenderedVersion)
			Expect(err).ToNot(HaveOccurred())
			// ensure we see our expected messages
			logs, err := harness.ReadPrimaryVMAgentLogs("")
			Expect(err).NotTo(HaveOccurred())
			Expect(logs).To(And(ContainSubstring(templateHookDirectory), ContainSubstring(firstFileContents)))

			By("adding a second file, and updating the first file to the hooks watch directory")
			nextRenderedVersion, err = harness.PrepareNextDeviceVersion(deviceId)
			Expect(err).ToNot(HaveOccurred())

			configSpec, err = newHookSpec(templateSpecProviderArgs{
				name:    firstFileName,
				path:    firstFilePath,
				content: firstFileUpdatedContents,
			}, templateSpecProviderArgs{
				name:    secondFileName,
				path:    secondFilePath,
				content: secondFileContents,
			})
			Expect(err).ToNot(HaveOccurred())
			err = harness.UpdateDeviceWithRetries(deviceId, func(device *v1alpha1.Device) {
				device.Spec.Config = &configSpec
			})
			Expect(err).ToNot(HaveOccurred())
			err = harness.WaitForDeviceNewRenderedVersion(deviceId, nextRenderedVersion)
			Expect(err).ToNot(HaveOccurred())

			// ensure we see our expected messages
			logs, err = harness.ReadPrimaryVMAgentLogs("")
			Expect(err).NotTo(HaveOccurred())
			Expect(logs).To(And(ContainSubstring(firstFileUpdatedContents), ContainSubstring(secondFileContents)))
		})
	})
})

var (
	inlinePath                = "/etc/ssh/sshd_config.d/custom-ssh.conf"
	inlineConfigName          = "sshd-inline-config"
	inlineConfigName3         = "sshd-hook-inline-config"
	inlineConfigLifecycleName = "lifecycle-hook-inline-config"
	rootUser                  = "root"
	hookPath                  = "/etc/flightctl/hooks.d/afterupdating/custom-hook.yaml"
	deviceSpec                v1alpha1.DeviceSpec
	noPasswordLoginError      = "user@localhost: Permission denied"
	tooManyAuthFailuresError  = "Too many authentication failures"
)

// sshdConfigurationContent defines the inline SSH configuration content for customizing the sshd settings on a device.
var sshdConfigurationContent = `
# Custom SSH Configuration
PasswordAuthentication yes
ClientAliveInterval 300
MaxAuthTries 1
`

// sshdConfigurationContent2 defines a multi-line string containing custom SSH server configuration settings.
var sshdConfigurationContent2 = `
# Custom SSH Configuration
PermitRootLogin yes
PasswordAuthentication no
ClientAliveInterval 300
MaxAuthTries 2
`

// sshdHook defines a YAML configuration string that triggers a validation of SSH daemon configuration upon certain file events.
var sshdHook = `
- if:
  - path: /etc/ssh/sshd_config.d/
    op: [created, updated, removed]
  run: sudo sshd -t
`

// templateHook defines a YAML configuration string that will print both the triggering Path and all updated files
// to the logger
var templateHook = `
- if:
  - path: /var/home/user/
    op: [created, updated]
  run: /usr/bin/bash -c "/usr/bin/logger \"${ Path }\"; for file in ${ Files }; do /usr/bin/logger -f \"$file\"; done;"
`

var mode = 0644
var modePointer = &mode
var inlineConfigSpec = v1alpha1.FileSpec{
	Path:    inlinePath,
	Mode:    modePointer,
	Content: sshdConfigurationContent,
}

// inlineConfigValid is an instance of InlineConfigProviderSpec configured with inline file specifications and a provider name.
var inlineConfigValid = v1alpha1.InlineConfigProviderSpec{
	Inline: []v1alpha1.FileSpec{inlineConfigSpec},
	Name:   inlineConfigName,
}

// inlineConfigSpec2 defines a file specification for creating a custom SSH server configuration file at a specified path.
var inlineConfigSpec2 = v1alpha1.FileSpec{
	Path:    inlinePath,
	Mode:    modePointer,
	Content: sshdConfigurationContent2,
}
var inlineConfigValid2 = v1alpha1.InlineConfigProviderSpec{
	Inline: []v1alpha1.FileSpec{inlineConfigSpec2},
	Name:   inlineConfigName,
}

var inlineConfigSpec3 = v1alpha1.FileSpec{
	Path:    hookPath,
	Mode:    modePointer,
	Content: sshdHook,
}

var inlineConfigValid3 = v1alpha1.InlineConfigProviderSpec{
	Inline: []v1alpha1.FileSpec{inlineConfigSpec3},
	Name:   inlineConfigName3,
}

var (
	afterUpdatingContent = `
- run: /usr/bin/logger "this is a test message from afterupdating hook"
`
	afterUpdatingPath     = "/etc/flightctl/hooks.d/afterupdating/display-hook.yaml"
	afterRebootingContent = `
- run: /usr/bin/logger "this is a test message from afterrebooting hook"
`
	afterRebootingPath     = "/etc/flightctl/hooks.d/afterrebooting/display-hook.yaml"
	beforeRebootingContent = `
- run: /usr/bin/logger "this is a test message from beforerebooting hook"
`
	beforeRebootingPath   = "/etc/flightctl/hooks.d/beforerebooting/display-hook.yaml"
	beforeUpdatingContent = `
- run: /usr/bin/logger "this is a test message from beforeupdating hook"
`
	beforeUpdatingPath = "/etc/flightctl/hooks.d/beforeupdating/display-hook.yaml"
)

// inlineConfigSpec4 defines a file specification with path, mode, and content for the after-updating lifecycle hook.
var inlineConfigSpec4 = v1alpha1.FileSpec{
	Path:    afterUpdatingPath,
	Mode:    modePointer,
	Content: afterUpdatingContent,
}
var inlineConfigSpec5 = v1alpha1.FileSpec{
	Path:    afterRebootingPath,
	Mode:    modePointer,
	Content: afterRebootingContent,
}
var inlineConfigSpec6 = v1alpha1.FileSpec{
	Path:    beforeRebootingPath,
	Mode:    modePointer,
	Content: beforeRebootingContent,
}
var inlineConfigSpec7 = v1alpha1.FileSpec{
	Path:    beforeUpdatingPath,
	Mode:    modePointer,
	Content: beforeUpdatingContent,
}

var inlineTemplateHookConfigSpec = v1alpha1.FileSpec{
	Path:    hookPath,
	Mode:    modePointer,
	Content: templateHook,
}

var inlineConfigValidLifecycle = v1alpha1.InlineConfigProviderSpec{
	Inline: []v1alpha1.FileSpec{inlineConfigSpec4, inlineConfigSpec5, inlineConfigSpec6, inlineConfigSpec7},
	Name:   inlineConfigLifecycleName,
}

var inlineConfigValidTemplateLifeCycle = v1alpha1.InlineConfigProviderSpec{
	Inline: []v1alpha1.FileSpec{inlineTemplateHookConfigSpec},
	Name:   inlineConfigLifecycleName,
}

const (
	templateHookDirectory = "/var/home/user"
)

type templateSpecProviderArgs struct {
	name    string
	path    string
	content string
}

func newHookSpec(args ...templateSpecProviderArgs) ([]v1alpha1.ConfigProviderSpec, error) {
	var hookInlineSpec v1alpha1.ConfigProviderSpec
	err := hookInlineSpec.FromInlineConfigProviderSpec(inlineConfigValidTemplateLifeCycle)
	if err != nil {
		return nil, err
	}

	spec := []v1alpha1.ConfigProviderSpec{
		hookInlineSpec,
	}

	for _, arg := range args {
		provider, err := newTemplateSpecProvider(arg.name, arg.path, arg.content)
		if err != nil {
			return nil, err
		}
		spec = append(spec, provider)
	}

	return spec, nil
}

func newTemplateSpecProvider(name string, path string, contents string) (v1alpha1.ConfigProviderSpec, error) {
	var inlineResourceFileSpec = v1alpha1.FileSpec{
		Path:    fmt.Sprintf("%s/%s", templateHookDirectory, path),
		Mode:    modePointer,
		Content: contents,
	}
	inlineSpec := v1alpha1.InlineConfigProviderSpec{
		Inline: []v1alpha1.FileSpec{inlineResourceFileSpec},
		Name:   name,
	}
	var provider v1alpha1.ConfigProviderSpec
	err := provider.FromInlineConfigProviderSpec(inlineSpec)
	if err != nil {
		return provider, err
	}
	return provider, nil
}
