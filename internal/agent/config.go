package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/flightctl/flightctl/internal/util"
	"gopkg.in/yaml.v2"
)

type Config struct {
	Agent *agentConfig `json:"agent,omitempty"`
}

type agentConfig struct {
	Server               string        `json:"server,omitempty"`
	EnrollmentUi         string        `json:"enrollmentUi,omitempty"`
	TpmPath              string        `json:"tpmPath,omitempty"`
	FetchSpecInterval    util.Duration `json:"fetchSpecInterval,omitempty"`
	StatusUpdateInterval util.Duration `json:"statusUpdateInterval,omitempty"`
}

func NewDefault() *Config {
	return &Config{
		&agentConfig{
			Server:               "https://localhost:3333",
			EnrollmentUi:         "",
			TpmPath:              "",
			FetchSpecInterval:    util.Duration(DefaultFetchSpecInterval),
			StatusUpdateInterval: util.Duration(DefaultStatusUpdateInterval),
		},
	}
}

// TODO: dedupe with internal/config/config.go
func NewFromFile(cfgFile string) (*Config, error) {
	cfg, err := Load(cfgFile)
	if err != nil {
		return nil, err
	}
	if err := Validate(cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

func LoadOrGenerate(cfgFile string) (*Config, error) {
	if _, err := os.Stat(cfgFile); os.IsNotExist(err) {
		if err := os.MkdirAll(filepath.Dir(cfgFile), os.FileMode(0755)); err != nil {
			return nil, fmt.Errorf("creating directory for config file: %v", err)
		}
		if err := Save(NewDefault(), cfgFile); err != nil {
			return nil, err
		}
	}
	return NewFromFile(cfgFile)
}

func Load(cfgFile string) (*Config, error) {
	contents, err := os.ReadFile(cfgFile)
	if err != nil {
		return nil, fmt.Errorf("reading config file: %v", err)
	}
	c := &Config{}
	if err := yaml.Unmarshal(contents, c); err != nil {
		return nil, fmt.Errorf("decoding config: %v", err)
	}
	return c, nil
}

func Save(cfg *Config, cfgFile string) error {
	contents, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("encoding config: %v", err)
	}
	if err := os.WriteFile(cfgFile, contents, 0600); err != nil {
		return fmt.Errorf("writing config file: %v", err)
	}
	return nil
}

func Validate(cfg *Config) error {
	return nil
}

func (cfg *Config) String() string {
	contents, err := json.Marshal(cfg)
	if err != nil {
		return "<error>"
	}
	return string(contents)
}
