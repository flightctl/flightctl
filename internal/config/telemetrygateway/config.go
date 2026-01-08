package telemetrygateway

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/flightctl/flightctl/internal/config/ca"
	"github.com/flightctl/flightctl/internal/config/common"
	"sigs.k8s.io/yaml"
)

// Config holds the configuration for the flightctl-telemetry-gateway service.
type Config struct {
	Service          *ServiceConfig          `json:"service,omitempty"`
	TelemetryGateway *TelemetryGatewayConfig `json:"telemetrygateway,omitempty"`
	Auth             *common.AuthConfig      `json:"auth,omitempty"`
	Tracing          *common.TracingConfig   `json:"tracing,omitempty"`
	CA               *ca.Config              `json:"ca,omitempty"`
}

// ServiceConfig holds telemetry gateway service-specific configuration.
type ServiceConfig struct {
	LogLevel string `json:"logLevel,omitempty"`
}

// TelemetryGatewayConfig holds telemetry gateway specific settings.
type TelemetryGatewayConfig struct {
	LogLevel string    `json:"logLevel,omitempty"`
	TLS      TLSConfig `json:"tls,omitempty"`
	Listen   Listen    `json:"listen,omitempty"`
	Export   *Export   `json:"export,omitempty"`
	Forward  *Forward  `json:"forward,omitempty"`
}

// TLSConfig holds TLS settings for the telemetry gateway.
type TLSConfig struct {
	CertFile string `json:"certFile,omitempty"`
	KeyFile  string `json:"keyFile,omitempty"`
	CACert   string `json:"caCert,omitempty"`
}

// Listen holds listen settings for the telemetry gateway.
type Listen struct {
	Device string `json:"device,omitempty"`
}

// Export holds export settings for the telemetry gateway.
type Export struct {
	Prometheus string `json:"prometheus,omitempty"`
}

// Forward holds forwarding settings for the telemetry gateway.
type Forward struct {
	Endpoint string      `json:"endpoint,omitempty"`
	TLS      *ForwardTLS `json:"tls,omitempty"`
}

// ForwardTLS holds TLS settings for forwarding.
type ForwardTLS struct {
	InsecureSkipTlsVerify bool   `json:"insecureSkipTlsVerify,omitempty"`
	CAFile                string `json:"caFile,omitempty"`
	CertFile              string `json:"certFile,omitempty"`
	KeyFile               string `json:"keyFile,omitempty"`
}

// NewDefault returns a default telemetry gateway configuration.
func NewDefault() *Config {
	certDir := common.CertificateDir()
	return &Config{
		Service: &ServiceConfig{
			LogLevel: "info",
		},
		TelemetryGateway: &TelemetryGatewayConfig{
			LogLevel: "info",
			TLS: TLSConfig{
				CertFile: "/etc/telemetry-gateway/certs/server.crt",
				KeyFile:  "/etc/telemetry-gateway/certs/server.key",
				CACert:   "/etc/telemetry-gateway/certs/ca.crt",
			},
			Listen: Listen{Device: "0.0.0.0:4317"},
		},
		Auth: common.NewDefaultAuth(),
		CA:   ca.NewDefault(certDir),
	}
}

// ConfigFile returns the default config file path.
func ConfigFile() string {
	return common.ConfigFile()
}

// Load loads the telemetry gateway configuration from a file.
func Load(cfgFile string) (*Config, error) {
	contents, err := os.ReadFile(cfgFile)
	if err != nil {
		return nil, fmt.Errorf("reading config file: %w", err)
	}

	c := NewDefault()
	if err := yaml.Unmarshal(contents, c); err != nil {
		return nil, fmt.Errorf("decoding config: %w", err)
	}

	return c, nil
}

// LoadOrGenerate loads the config or generates a default one if not found.
func LoadOrGenerate(cfgFile string) (*Config, error) {
	if _, err := os.Stat(cfgFile); os.IsNotExist(err) {
		if err := common.EnsureConfigDir(cfgFile); err != nil {
			return nil, err
		}
		if err := Save(NewDefault(), cfgFile); err != nil {
			return nil, err
		}
	}
	return Load(cfgFile)
}

// Save saves the configuration to a file.
func Save(cfg *Config, cfgFile string) error {
	return common.SaveConfig(cfg, cfgFile)
}

// LogLevel returns the configured log level.
func (c *Config) LogLevel() string {
	if c.TelemetryGateway != nil && c.TelemetryGateway.LogLevel != "" {
		return c.TelemetryGateway.LogLevel
	}
	if c.Service != nil && c.Service.LogLevel != "" {
		return c.Service.LogLevel
	}
	return "info"
}

// String returns a JSON representation of the config.
func (c *Config) String() string {
	contents, err := json.Marshal(c)
	if err != nil {
		return "<error>"
	}
	return string(contents)
}

// TracingConfig returns the tracing configuration.
func (c *Config) TracingConfig() *common.TracingConfig {
	return c.Tracing
}

// Gateway returns the telemetry gateway configuration.
func (c *Config) Gateway() *TelemetryGatewayConfig {
	return c.TelemetryGateway
}

// AuthConfig returns the auth configuration.
func (c *Config) AuthConfig() *common.AuthConfig {
	return c.Auth
}

// CAConfig returns the CA configuration.
func (c *Config) CAConfig() *ca.Config {
	return c.CA
}
