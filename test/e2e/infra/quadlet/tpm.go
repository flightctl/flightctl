package quadlet

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
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
func (p *TPMProvider) InjectCerts(ctx context.Context, certs map[string][]byte) error {
	certsDir := filepath.Join(p.infraP.configDir, tpmCACertsDir)

	if err := p.ensureCertsDirectory(certsDir); err != nil {
		return fmt.Errorf("failed to create TPM certs directory: %w", err)
	}

	certPaths := make([]string, 0, len(certs))
	for name, certData := range certs {
		certPath := filepath.Join(certsDir, name)
		if err := p.writeCertFile(certPath, certData); err != nil {
			return fmt.Errorf("failed to write cert %s: %w", name, err)
		}
		certPaths = append(certPaths, certPath)
	}
	logrus.Infof("Wrote %d TPM CA certs to %s", len(certs), certsDir)

	if err := p.updateAPIConfigTPMPaths(certPaths); err != nil {
		return fmt.Errorf("failed to update API config with TPM CA paths: %w", err)
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

func (p *TPMProvider) writeCertFile(path string, data []byte) error {
	_, err := p.infraP.runCommandWithStdin(
		&byteReader{data: data},
		"sh", "-c", `cat > "$1"`, "_", path,
	)
	return err
}

func (p *TPMProvider) updateAPIConfigTPMPaths(certPaths []string) error {
	configContent, err := p.infraP.GetServiceConfig(infra.ServiceAPI)
	if err != nil {
		return fmt.Errorf("failed to get API config: %w", err)
	}

	var configMap map[string]interface{}
	if err := yaml.Unmarshal([]byte(configContent), &configMap); err != nil {
		return fmt.Errorf("failed to parse API config: %w", err)
	}

	service, ok := configMap["service"].(map[string]interface{})
	if !ok {
		service = make(map[string]interface{})
		configMap["service"] = service
	}
	service["tpmCAPaths"] = certPaths

	updatedYAML, err := yaml.Marshal(configMap)
	if err != nil {
		return fmt.Errorf("failed to marshal updated API config: %w", err)
	}

	if err := p.infraP.SetServiceConfig(infra.ServiceAPI, "config.yaml", string(updatedYAML)); err != nil {
		return fmt.Errorf("failed to write API config: %w", err)
	}

	logrus.Infof("Updated API config with %d TPM CA paths", len(certPaths))
	return nil
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
