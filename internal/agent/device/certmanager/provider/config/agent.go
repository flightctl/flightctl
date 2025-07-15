package config

import (
	"context"

	"github.com/flightctl/flightctl/internal/agent/config"
	"github.com/flightctl/flightctl/internal/agent/device/certmanager/provider"
)

// AgentConfigProvider provides certificate configurations from the agent's main configuration file.
// It extends the FileConfigProvider to read certificate configurations from the agent config
// while maintaining file watching capabilities for dynamic configuration updates.
type AgentConfigProvider struct {
	*FileConfigProvider        // Embedded file provider for change detection
	filePath            string // Path to the agent configuration file
}

// NewAgentConfigProvider creates a new configuration provider that reads certificate configurations
// from the agent's main configuration file. It supports file watching for dynamic updates.
func NewAgentConfigProvider(ctx context.Context, configFile string) *AgentConfigProvider {
	return &AgentConfigProvider{
		FileConfigProvider: NewFileConfigProvider(ctx, configFile),
		filePath:           configFile,
	}
}

// Name returns the unique identifier for this configuration provider.
func (p *AgentConfigProvider) Name() string {
	return "agent-config"
}

// GetCertificateConfigs loads and returns certificate configurations from the agent config file.
func (p *AgentConfigProvider) GetCertificateConfigs() ([]provider.CertificateConfig, error) {
	cfg, err := config.Load(p.filePath)
	if err != nil {
		return nil, err
	}
	return cfg.Certificates, nil
}
