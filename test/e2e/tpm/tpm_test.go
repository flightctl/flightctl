// Package tpm provides E2E tests for TPM (Trusted Platform Module) device authentication and attestation functionality.
//
// ENVIRONMENT CONFIGURATION:
// Set FLIGHTCTL_REAL_TPM=true to test with real hardware TPM devices (expects "Verified" status)
// Set FLIGHTCTL_REAL_TPM=false or leave unset to test with virtual TPM (expects "Failed" status)
//
// IMPORTANT NOTES:
// - Virtual TPM (software TPM in VMs) verification shows "Failed" status due to lack of chain of trust
// - Real hardware TPM devices show "Verified" status when properly configured
// - The test validates TPM functionality (enrollment, attestation data) works correctly in both cases
// - Test assertions automatically adjust based on FLIGHTCTL_REAL_TPM environment variable
//
// USAGE EXAMPLES:
// # Test with virtual TPM (default behavior)
// go test ./test/e2e/tpm
//
// # Test with real hardware TPM
// FLIGHTCTL_REAL_TPM=true go test ./test/e2e/tpm
package tpm

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
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

// isRealTPMDevice returns true if testing with real TPM hardware (expects "Verified" status)
// Set environment variable FLIGHTCTL_REAL_TPM=true to test with real hardware TPM devices
func isRealTPMDevice() bool {
	return strings.ToLower(os.Getenv("FLIGHTCTL_REAL_TPM")) == "true"
}

// getExpectedTPMStatus returns the expected TPM integrity status based on hardware type
func getExpectedTPMStatus() v1alpha1.DeviceIntegrityCheckStatusType {
	if isRealTPMDevice() {
		return v1alpha1.DeviceIntegrityCheckStatusVerified
	}
	return v1alpha1.DeviceIntegrityCheckStatusFailed
}

// getExpectedIntegrityStatus returns the expected overall integrity status based on hardware type
func getExpectedIntegrityStatus() v1alpha1.DeviceIntegrityStatusSummaryType {
	if isRealTPMDevice() {
		return v1alpha1.DeviceIntegrityStatusVerified
	}
	return v1alpha1.DeviceIntegrityStatusFailed
}

var _ = Describe("TPM Device Authentication", func() {
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

		// Setup device with TPM using the new harness function
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

		// Create test-specific context for cleanup (same as BeforeEach does)
		// This ensures we have the test ID needed for resource cleanup
		ctx := util.StartSpecTracerForGinkgo(suiteCtx)
		harness.SetTestContext(ctx)

		err := harness.CleanUpTestResources()
		Expect(err).ToNot(HaveOccurred())

		GinkgoWriter.Printf("✅ [AfterEach] Worker %d: Test cleanup completed\n", workerID)
	})

	Context("TPM Enrollment", func() {
		// NOTE: Test behavior depends on environment variable FLIGHTCTL_REAL_TPM:
		// - FLIGHTCTL_REAL_TPM=false (default): Virtual TPM - expects "Failed" verification status
		// - FLIGHTCTL_REAL_TPM=true: Real hardware TPM - expects "Verified" verification status
		// Virtual TPM verification shows "Failed" due to lack of chain of trust (expected behavior)
		// Test validates TPM enrollment and attestation functionality works correctly in both cases
		It("Should enroll device with TPM enabled and verify attestation", Label("83974", "tpm", "sanity"), func() {
			By("verifying TPM device presence")
			stdout, err := harness.VM.RunSSH([]string{"ls", "-la", "/dev/tpm*"}, nil)
			Expect(err).ToNot(HaveOccurred())
			Expect(stdout.String()).To(ContainSubstring("/dev/tpm0"))

			By("verifying TPM version")
			_, err = harness.VM.RunSSH([]string{"sudo", "tpm2_startup", "-c"}, nil)
			Expect(err).ToNot(HaveOccurred())

			By("verifying agent reports TPM usage in logs")
			util.EventuallySlow(harness.ReadPrimaryVMAgentLogs).
				WithArguments(logLookbackDuration, util.FLIGHTCTL_AGENT_SERVICE).
				Should(ContainSubstring("Using TPM-based identity provider"))

			By("waiting for enrollment request with TPM attestation")
			var enrollmentID string

			Eventually(func() error {
				enrollmentID = harness.GetEnrollmentIDFromServiceLogs("flightctl-agent")
				if enrollmentID == "" {
					return errors.New("enrollment ID not found in agent logs")
				}

				logrus.Infof("Enrollment ID found in VM console output: %s", enrollmentID)
				return nil
			}, util.TIMEOUT, util.POLLING).Should(Succeed())

			// Wait for enrollment request to be created with TPM attestation data
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

				logrus.Infof("Enrollment request with TPM attestation data created successfully!")
				logrus.Infof("Device status present with SystemInfo")

				// Check for TPM-related information in SystemInfo
				systemInfo := enrollmentRequest.Spec.DeviceStatus.SystemInfo
				logrus.Infof("SystemInfo available keys: %+v", systemInfo)

				// Check TPM attestation data
				err = harness.VerifyEnrollmentTPMAttestationData(systemInfo)
				Expect(err).ToNot(HaveOccurred())

				logrus.Infof("TPM attestation data found in enrollment request")
				return nil
			}, util.TIMEOUT, 5*util.POLLING).Should(Succeed())

			By("approving enrollment and waiting for device online")
			deviceId, _ := harness.EnrollAndWaitForOnlineStatus()

			By("checking TPM key persistence")
			Eventually(func() error {
				_, err := harness.VM.RunSSH([]string{"ls", "-la", "/var/lib/flightctl/tpm-blob.yaml"}, nil)
				return err
			}, util.TIMEOUT, 5*util.POLLING).Should(Succeed())

			// NOTE: Verification status depends on TPM hardware type and environment configuration
			tpmType := "Virtual"
			expectedStatus := "Failed"
			if isRealTPMDevice() {
				tpmType = "Real Hardware"
				expectedStatus = "Verified"
			}
			logrus.Infof("Testing with %s TPM - expecting %s integrity verification status", tpmType, expectedStatus)

			By(fmt.Sprintf("verifying TPM integrity verification reports %s status (%s TPM)", expectedStatus, tpmType))

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
					logrus.Infof("Device status is nil")
					return false
				}

				logrus.Infof("Device integrity status: %s", device.Status.Integrity.Status)

				if device.Status.Integrity.Tpm == nil {
					logrus.Infof("Device TPM integrity is nil")
					return false
				}

				logrus.Infof("TPM integrity status: %s", device.Status.Integrity.Tpm.Status)

				if device.Status.Integrity.DeviceIdentity != nil {
					logrus.Infof("Device identity integrity status: %s", device.Status.Integrity.DeviceIdentity.Status)
				} else {
					logrus.Infof("Device identity integrity is nil")
				}

				// Check that verification completes with expected status
				integrityComplete := device.Status.Integrity.Tpm != nil &&
					device.Status.Integrity.Tpm.Status != "" &&
					device.Status.Integrity.DeviceIdentity != nil &&
					device.Status.Integrity.DeviceIdentity.Status != ""

				logrus.Infof("Integrity verification complete: %t", integrityComplete)
				return integrityComplete
			}, 2*util.TIMEOUT, 5*util.POLLING).Should(BeTrue())

			// Verify TPM integrity structure is present and has expected status
			Expect(device.Status.Integrity.Tpm).ToNot(BeNil())
			Expect(device.Status.Integrity.DeviceIdentity).ToNot(BeNil())

			// Check integrity status based on TPM hardware type
			expectedTPMStatus := getExpectedTPMStatus()
			expectedIntegrityStatus := getExpectedIntegrityStatus()

			Expect(device.Status.Integrity.Tpm.Status).To(Equal(expectedTPMStatus))
			Expect(device.Status.Integrity.DeviceIdentity.Status).To(Equal(expectedTPMStatus))
			Expect(device.Status.Integrity.Status).To(Equal(expectedIntegrityStatus))

			logrus.Infof("%s TPM integrity verification completed as expected - TPM: %s, Device Identity: %s, Overall: %s",
				tpmType,
				device.Status.Integrity.Tpm.Status,
				device.Status.Integrity.DeviceIdentity.Status,
				device.Status.Integrity.Status)

			By("verifying TPM attestation data is present in device system info")
			err = harness.VerifyDeviceTPMAttestationData(device)
			Expect(err).ToNot(HaveOccurred())

			By("verifying TPM-based identity is used for communication")
			// Make a config change to verify TPM-signed communication works
			newRenderedVersion, err := harness.PrepareNextDeviceVersion(deviceId)
			Expect(err).ToNot(HaveOccurred())

			// Add a simple inline config to trigger a rendered version update
			testConfig := v1alpha1.ConfigProviderSpec{}
			testFilePath := "/tmp/tpm-test-marker"
			testFileContent := fmt.Sprintf("TPM test marker - %d", time.Now().Unix())

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
			Expect(configOutput.String()).To(ContainSubstring("TPM test marker"))
			logrus.Infof("Configuration successfully applied via TPM-signed communication: %s", strings.TrimSpace(configOutput.String()))

			By("verifying TPM communication is working for device updates")
			// The fact that configuration was successfully applied proves TPM is working
			// We don't need to check logs since they contain historical startup messages
			// that predate the TPM configuration being applied

			logrus.Infof("✅ TPM verification PASSED:")
			logrus.Infof("  - Device enrolled with TPM attestation data")
			logrus.Infof("  - TPM integrity verification completed (%s TPM)", tpmType)
			logrus.Infof("  - Configuration successfully applied via TPM-signed communication")
			logrus.Infof("  - All TPM functionality verified working correctly")

			// Optional: Check that the test marker file still exists as final verification
			_, err = harness.VM.RunSSH([]string{"test", "-f", testFilePath}, nil)
			Expect(err).ToNot(HaveOccurred(), "TPM-applied configuration file should still exist")
		})
	})
})
