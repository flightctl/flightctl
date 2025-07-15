package config

import (
	"context"

	agent_config "github.com/flightctl/flightctl/internal/agent/config"
	"github.com/flightctl/flightctl/internal/agent/device/certmanager/provider"
)

// AgentConfigProvider provides certificate configurations from the agent's main configuration.
// It can load configurations directly from a file, or fall back to an in-memory config object.
// It embeds FileConfigProvider to support file watching and dynamic updates.
type AgentConfigProvider struct {
	*FileConfigProvider                      // Embedded file provider for change detection
	config              *agent_config.Config // Optional in-memory configuration fallback
	filePath            string               // Path to the agent configuration file
}

// NewAgentConfigProvider creates a new AgentConfigProvider that reads certificate configurations
// from the given file path if provided, or uses an in-memory config fallback when no file is present.
// It supports file watching for dynamic updates if a file path is specified.
func NewAgentConfigProvider(ctx context.Context, cfg *agent_config.Config, configFile string) *AgentConfigProvider {
	return &AgentConfigProvider{
		FileConfigProvider: NewFileConfigProvider(ctx, configFile),
		config:             cfg,
		filePath:           configFile,
	}
}

// Name returns the unique identifier for this configuration provider.
func (p *AgentConfigProvider) Name() string {
	return "agent-config"
}

// GetCertificateConfigs loads and returns certificate configurations.
// If a file path is set, it loads and parses the configuration file dynamically.
// If no file path is set but an in-memory config is available, it uses that.
// If neither is available, it returns nil.
func (p *AgentConfigProvider) GetCertificateConfigs() ([]provider.CertificateConfig, error) {
	if p.filePath != "" {
		cfg, err := agent_config.Load(p.filePath)
		if err != nil {
			return nil, err
		}
		return cfg.Certificates, nil
	}

	if p.config != nil {
		return p.config.Certificates, nil
	}

	return nil, nil
}
