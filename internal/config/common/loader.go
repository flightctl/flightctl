package common

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/flightctl/flightctl/internal/util"
	"sigs.k8s.io/yaml"
)

const appName = "flightctl"

// ConfigDir returns the default config directory path.
func ConfigDir() string {
	return filepath.Join(util.MustString(os.UserHomeDir), "."+appName)
}

// ConfigFile returns the default config file path.
func ConfigFile() string {
	return filepath.Join(ConfigDir(), "config.yaml")
}

// ClientConfigFile returns the default client config file path.
func ClientConfigFile() string {
	return filepath.Join(ConfigDir(), "client.yaml")
}

// CertificateDir returns the default certificate directory path.
func CertificateDir() string {
	return filepath.Join(ConfigDir(), "certs")
}

// LoadConfig loads a config from a file, applying it over defaults.
func LoadConfig[T any](cfgFile string, defaults *T) (*T, error) {
	contents, err := os.ReadFile(cfgFile)
	if err != nil {
		return nil, fmt.Errorf("reading config file: %w", err)
	}
	if err := yaml.Unmarshal(contents, defaults); err != nil {
		return nil, fmt.Errorf("decoding config: %w", err)
	}
	return defaults, nil
}

// SaveConfig saves a config to a file.
func SaveConfig[T any](cfg *T, cfgFile string) error {
	contents, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("encoding config: %w", err)
	}
	if err := os.WriteFile(cfgFile, contents, 0600); err != nil {
		return fmt.Errorf("writing config file: %w", err)
	}
	return nil
}

// EnsureConfigDir creates the config directory if it doesn't exist.
func EnsureConfigDir(cfgFile string) error {
	dir := filepath.Dir(cfgFile)
	if err := os.MkdirAll(dir, os.FileMode(0755)); err != nil {
		return fmt.Errorf("creating directory for config file: %w", err)
	}
	return nil
}
