package common

import (
	"os"

	api "github.com/flightctl/flightctl/api/v1beta1"
)

// KVConfig holds key-value store (Redis) configuration.
type KVConfig struct {
	Hostname string           `json:"hostname,omitempty"`
	Port     uint             `json:"port,omitempty"`
	Password api.SecureString `json:"password,omitempty"`
}

// NewDefaultKV returns a default KV store configuration.
func NewDefaultKV() *KVConfig {
	return &KVConfig{
		Hostname: "localhost",
		Port:     6379,
		Password: "adminpass",
	}
}

// ApplyDefaults applies default values and environment variable overrides.
func (c *KVConfig) ApplyDefaults() {
	if c == nil {
		return
	}
	c.ApplyEnvOverrides()
}

// ApplyEnvOverrides applies environment variable overrides to the KV config.
func (c *KVConfig) ApplyEnvOverrides() {
	if c == nil {
		return
	}
	if kvPass := os.Getenv("KV_PASSWORD"); kvPass != "" {
		c.Password = api.SecureString(kvPass)
	}
}
