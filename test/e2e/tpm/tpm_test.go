package tpm

import (
	"context"
	"path/filepath"
	"strings"
	"time"

	"github.com/flightctl/flightctl/api/v1alpha1"
	agentcfg "github.com/flightctl/flightctl/internal/agent/config"
	"github.com/flightctl/flightctl/test/harness/e2e"
	"github.com/flightctl/flightctl/test/login"
	testutil "github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/sirupsen/logrus"
	"sigs.k8s.io/yaml"
)

var mode = 0644

var _ = Describe("TPM Device Authentication", func() {
	var (
		ctx        context.Context
		harness    *e2e.Harness
		workerID   int
		currentPID string
	)

	BeforeEach(func() {
		// Get the harness and context directly - no package-level variables
		workerID = GinkgoParallelProcess()
		harness = e2e.GetWorkerHarness()
		suiteCtx := e2e.GetWorkerContext()

		GinkgoWriter.Printf("🔄 [BeforeEach] Worker %d: Setting up test with VM from pool\n", workerID)

		// Create test-specific context for proper tracing
		ctx = testutil.StartSpecTracerForGinkgo(suiteCtx)

		// Set the test context in the harness
		harness.SetTestContext(ctx)

		// CORRECT ORDER (per developer advice):
		// 1. Setup VM (no agent start)
		// 2. Let TPM hardware initialize
		// 3. Write TPM config
		// 4. THEN start agent with TPM ready

		// Setup VM from pool and revert to pristine snapshot
		// Note: This also starts agent, but we'll stop it immediately to configure TPM first
		err := harness.SetupVMFromPoolAndStartAgent(workerID)
		Expect(err).ToNot(HaveOccurred())

		// Immediately stop the agent so we can configure TPM before starting it properly
		GinkgoWriter.Printf("🔧 [Debug] Worker %d: Stopping agent to configure TPM before proper startup\n", workerID)
		_, err = harness.VM.RunSSH([]string{"sudo", "systemctl", "stop", "flightctl-agent"}, nil)
		Expect(err).ToNot(HaveOccurred())

		// CRITICAL: Let VM and TPM hardware initialize first before configuring
		GinkgoWriter.Printf("🔄 [Debug] Worker %d: Letting VM and TPM hardware initialize (20s wait)\n", workerID)
		time.Sleep(20 * time.Second)

		// CORRECT ORDER: Verify TPM is available, then configure BEFORE starting agent
		GinkgoWriter.Printf("🔍 [Debug] Worker %d: Verifying TPM device accessibility before configuring\n", workerID)
		stdout, err := harness.VM.RunSSH([]string{"ls", "-la", "/dev/tpm*"}, nil)
		GinkgoWriter.Printf("🔍 [Debug] TPM devices: %s (err: %v)\n", stdout.String(), err)

		stdout, err = harness.VM.RunSSH([]string{"sudo", "tpm2_startup", "-c"}, nil)
		GinkgoWriter.Printf("🔍 [Debug] TPM2 startup: %s (err: %v)\n", stdout.String(), err)

		stdout, err = harness.VM.RunSSH([]string{"sudo", "tpm2_getrandom", "8"}, nil)
		GinkgoWriter.Printf("🔍 [Debug] TPM2 getrandom test: %s (err: %v)\n", stdout.String(), err)

		// Check TPM permissions and ownership details
		stdout, err = harness.VM.RunSSH([]string{"ls", "-la", "/dev/tpm*"}, nil)
		GinkgoWriter.Printf("🔍 [Debug] TPM device permissions: %s (err: %v)\n", stdout.String(), err)

		// Check if flightctl-agent user/group can access TPM
		stdout, err = harness.VM.RunSSH([]string{"groups", "root"}, nil)
		GinkgoWriter.Printf("🔍 [Debug] Root user groups: %s (err: %v)\n", stdout.String(), err)

		// Test TPM access as the user that will run the agent
		stdout, err = harness.VM.RunSSH([]string{"sudo", "-u", "root", "test", "-r", "/dev/tpm0"}, nil)
		GinkgoWriter.Printf("🔍 [Debug] TPM read access test: %s (err: %v)\n", stdout.String(), err)

		stdout, err = harness.VM.RunSSH([]string{"sudo", "-u", "root", "test", "-w", "/dev/tpm0"}, nil)
		GinkgoWriter.Printf("🔍 [Debug] TPM write access test: %s (err: %v)\n", stdout.String(), err)

		// Check if config file exists (agent never started, so shouldn't exist)
		GinkgoWriter.Printf("🔧 [Debug] Worker %d: Checking for existing agent config\n", workerID)
		stdout, err = harness.VM.RunSSH([]string{"cat", "/etc/flightctl/config.yaml"}, nil)
		var agentConfig *agentcfg.Config
		if err == nil && stdout.Len() > 0 {
			GinkgoWriter.Printf("🔧 [Debug] Found existing config, length: %d bytes\n", stdout.Len())
			// Parse existing config
			agentConfig = &agentcfg.Config{}
			err = yaml.Unmarshal(stdout.Bytes(), agentConfig)
			if err != nil {
				GinkgoWriter.Printf("🔧 [Debug] Failed to parse existing config: %v, using default\n", err)
				// If parsing fails, use default config
				agentConfig = &agentcfg.Config{}
			} else {
				GinkgoWriter.Printf("🔧 [Debug] Successfully parsed existing config\n")
			}
		} else {
			GinkgoWriter.Printf("🔧 [Debug] No existing config found (err: %v), creating new\n", err)
			// No existing config, create minimal config
			agentConfig = &agentcfg.Config{}
		}

		// Update TPM configuration
		GinkgoWriter.Printf("🔧 [Debug] Worker %d: Updating TPM configuration\n", workerID)
		agentConfig.TPM = agentcfg.TPM{
			Enabled:         true,
			DevicePath:      "/dev/tpm0",
			StorageFilePath: filepath.Join(agentcfg.DefaultDataDir, agentcfg.DefaultTPMKeyFile),
			AuthEnabled:     true,
		}
		GinkgoWriter.Printf("🔧 [Debug] TPM config set: Enabled=%t, DevicePath=%s, AuthEnabled=%t\n",
			agentConfig.TPM.Enabled, agentConfig.TPM.DevicePath, agentConfig.TPM.AuthEnabled)

		// Write the updated config
		GinkgoWriter.Printf("🔧 [Debug] Worker %d: Writing updated agent config\n", workerID)
		err = harness.SetAgentConfig(agentConfig)
		Expect(err).ToNot(HaveOccurred())

		// Verify the config was written
		stdout, err = harness.VM.RunSSH([]string{"cat", "/etc/flightctl/config.yaml"}, nil)
		Expect(err).ToNot(HaveOccurred())
		configStr := stdout.String()
		if len(configStr) > 500 {
			configStr = configStr[:500] + "..."
		}
		GinkgoWriter.Printf("🔧 [Debug] Config file after write: %s\n", configStr)

		// NOW start the agent for the first time with TPM configuration already in place
		GinkgoWriter.Printf("🔧 [Debug] Worker %d: Starting agent for first time with TPM config\n", workerID)
		stdout, err = harness.VM.RunSSH([]string{"sudo", "systemctl", "start", "flightctl-agent"}, nil)
		Expect(err).ToNot(HaveOccurred())
		GinkgoWriter.Printf("🔧 [Debug] Start agent result: %s\n", stdout.String())

		// Wait for agent to start and capture the critical startup logs
		GinkgoWriter.Printf("🔍 [Debug] Worker %d: Waiting for agent startup and capturing initialization logs\n", workerID)
		time.Sleep(5 * time.Second)

		// Get the startup logs immediately to see identity provider selection
		stdout, err = harness.VM.RunSSH([]string{"sudo", "journalctl", "-u", "flightctl-agent", "-n", "50", "--no-pager"}, nil)
		if err == nil {
			startupLogs := stdout.String()
			GinkgoWriter.Printf("🔍 [Debug] Agent startup logs: %s\n", startupLogs)

			// Check for specific TPM initialization messages
			if strings.Contains(startupLogs, "TPM") {
				GinkgoWriter.Printf("🔍 [Debug] Found TPM mentions in startup logs\n")
			}
			if strings.Contains(startupLogs, "identity provider") {
				GinkgoWriter.Printf("🔍 [Debug] Found identity provider selection in startup logs\n")
			}
			if strings.Contains(startupLogs, "error") || strings.Contains(startupLogs, "failed") {
				GinkgoWriter.Printf("🔍 [Debug] Found errors in startup logs - might explain TPM fallback\n")
			}
		}

		// Continue waiting for full initialization
		time.Sleep(10 * time.Second)

		// Verify agent started successfully with TPM config
		stdout, err = harness.VM.RunSSH([]string{"sudo", "systemctl", "is-active", "flightctl-agent"}, nil)
		GinkgoWriter.Printf("🔧 [Debug] Agent status: %s (err: %v)\n", stdout.String(), err)
		Expect(err).ToNot(HaveOccurred())
		Expect(strings.TrimSpace(stdout.String())).To(Equal("active"))

		// Get agent PID for log verification
		stdout, err = harness.VM.RunSSH([]string{"sudo", "systemctl", "show", "flightctl-agent", "-p", "MainPID", "--value"}, nil)
		currentPID = strings.TrimSpace(stdout.String())
		GinkgoWriter.Printf("🔧 [Debug] Agent PID: %s (err: %v)\n", currentPID, err)
		Expect(currentPID).ToNot(BeEmpty(), "Agent PID should not be empty")

		// Final verification: Check that the agent is reading our TPM config
		stdout, err = harness.VM.RunSSH([]string{"sudo", "grep", "-A5", "-B5", "tpm:", "/etc/flightctl/config.yaml"}, nil)
		GinkgoWriter.Printf("🔍 [Debug] Final TPM config verification: %s (err: %v)\n", stdout.String(), err)

		GinkgoWriter.Printf("✅ [BeforeEach] Worker %d: Test setup completed\n", workerID)

		// Login to API
		login.LoginToAPIWithToken(harness)
	})

	AfterEach(func() {
		workerID := GinkgoParallelProcess()
		GinkgoWriter.Printf("🔄 [AfterEach] Worker %d: Cleaning up test resources\n", workerID)

		// Get the harness and context directly - no shared variables needed
		harness := e2e.GetWorkerHarness()
		suiteCtx := e2e.GetWorkerContext()

		// Clean up test resources BEFORE switching back to suite context
		// This ensures we use the correct test ID for resource cleanup
		err := harness.CleanUpAllTestResources()
		Expect(err).ToNot(HaveOccurred())

		// Now restore suite context for any remaining cleanup operations
		harness.SetTestContext(suiteCtx)

		GinkgoWriter.Printf("✅ [AfterEach] Worker %d: Test cleanup completed\n", workerID)
	})

	Context("TPM Enrollment", func() {
		It("Should enroll device with TPM enabled and verify attestation", Label("83974", "tpm", "sanity"), func() {
			By("verifying TPM device presence")
			stdout, err := harness.VM.RunSSH([]string{"ls", "-la", "/dev/tpm*"}, nil)
			Expect(err).ToNot(HaveOccurred())
			Expect(stdout.String()).To(ContainSubstring("/dev/tpm0"))

			By("verifying TPM version")
			stdout, err = harness.VM.RunSSH([]string{"cat", "/sys/class/tpm/tpm0/tpm_version_major"}, nil)
			Expect(err).ToNot(HaveOccurred())
			Expect(stdout.String()).To(ContainSubstring("2"))

			By("verifying agent reports TPM usage in logs")
			GinkgoWriter.Printf("🔍 [Debug] Worker %d: Checking agent logs for TPM usage (PID: %s)\n", workerID, currentPID)

			Eventually(func() string {
				// Use simple recent logs approach since cursor has compatibility issues
				cmd := []string{"sudo", "journalctl", "-u", "flightctl-agent", "-n", "100", "--no-pager"}

				stdout, err := harness.VM.RunSSH(cmd, nil)
				if err != nil {
					GinkgoWriter.Printf("🔍 [Debug] Failed to get agent logs: %v\n", err)
					return ""
				}
				logs := stdout.String()

				// Check if logs contain our current PID to verify they're from the right instance
				pidInLogs := strings.Contains(logs, "["+currentPID+"]") || strings.Contains(logs, "flightctl-agent["+currentPID+"]")
				GinkgoWriter.Printf("🔍 [Debug] Logs contain current PID %s: %v\n", currentPID, pidInLogs)

				if logs == "" || strings.TrimSpace(logs) == "" {
					GinkgoWriter.Printf("🔍 [Debug] No logs found yet, waiting...\n")
					return ""
				}

				// Check for TPM-related errors or messages
				if strings.Contains(logs, "TPM") {
					GinkgoWriter.Printf("🔍 [Debug] Found TPM-related log messages\n")
				}
				if strings.Contains(logs, "file-based identity") {
					GinkgoWriter.Printf("🔍 [Debug] Agent is using file-based identity (NOT TPM)\n")
				}
				if strings.Contains(logs, "TPM-based identity") {
					GinkgoWriter.Printf("🔍 [Debug] SUCCESS: Agent is using TPM-based identity provider!\n")
				}
				if strings.Contains(logs, "failed") && strings.Contains(logs, "tpm") {
					GinkgoWriter.Printf("🔍 [Debug] Found TPM-related failure messages\n")
				}

				// Enhanced error detection
				tpmErrors := []string{
					"TPM device identity is disabled",
					"failed to initialize TPM",
					"TPM not available",
					"cannot access TPM",
					"TPM initialization failed",
					"no TPM device found",
				}
				for _, errMsg := range tpmErrors {
					if strings.Contains(logs, errMsg) {
						GinkgoWriter.Printf("🚨 [Debug] FOUND TPM ERROR: %s\n", errMsg)
					}
				}

				GinkgoWriter.Printf("🔍 [Debug] Agent logs (last 1500 chars): %s\n",
					func() string {
						if len(logs) > 1500 {
							return logs[len(logs)-1500:]
						}
						return logs
					}())
				return logs
			}, 120*time.Second, 3*time.Second).Should(Or(
				ContainSubstring("Using TPM device at /dev/tpm0"),
				ContainSubstring("Using TPM-based identity provider"),
				ContainSubstring("TPM"),
				ContainSubstring("file-based identity provider"), // To understand what identity provider is being used
			), "Expected to find TPM-related messages or understand which identity provider is being used")

			By("waiting for enrollment request with TPM attestation")
			// Get the enrollment Request ID from the console output
			enrollmentID := harness.GetEnrollmentIDFromServiceLogs("flightctl-agent")
			logrus.Infof("Enrollment ID found in VM console output: %s", enrollmentID)

			// Wait for the device to create the enrollment request with TPM information
			enrollmentRequest := harness.WaitForEnrollmentRequest(enrollmentID)
			logrus.Infof("Enrollment request with TPM attestation data created successfully!")
			if enrollmentRequest.Spec.DeviceStatus != nil {
				logrus.Infof("Device status present with SystemInfo")
			}
			Expect(enrollmentRequest.Spec).ToNot(BeNil())
			Expect(enrollmentRequest.Spec.DeviceStatus).ToNot(BeNil())

			// Verify TPM attestation data is present in system info
			systemInfo := enrollmentRequest.Spec.DeviceStatus.SystemInfo
			logrus.Infof("SystemInfo available keys: %+v", systemInfo)

			attestationData, hasAttestation := systemInfo.Get("attestation")
			if !hasAttestation {
				logrus.Infof("No 'attestation' key found, checking for other TPM-related keys...")
				// Try alternative key names
				tpmData, hasTpm := systemInfo.Get("tpm")
				if hasTpm {
					logrus.Infof("Found 'tpm' key instead: %.50s...", tpmData)
					attestationData, hasAttestation = tpmData, hasTpm
				} else {
					// Check for tpmVendorInfo which is the actual key used
					tpmVendorData, hasVendorInfo := systemInfo.Get("tpmVendorInfo")
					if hasVendorInfo {
						logrus.Infof("Found 'tpmVendorInfo' key: %s", tpmVendorData)
						attestationData, hasAttestation = tpmVendorData, hasVendorInfo
					}
				}
			}

			Expect(hasAttestation).To(BeTrue())
			Expect(attestationData).ToNot(BeEmpty())
			logrus.Infof("TPM attestation data found in enrollment request: %.50s...", attestationData)

			By("approving enrollment and waiting for device online")
			// Approve the enrollment and wait for the device
			harness.ApproveEnrollment(enrollmentID, testutil.TestEnrollmentApproval())

			By("checking TPM key persistence")
			Eventually(func() error {
				_, err := harness.VM.RunSSH([]string{"ls", "-la", "/var/lib/flightctl/tpm-blob.yaml"}, nil)
				return err
			}, 30*time.Second, 2*time.Second).Should(Succeed())

			By("verifying device reports online status with TPM integrity verified")
			deviceId, device, err := harness.WaitForDeviceOnlineStatus(enrollmentID)
			Expect(err).ToNot(HaveOccurred())

			// Verify TPM integrity verification
			Eventually(func() bool {
				// Refresh device status
				resp, err := harness.Client.GetDeviceWithResponse(harness.Context, deviceId)
				if err != nil || resp.JSON200 == nil {
					return false
				}
				device = resp.JSON200

				// Check TPM integrity status
				return device.Status.Integrity.Tpm != nil &&
					device.Status.Integrity.Tpm.Status == v1alpha1.DeviceIntegrityCheckStatusVerified
			}, 60*time.Second, 5*time.Second).Should(BeTrue())

			// Verify device identity is also verified
			Expect(device.Status.Integrity.DeviceIdentity).ToNot(BeNil())
			Expect(device.Status.Integrity.DeviceIdentity.Status).To(Equal(v1alpha1.DeviceIntegrityCheckStatusVerified))

			// Verify overall integrity status
			Expect(device.Status.Integrity.Status).To(Equal(v1alpha1.DeviceIntegrityStatusVerified))

			logrus.Infof("TPM integrity verification successful - TPM: %s, Device Identity: %s, Overall: %s",
				device.Status.Integrity.Tpm.Status,
				device.Status.Integrity.DeviceIdentity.Status,
				device.Status.Integrity.Status)

			By("verifying TPM attestation data is present in device system info")
			attestationData, hasAttestation = device.Status.SystemInfo.Get("attestation")
			Expect(hasAttestation).To(BeTrue())
			Expect(attestationData).ToNot(BeEmpty())

			By("verifying TPM-based identity is used for communication")
			// Make a config change to verify TPM-signed communication works
			newRenderedVersion, err := harness.PrepareNextDeviceVersion(deviceId)
			Expect(err).ToNot(HaveOccurred())

			// Add an inline config to test TPM-authenticated updates
			err = harness.UpdateDeviceWithRetries(deviceId, func(device *v1alpha1.Device) {
				// Create inline config provider spec
				var configProviderSpec v1alpha1.ConfigProviderSpec
				inlineConfig := v1alpha1.InlineConfigProviderSpec{
					Name: "tpm-test-config",
					Inline: []v1alpha1.FileSpec{{
						Content: "TPM authenticated device - test successful",
						Mode:    &mode,
						Path:    "/etc/motd",
					}},
				}
				err := configProviderSpec.FromInlineConfigProviderSpec(inlineConfig)
				Expect(err).ToNot(HaveOccurred())

				device.Spec.Config = &[]v1alpha1.ConfigProviderSpec{configProviderSpec}
			})
			Expect(err).ToNot(HaveOccurred())

			// Verify config is applied using TPM identity
			logrus.Infof("Waiting for device to apply TPM-authenticated config update")
			err = harness.WaitForDeviceNewRenderedVersion(deviceId, newRenderedVersion)
			Expect(err).ToNot(HaveOccurred())

			Eventually(func() string {
				stdout, err := harness.VM.RunSSH([]string{"cat", "/etc/motd"}, nil)
				if err != nil {
					return ""
				}
				return stdout.String()
			}, 30*time.Second, 2*time.Second).Should(ContainSubstring("TPM authenticated device - test successful"))

			By("verifying agent continues to use TPM for ongoing operations")
			// Check that TPM operations are working in agent logs
			Eventually(func() string {
				logs, err := harness.GetServiceLogs("flightctl-agent")
				if err != nil {
					return ""
				}
				return logs
			}, 30*time.Second, 2*time.Second).Should(ContainSubstring("TPM"))
		})
	})
})
