package encryption_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/flightctl/flightctl/test/e2e/infra"
	"github.com/flightctl/flightctl/test/e2e/infra/setup"
	"github.com/flightctl/flightctl/test/harness/e2e"
	testutil "github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
)

const (
	// defaultKeyID is the encryption key ID used at service startup.
	defaultKeyID = "default"
	// encryptionKeyDir is the mount path for encryption keys inside service containers.
	encryptionKeyDir = "/root/.flightctl/encryption"
)

// ciphertextMatchesKeyID reports whether value begins with the expected ciphertext prefix
// "enc:v1:<keyID>:" indicating the value was encrypted under keyID.
func ciphertextMatchesKeyID(value, keyID string) bool {
	return strings.HasPrefix(value, fmt.Sprintf("enc:v1:%s:", keyID))
}

// queryDB executes a psql query against the flightctl database and returns the trimmed output.
// Returns an error if providers are not initialized or if the query fails.
func queryDB(p *infra.Providers, sql string) (string, error) {
	if p == nil || p.Infra == nil {
		return "", fmt.Errorf("queryDB: providers not initialized")
	}
	output, err := p.Infra.ExecInService(infra.ServiceDB, []string{
		"psql", "-d", "flightctl", "-t", "-A", "-c", sql,
	})
	if err != nil {
		GinkgoWriter.Printf("queryDB: psql failed (sql=%q): %v\n", sql, err)
		return "", fmt.Errorf("psql query failed: %w", err)
	}
	return strings.TrimSpace(output), nil
}

// buildOIDCAuthProviderYAML renders a minimal OIDC AuthProvider manifest string for encryption tests.
func buildOIDCAuthProviderYAML(name, issuerURL, clientID, clientSecret string) string {
	return fmt.Sprintf(`apiVersion: flightctl.io/v1beta1
kind: AuthProvider
metadata:
  name: %s
spec:
  providerType: oidc
  displayName: %s
  issuer: %s
  clientId: %s
  clientSecret: %s
  enabled: false
  scopes:
    - openid
    - profile
    - email
  usernameClaim:
    - preferred_username
  organizationAssignment:
    type: static
    organizationName: default
  roleAssignment:
    type: static
    roles:
      - flightctl-admin
`, name, name, issuerURL, clientID, clientSecret)
}

// applyManifest writes manifest YAML to a temp file and applies it via the harness CLI.
// Login must be established by the caller before invoking this function.
func applyManifest(harness *e2e.Harness, manifestYAML string) (string, error) {
	if harness == nil {
		return "", fmt.Errorf("applyManifest: harness is required")
	}
	if manifestYAML == "" {
		return "", fmt.Errorf("applyManifest: manifestYAML is empty")
	}
	tmp, err := os.CreateTemp("", "enc-manifest-*.yaml")
	if err != nil {
		GinkgoWriter.Printf("applyManifest: create temp file failed: %v\n", err)
		return "", fmt.Errorf("create temp manifest file: %w", err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(manifestYAML); err != nil {
		GinkgoWriter.Printf("applyManifest: write temp file failed: %v\n", err)
		return "", fmt.Errorf("write manifest: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("close temp file: %w", err)
	}
	out, err := harness.ApplyResource(tmp.Name())
	if err != nil {
		GinkgoWriter.Printf("applyManifest: ApplyResource failed: %v\n", err)
	}
	return out, err
}

// deleteAuthProvider deletes an AuthProvider CR by name via the harness CLI.
// Login must be established by the caller before invoking this function.
func deleteAuthProvider(harness *e2e.Harness, name string) error {
	if harness == nil {
		return fmt.Errorf("deleteAuthProvider: harness is required")
	}
	if name == "" {
		return fmt.Errorf("deleteAuthProvider: name is required")
	}
	_, err := harness.ManageResource("delete", "authprovider", name)
	if err != nil {
		GinkgoWriter.Printf("deleteAuthProvider: ManageResource failed for %q: %v\n", name, err)
	}
	return err
}

// writeKeyToService writes raw key bytes to a named file in the encryption directory
// on the given service's container using ExecInServiceWithStdin + tee.
func writeKeyToService(svc infra.ServiceName, keyFileName string, keyBytes []byte) error {
	if keyFileName == "" {
		return fmt.Errorf("writeKeyToService: keyFileName is required")
	}
	if len(keyBytes) == 0 {
		return fmt.Errorf("writeKeyToService: keyBytes is empty")
	}
	p := setup.GetDefaultProviders()
	if p == nil || p.Infra == nil {
		return fmt.Errorf("writeKeyToService: providers not initialized")
	}
	keyPath := filepath.Join(encryptionKeyDir, keyFileName)
	_, err := p.Infra.ExecInServiceWithStdin(svc, []string{"tee", keyPath}, bytes.NewReader(keyBytes))
	if err != nil {
		GinkgoWriter.Printf("writeKeyToService: failed to write %s on %s: %v\n", keyPath, svc, err)
		return fmt.Errorf("write key file %s on %s: %w", keyPath, svc, err)
	}
	return nil
}

// addKeyEntryToConfig injects a new key entry into the encryption.keys list and updates
// activeKeyID in the YAML config string. The key is stored at encryptionKeyDir/<keyFileName>.
// Returns an error if the encryption.activeKeyID field is not found in config.
func addKeyEntryToConfig(config, newKeyID, keyFileName string) (string, error) {
	if newKeyID == "" || keyFileName == "" {
		return "", fmt.Errorf("addKeyEntryToConfig: newKeyID and keyFileName are required")
	}
	newKeyPath := filepath.Join(encryptionKeyDir, keyFileName)
	lines := strings.Split(config, "\n")
	var out []string
	inEncryptionBlock := false
	activeKeyIDUpdated := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "encryption:" {
			inEncryptionBlock = true
			out = append(out, line)
			continue
		}
		if inEncryptionBlock {
			if strings.HasPrefix(trimmed, "activeKeyID:") {
				out = append(out, fmt.Sprintf("  activeKeyID: %s", newKeyID))
				activeKeyIDUpdated = true
				continue
			}
			if trimmed == "keys:" {
				out = append(out, line)
				out = append(out, fmt.Sprintf("  - id: %s", newKeyID))
				out = append(out, fmt.Sprintf("    path: %s", newKeyPath))
				continue
			}
			if trimmed != "" && !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") {
				inEncryptionBlock = false
			}
		}
		out = append(out, line)
	}

	if !activeKeyIDUpdated {
		return "", fmt.Errorf("addKeyEntryToConfig: encryption.activeKeyID not found in config; cannot rotate key")
	}
	return strings.Join(out, "\n"), nil
}

// removeKeyEntryFromConfig removes the entry for keyIDToRemove (and its following path: line)
// from the encryption.keys list in the YAML config string.
func removeKeyEntryFromConfig(config, keyIDToRemove string) string {
	lines := strings.Split(config, "\n")
	var out []string
	skipNext := false
	for _, line := range lines {
		if skipNext {
			skipNext = false
			continue
		}
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "- id: ") {
			id := strings.TrimSpace(strings.TrimPrefix(trimmed, "- id: "))
			if id == keyIDToRemove {
				skipNext = true
				continue
			}
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

// setEncryptionConfigOnAllServices updates the encryption config on the API, worker, and periodic services.
func setEncryptionConfigOnAllServices(config string) error {
	p := setup.GetDefaultProviders()
	if p == nil || p.Infra == nil {
		return fmt.Errorf("setEncryptionConfigOnAllServices: providers not initialized")
	}
	for _, svc := range []infra.ServiceName{infra.ServiceAPI, infra.ServiceWorker, infra.ServicePeriodic} {
		if err := p.Infra.SetServiceConfig(svc, "config.yaml", config); err != nil {
			GinkgoWriter.Printf("setEncryptionConfigOnAllServices: failed to set config on %s: %v\n", svc, err)
			return fmt.Errorf("set config on %s: %w", svc, err)
		}
	}
	return nil
}

// rotateEncryptionKey writes a new key file to the API, worker, and periodic service containers,
// adds it to the encryption config, and sets it as the active key. Returns the original config
// so the caller can restore it. The caller must restart services after this call.
func rotateEncryptionKey(newKeyID string, keyBytes []byte) (savedConfig string, err error) {
	if newKeyID == "" {
		return "", fmt.Errorf("rotateEncryptionKey: newKeyID is required")
	}
	if len(keyBytes) == 0 {
		return "", fmt.Errorf("rotateEncryptionKey: keyBytes is empty")
	}
	p := setup.GetDefaultProviders()
	if p == nil || p.Infra == nil {
		return "", fmt.Errorf("rotateEncryptionKey: providers not initialized")
	}
	savedConfig, err = p.Infra.GetServiceConfig(infra.ServiceAPI)
	if err != nil {
		GinkgoWriter.Printf("rotateEncryptionKey: failed to read API service config: %v\n", err)
		return "", fmt.Errorf("read API service config: %w", err)
	}
	keyFileName := "key-" + newKeyID
	for _, svc := range []infra.ServiceName{infra.ServiceAPI, infra.ServiceWorker, infra.ServicePeriodic} {
		if err := writeKeyToService(svc, keyFileName, keyBytes); err != nil {
			return "", fmt.Errorf("write key to %s: %w", svc, err)
		}
	}
	updatedConfig, err := addKeyEntryToConfig(savedConfig, newKeyID, keyFileName)
	if err != nil {
		return "", fmt.Errorf("build updated config: %w", err)
	}
	if err := setEncryptionConfigOnAllServices(updatedConfig); err != nil {
		return "", fmt.Errorf("update service configs: %w", err)
	}
	return savedConfig, nil
}

// removeEncryptionKey removes the entry for keyIDToRemove from all service configs without
// deleting the key file. Returns the original config so the caller can restore it.
// The caller must restart services after this call.
func removeEncryptionKey(keyIDToRemove string) (savedConfig string, err error) {
	if keyIDToRemove == "" {
		return "", fmt.Errorf("removeEncryptionKey: keyIDToRemove is required")
	}
	p := setup.GetDefaultProviders()
	if p == nil || p.Infra == nil {
		return "", fmt.Errorf("removeEncryptionKey: providers not initialized")
	}
	savedConfig, err = p.Infra.GetServiceConfig(infra.ServiceAPI)
	if err != nil {
		GinkgoWriter.Printf("removeEncryptionKey: failed to read API service config: %v\n", err)
		return "", fmt.Errorf("read API service config: %w", err)
	}
	updatedConfig := removeKeyEntryFromConfig(savedConfig, keyIDToRemove)
	if err := setEncryptionConfigOnAllServices(updatedConfig); err != nil {
		return "", fmt.Errorf("update service configs: %w", err)
	}
	return savedConfig, nil
}

// restoreEncryptionConfig restores the original saved config to all services.
// The caller must restart services after this call.
func restoreEncryptionConfig(savedConfig string) error {
	if savedConfig == "" {
		return fmt.Errorf("restoreEncryptionConfig: savedConfig is empty")
	}
	return setEncryptionConfigOnAllServices(savedConfig)
}

// restartServicesAndWait restarts the API, worker, and periodic services and waits for readiness.
func restartServicesAndWait() error {
	p := setup.GetDefaultProviders()
	if p == nil || p.Lifecycle == nil {
		return fmt.Errorf("restartServicesAndWait: providers not initialized")
	}
	for _, svc := range []infra.ServiceName{infra.ServiceAPI, infra.ServiceWorker, infra.ServicePeriodic} {
		if err := p.Lifecycle.Restart(svc); err != nil {
			GinkgoWriter.Printf("restartServicesAndWait: failed to restart %s: %v\n", svc, err)
			return fmt.Errorf("restart %s: %w", svc, err)
		}
	}
	for _, svc := range []infra.ServiceName{infra.ServiceAPI, infra.ServiceWorker, infra.ServicePeriodic} {
		if err := p.Lifecycle.WaitForReady(svc, testutil.DURATION_TIMEOUT); err != nil {
			GinkgoWriter.Printf("restartServicesAndWait: %s not ready within timeout: %v\n", svc, err)
			return fmt.Errorf("wait for %s ready: %w", svc, err)
		}
	}
	return nil
}

// backupRestoreExternalDBSkipReason returns a non-empty skip message when the backup/restore
// encryption tests cannot run (external DB profile or no built-in DB pod available).
func backupRestoreExternalDBSkipReason() string {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("E2E_EXTERNAL_DATABASE"))) {
	case "1", "true", "yes":
		return "Encryption backup/restore e2e skipped: E2E_EXTERNAL_DATABASE set (external DB profile). " +
			"These tests require pg_dump via the built-in flightctl-db pod; external DB coverage tracked under EDM-3213."
	}
	p := setup.GetDefaultProviders()
	if p != nil && p.Infra != nil && !p.Infra.BuiltinDatabaseWorkloadAvailable() {
		return "Encryption backup/restore e2e skipped: no flightctl-db pod (external PostgreSQL / Helm db.type=external). EDM-3213."
	}
	return ""
}

// encryptionKeyArchiveEntryPrefix returns the archive directory prefix for encryption key material.
// K8s backup writes encryption/flightctl-encryption-key.yaml; Quadlet writes encryption/<keyfiles>.
func encryptionKeyArchiveEntryPrefix() string {
	return filepath.Join("encryption") + string(filepath.Separator)
}

// listTarGzEntries returns the entry names from a .tar.gz archive.
// Returns an error if the archive cannot be opened or read.
func listTarGzEntries(archivePath string) ([]string, error) {
	if archivePath == "" {
		return nil, fmt.Errorf("listTarGzEntries: archivePath is required")
	}
	file, err := os.Open(archivePath)
	if err != nil {
		GinkgoWriter.Printf("listTarGzEntries: cannot open archive %q: %v\n", archivePath, err)
		return nil, fmt.Errorf("open archive: %w", err)
	}
	defer file.Close()

	gzr, err := gzip.NewReader(file)
	if err != nil {
		GinkgoWriter.Printf("listTarGzEntries: cannot create gzip reader for %q: %v\n", archivePath, err)
		return nil, fmt.Errorf("create gzip reader: %w", err)
	}
	defer gzr.Close()

	var entries []string
	tr := tar.NewReader(gzr)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			GinkgoWriter.Printf("listTarGzEntries: error reading tar entry in %q: %v\n", archivePath, err)
			return nil, fmt.Errorf("read tar entry: %w", err)
		}
		entries = append(entries, hdr.Name)
	}
	return entries, nil
}

// prometheusURL returns the Prometheus base URL for encryption metric assertions.
// It first tries the infra provider (Quadlet/OCP), then falls back to the auxiliary Prometheus
// testcontainer started in BeforeSuite (Kind).
func prometheusURL() (string, func(), error) {
	noopCleanup := func() {}
	providers := setup.GetDefaultProviders()
	if providers == nil || providers.Infra == nil {
		return "", noopCleanup, fmt.Errorf("prometheusURL: providers not initialized")
	}
	url, cleanup, err := providers.Infra.ExposeService(infra.ServicePrometheus, "http")
	if err == nil && url != "" {
		if cleanup == nil {
			cleanup = noopCleanup
		}
		GinkgoWriter.Printf("prometheusURL: using infra Prometheus at %s\n", url)
		return url, cleanup, nil
	}
	if auxSvcs == nil || auxSvcs.Prometheus == nil || auxSvcs.Prometheus.URL == "" {
		return "", noopCleanup, fmt.Errorf("prometheusURL: no Prometheus available (infra: %v; auxiliary not started)", err)
	}
	GinkgoWriter.Printf("prometheusURL: using auxiliary Prometheus at %s\n", auxSvcs.Prometheus.URL)
	return auxSvcs.Prometheus.URL, noopCleanup, nil
}
