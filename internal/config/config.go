package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/flightctl/flightctl/internal/util"
	"sigs.k8s.io/yaml"
)

const (
	appName = "flightctl"
)

type Config struct {
	Database *dbConfig    `json:"database,omitempty"`
	Service  *svcConfig   `json:"service,omitempty"`
	Queue    *queueConfig `json:"queue,omitempty"`
	Auth     *authConfig  `json:"auth,omitempty"`
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
	Address              string   `json:"address,omitempty"`
	AgentEndpointAddress string   `json:"agentEndpointAddress,omitempty"`
	CertStore            string   `json:"cert,omitempty"`
	BaseUrl              string   `json:"baseUrl,omitempty"`
	BaseAgentEndpointUrl string   `json:"baseAgentEndpointUrl,omitempty"`
	CaCertFile           string   `json:"caCertFile,omitempty"`
	CaKeyFile            string   `json:"caKeyFile,omitempty"`
	SrvCertFile          string   `json:"srvCertFile,omitempty"`
	SrvKeyFile           string   `json:"srvKeyFile,omitempty"`
	AltNames             []string `json:"altNames,omitempty"`
	LogLevel             string   `json:"logLevel,omitempty"`
}

type queueConfig struct {
	AmqpURL string `json:"amqpUrl,omitempty"`
}

type authConfig struct {
	K8sApiUrl string `json:"k8sApiUrl,omitempty"`
}

func ConfigDir() string {
	return filepath.Join(util.MustString(os.UserHomeDir), "."+appName)
}

func ConfigFile() string {
	return filepath.Join(ConfigDir(), "config.yaml")
}

func ClientConfigFile() string {
	return filepath.Join(ConfigDir(), "client.yaml")
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
			Address:              ":3443",
			AgentEndpointAddress: ":7443",
			CertStore:            CertificateDir(),
			BaseUrl:              "https://localhost:3443",
			BaseAgentEndpointUrl: "https://localhost:7443",
			LogLevel:             "info",
		},
		Queue: &queueConfig{
			AmqpURL: "amqp://localhost:5672",
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
