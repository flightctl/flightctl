package tpm

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/flightctl/flightctl/test/harness/e2e"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestTPM(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "TPM E2E Suite")
}

const (
	// Eventually polling timeout/interval constants
	TIMEOUT      = time.Minute
	LONG_TIMEOUT = 10 * time.Minute
	POLLING      = time.Second
	LONG_POLLING = 10 * time.Second
)

// Initialize suite-specific settings
func init() {
	SetDefaultEventuallyTimeout(TIMEOUT)
	SetDefaultEventuallyPollingInterval(POLLING)
}

var _ = BeforeSuite(func() {
	// Setup VM and harness for this worker
	_, _, err := e2e.SetupWorkerHarness()
	Expect(err).ToNot(HaveOccurred())

	// Setup swtpm CA certificates for virtual TPM verification
	// This enables proper certificate chain validation for software TPM
	GinkgoWriter.Printf("🔐 Setting up swtpm CA certificates for virtual TPM tests...\n")
	err = setupSWTPMCertificates()
	if err != nil {
		GinkgoWriter.Printf("⚠️  Warning: Failed to setup swtpm certificates: %v\n", err)
		GinkgoWriter.Printf("    Virtual TPM tests may show 'Failed' verification status\n")
	} else {
		GinkgoWriter.Printf("✅ swtpm CA certificates configured successfully\n")
	}
})

// setupSWTPMCertificates copies swtpm CA certificates and configures the API deployment
func setupSWTPMCertificates() error {
	swTPMCADir := "/var/lib/swtpm-localca"
	tempCertDir := filepath.Join(os.TempDir(), fmt.Sprintf("swtpm-certs-%d", os.Getpid()))

	// Check if swtpm certificates exist
	if _, err := os.Stat(swTPMCADir); os.IsNotExist(err) {
		return fmt.Errorf("swtpm CA directory not found: %s", swTPMCADir)
	}

	// Create temp directory for certificates
	if err := os.MkdirAll(tempCertDir, 0755); err != nil {
		return fmt.Errorf("failed to create temp cert directory: %w", err)
	}
	defer os.RemoveAll(tempCertDir) // Clean up temp directory

	// Copy swtpm certificates (requires sudo as swtpm-localca may be root-owned)
	copyCmd := exec.Command("sudo", "sh", "-c",
		fmt.Sprintf("cp /var/lib/swtpm-localca/*cert.pem %s/ && chown -R %d:%d %s",
			tempCertDir, os.Getuid(), os.Getgid(), tempCertDir))

	output, err := copyCmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to copy swtpm certificates: %w, output: %s", err, string(output))
	}

	// Verify certificates were copied
	entries, err := os.ReadDir(tempCertDir)
	if err != nil {
		return fmt.Errorf("failed to read cert directory: %w", err)
	}
	if len(entries) == 0 {
		return fmt.Errorf("no certificates found after copy")
	}

	GinkgoWriter.Printf("  📋 Found %d swtpm certificate(s)\n", len(entries))

	// Call the script to add certificates to deployment
	scriptPath := "test/scripts/add-certs-to-deployment.sh"
	addCertsCmd := exec.Command(scriptPath, tempCertDir)
	addCertsCmd.Env = append(os.Environ(), "NAMESPACE=flightctl-external")

	output, err = addCertsCmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to add certificates to deployment: %w, output: %s", err, string(output))
	}

	GinkgoWriter.Printf("  📜 Certificate deployment output:\n%s\n", string(output))

	// Wait for API pod to be ready after restart
	GinkgoWriter.Printf("  ⏳ Waiting for API deployment to stabilize...\n")
	time.Sleep(30 * time.Second)

	return nil
}
