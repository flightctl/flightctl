package tpm

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/flightctl/flightctl/test/e2e/infra/setup"
	"github.com/sirupsen/logrus"
)

const (
	swtpmLocalCAConfName = "swtpm-localca.conf"
	manufacturerCertsDir = "tpm-manufacturer-certs"
)

// InjectTPMCerts collects TPM CA certificates and injects them into the API server.
// It always injects swtpm certs. If includeManufacturer is true, it also injects
// all manufacturer certs from tpm-manufacturer-certs/ in the repo.
func InjectTPMCerts(ctx context.Context, includeManufacturer bool) error {
	certs, err := collectSwtpmCerts()
	if err != nil {
		return fmt.Errorf("failed to collect swtpm CA certs: %w", err)
	}
	logrus.Infof("Collected %d swtpm CA certificate(s)", len(certs))

	if includeManufacturer {
		mfgCerts, err := collectManufacturerCerts()
		if err != nil {
			return fmt.Errorf("failed to collect manufacturer certs: %w", err)
		}
		if len(mfgCerts) == 0 {
			return fmt.Errorf("no manufacturer certs found but includeManufacturer was requested")
		}
		logrus.Infof("Collected %d manufacturer CA certificate(s)", len(mfgCerts))
		for k, v := range mfgCerts {
			certs[k] = v
		}
	}

	if len(certs) == 0 {
		return fmt.Errorf("no valid TPM CA certificates found")
	}

	providers := setup.GetDefaultProviders()
	if providers == nil || providers.TPM == nil {
		return fmt.Errorf("TPM provider not available")
	}

	return providers.TPM.InjectCerts(ctx, certs)
}

// CleanupTPMCerts removes TPM CA certificates from the API server configuration.
func CleanupTPMCerts(ctx context.Context) error {
	providers := setup.GetDefaultProviders()
	if providers == nil || providers.TPM == nil {
		return fmt.Errorf("TPM provider not available")
	}

	return providers.TPM.CleanupCerts(ctx)
}

func collectSwtpmCerts() (map[string][]byte, error) {
	caDir, err := swtpmLocalCADirFromConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to determine swtpm-localca directory: %w", err)
	}

	certs := make(map[string][]byte)
	entries, err := os.ReadDir(caDir)
	if err != nil {
		return nil, fmt.Errorf("failed to read swtpm CA directory %s: %w", caDir, err)
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), "cert.pem") {
			continue
		}
		certPath := filepath.Join(caDir, entry.Name())
		data, err := os.ReadFile(certPath)
		if err != nil {
			logrus.Warnf("Failed to read swtpm cert %s: %v", certPath, err)
			continue
		}
		if !isValidPEMCert(data) {
			logrus.Warnf("Skipping invalid certificate: %s", certPath)
			continue
		}
		certs[entry.Name()] = data
	}

	return certs, nil
}

// swtpmLocalCADirFromConfig parses the user-level swtpm-localca.conf to find the statedir.
// E2E VMs use qemu:///session, so certs are in the user's config directory.
func swtpmLocalCADirFromConfig() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to get home directory: %w", err)
	}

	confPath := filepath.Join(home, ".config", swtpmLocalCAConfName)
	data, err := os.ReadFile(confPath)
	if err != nil {
		return "", fmt.Errorf("failed to read %s: %w", confPath, err)
	}

	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "statedir") {
			parts := strings.SplitN(line, "=", 2)
			if len(parts) == 2 {
				return strings.TrimSpace(parts[1]), nil
			}
		}
	}

	return "", fmt.Errorf("statedir not found in %s", confPath)
}

func collectManufacturerCerts() (map[string][]byte, error) {
	certs := make(map[string][]byte)
	// ginkgo runs from test/e2e/<suite>/, repo root is ../../../
	repoRoot := filepath.Join("..", "..", "..")
	certsDir := filepath.Join(repoRoot, manufacturerCertsDir)

	err := filepath.Walk(certsDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(info.Name(), ".pem") {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			logrus.Warnf("Failed to read manufacturer cert %s: %v", path, readErr)
			return nil
		}
		if !isValidPEMCert(data) {
			logrus.Warnf("Skipping invalid certificate: %s", path)
			return nil
		}
		// Flatten vendor subdirectory into filename to avoid key collisions
		relPath, _ := filepath.Rel(certsDir, path)
		key := strings.ReplaceAll(relPath, string(os.PathSeparator), "-")
		certs[key] = data
		return nil
	})

	return certs, err
}

func isValidPEMCert(data []byte) bool {
	block, _ := pem.Decode(data)
	if block == nil {
		return false
	}
	_, err := x509.ParseCertificate(block.Bytes)
	return err == nil
}
