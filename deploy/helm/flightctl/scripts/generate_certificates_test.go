package scripts_test

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func TestCertificateGeneration(t *testing.T) {
	scriptPath := getScriptPath(t)

	t.Run("generates certificates with correct SANs", func(t *testing.T) {
		certDir := t.TempDir()

		cmd := exec.Command("bash", scriptPath,
			"--cert-dir", certDir,
			"--api-san", "api.example.com",
			"--api-san", "localhost",
		)
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("Script failed: %v\nOutput: %s", err, output)
		}

		// Verify API cert was created
		apiCert := filepath.Join(certDir, "flightctl-api", "server.crt")
		if _, err := os.Stat(apiCert); os.IsNotExist(err) {
			t.Fatal("API server certificate was not created")
		}

		// Verify SAN is present
		if !certHasSAN(t, apiCert, "api.example.com") {
			t.Error("API certificate missing expected SAN: api.example.com")
		}
	})

	t.Run("skips regeneration when SANs match", func(t *testing.T) {
		certDir := t.TempDir()

		// First run - generate certs
		cmd := exec.Command("bash", scriptPath,
			"--cert-dir", certDir,
			"--api-san", "api.example.com",
		)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("First run failed: %v\nOutput: %s", err, output)
		}

		// Get SHA256 fingerprint of original cert
		apiCert := filepath.Join(certDir, "flightctl-api", "server.crt")
		origFingerprint := certFingerprint(t, apiCert)

		// Second run - same SANs, should skip
		cmd = exec.Command("bash", scriptPath,
			"--cert-dir", certDir,
			"--api-san", "api.example.com",
		)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("Second run failed: %v\nOutput: %s", err, output)
		}

		// Verify cert was not regenerated using fingerprint
		newFingerprint := certFingerprint(t, apiCert)
		if origFingerprint != newFingerprint {
			t.Errorf("Certificate was regenerated when SANs matched - should have been skipped\nOriginal: %s\nNew: %s", origFingerprint, newFingerprint)
		}
	})

	t.Run("regenerates when SANs change", func(t *testing.T) {
		certDir := t.TempDir()

		// First run - generate with old SAN
		cmd := exec.Command("bash", scriptPath,
			"--cert-dir", certDir,
			"--api-san", "api.old-domain.com",
		)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("First run failed: %v\nOutput: %s", err, output)
		}

		apiCert := filepath.Join(certDir, "flightctl-api", "server.crt")
		if !certHasSAN(t, apiCert, "api.old-domain.com") {
			t.Fatal("Initial certificate missing expected SAN")
		}

		// Second run - different SAN, should regenerate
		cmd = exec.Command("bash", scriptPath,
			"--cert-dir", certDir,
			"--api-san", "api.new-domain.com",
		)
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("Second run failed: %v\nOutput: %s", err, output)
		}

		// Verify cert has new SAN
		if !certHasSAN(t, apiCert, "api.new-domain.com") {
			t.Error("Certificate was not regenerated with new SAN")
		}
	})

	t.Run("preserves CA when server certs regenerate", func(t *testing.T) {
		certDir := t.TempDir()

		// First run
		cmd := exec.Command("bash", scriptPath,
			"--cert-dir", certDir,
			"--api-san", "api.old-domain.com",
		)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("First run failed: %v\nOutput: %s", err, output)
		}

		// Get CA cert content
		caCert := filepath.Join(certDir, "ca.crt")
		origCA, err := os.ReadFile(caCert)
		if err != nil {
			t.Fatalf("Failed to read CA cert: %v", err)
		}

		// Second run - different SAN
		cmd = exec.Command("bash", scriptPath,
			"--cert-dir", certDir,
			"--api-san", "api.new-domain.com",
		)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("Second run failed: %v\nOutput: %s", err, output)
		}

		// Verify CA was preserved
		newCA, err := os.ReadFile(caCert)
		if err != nil {
			t.Fatalf("Failed to read CA cert after regeneration: %v", err)
		}

		if string(origCA) != string(newCA) {
			t.Error("CA certificate was changed when it should have been preserved")
		}
	})

	t.Run("preserves client-signer CA when server certs regenerate", func(t *testing.T) {
		certDir := t.TempDir()

		// First run
		cmd := exec.Command("bash", scriptPath,
			"--cert-dir", certDir,
			"--api-san", "api.old-domain.com",
		)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("First run failed: %v\nOutput: %s", err, output)
		}

		// Get client-signer CA content
		clientSignerCert := filepath.Join(certDir, "flightctl-api", "client-signer.crt")
		origClientSigner, err := os.ReadFile(clientSignerCert)
		if err != nil {
			t.Fatalf("Failed to read client-signer cert: %v", err)
		}

		// Second run - different SAN
		cmd = exec.Command("bash", scriptPath,
			"--cert-dir", certDir,
			"--api-san", "api.new-domain.com",
		)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("Second run failed: %v\nOutput: %s", err, output)
		}

		// Verify client-signer CA was preserved
		newClientSigner, err := os.ReadFile(clientSignerCert)
		if err != nil {
			t.Fatalf("Failed to read client-signer cert after regeneration: %v", err)
		}

		if string(origClientSigner) != string(newClientSigner) {
			t.Error("Client-signer CA certificate was changed when it should have been preserved")
		}
	})

	t.Run("regenerates when additional SAN is added", func(t *testing.T) {
		certDir := t.TempDir()

		// First run - generate with initial SANs
		cmd := exec.Command("bash", scriptPath,
			"--cert-dir", certDir,
			"--api-san", "api.example.com",
			"--api-san", "localhost",
		)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("First run failed: %v\nOutput: %s", err, output)
		}

		apiCert := filepath.Join(certDir, "flightctl-api", "server.crt")

		// Second run - add a new SAN
		cmd = exec.Command("bash", scriptPath,
			"--cert-dir", certDir,
			"--api-san", "api.example.com",
			"--api-san", "localhost",
			"--api-san", "new-host.example.com",
		)
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("Second run failed: %v\nOutput: %s", err, output)
		}

		// Verify cert has the new SAN (was regenerated)
		if !certHasSAN(t, apiCert, "new-host.example.com") {
			t.Error("Certificate was not regenerated with new SAN - all SANs should be checked")
		}
	})
}

func getScriptPath(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("Failed to get test file location")
	}
	return filepath.Join(filepath.Dir(filename), "generate-certificates.sh")
}

func certHasSAN(t *testing.T, certPath, expectedSAN string) bool {
	t.Helper()
	cmd := exec.Command("openssl", "x509", "-in", certPath, "-noout", "-checkhost", expectedSAN)
	err := cmd.Run()
	return err == nil
}

func certFingerprint(t *testing.T, certPath string) string {
	t.Helper()
	data, err := os.ReadFile(certPath)
	if err != nil {
		t.Fatalf("Failed to read certificate %s: %v", certPath, err)
	}
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}
