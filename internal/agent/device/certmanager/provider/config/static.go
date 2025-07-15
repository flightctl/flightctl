package config

import "github.com/flightctl/flightctl/internal/agent/device/certmanager/provider"

// StaticConfigProvider provides certificate configurations from a static in-memory list.
// This is useful for testing, development, or scenarios where certificate configurations
// are known at compile time and don't need to change dynamically.
type StaticConfigProvider struct {
	Certificates []provider.CertificateConfig // Static list of certificate configurations
}

// NewStaticConfigProvider creates a new static configuration provider with the given
// certificate configurations. The configurations are stored in memory and remain constant.
func NewStaticConfigProvider(cc []provider.CertificateConfig) *StaticConfigProvider {
	return &StaticConfigProvider{
		Certificates: cc,
	}
}

// Name returns the unique identifier for this configuration provider.
// Static providers always use the same name since they don't have variable parameters.
func (p *StaticConfigProvider) Name() string {
	return "static"
}

// GetCertificateConfigs returns the static list of certificate configurations.
// This list never changes and is set during provider creation.
func (p *StaticConfigProvider) GetCertificateConfigs() ([]provider.CertificateConfig, error) {
	return p.Certificates, nil
}
