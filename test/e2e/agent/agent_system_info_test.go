package agent_test

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"strings"

	"github.com/flightctl/flightctl/test/harness/e2e"
	"github.com/flightctl/flightctl/test/login"
	testutil "github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Agent System Info", func() {
	var (
		ctx      context.Context
		deviceId string
	)

	BeforeEach(func() {
		// Get harness and context directly - no shared package-level variables
		harness := e2e.GetWorkerHarness()
		suiteCtx := e2e.GetWorkerContext()

		ctx = testutil.StartSpecTracerForGinkgo(suiteCtx)
		harness.SetTestContext(ctx)
		login.LoginToAPIWithToken(harness)
		deviceId = harness.StartVMAndEnroll()
	})

	AfterEach(func() {
		// No need to cleanup here as it's handled by the suite
	})

	It("should show default system infos in the systemInfo status after enrollment and update it", Label("81787", "sanity", "agent"), func() {
		// Get harness directly - no shared package-level variable
		harness := e2e.GetWorkerHarness()

		By("Waiting for system info to be reported and verifying all fields")
		Eventually(func() bool {
			sysInfo := harness.GetDeviceSystemInfo(deviceId)
			if sysInfo == nil {
				return false
			}

			// Check basic system info fields
			basicFieldsValid := sysInfo.OperatingSystem == runtime.GOOS &&
				sysInfo.Architecture == runtime.GOARCH &&
				sysInfo.AgentVersion != "" &&
				sysInfo.BootID != ""

			if !basicFieldsValid {
				return false
			}

			// Check all required additional properties are present
			if sysInfo.AdditionalProperties == nil {
				return false
			}

			for _, key := range testutil.DefaultSystemInfo {
				if _, exists := sysInfo.AdditionalProperties[key]; !exists {
					return false
				}
			}

			return true
		}, e2e.TIMEOUT, e2e.POLLING).Should(BeTrue(), "All system info fields should be correctly populated")

		By("Removing system-info block from agent config")
		err := removingAgentConfig(harness)
		Expect(err).NotTo(HaveOccurred())

		By("Updating agent with subset of system info")
		err = updateAgentConfigFile(harness, systemInfoSubsetConfig)
		Expect(err).NotTo(HaveOccurred())

		By("Waiting for device to report only selected system info")
		Eventually(func() map[string]string {
			sysInfo := harness.GetDeviceSystemInfo(deviceId)
			if sysInfo == nil {
				return nil
			}
			return sysInfo.AdditionalProperties
		}, e2e.TIMEOUT, e2e.POLLING).Should(And(
			HaveKey("hostname"),
			HaveKey("kernel"),
			HaveKey("productSerial"),
		), "Only selected system info keys should be present")

		By("Verifying non-selected system info is not reported")
		Eventually(func() map[string]string {
			sysInfo := harness.GetDeviceSystemInfo(deviceId)
			if sysInfo == nil {
				return nil
			}
			return sysInfo.AdditionalProperties
		}, e2e.TIMEOUT, e2e.POLLING).Should(And(
			Not(HaveKey("distroName")),
			Not(HaveKey("distroVersion")),
			Not(HaveKey("netInterfaceDefault")),
		), "Non-selected system info should not be present")

		By("Removing system-info block from agent config")
		err = removingAgentConfig(harness)
		Expect(err).NotTo(HaveOccurred())

		By("Updating agent config to disable all system info details")
		err = updateAgentConfigFile(harness, systemInfoDisabledConfig)
		Expect(err).NotTo(HaveOccurred())

		By("Waiting for device to report no system info details")
		Eventually(func() map[string]string {
			sysInfo := harness.GetDeviceSystemInfo(deviceId)
			if sysInfo == nil {
				return nil
			}
			return sysInfo.AdditionalProperties
		}, e2e.TIMEOUT, e2e.POLLING).Should(BeEmpty(), "No system info details should be present")

		By("Verifying basic system info fields are still populated")
		Eventually(func() bool {
			sysInfo := harness.GetDeviceSystemInfo(deviceId)
			if sysInfo == nil {
				return false
			}
			return sysInfo.OperatingSystem != "" &&
				sysInfo.Architecture != "" &&
				sysInfo.AgentVersion != "" &&
				sysInfo.BootID != ""
		}, e2e.TIMEOUT, e2e.POLLING).Should(BeTrue(), "Basic system info fields should still be populated")

		By("Verifying pre-built custom system info scripts exist")
		output, err := harness.VM.RunSSH([]string{"sudo", "ls", "-la", customInfoScriptsPath}, nil)
		Expect(err).NotTo(HaveOccurred())

		Expect(output.String()).To(ContainSubstring("siteName"))
		Expect(output.String()).To(ContainSubstring("emptyValue"))
		Expect(output.String()).To(ContainSubstring("keyNotShown"))

		By("Updating agent config with custom system info")
		err = updateAgentConfigFile(harness, customSystemInfoConfig)
		Expect(err).NotTo(HaveOccurred())

		By("Waiting for device to report custom info")
		Eventually(func() map[string]string {
			sysInfo := harness.GetDeviceSystemInfo(deviceId)
			if sysInfo == nil || sysInfo.CustomInfo == nil {
				return nil
			}
			return *sysInfo.CustomInfo
		}, e2e.TIMEOUT, e2e.POLLING).Should(And(
			HaveKeyWithValue("siteName", "my site"),
			HaveKeyWithValue("emptyValue", ""), // Empty command should result in empty value
		), "Custom system info should be present with correct values")

		By("Verifying unlisted custom scripts are not reported")
		Consistently(func() map[string]string {
			sysInfo := harness.GetDeviceSystemInfo(deviceId)
			if sysInfo == nil || sysInfo.CustomInfo == nil {
				return nil
			}
			return *sysInfo.CustomInfo
		}, "5s", e2e.POLLING).ShouldNot(HaveKey("keyNotShown"), "Unlisted custom keys should not be reported")

	})
})

// removingAgentConfig removes the system-info block from the agent config
func removingAgentConfig(harness *e2e.Harness) error {
	// Get the script path and read its content
	scriptPath := testutil.GetScriptPath("remove_system_info.sh")
	scriptContent, err := os.ReadFile(scriptPath)
	if err != nil {
		return fmt.Errorf("failed to read remove_system_info.sh script: %w", err)
	}

	// Execute the script content on the VM
	_, err = harness.VM.RunSSH([]string{bashCommand, string(scriptContent)}, nil)
	if err != nil {
		return fmt.Errorf("failed to remove system-info from agent config: %w", err)
	}
	return nil
}

// updateAgentConfigFileupdates the agent config
func updateAgentConfigFile(harness *e2e.Harness, config string) error {

	By("Adding new system-info to agent config")
	_, err := harness.VM.RunSSH([]string{bashCommand, "cat << 'CONFIGEOF' | sudo tee -a " + agentConfigPath + " > /dev/null\n" + config + "\nCONFIGEOF"}, nil)
	if err != nil {
		return fmt.Errorf("failed to add new system-info to agent config: %w", err)
	}

	By("Sending SIGHUP to reload agent config")
	err = harness.ResetAgent()
	Expect(err).NotTo(HaveOccurred())

	By("Waiting for config reload to complete")
	// Wait for the agent service to be running properly after config reload
	Eventually(func() bool {
		output, err := harness.VM.RunSSH([]string{"sudo", "systemctl", "is-active", "flightctl-agent"}, nil)
		if err != nil {
			return false
		}
		return strings.TrimSpace(output.String()) == "active"
	}, "10s", e2e.POLLING).Should(BeTrue(), "Agent service should be active after config reload")

	return nil
}

// Configuration templates for different test scenarios
var (
	systemInfoSubsetConfig = `
system-info:
 - hostname
 - kernel
 - productSerial
`

	systemInfoDisabledConfig = `
system-info: []
`

	customSystemInfoConfig = `
system-info-custom:
 - siteName
 - emptyValue
`
)

const (
	customInfoScriptsPath = "/usr/lib/flightctl/custom-info.d/"
	agentConfigPath       = "/etc/flightctl/config.yaml"
	bashCommand           = "bash -c"
)
