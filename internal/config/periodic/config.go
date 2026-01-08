package periodic

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/flightctl/flightctl/internal/config/common"
	"github.com/flightctl/flightctl/internal/util"
	"sigs.k8s.io/yaml"
)

// Config holds the configuration for the flightctl-periodic service.
type Config struct {
	Database *common.DatabaseConfig `json:"database,omitempty"`
	Service  *ServiceConfig         `json:"service,omitempty"`
	KV       *common.KVConfig       `json:"kv,omitempty"`
	Tracing  *common.TracingConfig  `json:"tracing,omitempty"`
	Periodic *PeriodicConfig        `json:"periodic,omitempty"`
	GitOps   *GitOpsConfig          `json:"gitOps,omitempty"`
}

// ServiceConfig holds periodic service-specific configuration.
type ServiceConfig struct {
	LogLevel             string        `json:"logLevel,omitempty"`
	EventRetentionPeriod util.Duration `json:"eventRetentionPeriod,omitempty"`
	RenderedWaitTimeout  util.Duration `json:"renderedWaitTimeout,omitempty"`
}

// PeriodicConfig holds periodic task configuration.
type PeriodicConfig struct {
	Consumers int `json:"consumers,omitempty"`
}

// GitOpsConfig holds GitOps-specific configuration.
type GitOpsConfig struct {
	// IgnoreResourceUpdates lists JSON pointer paths that should be ignored
	// when comparing desired vs. live resources during GitOps sync.
	IgnoreResourceUpdates []string `json:"ignoreResourceUpdates,omitempty"`
}

// NewDefault returns a default periodic configuration.
func NewDefault() *Config {
	return &Config{
		Database: common.NewDefaultDatabase(),
		Service: &ServiceConfig{
			LogLevel:             "info",
			EventRetentionPeriod: util.Duration(7 * 24 * time.Hour), // 1 week
			RenderedWaitTimeout:  util.Duration(2 * time.Minute),
		},
		KV: common.NewDefaultKV(),
		GitOps: &GitOpsConfig{
			IgnoreResourceUpdates: []string{
				"/metadata/resourceVersion",
			},
		},
	}
}

// ConfigFile returns the default config file path.
func ConfigFile() string {
	return common.ConfigFile()
}

// Load loads the periodic configuration from a file.
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
	allowedGitOpsIgnoreResourceUpdates := map[string]struct{}{
		"/metadata/resourceVersion": {},
	}

	if c.GitOps != nil {
		for _, path := range c.GitOps.IgnoreResourceUpdates {
			if _, ok := allowedGitOpsIgnoreResourceUpdates[path]; !ok {
				return fmt.Errorf("invalid ignoreResourceUpdates value: %s", path)
			}
		}
	}
	return nil
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

// GitOpsIgnoreResourceUpdates returns the list of paths to ignore during GitOps sync.
func (c *Config) GitOpsIgnoreResourceUpdates() []string {
	if c.GitOps == nil {
		return nil
	}
	return c.GitOps.IgnoreResourceUpdates
}

// EventRetentionPeriod returns the event retention period.
func (c *Config) EventRetentionPeriod() util.Duration {
	if c.Service != nil {
		return c.Service.EventRetentionPeriod
	}
	return util.Duration(7 * 24 * time.Hour) // 1 week default
}

// RenderedWaitTimeout returns the rendered wait timeout.
func (c *Config) RenderedWaitTimeout() util.Duration {
	if c.Service != nil {
		return c.Service.RenderedWaitTimeout
	}
	return util.Duration(2 * time.Minute) // 2 minutes default
}

// Consumers returns the number of consumers.
func (c *Config) Consumers() int {
	if c.Periodic != nil {
		return c.Periodic.Consumers
	}
	return 0
}
