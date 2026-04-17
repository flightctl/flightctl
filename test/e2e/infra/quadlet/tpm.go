package quadlet

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/flightctl/flightctl/test/e2e/infra"
	"github.com/sirupsen/logrus"
	"sigs.k8s.io/yaml"
)

const (
	tpmCACertsDir         = "tpm-cas"
	deploymentRolloutWait = 2 * time.Minute
)

// TPMProvider implements infra.TPMProvider for Quadlet environments.
type TPMProvider struct {
	infraP    *InfraProvider
	lifecycle *ServiceLifecycleProvider
}

// NewTPMProvider creates a new Quadlet TPMProvider.
func NewTPMProvider(infraP *InfraProvider, lifecycle *ServiceLifecycleProvider) *TPMProvider {
	return &TPMProvider{
		infraP:    infraP,
		lifecycle: lifecycle,
	}
}

// InjectCerts configures TPM CA certificates for the API server.
// For Quadlet: writes certs to /etc/flightctl/tpm-cas/, updates API config, restarts service.
// It skips the service restart if all certs and config are already in place.
func (p *TPMProvider) InjectCerts(ctx context.Context, certs map[string][]byte) error {
	certsDir := filepath.Join(p.infraP.configDir, tpmCACertsDir)

	if err := p.ensureCertsDirectory(certsDir); err != nil {
		return fmt.Errorf("failed to create TPM certs directory: %w", err)
	}

	certsChanged := false
	certPaths := make([]string, 0, len(certs))
	for name, certData := range certs {
		certPath := filepath.Join(certsDir, name)
		changed, err := p.writeCertFileIfChanged(certPath, certData)
		if err != nil {
			return fmt.Errorf("failed to write cert %s: %w", name, err)
		}
		if changed {
			certsChanged = true
		}
		certPaths = append(certPaths, certPath)
	}
	sort.Strings(certPaths)
	logrus.Infof("Wrote %d TPM CA certs to %s (changed=%t)", len(certs), certsDir, certsChanged)

	configChanged, err := p.updateAPIConfigTPMPaths(certPaths)
	if err != nil {
		return fmt.Errorf("failed to update API config with TPM CA paths: %w", err)
	}

	if !certsChanged && !configChanged {
		logrus.Info("TPM certs already configured, skipping service restart")
		return nil
	}

	if err := p.lifecycle.Restart(infra.ServiceAPI); err != nil {
		return fmt.Errorf("failed to restart API service: %w", err)
	}

	if err := p.lifecycle.WaitForReady(infra.ServiceAPI, deploymentRolloutWait); err != nil {
		return fmt.Errorf("failed waiting for API service: %w", err)
	}

	logrus.Info("API service restarted with TPM CA certs")
	return nil
}

func (p *TPMProvider) ensureCertsDirectory(dir string) error {
	_, err := p.infraP.runCommand("mkdir", "-p", dir)
	return err
}

func (p *TPMProvider) writeCertFileIfChanged(path string, data []byte) (bool, error) {
	existing, err := p.infraP.runCommand("cat", path)
	if err == nil && bytes.Equal([]byte(strings.TrimSpace(existing)), bytes.TrimSpace(data)) {
		return false, nil
	}

	_, err = p.infraP.runCommandWithStdin(
		&byteReader{data: data},
		"sh", "-c", `cat > "$1"`, "_", path,
	)
	if err != nil {
		return false, err
	}
	return true, nil
}

func (p *TPMProvider) updateAPIConfigTPMPaths(certPaths []string) (bool, error) {
	configContent, err := p.infraP.GetServiceConfig(infra.ServiceAPI)
	if err != nil {
		return false, fmt.Errorf("failed to get API config: %w", err)
	}

	var configMap map[string]any
	if err := yaml.Unmarshal([]byte(configContent), &configMap); err != nil {
		return false, fmt.Errorf("failed to parse API config: %w", err)
	}

	service, ok := configMap["service"].(map[string]any)
	if !ok {
		service = make(map[string]any)
		configMap["service"] = service
	}

	if existingPaths, ok := service["tpmCAPaths"].([]any); ok {
		existing := make([]string, 0, len(existingPaths))
		for _, p := range existingPaths {
			if s, ok := p.(string); ok {
				existing = append(existing, s)
			}
		}
		sort.Strings(existing)
		if slices.Equal(existing, certPaths) {
			logrus.Info("TPM CA paths in API config already up to date")
			return false, nil
		}
	}

	service["tpmCAPaths"] = certPaths

	updatedYAML, err := yaml.Marshal(configMap)
	if err != nil {
		return false, fmt.Errorf("failed to marshal updated API config: %w", err)
	}

	if err := p.infraP.SetServiceConfig(infra.ServiceAPI, "config.yaml", string(updatedYAML)); err != nil {
		return false, fmt.Errorf("failed to write API config: %w", err)
	}

	logrus.Infof("Updated API config with %d TPM CA paths", len(certPaths))
	return true, nil
}

// CleanupCerts removes TPM CA certificates from the API server configuration.
func (p *TPMProvider) CleanupCerts(ctx context.Context) error {
	certsDir := filepath.Join(p.infraP.configDir, tpmCACertsDir)

	if err := p.removeAPIConfigTPMPaths(); err != nil {
		logrus.Warnf("Failed to remove TPM paths from API config: %v", err)
	}

	if err := p.removeCertsDirectory(certsDir); err != nil {
		logrus.Warnf("Failed to remove TPM certs directory: %v", err)
	}

	if err := p.lifecycle.Restart(infra.ServiceAPI); err != nil {
		return fmt.Errorf("failed to restart API service: %w", err)
	}

	if err := p.lifecycle.WaitForReady(infra.ServiceAPI, deploymentRolloutWait); err != nil {
		return fmt.Errorf("failed waiting for API service: %w", err)
	}

	logrus.Info("API service restarted after TPM cert cleanup")
	return nil
}

func (p *TPMProvider) removeCertsDirectory(dir string) error {
	_, err := p.infraP.runCommand("rm", "-rf", dir)
	return err
}

func (p *TPMProvider) removeAPIConfigTPMPaths() error {
	configContent, err := p.infraP.GetServiceConfig(infra.ServiceAPI)
	if err != nil {
		return fmt.Errorf("failed to get API config: %w", err)
	}

	var configMap map[string]any
	if err := yaml.Unmarshal([]byte(configContent), &configMap); err != nil {
		return fmt.Errorf("failed to parse API config: %w", err)
	}

	service, ok := configMap["service"].(map[string]any)
	if !ok {
		return nil
	}

	delete(service, "tpmCAPaths")
	configMap["service"] = service

	updatedYAML, err := yaml.Marshal(configMap)
	if err != nil {
		return fmt.Errorf("failed to marshal updated API config: %w", err)
	}

	if err := p.infraP.SetServiceConfig(infra.ServiceAPI, "config.yaml", string(updatedYAML)); err != nil {
		return fmt.Errorf("failed to write API config: %w", err)
	}

	logrus.Info("Removed tpmCAPaths from API config")
	return nil
}

type byteReader struct {
	data []byte
	pos  int
}

func (r *byteReader) Read(p []byte) (n int, err error) {
	if r.pos >= len(r.data) {
		return 0, io.EOF
	}
	n = copy(p, r.data[r.pos:])
	r.pos += n
	return n, nil
}
