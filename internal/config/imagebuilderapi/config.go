package imagebuilderapi

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/flightctl/flightctl/internal/config/common"
	"github.com/flightctl/flightctl/internal/util"
	"sigs.k8s.io/yaml"
)

// Config holds the configuration for the flightctl-imagebuilder-api service.
type Config struct {
	Database            *common.DatabaseConfig `json:"database,omitempty"`
	ImageBuilderService *ServiceConfig         `json:"imageBuilderService,omitempty"`
	KV                  *common.KVConfig       `json:"kv,omitempty"`
	Auth                *common.AuthConfig     `json:"auth,omitempty"`
	Tracing             *common.TracingConfig  `json:"tracing,omitempty"`
}

// ServiceConfig holds imagebuilder API service-specific configuration.
type ServiceConfig struct {
	Address               string                     `json:"address,omitempty"`
	LogLevel              string                     `json:"logLevel,omitempty"`
	TLSCertFile           string                     `json:"tlsCertFile,omitempty"`
	TLSKeyFile            string                     `json:"tlsKeyFile,omitempty"`
	InsecureSkipTlsVerify bool                       `json:"insecureSkipTlsVerify,omitempty"`
	HttpReadTimeout       util.Duration              `json:"httpReadTimeout,omitempty"`
	HttpReadHeaderTimeout util.Duration              `json:"httpReadHeaderTimeout,omitempty"`
	HttpWriteTimeout      util.Duration              `json:"httpWriteTimeout,omitempty"`
	HttpIdleTimeout       util.Duration              `json:"httpIdleTimeout,omitempty"`
	HttpMaxNumHeaders     int                        `json:"httpMaxNumHeaders,omitempty"`
	HttpMaxUrlLength      int                        `json:"httpMaxUrlLength,omitempty"`
	HttpMaxRequestSize    int                        `json:"httpMaxRequestSize,omitempty"`
	RateLimit             *common.RateLimitConfig    `json:"rateLimit,omitempty"`
	HealthChecks          *common.HealthChecksConfig `json:"healthChecks,omitempty"`
}

// NewDefault returns a default imagebuilder API configuration.
func NewDefault() *Config {
	return &Config{
		Database:            common.NewDefaultDatabase(),
		ImageBuilderService: NewDefaultService(),
		KV:                  common.NewDefaultKV(),
		Auth:                common.NewDefaultAuth(),
	}
}

// NewDefaultService returns a default imagebuilder service configuration.
func NewDefaultService() *ServiceConfig {
	return &ServiceConfig{
		Address:               ":8445",
		LogLevel:              "info",
		HttpReadTimeout:       util.Duration(5 * time.Minute),
		HttpReadHeaderTimeout: util.Duration(5 * time.Minute),
		HttpWriteTimeout:      util.Duration(5 * time.Minute),
		HttpIdleTimeout:       util.Duration(5 * time.Minute),
		HttpMaxNumHeaders:     32,
		HttpMaxUrlLength:      2000,
		HttpMaxRequestSize:    50 * 1024 * 1024, // 50MB
		HealthChecks: &common.HealthChecksConfig{
			Enabled:          true,
			ReadinessPath:    "/readyz",
			LivenessPath:     "/healthz",
			ReadinessTimeout: util.Duration(2 * time.Second),
		},
	}
}

// ConfigFile returns the default config file path.
func ConfigFile() string {
	return common.ConfigFile()
}

// Load loads the imagebuilder API configuration from a file.
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

// Validate validates the configuration.
func (c *Config) Validate() error {
	if c.ImageBuilderService != nil && c.ImageBuilderService.HealthChecks != nil {
		if err := c.ImageBuilderService.HealthChecks.Validate("imageBuilderService.healthChecks."); err != nil {
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

// LogLevel returns the configured log level.
func (c *Config) LogLevel() string {
	if c.ImageBuilderService != nil && c.ImageBuilderService.LogLevel != "" {
		return c.ImageBuilderService.LogLevel
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

// DatabaseConfig returns the database configuration.
func (c *Config) DatabaseConfig() *common.DatabaseConfig {
	return c.Database
}

// KVConfig returns the KV configuration.
func (c *Config) KVConfig() *common.KVConfig {
	return c.KV
}
