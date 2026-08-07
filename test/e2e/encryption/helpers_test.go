package encryption_test

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	authproviderhelpers "github.com/flightctl/flightctl/test/e2e/authprovider/helpers"
	"github.com/flightctl/flightctl/test/e2e/infra"
	"github.com/flightctl/flightctl/test/e2e/infra/setup"
	"github.com/flightctl/flightctl/test/harness/e2e"
	testutil "github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	"sigs.k8s.io/yaml"
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

// queryDB executes a psql query against the built-in flightctl database and returns
// the trimmed output. Requires a reachable flightctl-db pod; fails on external-DB deployments.
func queryDB(p *infra.Providers, sql string) (string, error) {
	return infra.QueryDB(p, sql)
}

// buildOIDCAuthProviderYAML renders a minimal OIDC AuthProvider manifest string for encryption tests.
// All encryption tests create providers with enabled=false since they only exercise credential storage.
func buildOIDCAuthProviderYAML(name, issuerURL, clientID, clientSecret string) string {
	return authproviderhelpers.BuildOIDCAuthProviderYAML(name, issuerURL, clientID, clientSecret, false)
}

// applyManifest writes manifest YAML to a temp file and applies it via the harness CLI.
// Login must be established by the caller before invoking this function.
func applyManifest(harness *e2e.Harness, manifestYAML string) (string, error) {
	return authproviderhelpers.ApplyManifest(harness, manifestYAML)
}

// deleteAuthProvider deletes an AuthProvider CR by name via the harness CLI.
// Login must be established by the caller before invoking this function.
func deleteAuthProvider(harness *e2e.Harness, name string) error {
	_, err := authproviderhelpers.DeleteAuthProvider(harness, name)
	return err
}

// writeKeyToService makes a named encryption key available to the given service.
// It delegates to InfraProvider.SetEncryptionKey, which:
//   - On K8s/OCP: patches the flightctl-encryption-key Secret and triggers a rollout restart
//     of the service's deployment so pods mount the updated (read-only projected) Secret.
//   - On Quadlet: writes the key file directly to the host at /etc/flightctl/encryption/<keyFileName>,
//     which is bind-mounted into the container.
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
	if err := p.Infra.SetEncryptionKey(svc, keyFileName, keyBytes); err != nil {
		GinkgoWriter.Printf("writeKeyToService: failed to set key %s on %s: %v\n", keyFileName, svc, err)
		return fmt.Errorf("write key file %s on %s: %w", keyFileName, svc, err)
	}
	return nil
}

// encryptionConfigShape is the YAML shape of the encryption block for marshal/unmarshal.
type encryptionConfigShape struct {
	ActiveKeyID string `json:"activeKeyID"`
	Keys        []struct {
		ID   string `json:"id"`
		Path string `json:"path"`
	} `json:"keys"`
}

// addKeyEntryToConfig injects a new key entry into the encryption.keys list and updates
// activeKeyID in the YAML config string. The key is stored at encryptionKeyDir/<keyFileName>.
// If the config has no encryption: block (e.g. Quadlet where defaults are baked into the binary),
// the full block is appended, including the implicit default key at encryptionKeyDir/key.
// Uses YAML marshal/unmarshal to avoid indentation-sensitivity issues across environments.
func addKeyEntryToConfig(config, newKeyID, keyFileName string) (string, error) {
	if newKeyID == "" || keyFileName == "" {
		return "", fmt.Errorf("addKeyEntryToConfig: newKeyID and keyFileName are required")
	}
	newKeyPath := filepath.Join(encryptionKeyDir, keyFileName)

	// Parse the full config into a generic map so we can manipulate the encryption section
	// without disturbing any other top-level keys.
	var root map[string]interface{}
	if err := yaml.Unmarshal([]byte(config), &root); err != nil {
		return "", fmt.Errorf("addKeyEntryToConfig: parse config: %w", err)
	}
	if root == nil {
		root = make(map[string]interface{})
	}

	// Extract (or synthesise) the encryption sub-tree.
	var enc encryptionConfigShape
	if raw, ok := root["encryption"]; ok && raw != nil {
		// Re-marshal the sub-tree then unmarshal into the typed struct so we get clean field access.
		sub, err := yaml.Marshal(raw)
		if err != nil {
			return "", fmt.Errorf("addKeyEntryToConfig: re-marshal encryption block: %w", err)
		}
		if err := yaml.Unmarshal(sub, &enc); err != nil {
			return "", fmt.Errorf("addKeyEntryToConfig: parse encryption block: %w", err)
		}
	} else {
		// No encryption block — seed with the implicit default key so it stays available for decryption.
		enc.ActiveKeyID = defaultKeyID
		enc.Keys = []struct {
			ID   string `json:"id"`
			Path string `json:"path"`
		}{
			{ID: defaultKeyID, Path: filepath.Join(encryptionKeyDir, "key")},
		}
	}

	// Guard against duplicate key IDs (e.g. on test retry without a successful config restore).
	for _, k := range enc.Keys {
		if k.ID == newKeyID {
			return "", fmt.Errorf("addKeyEntryToConfig: key ID %q already present in config — restore original config before adding", newKeyID)
		}
	}

	// Prepend the new key as the active key so it is tried first on encryption.
	enc.ActiveKeyID = newKeyID
	enc.Keys = append([]struct {
		ID   string `json:"id"`
		Path string `json:"path"`
	}{{ID: newKeyID, Path: newKeyPath}}, enc.Keys...)

	// Put the updated encryption block back into the root map and marshal the whole thing.
	encBytes, err := yaml.Marshal(enc)
	if err != nil {
		return "", fmt.Errorf("addKeyEntryToConfig: marshal encryption block: %w", err)
	}
	var encMap interface{}
	if err := yaml.Unmarshal(encBytes, &encMap); err != nil {
		return "", fmt.Errorf("addKeyEntryToConfig: re-parse encryption block: %w", err)
	}
	root["encryption"] = encMap

	out, err := yaml.Marshal(root)
	if err != nil {
		return "", fmt.Errorf("addKeyEntryToConfig: marshal config: %w", err)
	}
	return string(out), nil
}

// removeKeyEntryFromConfig removes the entry for keyIDToRemove from the encryption.keys list
// in the YAML config string. Returns an error if any YAML parse or marshal step fails;
// a silent fallback to the original config would mask a broken key-removal operation
// (the caller would proceed with the key still present, causing later assertions to misfail).
func removeKeyEntryFromConfig(config, keyIDToRemove string) (string, error) {
	var root map[string]interface{}
	if err := yaml.Unmarshal([]byte(config), &root); err != nil {
		return "", fmt.Errorf("removeKeyEntryFromConfig: parse config: %w", err)
	}
	if root == nil {
		return "", fmt.Errorf("removeKeyEntryFromConfig: parsed config is nil")
	}

	raw, ok := root["encryption"]
	if !ok || raw == nil {
		return "", fmt.Errorf("removeKeyEntryFromConfig: no encryption block in config")
	}

	sub, err := yaml.Marshal(raw)
	if err != nil {
		return "", fmt.Errorf("removeKeyEntryFromConfig: re-marshal encryption block: %w", err)
	}
	var enc encryptionConfigShape
	if err := yaml.Unmarshal(sub, &enc); err != nil {
		return "", fmt.Errorf("removeKeyEntryFromConfig: parse encryption block: %w", err)
	}

	filtered := enc.Keys[:0]
	for _, k := range enc.Keys {
		if k.ID != keyIDToRemove {
			filtered = append(filtered, k)
		}
	}
	enc.Keys = filtered
	// If the removed key was the active key, promote the first remaining key.
	// Leaving activeKeyID pointing to a key no longer in the ring causes the
	// service to crash at startup (cannot load the active key).
	if enc.ActiveKeyID == keyIDToRemove && len(enc.Keys) > 0 {
		enc.ActiveKeyID = enc.Keys[0].ID
	}

	encBytes, err := yaml.Marshal(enc)
	if err != nil {
		return "", fmt.Errorf("removeKeyEntryFromConfig: marshal updated encryption block: %w", err)
	}
	var encMap interface{}
	if err := yaml.Unmarshal(encBytes, &encMap); err != nil {
		return "", fmt.Errorf("removeKeyEntryFromConfig: re-parse updated encryption block: %w", err)
	}
	root["encryption"] = encMap

	out, err := yaml.Marshal(root)
	if err != nil {
		return "", fmt.Errorf("removeKeyEntryFromConfig: marshal config: %w", err)
	}
	return string(out), nil
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
	GinkgoWriter.Printf("rotateEncryptionKey: updatedConfig to be written:\n%s\n", updatedConfig)
	if err := setEncryptionConfigOnAllServices(updatedConfig); err != nil {
		return "", fmt.Errorf("update service configs: %w", err)
	}
	// Read back to confirm what was actually stored.
	if readback, err := p.Infra.GetServiceConfig(infra.ServiceAPI); err == nil {
		GinkgoWriter.Printf("rotateEncryptionKey: API config readback after set:\n%s\n", readback)
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
	updatedConfig, err := removeKeyEntryFromConfig(savedConfig, keyIDToRemove)
	if err != nil {
		return "", fmt.Errorf("remove key %s from config: %w", keyIDToRemove, err)
	}
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

// resetEncryptionConfigToDefault rewrites the encryption section on all services to use only the
// default key (activeKeyID: default, path: /root/.flightctl/encryption/key). It is used at suite
// startup to recover from a previous run that left services with a rotated-key config that
// prevents them from starting (e.g. because the key file or its encoding was incorrect).
// The caller must restart services after this call.
func resetEncryptionConfigToDefault() error {
	p := setup.GetDefaultProviders()
	if p == nil || p.Infra == nil {
		return fmt.Errorf("resetEncryptionConfigToDefault: providers not initialized")
	}
	config, err := p.Infra.GetServiceConfig(infra.ServiceAPI)
	if err != nil {
		return fmt.Errorf("resetEncryptionConfigToDefault: read API config: %w", err)
	}
	var root map[string]interface{}
	if err := yaml.Unmarshal([]byte(config), &root); err != nil || root == nil {
		return fmt.Errorf("resetEncryptionConfigToDefault: parse config: %w", err)
	}
	defaultEnc := encryptionConfigShape{
		ActiveKeyID: defaultKeyID,
		Keys: []struct {
			ID   string `json:"id"`
			Path string `json:"path"`
		}{{ID: defaultKeyID, Path: filepath.Join(encryptionKeyDir, "key")}},
	}
	encBytes, err := yaml.Marshal(defaultEnc)
	if err != nil {
		return fmt.Errorf("resetEncryptionConfigToDefault: marshal default encryption block: %w", err)
	}
	var encMap interface{}
	if err := yaml.Unmarshal(encBytes, &encMap); err != nil {
		return fmt.Errorf("resetEncryptionConfigToDefault: re-parse default encryption block: %w", err)
	}
	root["encryption"] = encMap
	out, err := yaml.Marshal(root)
	if err != nil {
		return fmt.Errorf("resetEncryptionConfigToDefault: marshal config: %w", err)
	}
	return setEncryptionConfigOnAllServices(string(out))
}

// captureOriginalServiceConfig reads the current API service config and returns it.
// Returns empty string on error (non-fatal; recovery falls back to resetEncryptionConfigToDefault).
func captureOriginalServiceConfig() string {
	p := setup.GetDefaultProviders()
	if p == nil || p.Infra == nil {
		return ""
	}
	cfg, err := p.Infra.GetServiceConfig(infra.ServiceAPI)
	if err != nil {
		GinkgoWriter.Printf("captureOriginalServiceConfig: could not read API config: %v\n", err)
		return ""
	}
	return cfg
}

// resetMigrationCheckpoints deletes all encryption-migration checkpoint rows from the DB.
// This is called after a key rotation test restores the original config to prevent stale
// "complete" checkpoints for the rotated key from short-circuiting the migration worker when
// the same key ID is used again in a subsequent test (e.g. S1 restores → S2 re-rotates to
// the same "rotated-key" ID; without this reset the worker sees Complete=true and skips).
// Non-fatal: logs a warning on failure.
func resetMigrationCheckpoints() {
	p := setup.GetDefaultProviders()
	if p == nil || p.Infra == nil {
		return
	}
	if !p.Infra.BuiltinDatabaseWorkloadAvailable() {
		return
	}
	if _, err := queryDB(p, "DELETE FROM checkpoints WHERE consumer = 'encryption-migration'"); err != nil {
		GinkgoWriter.Printf("resetMigrationCheckpoints: failed to delete checkpoints (non-fatal): %v\n", err)
	}
}

// deleteStaleCanaries removes any canary rows whose key_id does not match keepKeyID.
// This prevents the service from crash-looping on startup when ValidateCanaries tries to
// decrypt a canary that was created for a key no longer present in the config.
// keepKeyID should be the active key ID after config restore (e.g. defaultKeyID).
// Non-fatal: logs a warning on failure rather than failing the suite.
func deleteStaleCanaries(keepKeyID string) {
	p := setup.GetDefaultProviders()
	if p == nil || p.Infra == nil {
		return
	}
	if !p.Infra.BuiltinDatabaseWorkloadAvailable() {
		return
	}
	if _, err := queryDB(p, fmt.Sprintf(
		"DELETE FROM encryption_canaries WHERE key_id != '%s'", keepKeyID,
	)); err != nil {
		GinkgoWriter.Printf("deleteStaleCanaries: failed to delete stale canary rows (non-fatal): %v\n", err)
	}
}

// cleanUpStableEncryptionResources deletes resources with stable (non-test-ID) names that may
// have been left by a previous crashed test run. These resources use a shared issuer+clientId
// that causes 409 conflicts when subsequent tests try to create their own authproviders.
// Non-fatal: logs a warning on failure.
func cleanUpStableEncryptionResources(h *e2e.Harness) {
	const stableAuthProviderName = "enc-rot-ap-stable"
	if err := deleteAuthProvider(h, stableAuthProviderName); err != nil {
		GinkgoWriter.Printf("cleanUpStableEncryptionResources: delete %s (non-fatal): %v\n", stableAuthProviderName, err)
	}
}

// recoverServicesToOriginalConfig restores all services to the config captured at suite start
// and resets the encryption key Secret to only the default "key" entry.
// Falls back to resetEncryptionConfigToDefault if no original config was saved.
// The caller must restart services after this call.
func recoverServicesToOriginalConfig() error {
	p := setup.GetDefaultProviders()
	if p != nil && p.Infra != nil {
		if err := p.Infra.ResetEncryptionKeys(); err != nil {
			GinkgoWriter.Printf("recoverServicesToOriginalConfig: reset encryption keys failed (non-fatal): %v\n", err)
		}
	}
	// Remove stale canary rows before restarting. If canaries for rotated keys remain in DB,
	// ValidateCanaries on startup will crash-loop (it cannot decrypt them with the restored config).
	deleteStaleCanaries(defaultKeyID)
	if originalServiceConfig != "" {
		return setEncryptionConfigOnAllServices(originalServiceConfig)
	}
	return resetEncryptionConfigToDefault()
}

// restartServicesAndWait restarts the API, worker, and periodic services and waits for readiness.
// Uses LONG_TIMEOUT (10m) because OCP pod scheduling and startup can exceed DURATION_TIMEOUT (5m).
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
		if err := p.Lifecycle.WaitForReady(svc, testutil.LONG_TIMEOUT); err != nil {
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
