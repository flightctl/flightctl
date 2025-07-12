package config

import (
	"github.com/flightctl/flightctl/internal/agent/device/certmanager/common"
)

type StaticConfigProvider struct {
	Certificates []common.CertificateConfig
}

func NewStaticConfigProvider(cc []common.CertificateConfig) *StaticConfigProvider {
	return &StaticConfigProvider{
		Certificates: cc,
	}
}

func (p *StaticConfigProvider) Name() string {
	return "static"
}

func (p *StaticConfigProvider) GetCertificateConfigs() ([]common.CertificateConfig, error) {
	return p.Certificates, nil
}
