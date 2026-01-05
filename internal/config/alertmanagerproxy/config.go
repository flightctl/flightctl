package alertmanagerproxy

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/flightctl/flightctl/internal/config/common"
	"github.com/flightctl/flightctl/internal/crypto"
	"github.com/sirupsen/logrus"
	"sigs.k8s.io/yaml"
)

var (
	ErrCheckingServerCerts = errors.New("failed to check if server certificate and key can be read")
	ErrServerCertsNotFound = errors.New("server certificate and key files are missing or unreadable")
	ErrInvalidServerCerts  = errors.New("failed to parse or load server certificate and key")
)

// Config holds the configuration for the flightctl-alertmanager-proxy service.
type Config struct {
	Database *common.DatabaseConfig `json:"database,omitempty"`
	Service  *ServiceConfig         `json:"service,omitempty"`
	KV       *common.KVConfig       `json:"kv,omitempty"`
	Auth     *common.AuthConfig     `json:"auth,omitempty"`
	Tracing  *common.TracingConfig  `json:"tracing,omitempty"`
}

// ServiceConfig holds alertmanager proxy service-specific configuration.
type ServiceConfig struct {
	CertStore          string                     `json:"cert,omitempty"`
	SrvCertFile        string                     `json:"srvCertificateFile,omitempty"`
	SrvKeyFile         string                     `json:"srvKeyFile,omitempty"`
	ServerCertName     string                     `json:"serverCertName,omitempty"`
	LogLevel           string                     `json:"logLevel,omitempty"`
	HttpMaxNumHeaders  int                        `json:"httpMaxNumHeaders,omitempty"`
	HttpMaxUrlLength   int                        `json:"httpMaxUrlLength,omitempty"`
	HttpMaxRequestSize int                        `json:"httpMaxRequestSize,omitempty"`
	RateLimit          *common.RateLimitConfig    `json:"rateLimit,omitempty"`
	HealthChecks       *common.HealthChecksConfig `json:"healthChecks,omitempty"`
}

// NewDefault returns a default alertmanager proxy configuration.
func NewDefault() *Config {
	certDir := common.CertificateDir()
	return &Config{
		Database: common.NewDefaultDatabase(),
		Service: &ServiceConfig{
			CertStore:          certDir,
			ServerCertName:     "server",
			LogLevel:           "info",
			HttpMaxNumHeaders:  32,
			HttpMaxUrlLength:   2000,
			HttpMaxRequestSize: 50 * 1024 * 1024, // 50MB
		},
		KV:   common.NewDefaultKV(),
		Auth: common.NewDefaultAuth(),
	}
}

// ConfigFile returns the default config file path.
func ConfigFile() string {
	return common.ConfigFile()
}

// Load loads the alertmanager proxy configuration from a file.
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

func (c *Config) applyEnvOverrides() {
	if c.Database != nil {
		c.Database.ApplyEnvOverrides()
	}
	if c.KV != nil {
		c.KV.ApplyEnvOverrides()
	}
}

// LogLevel returns the configured log level.
func (c *Config) LogLevel() string {
	if c.Service != nil && c.Service.LogLevel != "" {
		return c.Service.LogLevel
	}
	return "info"
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

// TracingConfig returns the tracing configuration.
func (c *Config) TracingConfig() *common.TracingConfig {
	return c.Tracing
}

// AuthConfig returns the auth configuration.
func (c *Config) AuthConfig() *common.AuthConfig {
	return c.Auth
}

// DatabaseConfig returns the database configuration.
func (c *Config) DatabaseConfig() *common.DatabaseConfig {
	return c.Database
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
