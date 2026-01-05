package restore

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/flightctl/flightctl/internal/config/common"
	"sigs.k8s.io/yaml"
)

// Config holds the configuration for the flightctl-restore service.
type Config struct {
	Database *common.DatabaseConfig `json:"database,omitempty"`
	Service  *ServiceConfig         `json:"service,omitempty"`
	KV       *common.KVConfig       `json:"kv,omitempty"`
	Tracing  *common.TracingConfig  `json:"tracing,omitempty"`
}

// ServiceConfig holds restore service-specific configuration.
type ServiceConfig struct {
	LogLevel string `json:"logLevel,omitempty"`
}

// NewDefault returns a default restore configuration.
func NewDefault() *Config {
	return &Config{
		Database: common.NewDefaultDatabase(),
		Service: &ServiceConfig{
			LogLevel: "info",
		},
		KV: common.NewDefaultKV(),
	}
}

// ConfigFile returns the default config file path.
func ConfigFile() string {
	return common.ConfigFile()
}

// Load loads the restore configuration from a file.
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

// DatabaseConfig returns the database configuration.
func (c *Config) DatabaseConfig() *common.DatabaseConfig {
	return c.Database
}

// KVConfig returns the KV configuration.
func (c *Config) KVConfig() *common.KVConfig {
	return c.KV
}
