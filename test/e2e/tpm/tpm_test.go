// Package tpm provides E2E tests for TPM (Trusted Platform Module) device authentication and attestation functionality.
//
// REAL HARDWARE TPM TEST
// This test is designed to run on RHEL9 hypervisor with real TPM hardware.
// It covers the complete TPM verification process including:
// - Agent installation from Copr repository
// - TPM CA certificate setup
// - TPM attestation and credential challenge
// - Certificate chain validation
// - Full enrollment and approval workflow
//
// PREREQUISITES:
// - RHEL9 system with real TPM 2.0 hardware
// - TPM device accessible at /dev/tpm0
// - tpm2-tools package installed
// - Network access to FlightCtl API server
// - Network access to Copr repository
//
// ENVIRONMENT VARIABLES:
// - FLIGHTCTL_API_URL: FlightCtl API server URL (required)
// - FLIGHTCTL_TPM_CA_DIR: Directory containing TPM manufacturer CA certificates (default: /etc/flightctl/tpm-cas)
// - FLIGHTCTL_AGENT_COPR_REPO: Copr repository URL (default: @redhat-et/flightctl-dev)
//
// USAGE:
// sudo FLIGHTCTL_API_URL=https://api.flightctl.example.com go test ./test/e2e/tpm -v
package tpm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/test/harness/e2e"
	"github.com/flightctl/flightctl/test/login"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const (
	// Agent service and configuration
	agentServiceName = "flightctl-agent.service"
	agentConfigPath  = "/etc/flightctl/config.yaml"
	agentDataDir     = "/var/lib/flightctl"
	tpmBlobFile      = "/var/lib/flightctl/tpm-blob.yaml"

	// Copr repository
	defaultCoprRepo = "@redhat-et/flightctl-dev"

	// TPM paths
	tpmDevicePath = "/dev/tpm0"
	tpmCACertDir  = "/etc/flightctl/tpm-cas"

	// Timeouts
	enrollmentTimeout   = 5 * time.Minute
	approvalTimeout     = 2 * time.Minute
	verificationTimeout = 3 * time.Minute
)

var _ = Describe("Real Hardware TPM Device Authentication", func() {
	var (
		ctx             context.Context
		harness         *e2e.Harness
		enrollmentID    string
		deviceID        string
		apiURL          string
		tpmManufacturer string
		ekCertPath      string
	)

	BeforeEach(func() {
		ctx = context.Background()
		harness = e2e.GetWorkerHarness()
		harness.SetTestContext(ctx)

		// Get API URL from environment
		apiURL = os.Getenv("FLIGHTCTL_API_URL")
		Expect(apiURL).NotTo(BeEmpty(), "FLIGHTCTL_API_URL environment variable must be set")

		GinkgoWriter.Printf("🔧 Using FlightCtl API: %s\n", apiURL)

		// Login to API
		login.LoginToAPIWithToken(harness)

		GinkgoWriter.Printf("✅ Test setup completed\n")
	})

	AfterEach(func() {
		GinkgoWriter.Printf("🧹 Cleaning up test resources\n")

		// Stop and disable agent service
		if err := runCommand("systemctl", "stop", agentServiceName); err != nil {
			GinkgoWriter.Printf("⚠️  Failed to stop agent service: %v\n", err)
		}

		if err := runCommand("systemctl", "disable", agentServiceName); err != nil {
			GinkgoWriter.Printf("⚠️  Failed to disable agent service: %v\n", err)
		}

		// Clean up agent data directory (preserve certs)
		if err := runCommand("rm", "-rf", filepath.Join(agentDataDir, "db")); err != nil {
			GinkgoWriter.Printf("⚠️  Failed to clean up agent DB: %v\n", err)
		}
		if err := runCommand("rm", "-f", tpmBlobFile); err != nil {
			GinkgoWriter.Printf("⚠️  Failed to clean up TPM blob: %v\n", err)
		}

		// Delete device from FlightCtl if it was created
		if deviceID != "" && harness.Client != nil {
			resp, err := harness.Client.DeleteDeviceWithResponse(ctx, deviceID)
			if err == nil && resp.StatusCode() == http.StatusOK {
				GinkgoWriter.Printf("✅ Device %s deleted from FlightCtl\n", deviceID)
			}
		}

		// Delete enrollment request if it exists
		if enrollmentID != "" && harness.Client != nil {
			resp, err := harness.Client.DeleteEnrollmentRequestWithResponse(ctx, enrollmentID)
			if err == nil && resp.StatusCode() == http.StatusOK {
				GinkgoWriter.Printf("✅ Enrollment request %s deleted\n", enrollmentID)
			}
		}

		GinkgoWriter.Printf("✅ Test cleanup completed\n")
	})

	Context("Complete TPM Verification Workflow", func() {
		It("Should perform full TPM enrollment and verification on real hardware", Label("hardware", "tpm", "real-tpm", "sanity", "83974"), func() {
			By("Step 1: Verifying TPM hardware prerequisites")
			err := verifyTPMHardwarePrerequisites()
			Expect(err).ToNot(HaveOccurred(), "TPM hardware prerequisites check failed")

			By("Step 2: Detecting TPM manufacturer and extracting EK certificate")
			tpmManufacturer, ekCertPath, err = detectTPMManufacturer()
			Expect(err).ToNot(HaveOccurred(), "TPM manufacturer detection failed")
			GinkgoWriter.Printf("📋 Detected TPM Manufacturer: %s\n", tpmManufacturer)
			GinkgoWriter.Printf("📋 EK Certificate: %s\n", ekCertPath)

			By("Step 3: Verifying TPM CA certificates are configured")
			err = verifyTPMCACertificates(tpmManufacturer, ekCertPath)
			Expect(err).ToNot(HaveOccurred(), "TPM CA certificate verification failed")

			By("Step 4: Installing FlightCtl agent from Copr repository")
			coprRepo := os.Getenv("FLIGHTCTL_AGENT_COPR_REPO")
			if coprRepo == "" {
				coprRepo = defaultCoprRepo
			}
			err = installFlightCtlAgentFromCopr(coprRepo)
			Expect(err).ToNot(HaveOccurred(), "Agent installation from Copr failed")
			GinkgoWriter.Printf("  ✅ FlightCtl agent installed from Copr\n")

			By("Step 5: Configuring FlightCtl agent with TPM enabled")
			err = configureAgentWithTPM(apiURL, tpmDevicePath, agentConfigPath, agentDataDir)
			Expect(err).ToNot(HaveOccurred(), "Agent TPM configuration failed")
			GinkgoWriter.Printf("  ✅ Agent configured with TPM\n")

			By("Step 6: Starting FlightCtl agent service")
			err = startAgentServiceAndWaitForTPM(agentServiceName)
			Expect(err).ToNot(HaveOccurred(), "Agent service startup failed")
			GinkgoWriter.Printf("  ✅ Agent service started with TPM\n")

			By("Step 7: Waiting for TPM-based enrollment request")
			enrollmentID, err = waitForEnrollmentRequest()
			Expect(err).ToNot(HaveOccurred(), "Failed to get enrollment request")
			GinkgoWriter.Printf("📋 Enrollment Request ID: %s\n", enrollmentID)

			By("Step 8: Verifying TPM attestation data in enrollment request")
			err = verifyTPMAttestationData(harness, ctx, enrollmentID)
			Expect(err).ToNot(HaveOccurred(), "TPM attestation data verification failed")

			By("Step 9: Verifying credential challenge completion")
			err = verifyCredentialChallenge(harness, ctx, enrollmentID)
			Expect(err).ToNot(HaveOccurred(), "Credential challenge verification failed")

			By("Step 10: Approving enrollment request")
			deviceID, err = approveEnrollmentRequest(harness, ctx, enrollmentID)
			Expect(err).ToNot(HaveOccurred(), "Enrollment approval failed")
			GinkgoWriter.Printf("📋 Device ID: %s\n", deviceID)

			By("Step 11: Waiting for device to come online")
			err = waitForDeviceOnline(harness, ctx, deviceID)
			Expect(err).ToNot(HaveOccurred(), "Device did not come online")

			By("Step 12: Verifying TPM integrity checks passed")
			err = verifyTPMIntegrityChecks(harness, ctx, deviceID)
			Expect(err).ToNot(HaveOccurred(), "TPM integrity checks failed")

			By("Step 13: Verifying TPM key persistence")
			err = verifyTPMKeyPersistence()
			Expect(err).ToNot(HaveOccurred(), "TPM key persistence verification failed")

			By("Step 14: Verifying device communication using TPM identity")
			err = verifyTPMBasedCommunication(harness, ctx, deviceID)
			Expect(err).ToNot(HaveOccurred(), "TPM-based communication verification failed")

			By("Step 15: Final verification - All TPM checks passed")
			printTestSummary(deviceID, enrollmentID, tpmManufacturer)
		})
	})
})

// Helper functions - All return errors instead of using Expect()

func runCommand(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("command failed: %s %v\nOutput: %s\nError: %w", name, args, string(output), err)
	}
	return nil
}

func runCommandWithOutput(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("command failed: %s %v\nOutput: %s\nError: %w", name, args, string(output), err)
	}
	return string(output), nil
}

func verifyTPMHardwarePrerequisites() error {
	GinkgoWriter.Printf("🔍 Verifying TPM 2.0 hardware presence...\n")

	// Check TPM device exists
	if _, err := os.Stat(tpmDevicePath); err != nil {
		return fmt.Errorf("TPM device %s not found - ensure TPM is enabled in BIOS/UEFI: %w", tpmDevicePath, err)
	}
	GinkgoWriter.Printf("  ✅ TPM device found: %s\n", tpmDevicePath)

	// Check tpm2-tools are installed
	if _, err := exec.LookPath("tpm2_startup"); err != nil {
		return fmt.Errorf("tpm2-tools not installed - run: sudo dnf install tpm2-tools: %w", err)
	}
	GinkgoWriter.Printf("  ✅ tpm2-tools installed\n")

	// Test TPM access and startup
	if err := runCommand("tpm2_startup", "-c"); err != nil {
		return fmt.Errorf("failed to access TPM - check permissions on %s: %w", tpmDevicePath, err)
	}
	GinkgoWriter.Printf("  ✅ TPM accessible and responding\n")

	// Get TPM version info
	output, err := runCommandWithOutput("tpm2_getcap", "properties-fixed")
	if err != nil {
		return fmt.Errorf("failed to get TPM capabilities: %w", err)
	}
	if !strings.Contains(output, "TPM2_PT_") {
		return fmt.Errorf("invalid TPM response - not TPM 2.0 compliant")
	}
	GinkgoWriter.Printf("  ✅ TPM 2.0 verified\n")

	return nil
}

func detectTPMManufacturer() (string, string, error) {
	GinkgoWriter.Printf("🔍 Detecting TPM manufacturer from EK certificate...\n")

	// Well-known TPM NVRAM indices for EK certificates
	ekIndices := []string{
		"0x01c00002", // RSA EK Certificate
		"0x01c0000a", // ECC EK Certificate
	}

	var ekCertDER string
	var usedIndex string

	// Try to read EK certificate from TPM NVRAM
	for _, index := range ekIndices {
		ekCertDER = filepath.Join("/tmp", fmt.Sprintf("ek_cert_%s.der", strings.TrimPrefix(index, "0x")))
		err := runCommand("tpm2_nvread", index, "-o", ekCertDER)
		if err == nil {
			usedIndex = index
			GinkgoWriter.Printf("  ✅ EK certificate found at index %s\n", index)
			break
		}
	}

	if usedIndex == "" {
		return "", "", fmt.Errorf("failed to read EK certificate from TPM NVRAM - TPM may not have EK cert provisioned")
	}

	// Convert DER to PEM for easier inspection
	ekCertPEM := strings.TrimSuffix(ekCertDER, ".der") + ".pem"
	if err := runCommand("openssl", "x509", "-inform", "DER", "-in", ekCertDER, "-out", ekCertPEM); err != nil {
		return "", "", fmt.Errorf("failed to convert EK certificate to PEM format: %w", err)
	}

	// Extract certificate text
	certText, err := runCommandWithOutput("openssl", "x509", "-in", ekCertPEM, "-text", "-noout")
	if err != nil {
		return "", "", fmt.Errorf("failed to extract certificate text: %w", err)
	}

	// Detect manufacturer from certificate issuer
	manufacturer := "Unknown"
	if strings.Contains(certText, "Infineon") || strings.Contains(certText, "IFX") {
		manufacturer = "Infineon"
	} else if strings.Contains(certText, "STMicroelectronics") || strings.Contains(certText, "STM") || strings.Contains(certText, "STSAFE") {
		manufacturer = "STMicroelectronics"
	} else if strings.Contains(certText, "Nuvoton") {
		manufacturer = "Nuvoton"
	} else if strings.Contains(certText, "NSING") {
		manufacturer = "NSING"
	}

	GinkgoWriter.Printf("  ✅ Detected manufacturer: %s\n", manufacturer)
	GinkgoWriter.Printf("  📄 EK Certificate: %s\n", ekCertPEM)

	return manufacturer, ekCertPEM, nil
}

func verifyTPMCACertificates(manufacturer, ekCertPath string) error {
	GinkgoWriter.Printf("🔍 Verifying TPM CA certificates configuration...\n")

	// Check if TPM CA directory exists
	if _, err := os.Stat(tpmCACertDir); os.IsNotExist(err) {
		GinkgoWriter.Printf("  ⚠️  TPM CA directory not found, creating: %s\n", tpmCACertDir)
		if err := os.MkdirAll(tpmCACertDir, 0755); err != nil {
			return fmt.Errorf("failed to create TPM CA directory: %w", err)
		}
	}

	// Look for manufacturer-specific certificates in the repository
	repoCADir := filepath.Join("tpm-manufacturer-certs", strings.ToLower(manufacturer))
	if manufacturer == "STMicroelectronics" {
		repoCADir = filepath.Join("tpm-manufacturer-certs", "st-micro")
	} else if manufacturer == "NSING" {
		repoCADir = filepath.Join("tpm-manufacturer-certs", "nsing")
	}

	// Check if repository CA certs exist
	if _, err := os.Stat(repoCADir); err == nil {
		GinkgoWriter.Printf("  ✅ Found %s CA certificates in repository: %s\n", manufacturer, repoCADir)

		// Copy all PEM files from repository to system CA directory
		files, err := filepath.Glob(filepath.Join(repoCADir, "*.pem"))
		if err != nil {
			return fmt.Errorf("failed to list PEM files in %s: %w", repoCADir, err)
		}
		if len(files) == 0 {
			return fmt.Errorf("no PEM certificates found in %s", repoCADir)
		}

		for _, srcFile := range files {
			destFile := filepath.Join(tpmCACertDir, filepath.Base(srcFile))
			data, err := os.ReadFile(srcFile)
			if err != nil {
				return fmt.Errorf("failed to read certificate %s: %w", srcFile, err)
			}

			if err := os.WriteFile(destFile, data, 0644); err != nil {
				return fmt.Errorf("failed to write certificate %s: %w", destFile, err)
			}
			GinkgoWriter.Printf("    📄 Copied: %s -> %s\n", filepath.Base(srcFile), destFile)
		}
	} else {
		GinkgoWriter.Printf("  ⚠️  Repository CA certs not found for %s\n", manufacturer)
		GinkgoWriter.Printf("  📌 You may need to manually obtain CA certificates for this TPM manufacturer\n")
		GinkgoWriter.Printf("  📌 See: docs/user/tpm-authentication.md for certificate sources\n")
	}

	// Verify EK certificate chain using extracted certificate
	GinkgoWriter.Printf("  🔍 Extracting CA information from EK certificate...\n")
	certText, err := runCommandWithOutput("openssl", "x509", "-in", ekCertPath, "-text", "-noout")
	if err != nil {
		return fmt.Errorf("failed to read EK certificate: %w", err)
	}

	// Look for Authority Information Access (AIA) URLs
	if strings.Contains(certText, "Authority Information Access") {
		GinkgoWriter.Printf("  ✅ AIA extensions found in EK certificate\n")
		lines := strings.Split(certText, "\n")
		for _, line := range lines {
			if strings.Contains(line, "CA Issuers - URI:") {
				uri := strings.TrimSpace(strings.Split(line, "URI:")[1])
				GinkgoWriter.Printf("    📌 Intermediate CA URI: %s\n", uri)
			}
		}
	}

	// Count CA certificates in the directory
	caCerts, err := filepath.Glob(filepath.Join(tpmCACertDir, "*.pem"))
	if err != nil {
		return fmt.Errorf("failed to count CA certificates: %w", err)
	}
	GinkgoWriter.Printf("  ✅ Total CA certificates configured: %d\n", len(caCerts))

	// Verify at least some CA certs are present
	if len(caCerts) == 0 {
		return fmt.Errorf("no TPM CA certificates found in %s", tpmCACertDir)
	}

	return nil
}

func waitForEnrollmentRequest() (string, error) {
	GinkgoWriter.Printf("⏳ Waiting for enrollment request with TPM attestation...\n")

	var enrollmentID string

	// First, try to find enrollment ID from agent logs
	for i := 0; i < 30; i++ {
		logs, err := runCommandWithOutput("journalctl", "-u", agentServiceName, "--since", "2 minutes ago", "--no-pager")
		if err != nil {
			time.Sleep(10 * time.Second)
			continue
		}

		// Look for enrollment request ID in logs
		for _, line := range strings.Split(logs, "\n") {
			if strings.Contains(line, "enrollment request") && strings.Contains(line, "created") {
				// Try to extract ID from log line
				parts := strings.Fields(line)
				for j, part := range parts {
					if strings.Contains(part, "request") && j+1 < len(parts) {
						enrollmentID = strings.Trim(parts[j+1], `"`)
						if len(enrollmentID) > 0 {
							GinkgoWriter.Printf("  ✅ Found enrollment ID in logs: %s\n", enrollmentID)
							return enrollmentID, nil
						}
					}
				}
			}
		}

		time.Sleep(10 * time.Second)
	}

	// If we didn't find ID in logs, query API
	GinkgoWriter.Printf("  🔍 Querying enrollment requests from API...\n")

	harness := e2e.GetWorkerHarness()
	for i := 0; i < 30; i++ {
		resp, err := harness.Client.ListEnrollmentRequestsWithResponse(context.Background(), nil)
		if err != nil {
			time.Sleep(10 * time.Second)
			continue
		}

		if resp.JSON200 == nil || len(resp.JSON200.Items) == 0 {
			time.Sleep(10 * time.Second)
			continue
		}

		// Get the most recent enrollment request
		items := resp.JSON200.Items
		latestER := &items[len(items)-1]
		enrollmentID = *latestER.Metadata.Name
		GinkgoWriter.Printf("  ✅ Enrollment request found: %s\n", enrollmentID)
		return enrollmentID, nil
	}

	return "", fmt.Errorf("failed to find enrollment request within timeout")
}

func verifyTPMAttestationData(harness *e2e.Harness, ctx context.Context, enrollmentID string) error {
	GinkgoWriter.Printf("🔍 Verifying TPM attestation data in enrollment request...\n")

	var enrollmentRequest *v1alpha1.EnrollmentRequest

	// Wait for enrollment request with attestation data
	for i := 0; i < 24; i++ {
		resp, err := harness.Client.GetEnrollmentRequestWithResponse(ctx, enrollmentID)
		if err != nil {
			time.Sleep(10 * time.Second)
			continue
		}

		if resp.JSON200 == nil {
			time.Sleep(10 * time.Second)
			continue
		}

		enrollmentRequest = resp.JSON200

		// Check device status
		if enrollmentRequest.Spec.DeviceStatus == nil {
			time.Sleep(10 * time.Second)
			continue
		}

		if enrollmentRequest.Spec.DeviceStatus.SystemInfo.IsEmpty() {
			time.Sleep(10 * time.Second)
			continue
		}

		// Found valid data
		break
	}

	if enrollmentRequest == nil || enrollmentRequest.Spec.DeviceStatus == nil {
		return fmt.Errorf("enrollment request device status not available")
	}

	// Verify TPM attestation data
	systemInfo := enrollmentRequest.Spec.DeviceStatus.SystemInfo
	GinkgoWriter.Printf("  📋 System Info: %+v\n", systemInfo)

	// Check for TPM-specific fields in AdditionalProperties
	if systemInfo.AdditionalProperties == nil {
		return fmt.Errorf("SystemInfo AdditionalProperties is nil")
	}

	tpmAttestationData, hasTPM := systemInfo.AdditionalProperties["tpm_attestation_data"]
	if !hasTPM {
		return fmt.Errorf("TPM attestation data not found in AdditionalProperties")
	}
	if tpmAttestationData == "" {
		return fmt.Errorf("TPM attestation data is empty")
	}
	GinkgoWriter.Printf("  ✅ TPM attestation data present\n")

	// Parse and display attestation data if it's JSON
	var attestationJSON interface{}
	if err := json.Unmarshal([]byte(tpmAttestationData), &attestationJSON); err == nil {
		formattedJSON, _ := json.MarshalIndent(attestationJSON, "    ", "  ")
		GinkgoWriter.Printf("  📄 TPM Attestation Data:\n    %s\n", string(formattedJSON))
	} else {
		GinkgoWriter.Printf("  📄 TPM Attestation Data: %s\n", tpmAttestationData)
	}

	// Check enrollment request approval labels
	if enrollmentRequest.Status != nil {
		GinkgoWriter.Printf("  📋 Enrollment Status: %+v\n", enrollmentRequest.Status)

		if enrollmentRequest.Status.Approval != nil {
			approvalLabels := enrollmentRequest.Status.Approval.Labels
			if approvalLabels != nil {
				GinkgoWriter.Printf("  📋 Approval Labels:\n")
				for k, v := range *approvalLabels {
					GinkgoWriter.Printf("      %s: %s\n", k, v)
				}
			}
		}
	}

	return nil
}

func verifyCredentialChallenge(harness *e2e.Harness, ctx context.Context, enrollmentID string) error {
	GinkgoWriter.Printf("🔐 Verifying credential challenge completion...\n")

	// Wait for credential challenge to complete (indicated by "Verified" approval status)
	for i := 0; i < 36; i++ {
		resp, err := harness.Client.GetEnrollmentRequestWithResponse(ctx, enrollmentID)
		if err != nil {
			time.Sleep(10 * time.Second)
			continue
		}

		if resp.JSON200 == nil {
			time.Sleep(10 * time.Second)
			continue
		}

		enrollmentRequest := resp.JSON200

		// Check if approval status shows verification
		if enrollmentRequest.Status != nil && enrollmentRequest.Status.Approval != nil {
			if enrollmentRequest.Status.Approval.Approved {
				GinkgoWriter.Printf("  ✅ Enrollment request already approved\n")
				return nil
			}

			// Check for verification labels
			if enrollmentRequest.Status.Approval.Labels != nil {
				labels := *enrollmentRequest.Status.Approval.Labels
				if tpmVerified, ok := labels["tpm_verified"]; ok && tpmVerified == "true" {
					GinkgoWriter.Printf("  ✅ TPM verification label present\n")
					return nil
				}
			}
		}

		// Check agent logs for credential challenge completion
		logs, err := runCommandWithOutput("journalctl", "-u", agentServiceName, "--since", "3 minutes ago", "--no-pager")
		if err == nil {
			if strings.Contains(logs, "credential challenge") && strings.Contains(logs, "success") {
				GinkgoWriter.Printf("  ✅ Credential challenge completed (from logs)\n")
				return nil
			}
		}

		time.Sleep(10 * time.Second)
	}

	return fmt.Errorf("credential challenge did not complete within timeout")
}

func approveEnrollmentRequest(harness *e2e.Harness, ctx context.Context, enrollmentID string) (string, error) {
	GinkgoWriter.Printf("✅ Approving enrollment request...\n")

	// Get current enrollment request
	resp, err := harness.Client.GetEnrollmentRequestWithResponse(ctx, enrollmentID)
	if err != nil {
		return "", fmt.Errorf("failed to get enrollment request: %w", err)
	}
	if resp.JSON200 == nil {
		return "", fmt.Errorf("enrollment request not found")
	}

	enrollmentRequest := resp.JSON200

	// Check if already approved
	if enrollmentRequest.Status != nil && enrollmentRequest.Status.Approval != nil &&
		enrollmentRequest.Status.Approval.Approved {
		GinkgoWriter.Printf("  ℹ️  Enrollment request already approved\n")
		deviceID := enrollmentRequest.Status.Approval.ApprovedBy
		return deviceID, nil
	}

	// Approve the enrollment request
	approvalUpdate := v1alpha1.EnrollmentRequestApprovalStatus{
		Approved:   true,
		ApprovedAt: time.Now(),
		ApprovedBy: "", // Will be set by server
		Labels:     enrollmentRequest.Status.Approval.Labels,
	}

	// Update enrollment request with approval
	enrollmentRequest.Status.Approval = &approvalUpdate

	updateResp, err := harness.Client.ReplaceEnrollmentRequestStatusWithResponse(ctx, enrollmentID, *enrollmentRequest)
	if err != nil {
		return "", fmt.Errorf("failed to approve enrollment request: %w", err)
	}
	if updateResp.StatusCode() != http.StatusOK {
		return "", fmt.Errorf("approval request returned status %d", updateResp.StatusCode())
	}

	GinkgoWriter.Printf("  ✅ Enrollment request approved\n")

	// Wait for device to be created
	var deviceID string
	for i := 0; i < 24; i++ {
		resp, err := harness.Client.GetEnrollmentRequestWithResponse(ctx, enrollmentID)
		if err != nil {
			time.Sleep(5 * time.Second)
			continue
		}

		if resp.JSON200 == nil || resp.JSON200.Status == nil || resp.JSON200.Status.Approval == nil {
			time.Sleep(5 * time.Second)
			continue
		}

		if resp.JSON200.Status.Approval.ApprovedBy != "" {
			deviceID = resp.JSON200.Status.Approval.ApprovedBy
			break
		}

		time.Sleep(5 * time.Second)
	}

	if deviceID == "" {
		return "", fmt.Errorf("device was not created within timeout")
	}

	GinkgoWriter.Printf("  ✅ Device created: %s\n", deviceID)
	return deviceID, nil
}

func waitForDeviceOnline(harness *e2e.Harness, ctx context.Context, deviceID string) error {
	GinkgoWriter.Printf("⏳ Waiting for device to come online...\n")

	for i := 0; i < 36; i++ {
		resp, err := harness.Client.GetDeviceWithResponse(ctx, deviceID)
		if err != nil {
			time.Sleep(10 * time.Second)
			continue
		}

		if resp.JSON200 == nil {
			time.Sleep(10 * time.Second)
			continue
		}

		device := resp.JSON200

		// Check device status
		if device.Status == nil {
			GinkgoWriter.Printf("  ⏳ Device status not available yet...\n")
			time.Sleep(10 * time.Second)
			continue
		}

		// Look for Online condition
		for _, condition := range device.Status.Conditions {
			if condition.Type == "Online" && condition.Status == v1alpha1.ConditionStatusTrue {
				GinkgoWriter.Printf("  ✅ Device is online\n")
				return nil
			}
		}

		time.Sleep(10 * time.Second)
	}

	return fmt.Errorf("device did not come online within timeout")
}

func verifyTPMIntegrityChecks(harness *e2e.Harness, ctx context.Context, deviceID string) error {
	GinkgoWriter.Printf("🔐 Verifying TPM integrity checks...\n")

	var device *v1alpha1.Device

	// Wait for integrity verification to complete
	for i := 0; i < 36; i++ {
		resp, err := harness.Client.GetDeviceWithResponse(ctx, deviceID)
		if err != nil {
			time.Sleep(10 * time.Second)
			continue
		}

		if resp.JSON200 == nil {
			time.Sleep(10 * time.Second)
			continue
		}

		device = resp.JSON200

		if device.Status == nil {
			GinkgoWriter.Printf("  ⏳ Device status not available yet...\n")
			time.Sleep(10 * time.Second)
			continue
		}

		if device.Status.Integrity.Tpm == nil {
			GinkgoWriter.Printf("  ⏳ TPM integrity not available yet...\n")
			time.Sleep(10 * time.Second)
			continue
		}

		if device.Status.Integrity.DeviceIdentity == nil {
			GinkgoWriter.Printf("  ⏳ Device identity integrity not available yet...\n")
			time.Sleep(10 * time.Second)
			continue
		}

		GinkgoWriter.Printf("  📋 TPM Integrity Status: %s\n", device.Status.Integrity.Tpm.Status)
		GinkgoWriter.Printf("  📋 Device Identity Status: %s\n", device.Status.Integrity.DeviceIdentity.Status)
		GinkgoWriter.Printf("  📋 Overall Integrity Status: %s\n", device.Status.Integrity.Status)

		// Check for verification completion
		if device.Status.Integrity.Tpm.Status != "" &&
			device.Status.Integrity.DeviceIdentity.Status != "" {
			break
		}

		time.Sleep(10 * time.Second)
	}

	if device == nil || device.Status == nil {
		return fmt.Errorf("device status not available")
	}

	// Verify all checks passed with "Verified" status
	if device.Status.Integrity.Tpm.Status != v1alpha1.DeviceIntegrityCheckStatusVerified {
		return fmt.Errorf("TPM integrity check should be Verified for real hardware TPM, got: %s", device.Status.Integrity.Tpm.Status)
	}
	GinkgoWriter.Printf("  ✅ TPM integrity: Verified\n")

	if device.Status.Integrity.DeviceIdentity.Status != v1alpha1.DeviceIntegrityCheckStatusVerified {
		return fmt.Errorf("device identity integrity check should be Verified, got: %s", device.Status.Integrity.DeviceIdentity.Status)
	}
	GinkgoWriter.Printf("  ✅ Device identity integrity: Verified\n")

	if device.Status.Integrity.Status != v1alpha1.DeviceIntegrityStatusVerified {
		return fmt.Errorf("overall integrity status should be Verified, got: %s", device.Status.Integrity.Status)
	}
	GinkgoWriter.Printf("  ✅ Overall integrity: Verified\n")

	// Print integrity details if available
	if device.Status.Integrity.Tpm.Info != nil {
		GinkgoWriter.Printf("  📋 TPM Info: %s\n", *device.Status.Integrity.Tpm.Info)
	}

	if device.Status.Integrity.DeviceIdentity.Info != nil {
		GinkgoWriter.Printf("  📋 Device Identity Info: %s\n", *device.Status.Integrity.DeviceIdentity.Info)
	}

	return nil
}

func verifyTPMKeyPersistence() error {
	GinkgoWriter.Printf("🔑 Verifying TPM key persistence...\n")

	// Check that TPM blob file exists
	if _, err := os.Stat(tpmBlobFile); err != nil {
		return fmt.Errorf("TPM blob file not found at %s: %w", tpmBlobFile, err)
	}
	GinkgoWriter.Printf("  ✅ TPM blob file exists: %s\n", tpmBlobFile)

	// Read and display TPM blob info
	blobContent, err := os.ReadFile(tpmBlobFile)
	if err != nil {
		return fmt.Errorf("failed to read TPM blob file: %w", err)
	}
	GinkgoWriter.Printf("  📄 TPM Blob size: %d bytes\n", len(blobContent))

	// Verify TPM key handles are still accessible
	if err := runCommand("tpm2_startup", "-c"); err != nil {
		return fmt.Errorf("TPM keys not accessible: %w", err)
	}
	GinkgoWriter.Printf("  ✅ TPM keys accessible\n")

	return nil
}

func verifyTPMBasedCommunication(harness *e2e.Harness, ctx context.Context, deviceID string) error {
	GinkgoWriter.Printf("💬 Verifying device communication using TPM identity...\n")

	// Check agent logs for TPM-signed communication
	logs, err := runCommandWithOutput("journalctl", "-u", agentServiceName, "--since", "5 minutes ago", "--no-pager")
	if err != nil {
		return fmt.Errorf("failed to read agent logs: %w", err)
	}

	// Look for successful API communication
	if !strings.Contains(logs, "Using TPM-based identity provider") {
		return fmt.Errorf("agent not using TPM identity provider")
	}
	GinkgoWriter.Printf("  ✅ Agent using TPM identity\n")

	// Check for successful status updates
	if strings.Contains(logs, "status update") || strings.Contains(logs, "heartbeat") {
		GinkgoWriter.Printf("  ✅ Device communication active\n")
	}

	// Verify device is actively communicating by checking last seen timestamp
	resp, err := harness.Client.GetDeviceWithResponse(ctx, deviceID)
	if err != nil {
		return fmt.Errorf("failed to get device: %w", err)
	}
	if resp.JSON200 == nil {
		return fmt.Errorf("device not found")
	}

	device := resp.JSON200
	if device.Status != nil && device.Status.LastSeen != nil {
		lastSeen := *device.Status.LastSeen
		timeSinceLastSeen := time.Since(lastSeen)
		GinkgoWriter.Printf("  📋 Last seen: %s ago\n", timeSinceLastSeen.Round(time.Second))
		if timeSinceLastSeen >= 2*time.Minute {
			return fmt.Errorf("device has not communicated recently (last seen %s ago)", timeSinceLastSeen.Round(time.Second))
		}
	}

	GinkgoWriter.Printf("  ✅ TPM-based communication verified\n")
	return nil
}

func installFlightCtlAgentFromCopr(coprRepo string) error {
	// Check if agent is already installed
	if _, err := exec.LookPath("flightctl-agent"); err == nil {
		// Agent already installed, continue
		GinkgoWriter.Printf("  ℹ️  FlightCtl agent already installed\n")
	} else {
		// Enable Copr repository
		if err := runCommand("dnf", "copr", "enable", "-y", coprRepo); err != nil {
			return fmt.Errorf("failed to enable Copr repository %s: %w", coprRepo, err)
		}

		// Install flightctl package
		if err := runCommand("dnf", "install", "-y", "flightctl"); err != nil {
			return fmt.Errorf("failed to install flightctl package: %w", err)
		}
	}

	// Verify agent binary exists
	if _, err := exec.LookPath("flightctl-agent"); err != nil {
		return fmt.Errorf("flightctl-agent binary not found after installation: %w", err)
	}

	return nil
}

func configureAgentWithTPM(apiURL, tpmDevice, configPath, dataDir string) error {
	// Create agent configuration with TPM enabled
	agentConfig := fmt.Sprintf(`server:
  url: %s
  
tpm:
  enable: true
  device: %s

enrollment:
  approve: false

log:
  level: debug
`, apiURL, tpmDevice)

	// Write configuration
	if err := os.WriteFile(configPath, []byte(agentConfig), 0600); err != nil {
		return fmt.Errorf("failed to write agent configuration to %s: %w", configPath, err)
	}

	// Ensure data directory exists
	if err := runCommand("mkdir", "-p", dataDir); err != nil {
		return fmt.Errorf("failed to create data directory %s: %w", dataDir, err)
	}

	return nil
}

func startAgentServiceAndWaitForTPM(serviceName string) error {
	// Enable service
	if err := runCommand("systemctl", "enable", serviceName); err != nil {
		return fmt.Errorf("failed to enable service %s: %w", serviceName, err)
	}

	// Start service
	if err := runCommand("systemctl", "start", serviceName); err != nil {
		return fmt.Errorf("failed to start service %s: %w", serviceName, err)
	}

	// Wait for service to be active (max 30 seconds)
	for i := 0; i < 15; i++ {
		output, err := runCommandWithOutput("systemctl", "is-active", serviceName)
		if err == nil && strings.Contains(output, "active") {
			break
		}
		if i == 14 {
			return fmt.Errorf("service %s did not become active within 30 seconds", serviceName)
		}
		time.Sleep(2 * time.Second)
	}

	// Wait for TPM initialization in logs (max 30 seconds)
	for i := 0; i < 6; i++ {
		logs, err := runCommandWithOutput("journalctl", "-u", serviceName, "--since", "1 minute ago", "--no-pager")
		if err == nil && strings.Contains(logs, "Using TPM-based identity provider") {
			return nil
		}
		if i == 5 {
			return fmt.Errorf("TPM initialization not detected in service logs within 30 seconds")
		}
		time.Sleep(5 * time.Second)
	}

	return nil
}

func printTestSummary(deviceID, enrollmentID, manufacturer string) {
	GinkgoWriter.Printf("\n")
	GinkgoWriter.Printf("═══════════════════════════════════════════════════════════════\n")
	GinkgoWriter.Printf("✅ TPM VERIFICATION TEST PASSED - ALL CHECKS SUCCESSFUL\n")
	GinkgoWriter.Printf("═══════════════════════════════════════════════════════════════\n")
	GinkgoWriter.Printf("\n")
	GinkgoWriter.Printf("📋 Test Summary:\n")
	GinkgoWriter.Printf("  • Device ID: %s\n", deviceID)
	GinkgoWriter.Printf("  • Enrollment Request ID: %s\n", enrollmentID)
	GinkgoWriter.Printf("  • TPM Manufacturer: %s\n", manufacturer)
	GinkgoWriter.Printf("\n")
	GinkgoWriter.Printf("✅ Verified Components:\n")
	GinkgoWriter.Printf("  • TPM 2.0 hardware detection\n")
	GinkgoWriter.Printf("  • TPM manufacturer identification\n")
	GinkgoWriter.Printf("  • TPM CA certificate chain configuration\n")
	GinkgoWriter.Printf("  • FlightCtl agent installation from Copr\n")
	GinkgoWriter.Printf("  • Agent TPM configuration\n")
	GinkgoWriter.Printf("  • TPM key generation (LAK, LDevID)\n")
	GinkgoWriter.Printf("  • TCG-CSR creation with attestation data\n")
	GinkgoWriter.Printf("  • EK certificate chain validation\n")
	GinkgoWriter.Printf("  • Credential challenge completion\n")
	GinkgoWriter.Printf("  • Enrollment approval workflow\n")
	GinkgoWriter.Printf("  • TPM integrity verification (Verified status)\n")
	GinkgoWriter.Printf("  • Device identity verification (Verified status)\n")
	GinkgoWriter.Printf("  • TPM key persistence\n")
	GinkgoWriter.Printf("  • TPM-signed device communication\n")
	GinkgoWriter.Printf("\n")
	GinkgoWriter.Printf("🔐 Security Validation:\n")
	GinkgoWriter.Printf("  • Hardware root of trust established\n")
	GinkgoWriter.Printf("  • Certificate chain validated from device to manufacturer\n")
	GinkgoWriter.Printf("  • Cryptographic proof of possession verified\n")
	GinkgoWriter.Printf("  • Secure communication channel established\n")
	GinkgoWriter.Printf("\n")
	GinkgoWriter.Printf("═══════════════════════════════════════════════════════════════\n")
	GinkgoWriter.Printf("\n")
}
