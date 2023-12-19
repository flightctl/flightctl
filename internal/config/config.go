package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/flightctl/flightctl/internal/agent"
	"github.com/flightctl/flightctl/internal/util"
	"sigs.k8s.io/yaml"
)

const (
	appName = "flightctl"
)

type Config struct {
	Database *dbConfig    `json:"database,omitempty"`
	Service  *svcConfig   `json:"service,omitempty"`
	Agent    *agentConfig `json:"agent,omitempty"`
}

type dbConfig struct {
	Type     string `json:"type,omitempty"`
	Hostname string `json:"hostname,omitempty"`
	Port     uint   `json:"port,omitempty"`
	Name     string `json:"name,omitempty"`
	User     string `json:"user,omitempty"`
	Password string `json:"password,omitempty"`
}

type svcConfig struct {
	Address     string   `json:"address,omitempty"`
	CertStore   string   `json:"cert,omitempty"`
	BaseUrl     string   `json:"baseUrl,omitempty"`
	CaCertFile  string   `json:"caCertFile,omitempty"`
	CaKeyFile   string   `json:"caKeyFile,omitempty"`
	SrvCertFile string   `json:"srvCertFile,omitempty"`
	SrvKeyFile  string   `json:"srvKeyFile,omitempty"`
	AltNames    []string `json:"altNames,omitempty"`
}

type agentConfig struct {
	Server               string        `json:"server,omitempty"`
	EnrollmentUi         string        `json:"enrollmentUi,omitempty"`
	TpmPath              string        `json:"tpmPath,omitempty"`
	FetchSpecInterval    util.Duration `json:"fetchSpecInterval,omitempty"`
	StatusUpdateInterval util.Duration `json:"statusUpdateInterval,omitempty"`
}

func ConfigDir() string {
	return filepath.Join(util.MustString(os.UserHomeDir), "."+appName)
}

func ConfigFile() string {
	return filepath.Join(ConfigDir(), "config.yaml")
}

func CertificateDir() string {
	return filepath.Join(ConfigDir(), "certs")
}

func NewDefault() *Config {
	c := &Config{
		Database: &dbConfig{
			Type:     "pgsql",
			Hostname: "localhost",
			Port:     5432,
			Name:     "flightctl",
			User:     "admin",
			Password: "adminpass",
		},
		Service: &svcConfig{
			Address:   ":3333",
			CertStore: CertificateDir(),
			BaseUrl:   "http://localhost:3333/api",
		},
		Agent: &agentConfig{
			Server:               "https://localhost:3333",
			EnrollmentUi:         "",
			TpmPath:              "",
			FetchSpecInterval:    util.Duration(agent.DefaultFetchSpecInterval),
			StatusUpdateInterval: util.Duration(agent.DefaultStatusUpdateInterval),
		},
	}
	return c
}

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
	if err := os.WriteFile(cfgFile, contents, 0644); err != nil {
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
