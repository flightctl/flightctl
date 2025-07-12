package config

import (
	"github.com/flightctl/flightctl/internal/agent/config"
	"github.com/flightctl/flightctl/internal/agent/device/certmanager/common"
)

type AgentConfigProvider struct {
	filePath string
}

func NewAgentConfigProvider(configFile string) *AgentConfigProvider {
	return &AgentConfigProvider{
		filePath: configFile,
	}
}

func (p *AgentConfigProvider) Name() string {
	return "agent"
}

func (p *AgentConfigProvider) GetCertificateConfigs() ([]common.CertificateConfig, error) {
	cfg, err := config.Load(p.filePath)
	if err != nil {
		return nil, err
	}
	return cfg.Certificates, nil
}
