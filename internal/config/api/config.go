package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	apiv1 "github.com/flightctl/flightctl/api/v1beta1"
	"github.com/flightctl/flightctl/internal/config/ca"
	"github.com/flightctl/flightctl/internal/config/common"
	"github.com/flightctl/flightctl/internal/crypto"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/sirupsen/logrus"
	"sigs.k8s.io/yaml"
)

var (
	ErrCheckingServerCerts = errors.New("failed to check if server certificate and key can be read")
	ErrServerCertsNotFound = errors.New("server certificate and key files are missing or unreadable")
	ErrInvalidServerCerts  = errors.New("failed to parse or load server certificate and key")
)

// Config holds the configuration for the flightctl-api service.
type Config struct {
	Database      *common.DatabaseConfig     `json:"database,omitempty"`
	Service       *ServiceConfig             `json:"service,omitempty"`
	KV            *common.KVConfig           `json:"kv,omitempty"`
	Auth          *common.AuthConfig         `json:"auth,omitempty"`
	Metrics       *common.MetricsConfig      `json:"metrics,omitempty"`
	Tracing       *common.TracingConfig      `json:"tracing,omitempty"`
	CA            *ca.Config                 `json:"ca,omitempty"`
	Organizations *OrganizationsConfig       `json:"organizations,omitempty"`
	Alertmanager  *common.AlertmanagerConfig `json:"alertmanager,omitempty"`
}

// ServiceConfig holds API service-specific configuration.
type ServiceConfig struct {
	Address                string                     `json:"address,omitempty"`
	AgentEndpointAddress   string                     `json:"agentEndpointAddress,omitempty"`
	CertStore              string                     `json:"cert,omitempty"`
	BaseUrl                string                     `json:"baseUrl,omitempty"`
	BaseAgentEndpointUrl   string                     `json:"baseAgentEndpointUrl,omitempty"`
	BaseUIUrl              string                     `json:"baseUIUrl,omitempty"`
	SrvCertFile            string                     `json:"srvCertificateFile,omitempty"`
	SrvKeyFile             string                     `json:"srvKeyFile,omitempty"`
	ServerCertName         string                     `json:"serverCertName,omitempty"`
	ServerCertValidityDays int                        `json:"serverCertValidityDays,omitempty"`
	AltNames               []string                   `json:"altNames,omitempty"`
	LogLevel               string                     `json:"logLevel,omitempty"`
	HttpReadTimeout        util.Duration              `json:"httpReadTimeout,omitempty"`
	HttpReadHeaderTimeout  util.Duration              `json:"httpReadHeaderTimeout,omitempty"`
	HttpWriteTimeout       util.Duration              `json:"httpWriteTimeout,omitempty"`
	HttpIdleTimeout        util.Duration              `json:"httpIdleTimeout,omitempty"`
	HttpMaxNumHeaders      int                        `json:"httpMaxNumHeaders,omitempty"`
	HttpMaxHeaderBytes     int                        `json:"httpMaxHeaderBytes,omitempty"`
	HttpMaxUrlLength       int                        `json:"httpMaxUrlLength,omitempty"`
	HttpMaxRequestSize     int                        `json:"httpMaxRequestSize,omitempty"`
	EventRetentionPeriod   util.Duration              `json:"eventRetentionPeriod,omitempty"`
	AlertPollingInterval   util.Duration              `json:"alertPollingInterval,omitempty"`
	RenderedWaitTimeout    util.Duration              `json:"renderedWaitTimeout,omitempty"`
	RateLimit              *common.RateLimitConfig    `json:"rateLimit,omitempty"`
	TPMCAPaths             []string                   `json:"tpmCAPaths,omitempty"`
	HealthChecks           *common.HealthChecksConfig `json:"healthChecks,omitempty"`
}

// OrganizationsConfig holds organizations feature configuration.
type OrganizationsConfig struct {
	Enabled bool `json:"enabled,omitempty"`
}

// ConfigOption is a functional option for configuring the API config.
type ConfigOption func(*Config)

// WithTracingEnabled enables tracing.
func WithTracingEnabled() ConfigOption {
	return func(c *Config) {
		c.Tracing = &common.TracingConfig{
			Enabled: true,
		}
	}
}

// WithOIDCAuth configures OIDC authentication.
func WithOIDCAuth(issuer, clientId string, enabled bool) ConfigOption {
	return func(c *Config) {
		if c.Auth == nil {
			c.Auth = &common.AuthConfig{
				DynamicProviderCacheTTL: util.Duration(5 * time.Second),
			}
		}
		c.Auth.OIDC = &apiv1.OIDCProviderSpec{
			Issuer:       issuer,
			ClientId:     clientId,
			Enabled:      &enabled,
			ProviderType: apiv1.Oidc,
		}
	}
}

// WithOAuth2Auth configures OAuth2 authentication.
func WithOAuth2Auth(authorizationUrl, tokenUrl, userinfoUrl, issuer, clientId string, enabled bool) ConfigOption {
	return func(c *Config) {
		if c.Auth == nil {
			c.Auth = &common.AuthConfig{
				DynamicProviderCacheTTL: util.Duration(5 * time.Second),
			}
		}
		c.Auth.OAuth2 = &apiv1.OAuth2ProviderSpec{
			AuthorizationUrl: authorizationUrl,
			TokenUrl:         tokenUrl,
			UserinfoUrl:      userinfoUrl,
			Issuer:           &issuer,
			ClientId:         clientId,
			Enabled:          &enabled,
			ProviderType:     apiv1.Oauth2,
		}
	}
}

// WithK8sAuth configures Kubernetes authentication.
func WithK8sAuth(apiUrl, rbacNs string) ConfigOption {
	return func(c *Config) {
		if c.Auth == nil {
			c.Auth = &common.AuthConfig{
				DynamicProviderCacheTTL: util.Duration(5 * time.Second),
			}
		}
		enabled := true
		c.Auth.K8s = &apiv1.K8sProviderSpec{
			ApiUrl:       apiUrl,
			RbacNs:       &rbacNs,
			ProviderType: apiv1.K8s,
			Enabled:      &enabled,
		}
	}
}

// WithAAPAuth configures AAP authentication.
func WithAAPAuth(apiUrl, externalApiUrl string) ConfigOption {
	return func(c *Config) {
		if c.Auth == nil {
			c.Auth = &common.AuthConfig{
				DynamicProviderCacheTTL: util.Duration(5 * time.Second),
			}
		}
		enabled := true
		c.Auth.AAP = &apiv1.AapProviderSpec{
			ApiUrl:       apiUrl,
			ProviderType: apiv1.Aap,
			Enabled:      &enabled,
		}
	}
}

// NewDefault returns a default API configuration.
func NewDefault(opts ...ConfigOption) *Config {
	certDir := common.CertificateDir()
	c := &Config{
		Database: common.NewDefaultDatabase(),
		Service: &ServiceConfig{
			Address:                ":3443",
			AgentEndpointAddress:   ":7443",
			CertStore:              certDir,
			BaseUrl:                "https://localhost:3443",
			BaseAgentEndpointUrl:   "https://localhost:7443",
			ServerCertName:         "server",
			ServerCertValidityDays: 730,
			LogLevel:               "info",
			HttpReadTimeout:        util.Duration(5 * time.Minute),
			HttpReadHeaderTimeout:  util.Duration(5 * time.Minute),
			HttpWriteTimeout:       util.Duration(5 * time.Minute),
			HttpIdleTimeout:        util.Duration(5 * time.Minute),
			HttpMaxNumHeaders:      32,
			HttpMaxHeaderBytes:     32 * 1024, // 32KB
			HttpMaxUrlLength:       2000,
			HttpMaxRequestSize:     50 * 1024 * 1024,                  // 50MB
			EventRetentionPeriod:   util.Duration(7 * 24 * time.Hour), // 1 week
			AlertPollingInterval:   util.Duration(1 * time.Minute),
			RenderedWaitTimeout:    util.Duration(2 * time.Minute),
			HealthChecks:           common.NewDefaultHealthChecks(),
		},
		KV:           common.NewDefaultKV(),
		Auth:         common.NewDefaultAuth(),
		Metrics:      common.NewDefaultMetrics(),
		CA:           ca.NewDefault(certDir),
		Alertmanager: common.NewDefaultAlertmanager(),
	}

	for _, opt := range opts {
		opt(c)
	}

	return c
}

// ConfigFile returns the default config file path.
func ConfigFile() string {
	return common.ConfigFile()
}

// Load loads the API configuration from a file.
func Load(cfgFile string) (*Config, error) {
	contents, err := os.ReadFile(cfgFile)
	if err != nil {
		return nil, fmt.Errorf("reading config file: %w", err)
	}

	c := NewDefault()
	if err := yaml.Unmarshal(contents, c); err != nil {
		return nil, fmt.Errorf("decoding config: %w", err)
	}

	c.applyEnvOverrides()
	if err := c.applyDefaults(); err != nil {
		return nil, fmt.Errorf("applying defaults: %w", err)
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

	cfg, err := Load(cfgFile)
	if err != nil {
		return nil, err
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// Save saves the configuration to a file.
func Save(cfg *Config, cfgFile string) error {
	return common.SaveConfig(cfg, cfgFile)
}

func (c *Config) applyEnvOverrides() {
	if c.Database != nil {
		c.Database.ApplyEnvOverrides()
	}
	if c.KV != nil {
		c.KV.ApplyEnvOverrides()
	}
}

func (c *Config) applyDefaults() error {
	if c.Auth != nil && c.Service != nil {
		if err := c.Auth.ApplyDefaults(c.Service.BaseUrl, c.Service.BaseUIUrl); err != nil {
			return err
		}
	}
	return nil
}

// Validate validates the configuration.
func (c *Config) Validate() error {
	if c.Service != nil && c.Service.HealthChecks != nil {
		if err := c.Service.HealthChecks.Validate(""); err != nil {
			return err
		}
	}
	if c.Auth != nil {
		if err := c.Auth.Validate(); err != nil {
			return err
		}
	}
	return nil
}

// String returns a JSON representation of the config with sensitive fields redacted.
func (c *Config) String() string {
	sanitized := c.sanitizeForLogging()
	contents, err := json.Marshal(sanitized)
	if err != nil {
		return "<error>"
	}
	return string(contents)
}

func (c *Config) sanitizeForLogging() *Config {
	if c == nil {
		return nil
	}

	// Create a shallow copy
	sanitized := *c
	sanitized.Auth = c.Auth.SanitizeForLogging()

	return &sanitized
}

// LogLevel returns the configured log level.
func (c *Config) LogLevel() string {
	if c.Service != nil {
		return c.Service.LogLevel
	}
	return "info"
}

// TracingConfig returns the tracing configuration.
func (c *Config) TracingConfig() *common.TracingConfig {
	return c.Tracing
}

// DatabaseConfig returns the database configuration.
func (c *Config) DatabaseConfig() *common.DatabaseConfig {
	return c.Database
}

// KVConfig returns the KV store configuration.
func (c *Config) KVConfig() *common.KVConfig {
	return c.KV
}

// AuthConfig returns the auth configuration.
func (c *Config) AuthConfig() *common.AuthConfig {
	return c.Auth
}

// MetricsConfig returns the metrics configuration.
func (c *Config) MetricsConfig() *common.MetricsConfig {
	return c.Metrics
}

// CAConfig returns the CA configuration.
func (c *Config) CAConfig() *ca.Config {
	return c.CA
}

// LoadServerCertificates loads the server certificates from the configured paths.
func LoadServerCertificates(cfg *Config, log *logrus.Logger) (*crypto.TLSCertificateConfig, error) {
	var keyFile, certFile string
	if cfg.Service.SrvCertFile != "" || cfg.Service.SrvKeyFile != "" {
		certFile = cfg.Service.SrvCertFile
		keyFile = cfg.Service.SrvKeyFile
	} else {
		certFile = crypto.CertStorePath(cfg.Service.ServerCertName+".crt", cfg.Service.CertStore)
		keyFile = crypto.CertStorePath(cfg.Service.ServerCertName+".key", cfg.Service.CertStore)
	}

	canReadCertAndKey, err := crypto.CanReadCertAndKey(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCheckingServerCerts, err)
	}
	if !canReadCertAndKey {
		return nil, ErrServerCertsNotFound
	}

	serverCerts, err := crypto.GetTLSCertificateConfig(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidServerCerts, err)
	}

	// check for expired certificate
	for _, x509Cert := range serverCerts.Certs {
		expired := time.Now().After(x509Cert.NotAfter)
		log.Printf("checking certificate: subject='%s', issuer='%s', expiry='%v'",
			x509Cert.Subject.CommonName, x509Cert.Issuer.CommonName, x509Cert.NotAfter)

		if expired {
			log.Warnf("server certificate for '%s' issued by '%s' has expired on: %v",
				x509Cert.Subject.CommonName, x509Cert.Issuer.CommonName, x509Cert.NotAfter)
		}
	}

	return serverCerts, nil
}
