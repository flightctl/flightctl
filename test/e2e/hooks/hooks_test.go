package hooks

import (
	"fmt"
	"strings"

	"github.com/flightctl/flightctl/api/v1beta1"
	"github.com/flightctl/flightctl/test/harness/e2e"
	"github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Device lifecycles and embedded hooks tests", func() {
	var (
		deviceId string
	)

	BeforeEach(func() {
		// Get harness directly - no shared package-level variable
		harness := e2e.GetWorkerHarness()
		deviceId, _ = harness.EnrollAndWaitForOnlineStatus()
	})

	Context("hooks", func() {
		It(`Verifies that lifecycles hooks are triggered after the device and agent events`, Label("78753", "sanity", "slow"), func() {
			// Get harness directly - no shared package-level variable
			harness := e2e.GetWorkerHarness()

			By("Update the device image to one containing an embedded hook")
			_, err := harness.CheckDeviceStatus(deviceId, v1beta1.DeviceSummaryStatusOnline)
			Expect(err).ToNot(HaveOccurred())

			nextRenderedVersion, err := harness.PrepareNextDeviceVersion(deviceId)
			Expect(err).ToNot(HaveOccurred())

			deviceImage := util.NewDeviceImageReference(util.DeviceTags.V6).String()

			var osImageSpec = v1beta1.DeviceOsSpec{
				Image: deviceImage,
			}

			err = harness.UpdateDeviceWithRetries(deviceId, func(device *v1beta1.Device) {
				device.Spec.Os = &osImageSpec

				GinkgoWriter.Printf("Updating %s with Os image\n", osImageSpec)
			})
			Expect(err).ToNot(HaveOccurred())

			err = harness.WaitForDeviceNewRenderedVersion(deviceId, nextRenderedVersion)
			Expect(err).ToNot(HaveOccurred())

			By("Add an inline configuration for sshd")
			nextRenderedVersion, err = harness.PrepareNextDeviceVersion(deviceId)
			Expect(err).ToNot(HaveOccurred())

			inlineConfigProviderSpec := v1beta1.ConfigProviderSpec{}
			err = inlineConfigProviderSpec.FromInlineConfigProviderSpec(inlineConfigValid)
			Expect(err).ToNot(HaveOccurred())

			deviceSpecConfig := []v1beta1.ConfigProviderSpec{inlineConfigProviderSpec}

			err = harness.UpdateDeviceConfigWithRetries(deviceId, deviceSpecConfig, nextRenderedVersion)
			Expect(err).ToNot(HaveOccurred())

			stdout, err := harness.VM.RunSSH([]string{"sudo", "cat", inlinePath}, nil)
			Expect(err).ToNot(HaveOccurred())
			Expect(stdout.String()).To(ContainSubstring(sshdConfigurationContent))
			GinkgoWriter.Printf("the configuration %s was found in the device\n", inlineConfigName)

			By("Check that the embedded sshd hook is triggered and sshd config reloaded trying to login with user and password")
			_, err = harness.VM.RunSSHWithUser([]string{"pwd"}, nil, rootUser)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring(tooManyAuthFailuresError))

			By("Update the sshd config")
			nextRenderedVersion, err = harness.PrepareNextDeviceVersion(deviceId)
			Expect(err).ToNot(HaveOccurred())

			err = inlineConfigProviderSpec.FromInlineConfigProviderSpec(inlineConfigValid2)
			Expect(err).ToNot(HaveOccurred())

			deviceSpecConfig = []v1beta1.ConfigProviderSpec{inlineConfigProviderSpec}

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

			deviceSpecConfig = []v1beta1.ConfigProviderSpec{inlineConfigProviderSpec}

			err = harness.UpdateDeviceConfigWithRetries(deviceId, deviceSpecConfig, nextRenderedVersion)
			Expect(err).ToNot(HaveOccurred())

			By("Update the sshd config")
			nextRenderedVersion, err = harness.PrepareNextDeviceVersion(deviceId)
			Expect(err).ToNot(HaveOccurred())

			inlineConfigProviderSpec1 := v1beta1.ConfigProviderSpec{}

			err = inlineConfigProviderSpec1.FromInlineConfigProviderSpec(inlineConfigValid)
			Expect(err).ToNot(HaveOccurred())

			configProviderSpec := []v1beta1.ConfigProviderSpec{inlineConfigProviderSpec, inlineConfigProviderSpec1}

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

			deviceSpecConfig = []v1beta1.ConfigProviderSpec{}

			err = harness.UpdateDeviceConfigWithRetries(deviceId, deviceSpecConfig, nextRenderedVersion)
			Expect(err).ToNot(HaveOccurred())

			_, err = harness.VM.RunSSH([]string{"pwd"}, nil)
			Expect(err).ToNot(HaveOccurred())

			By("Check pre/after update and pre/after reboot hooks from inline config works")
			nextRenderedVersion, err = harness.PrepareNextDeviceVersion(deviceId)
			Expect(err).ToNot(HaveOccurred())
			deviceImage = util.NewDeviceImageReference(util.DeviceTags.Base).String()

			osImageSpec.Image = deviceImage
			err = inlineConfigProviderSpec.FromInlineConfigProviderSpec(inlineConfigValidLifecycle)
			Expect(err).ToNot(HaveOccurred())

			deviceSpecConfig = []v1beta1.ConfigProviderSpec{inlineConfigProviderSpec}

			deviceSpec.Os = &osImageSpec
			deviceSpec.Config = &deviceSpecConfig

			err = harness.UpdateDeviceWithRetries(deviceId, func(device *v1beta1.Device) {
				device.Spec = &deviceSpec
				GinkgoWriter.Printf("Updating %s with a new image and configuration %s\n", deviceId, inlineConfigLifecycleName)
			})
			Expect(err).ToNot(HaveOccurred())

			err = harness.WaitForDeviceNewRenderedVersion(deviceId, nextRenderedVersion)
			Expect(err).ToNot(HaveOccurred())

			By("Check that in the device logs the hooks were triggered")
			Eventually(harness.ReadPrimaryVMAgentLogs, "30s", POLLING).
				WithArguments("", "").
				Should(
					SatisfyAll(
						ContainSubstring("this is a test message from afterupdating hook"),
						ContainSubstring("this is a test message from afterrebooting hook"),
						ContainSubstring("this is a test message from beforerebooting hook"),
						ContainSubstring("this is a test message from beforeupdating hook"),
					),
				)
		})
		It("Verifies that lifecycle hooks can be defined with template variables", Label("80022"), func() {
			// Get harness directly - no shared package-level variable
			harness := e2e.GetWorkerHarness()

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
			err = harness.UpdateDeviceWithRetries(deviceId, func(device *v1beta1.Device) {
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
			err = harness.UpdateDeviceWithRetries(deviceId, func(device *v1beta1.Device) {
				device.Spec.Config = &configSpec
			})
			Expect(err).ToNot(HaveOccurred())
			err = harness.WaitForDeviceNewRenderedVersion(deviceId, nextRenderedVersion)
			Expect(err).ToNot(HaveOccurred())
			// ensure we see our expected messages
			DeferCleanup(func() {
				if CurrentSpecReport().Failed() {
					// Debug info for first file check failure
					logs, err := harness.ReadPrimaryVMAgentLogs("", "")
					if err == nil {
						lines := strings.Split(logs, "\n")
						GinkgoWriter.Printf("=== FIRST FILE CHECK DEBUG (total %d lines) ===\n", len(lines))

						// Print logs in chunks to avoid size limits
						chunkSize := 50
						for i := 0; i < len(lines); i += chunkSize {
							end := i + chunkSize
							if end > len(lines) {
								end = len(lines)
							}
							GinkgoWriter.Printf("--- Lines %d-%d ---\n", i+1, end)
							GinkgoWriter.Printf("%s\n", strings.Join(lines[i:end], "\n"))
						}

						// Also show hook-specific filtered logs
						relevantLines := []string{}
						for _, line := range lines {
							if strings.Contains(line, "hook") || strings.Contains(line, "logger") ||
								strings.Contains(line, templateHookDirectory) ||
								strings.Contains(line, firstFileContents) {
								relevantLines = append(relevantLines, line)
							}
						}
						if len(relevantLines) > 0 {
							GinkgoWriter.Printf("=== HOOK-RELATED ENTRIES ===\n%s\n", strings.Join(relevantLines, "\n"))
						}
					}
				}
			})

			Eventually(harness.ReadPrimaryVMAgentLogs, "30s", POLLING).
				WithArguments("", "").
				Should(
					SatisfyAll(
						ContainSubstring(templateHookDirectory),
						ContainSubstring(firstFileContents),
					),
				)

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
			err = harness.UpdateDeviceWithRetries(deviceId, func(device *v1beta1.Device) {
				device.Spec.Config = &configSpec
			})
			Expect(err).ToNot(HaveOccurred())
			err = harness.WaitForDeviceNewRenderedVersion(deviceId, nextRenderedVersion)
			Expect(err).ToNot(HaveOccurred())

			// ensure we see our expected messages
			DeferCleanup(func() {
				if CurrentSpecReport().Failed() {
					// Print full logs chunked to stdout to avoid Gomega size limits
					logs, err := harness.ReadPrimaryVMAgentLogs("", "")
					if err == nil {
						lines := strings.Split(logs, "\n")
						GinkgoWriter.Printf("=== SECOND FILE CHECK DEBUG (total %d lines) ===\n", len(lines))

						// Print all logs in manageable chunks
						chunkSize := 50
						for i := 0; i < len(lines); i += chunkSize {
							end := i + chunkSize
							if end > len(lines) {
								end = len(lines)
							}
							GinkgoWriter.Printf("--- Lines %d-%d ---\n", i+1, end)
							GinkgoWriter.Printf("%s\n", strings.Join(lines[i:end], "\n"))
						}

						// Also show hook-specific filtered logs
						hookLines := []string{}
						for _, line := range lines {
							if strings.Contains(line, "hook") || strings.Contains(line, "logger") ||
								strings.Contains(line, templateHookDirectory) ||
								strings.Contains(line, firstFileUpdatedContents) ||
								strings.Contains(line, secondFileContents) {
								hookLines = append(hookLines, line)
							}
						}
						if len(hookLines) > 0 {
							GinkgoWriter.Printf("=== HOOK-RELATED ENTRIES ===\n%s\n", strings.Join(hookLines, "\n"))
						}
					}

					// Also check what files were actually created
					files, err := harness.VM.RunSSH([]string{"find", "/var/home/user/", "-type", "f", "-exec", "ls", "-la", "{}", ";"}, nil)
					if err == nil {
						GinkgoWriter.Printf("=== FILES IN WATCH DIRECTORY ===\n%s\n", files.String())
					}

					// Check hook configuration files
					hooks, err := harness.VM.RunSSH([]string{"find", "/etc/flightctl/hooks.d/", "-name", "*.yaml", "-exec", "cat", "{}", ";"}, nil)
					if err == nil {
						GinkgoWriter.Printf("=== HOOK CONFIGURATIONS ===\n%s\n", hooks.String())
					}
				}
			})

			Eventually(harness.ReadPrimaryVMAgentLogs, "30s", POLLING).
				WithArguments("", "").
				Should(
					SatisfyAll(
						ContainSubstring(firstFileUpdatedContents),
						ContainSubstring(secondFileContents),
					),
				)
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
	deviceSpec                v1beta1.DeviceSpec
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
var inlineConfigSpec = v1beta1.FileSpec{
	Path:    inlinePath,
	Mode:    modePointer,
	Content: sshdConfigurationContent,
}

// inlineConfigValid is an instance of InlineConfigProviderSpec configured with inline file specifications and a provider name.
var inlineConfigValid = v1beta1.InlineConfigProviderSpec{
	Inline: []v1beta1.FileSpec{inlineConfigSpec},
	Name:   inlineConfigName,
}

// inlineConfigSpec2 defines a file specification for creating a custom SSH server configuration file at a specified path.
var inlineConfigSpec2 = v1beta1.FileSpec{
	Path:    inlinePath,
	Mode:    modePointer,
	Content: sshdConfigurationContent2,
}
var inlineConfigValid2 = v1beta1.InlineConfigProviderSpec{
	Inline: []v1beta1.FileSpec{inlineConfigSpec2},
	Name:   inlineConfigName,
}

var inlineConfigSpec3 = v1beta1.FileSpec{
	Path:    hookPath,
	Mode:    modePointer,
	Content: sshdHook,
}

var inlineConfigValid3 = v1beta1.InlineConfigProviderSpec{
	Inline: []v1beta1.FileSpec{inlineConfigSpec3},
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
var inlineConfigSpec4 = v1beta1.FileSpec{
	Path:    afterUpdatingPath,
	Mode:    modePointer,
	Content: afterUpdatingContent,
}
var inlineConfigSpec5 = v1beta1.FileSpec{
	Path:    afterRebootingPath,
	Mode:    modePointer,
	Content: afterRebootingContent,
}
var inlineConfigSpec6 = v1beta1.FileSpec{
	Path:    beforeRebootingPath,
	Mode:    modePointer,
	Content: beforeRebootingContent,
}
var inlineConfigSpec7 = v1beta1.FileSpec{
	Path:    beforeUpdatingPath,
	Mode:    modePointer,
	Content: beforeUpdatingContent,
}

var inlineTemplateHookConfigSpec = v1beta1.FileSpec{
	Path:    hookPath,
	Mode:    modePointer,
	Content: templateHook,
}

var inlineConfigValidLifecycle = v1beta1.InlineConfigProviderSpec{
	Inline: []v1beta1.FileSpec{inlineConfigSpec4, inlineConfigSpec5, inlineConfigSpec6, inlineConfigSpec7},
	Name:   inlineConfigLifecycleName,
}

var inlineConfigValidTemplateLifeCycle = v1beta1.InlineConfigProviderSpec{
	Inline: []v1beta1.FileSpec{inlineTemplateHookConfigSpec},
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

func newHookSpec(args ...templateSpecProviderArgs) ([]v1beta1.ConfigProviderSpec, error) {
	var hookInlineSpec v1beta1.ConfigProviderSpec
	err := hookInlineSpec.FromInlineConfigProviderSpec(inlineConfigValidTemplateLifeCycle)
	if err != nil {
		return nil, err
	}

	spec := []v1beta1.ConfigProviderSpec{
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

func newTemplateSpecProvider(name string, path string, contents string) (v1beta1.ConfigProviderSpec, error) {
	var inlineResourceFileSpec = v1beta1.FileSpec{
		Path:    fmt.Sprintf("%s/%s", templateHookDirectory, path),
		Mode:    modePointer,
		Content: contents,
	}
	inlineSpec := v1beta1.InlineConfigProviderSpec{
		Inline: []v1beta1.FileSpec{inlineResourceFileSpec},
		Name:   name,
	}
	var provider v1beta1.ConfigProviderSpec
	err := provider.FromInlineConfigProviderSpec(inlineSpec)
	if err != nil {
		return provider, err
	}
	return provider, nil
}
