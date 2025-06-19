package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/flightctl/flightctl/internal/config/ca"
	"github.com/flightctl/flightctl/internal/util"
	"sigs.k8s.io/yaml"
)

const (
	appName = "flightctl"
)

type Config struct {
	Database     *dbConfig           `json:"database,omitempty"`
	Service      *svcConfig          `json:"service,omitempty"`
	KV           *kvConfig           `json:"kv,omitempty"`
	Alertmanager *alertmanagerConfig `json:"alertmanager,omitempty"`
	Auth         *authConfig         `json:"auth,omitempty"`
	Prometheus   *prometheusConfig   `json:"prometheus,omitempty"`
	CA           *ca.Config          `json:"ca,omitempty"`
	Tracing      *tracingConfig      `json:"tracing,omitempty"`
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
	Address                string        `json:"address,omitempty"`
	AgentEndpointAddress   string        `json:"agentEndpointAddress,omitempty"`
	CertStore              string        `json:"cert,omitempty"`
	BaseUrl                string        `json:"baseUrl,omitempty"`
	BaseAgentEndpointUrl   string        `json:"baseAgentEndpointUrl,omitempty"`
	BaseUIUrl              string        `json:"baseUIUrl,omitempty"`
	SrvCertFile            string        `json:"srvCertificateFile,omitempty"`
	SrvKeyFile             string        `json:"srvKeyFile,omitempty"`
	ServerCertName         string        `json:"serverCertName,omitempty"`
	ServerCertValidityDays int           `json:"serverCertValidityDays,omitempty"`
	AltNames               []string      `json:"altNames,omitempty"`
	LogLevel               string        `json:"logLevel,omitempty"`
	HttpReadTimeout        util.Duration `json:"httpReadTimeout,omitempty"`
	HttpReadHeaderTimeout  util.Duration `json:"httpReadHeaderTimeout,omitempty"`
	HttpWriteTimeout       util.Duration `json:"httpWriteTimeout,omitempty"`
	HttpIdleTimeout        util.Duration `json:"httpIdleTimeout,omitempty"`
	HttpMaxNumHeaders      int           `json:"httpMaxNumHeaders,omitempty"`
	HttpMaxHeaderBytes     int           `json:"httpMaxHeaderBytes,omitempty"`
	HttpMaxUrlLength       int           `json:"httpMaxUrlLength,omitempty"`
	HttpMaxRequestSize     int           `json:"httpMaxRequestSize,omitempty"`
	EventRetentionPeriod   util.Duration `json:"eventRetentionPeriod,omitempty"`
	AlertPollingInterval   util.Duration `json:"alertPollingInterval,omitempty"`
}

type kvConfig struct {
	Hostname string `json:"hostname,omitempty"`
	Port     uint   `json:"port,omitempty"`
	Password string `json:"password,omitempty"`
}

type alertmanagerConfig struct {
	Hostname string `json:"hostname,omitempty"`
	Port     uint   `json:"port,omitempty"`
}

type authConfig struct {
	K8s                   *k8sAuth  `json:"k8s,omitempty"`
	OIDC                  *oidcAuth `json:"oidc,omitempty"`
	AAP                   *aapAuth  `json:"aap,omitempty"`
	CACert                string    `json:"caCert,omitempty"`
	InsecureSkipTlsVerify bool      `json:"insecureSkipTlsVerify,omitempty"`
}

type k8sAuth struct {
	ApiUrl                  string `json:"apiUrl,omitempty"`
	ExternalOpenShiftApiUrl string `json:"externalOpenShiftApiUrl,omitempty"`
	RBACNs                  string `json:"rbacNs,omitempty"`
}

type oidcAuth struct {
	OIDCAuthority         string `json:"oidcAuthority,omitempty"`
	ExternalOIDCAuthority string `json:"externalOidcAuthority,omitempty"`
}

type aapAuth struct {
	ApiUrl         string `json:"apiUrl,omitempty"`
	ExternalApiUrl string `json:"externalApiUrl,omitempty"`
}

type prometheusConfig struct {
	Address        string    `json:"address,omitempty"`
	SloMax         float64   `json:"sloMax,omitempty"`
	ApiLatencyBins []float64 `json:"apiLatencyBins,omitempty"`
}

type tracingConfig struct {
	Enabled  bool   `json:"enabled,omitempty"`
	Endpoint string `json:"endpoint,omitempty"`
	Insecure bool   `json:"insecure,omitempty"`
}

type ConfigOption func(*Config)

func WithTracingEnabled() ConfigOption {
	return func(c *Config) {
		c.Tracing = &tracingConfig{
			Enabled: true,
		}
	}
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

func NewDefault(opts ...ConfigOption) *Config {
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
			Address:                ":3443",
			AgentEndpointAddress:   ":7443",
			CertStore:              CertificateDir(),
			BaseUrl:                "https://localhost:3443",
			BaseAgentEndpointUrl:   "https://localhost:7443",
			ServerCertName:         "server",
			ServerCertValidityDays: 365,
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
		},
		KV: &kvConfig{
			Hostname: "localhost",
			Port:     6379,
			Password: "adminpass",
		},
		Alertmanager: &alertmanagerConfig{
			Hostname: "localhost",
			Port:     9093,
		},
		Prometheus: &prometheusConfig{
			Address:        ":15690",
			SloMax:         4.0,
			ApiLatencyBins: []float64{1e-7, 1e-6, 1e-5, 1e-4, 1e-3, 1e-2, 1e-1, 1e0},
		},
	}
	c.CA = ca.NewDefault(CertificateDir())
	// CA certs are stored in the same location as Server Certs by default

	for _, opt := range opts {
		opt(c)
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
	c := NewDefault()
	if err := yaml.Unmarshal(contents, c); err != nil {
		return nil, fmt.Errorf("decoding config: %v", err)
	}

	if kvPass := os.Getenv("KV_PASSWORD"); kvPass != "" {
		c.KV.Password = kvPass
	}
	if dbUser := os.Getenv("DB_USER"); dbUser != "" {
		c.Database.User = dbUser
	}
	if dbPass := os.Getenv("DB_PASSWORD"); dbPass != "" {
		c.Database.Password = dbPass
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
