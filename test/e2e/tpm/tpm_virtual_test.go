// Package tpm provides E2E tests for TPM (Trusted Platform Module) device authentication and attestation functionality.
//
// VIRTUAL TPM TEST (CI-COMPATIBLE)
// This test uses virtual TPM (software TPM in VMs) and validates:
// - TPM device presence and functionality
// - Agent TPM configuration
// - TPM-based enrollment with attestation data
// - Certificate upload and verification
// - Device status with TPM integrity checks
//
// IMPORTANT NOTES:
// - Virtual TPM verification shows "Failed" status due to lack of certificate chain of trust
// - This is EXPECTED behavior for virtual/software TPM
// - Test validates TPM enrollment and attestation functionality works correctly
// - Real hardware TPM tests are in tpm_test.go (marked as "hardware" - skipped in CI)
//
// USAGE:
// go test ./test/e2e/tpm -v
package tpm

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/test/harness/e2e"
	"github.com/flightctl/flightctl/test/login"
	"github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
)

var (
	logLookbackDuration = "10 minutes ago"
)

var _ = Describe("Virtual TPM Device Authentication", func() {
	var (
		ctx      context.Context
		harness  *e2e.Harness
		workerID int
	)

	BeforeEach(func() {
		// Get the harness and context directly - no package-level variables
		workerID = GinkgoParallelProcess()
		harness = e2e.GetWorkerHarness()
		suiteCtx := e2e.GetWorkerContext()

		GinkgoWriter.Printf("🔄 [BeforeEach] Worker %d: Setting up test with VM from pool\n", workerID)

		// Create test-specific context for proper tracing
		ctx = util.StartSpecTracerForGinkgo(suiteCtx)

		// Set the test context in the harness
		harness.SetTestContext(ctx)

		// Setup device with TPM using the harness function
		// This will setup VM, configure TPM, and start agent
		err := harness.SetupDeviceWithTPM(workerID)
		Expect(err).ToNot(HaveOccurred())

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

	Context("Virtual TPM Enrollment and Verification", func() {
		// NOTE: Virtual TPM verification shows "Failed" status due to lack of chain of trust
		// This is EXPECTED behavior - test validates TPM functionality works correctly
		It("Should enroll device with virtual TPM and verify attestation data", Label("83974", "tpm", "sanity"), func() {
			By("verifying TPM device presence")
			stdout, err := harness.VM.RunSSH([]string{"ls", "-la", "/dev/tpm*"}, nil)
			Expect(err).ToNot(HaveOccurred())
			Expect(stdout.String()).To(ContainSubstring("/dev/tpm0"))
			logrus.Info("✅ TPM device found at /dev/tpm0")

			By("verifying TPM functionality")
			_, err = harness.VM.RunSSH([]string{"sudo", "tpm2_startup", "-c"}, nil)
			Expect(err).ToNot(HaveOccurred())
			logrus.Info("✅ TPM is functional")

			By("verifying agent reports TPM usage in logs")
			util.EventuallySlow(harness.ReadPrimaryVMAgentLogs).
				WithArguments(logLookbackDuration, util.FLIGHTCTL_AGENT_SERVICE).
				Should(ContainSubstring("Using TPM-based identity provider"))
			logrus.Info("✅ Agent using TPM-based identity provider")

			By("waiting for enrollment request with TPM attestation")
			var enrollmentID string

			Eventually(func() error {
				enrollmentID = harness.GetEnrollmentIDFromServiceLogs("flightctl-agent")
				if enrollmentID == "" {
					return errors.New("enrollment ID not found in agent logs")
				}

				logrus.Infof("Enrollment ID found: %s", enrollmentID)
				return nil
			}, util.TIMEOUT, util.POLLING).Should(Succeed())

			By("waiting for enrollment request with TPM attestation data")
			Eventually(func() error {
				enrollmentRequest := harness.WaitForEnrollmentRequest(enrollmentID)
				if enrollmentRequest == nil {
					return errors.New("enrollment request not found")
				}

				// Check if device status has system info
				if enrollmentRequest.Spec.DeviceStatus == nil {
					return errors.New("device status is nil")
				}

				if enrollmentRequest.Spec.DeviceStatus.SystemInfo.IsEmpty() {
					return errors.New("system info is empty")
				}

				logrus.Info("Enrollment request with TPM attestation data created")
				logrus.Infof("SystemInfo: %+v", enrollmentRequest.Spec.DeviceStatus.SystemInfo)

				// Check TPM attestation data
				err = harness.VerifyEnrollmentTPMAttestationData(enrollmentRequest.Spec.DeviceStatus.SystemInfo)
				Expect(err).ToNot(HaveOccurred())

				logrus.Info("✅ TPM attestation data found in enrollment request")
				return nil
			}, util.TIMEOUT, 5*util.POLLING).Should(Succeed())

			By("approving enrollment and waiting for device online")
			deviceId, _ := harness.EnrollAndWaitForOnlineStatus()
			logrus.Infof("✅ Device enrolled and online: %s", deviceId)

			By("checking TPM key persistence")
			Eventually(func() error {
				_, err := harness.VM.RunSSH([]string{"ls", "-la", "/var/lib/flightctl/tpm-blob.yaml"}, nil)
				return err
			}, util.TIMEOUT, 5*util.POLLING).Should(Succeed())
			logrus.Info("✅ TPM blob file exists")

			By("verifying TPM integrity verification status (expects 'Failed' for virtual TPM)")
			var device *v1alpha1.Device

			// Wait for integrity verification to complete
			Eventually(func() bool {
				// Refresh device status
				resp, err := harness.Client.GetDeviceWithResponse(harness.Context, deviceId)
				if err != nil || resp.JSON200 == nil {
					logrus.Infof("Failed to get device response: err=%v, resp=%v", err, resp)
					return false
				}
				device = resp.JSON200

				// Debug device integrity status
				if device.Status == nil {
					logrus.Info("Device status is nil")
					return false
				}

				logrus.Infof("Device integrity status: %s", device.Status.Integrity.Status)

				if device.Status.Integrity.Tpm == nil {
					logrus.Info("Device TPM integrity is nil")
					return false
				}

				logrus.Infof("TPM integrity status: %s", device.Status.Integrity.Tpm.Status)

				if device.Status.Integrity.DeviceIdentity != nil {
					logrus.Infof("Device identity integrity status: %s", device.Status.Integrity.DeviceIdentity.Status)
				} else {
					logrus.Info("Device identity integrity is nil")
				}

				// Check that verification completes (with any status)
				integrityComplete := device.Status.Integrity.Tpm != nil &&
					device.Status.Integrity.Tpm.Status != "" &&
					device.Status.Integrity.DeviceIdentity != nil &&
					device.Status.Integrity.DeviceIdentity.Status != ""

				logrus.Infof("Integrity verification complete: %t", integrityComplete)
				return integrityComplete
			}, 2*util.TIMEOUT, 5*util.POLLING).Should(BeTrue())

			// Verify TPM integrity structure is present
			Expect(device.Status.Integrity.Tpm).ToNot(BeNil())
			Expect(device.Status.Integrity.DeviceIdentity).ToNot(BeNil())

			// For virtual TPM, expect "Failed" status (no certificate chain)
			// This is EXPECTED behavior for virtual/software TPM
			Expect(device.Status.Integrity.Tpm.Status).To(Equal(v1alpha1.DeviceIntegrityCheckStatusFailed))
			Expect(device.Status.Integrity.DeviceIdentity.Status).To(Equal(v1alpha1.DeviceIntegrityCheckStatusFailed))
			Expect(device.Status.Integrity.Status).To(Equal(v1alpha1.DeviceIntegrityStatusFailed))

			logrus.Info("✅ Virtual TPM integrity verification completed with expected 'Failed' status")
			logrus.Infof("  - TPM: %s", device.Status.Integrity.Tpm.Status)
			logrus.Infof("  - Device Identity: %s", device.Status.Integrity.DeviceIdentity.Status)
			logrus.Infof("  - Overall: %s", device.Status.Integrity.Status)

			By("verifying TPM attestation data is present in device system info")
			err = harness.VerifyDeviceTPMAttestationData(device)
			Expect(err).ToNot(HaveOccurred())
			logrus.Info("✅ TPM attestation data present in device system info")

			By("verifying TPM-based identity is used for communication")
			// Make a config change to verify TPM-signed communication works
			newRenderedVersion, err := harness.PrepareNextDeviceVersion(deviceId)
			Expect(err).ToNot(HaveOccurred())

			// Add a simple inline config to trigger a rendered version update
			testConfig := v1alpha1.ConfigProviderSpec{}
			testFilePath := "/tmp/tpm-test-marker"
			testFileContent := fmt.Sprintf("Virtual TPM test marker - %d", time.Now().Unix())

			inlineConfig := v1alpha1.InlineConfigProviderSpec{
				Name: "tpm-test-config",
				Inline: []v1alpha1.FileSpec{
					{
						Path:    testFilePath,
						Content: testFileContent,
						Mode:    lo.ToPtr(0644),
					},
				},
			}
			err = testConfig.FromInlineConfigProviderSpec(inlineConfig)
			Expect(err).ToNot(HaveOccurred())

			// Update device config and wait for it to be applied using TPM-signed communication
			err = harness.UpdateDeviceConfigWithRetries(deviceId, []v1alpha1.ConfigProviderSpec{testConfig}, newRenderedVersion)
			Expect(err).ToNot(HaveOccurred())

			// Verify the configuration was actually applied to confirm TPM-signed communication worked
			var configOutput *bytes.Buffer
			configOutput, err = harness.VM.RunSSH([]string{"cat", testFilePath}, nil)
			Expect(err).ToNot(HaveOccurred())
			Expect(configOutput.String()).To(ContainSubstring("Virtual TPM test marker"))
			logrus.Infof("✅ Configuration successfully applied via TPM-signed communication: %s", strings.TrimSpace(configOutput.String()))

			By("verifying TPM communication is working for device updates")
			// The fact that configuration was successfully applied proves TPM is working
			// Virtual TPM can sign requests even though certificate chain validation fails

			logrus.Info("✅ VIRTUAL TPM VERIFICATION PASSED:")
			logrus.Info("  - Device enrolled with TPM attestation data")
			logrus.Info("  - TPM integrity verification completed (expected 'Failed' for virtual TPM)")
			logrus.Info("  - Configuration successfully applied via TPM-signed communication")
			logrus.Info("  - All TPM functionality verified working correctly with virtual TPM")

			// Optional: Check that the test marker file still exists as final verification
			_, err = harness.VM.RunSSH([]string{"test", "-f", testFilePath}, nil)
			Expect(err).ToNot(HaveOccurred(), "TPM-applied configuration file should still exist")
		})
	})
})
