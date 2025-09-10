package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/flightctl/flightctl/internal/config/ca"
	"github.com/flightctl/flightctl/internal/util"
	"sigs.k8s.io/yaml"
)

const (
	appName = "flightctl"
)

type Config struct {
	Database         *dbConfig               `json:"database,omitempty"`
	Service          *svcConfig              `json:"service,omitempty"`
	KV               *kvConfig               `json:"kv,omitempty"`
	Alertmanager     *alertmanagerConfig     `json:"alertmanager,omitempty"`
	Auth             *authConfig             `json:"auth,omitempty"`
	Metrics          *metricsConfig          `json:"metrics,omitempty"`
	CA               *ca.Config              `json:"ca,omitempty"`
	Tracing          *tracingConfig          `json:"tracing,omitempty"`
	GitOps           *gitOpsConfig           `json:"gitOps,omitempty"`
	Periodic         *periodicConfig         `json:"periodic,omitempty"`
	Organizations    *organizationsConfig    `json:"organizations,omitempty"`
	TelemetryGateway *telemetryGatewayConfig `json:"telemetrygateway,omitempty"`
}

type RateLimitConfig struct {
	Requests     int           `json:"requests,omitempty"`     // max requests per window
	Window       util.Duration `json:"window,omitempty"`       // e.g. "1m" for one minute
	AuthRequests int           `json:"authRequests,omitempty"` // max auth requests per window
	AuthWindow   util.Duration `json:"authWindow,omitempty"`   // e.g. "1h" for one hour
	// TrustedProxies specifies IP addresses/networks that are allowed to set proxy headers
	// If empty, proxy headers are ignored for security (only direct connection IPs are used)
	TrustedProxies []string `json:"trustedProxies,omitempty"`
}

type dbConfig struct {
	Type     string       `json:"type,omitempty"`
	Hostname string       `json:"hostname,omitempty"`
	Port     uint         `json:"port,omitempty"`
	Name     string       `json:"name,omitempty"`
	User     string       `json:"user,omitempty"`
	Password SecureString `json:"password,omitempty"`
	// Migration user configuration for schema changes
	MigrationUser     string       `json:"migrationUser,omitempty"`
	MigrationPassword SecureString `json:"migrationPassword,omitempty"`
}

type svcConfig struct {
	Address                string           `json:"address,omitempty"`
	AgentEndpointAddress   string           `json:"agentEndpointAddress,omitempty"`
	CertStore              string           `json:"cert,omitempty"`
	BaseUrl                string           `json:"baseUrl,omitempty"`
	BaseAgentEndpointUrl   string           `json:"baseAgentEndpointUrl,omitempty"`
	BaseUIUrl              string           `json:"baseUIUrl,omitempty"`
	SrvCertFile            string           `json:"srvCertificateFile,omitempty"`
	SrvKeyFile             string           `json:"srvKeyFile,omitempty"`
	ServerCertName         string           `json:"serverCertName,omitempty"`
	ServerCertValidityDays int              `json:"serverCertValidityDays,omitempty"`
	AltNames               []string         `json:"altNames,omitempty"`
	LogLevel               string           `json:"logLevel,omitempty"`
	HttpReadTimeout        util.Duration    `json:"httpReadTimeout,omitempty"`
	HttpReadHeaderTimeout  util.Duration    `json:"httpReadHeaderTimeout,omitempty"`
	HttpWriteTimeout       util.Duration    `json:"httpWriteTimeout,omitempty"`
	HttpIdleTimeout        util.Duration    `json:"httpIdleTimeout,omitempty"`
	HttpMaxNumHeaders      int              `json:"httpMaxNumHeaders,omitempty"`
	HttpMaxHeaderBytes     int              `json:"httpMaxHeaderBytes,omitempty"`
	HttpMaxUrlLength       int              `json:"httpMaxUrlLength,omitempty"`
	HttpMaxRequestSize     int              `json:"httpMaxRequestSize,omitempty"`
	EventRetentionPeriod   util.Duration    `json:"eventRetentionPeriod,omitempty"`
	AlertPollingInterval   util.Duration    `json:"alertPollingInterval,omitempty"`
	RenderedWaitTimeout    util.Duration    `json:"renderedWaitTimeout,omitempty"`
	RateLimit              *RateLimitConfig `json:"rateLimit,omitempty"`
	TPMCAPaths             []string         `json:"tpmCAPaths,omitempty"`
	HealthChecks           *healthChecks    `json:"healthChecks,omitempty"`
}

type healthChecks struct {
	Enabled          bool          `json:"enabled,omitempty"`
	ReadinessPath    string        `json:"readinessPath,omitempty"`
	LivenessPath     string        `json:"livenessPath,omitempty"`
	ReadinessTimeout util.Duration `json:"readinessTimeout,omitempty"`
}

type kvConfig struct {
	Hostname string       `json:"hostname,omitempty"`
	Port     uint         `json:"port,omitempty"`
	Password SecureString `json:"password,omitempty"`
}

type alertmanagerConfig struct {
	Hostname   string `json:"hostname,omitempty"`
	Port       uint   `json:"port,omitempty"`
	MaxRetries int    `json:"maxRetries,omitempty"`
	BaseDelay  string `json:"baseDelay,omitempty"`
	MaxDelay   string `json:"maxDelay,omitempty"`
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

type metricsConfig struct {
	Enabled               bool                         `json:"enabled,omitempty"`
	Address               string                       `json:"address,omitempty"`
	SystemCollector       *systemCollectorConfig       `json:"systemCollector,omitempty"`
	HttpCollector         *httpCollectorConfig         `json:"httpCollector,omitempty"`
	DeviceCollector       *deviceCollectorConfig       `json:"deviceCollector,omitempty"`
	FleetCollector        *fleetCollectorConfig        `json:"fleetCollector,omitempty"`
	RepositoryCollector   *repositoryCollectorConfig   `json:"repositoryCollector,omitempty"`
	ResourceSyncCollector *resourceSyncCollectorConfig `json:"resourceSyncCollector,omitempty"`
	WorkerCollector       *workerCollectorConfig       `json:"workerCollector,omitempty"`
}
type collectorConfig struct {
	Enabled bool `json:"enabled,omitempty"`
}

type periodicCollectorConfig struct {
	Enabled        bool          `json:"enabled,omitempty"`
	TickerInterval util.Duration `json:"tickerInterval,omitempty"`
}

type systemCollectorConfig struct {
	periodicCollectorConfig
}

type httpCollectorConfig struct {
	collectorConfig
}

type deviceCollectorConfig struct {
	periodicCollectorConfig
	GroupByFleet bool `json:"groupByFleet,omitempty"`
}

type fleetCollectorConfig struct {
	periodicCollectorConfig
}

type repositoryCollectorConfig struct {
	periodicCollectorConfig
}

type resourceSyncCollectorConfig struct {
	periodicCollectorConfig
}

type workerCollectorConfig struct {
	collectorConfig
}

type tracingConfig struct {
	Enabled  bool   `json:"enabled,omitempty"`
	Endpoint string `json:"endpoint,omitempty"`
	Insecure bool   `json:"insecure,omitempty"`
}

type gitOpsConfig struct {
	// IgnoreResourceUpdates lists JSON pointer paths that should be ignored
	// when comparing desired vs. live resources during GitOps sync.
	IgnoreResourceUpdates []string `json:"ignoreResourceUpdates,omitempty"`
}

type periodicConfig struct {
	Consumers int `json:"consumers,omitempty"`
}

type organizationsConfig struct {
	Enabled bool `json:"enabled,omitempty"`
}

type telemetryGatewayConfig struct {
	LogLevel string                    `json:"logLevel,omitempty"`
	TLS      telemetryGatewayTLSConfig `json:"tls,omitempty"`
	Listen   telemetryGatewayListen    `json:"listen,omitempty"`
	Export   *telemetryGatewayExport   `json:"export,omitempty"`
	Forward  *telemetryGatewayForward  `json:"forward,omitempty"`
}

type telemetryGatewayTLSConfig struct {
	CertFile string `json:"certFile,omitempty"`
	KeyFile  string `json:"keyFile,omitempty"`
	CACert   string `json:"caCert,omitempty"`
}

type telemetryGatewayListen struct {
	Device string `json:"device,omitempty"`
}

type telemetryGatewayExport struct {
	Prometheus string `json:"prometheus,omitempty"`
}

type telemetryGatewayForward struct {
	Endpoint string                      `json:"endpoint,omitempty"`
	TLS      *telemetryGatewayForwardTLS `json:"tls,omitempty"`
}

type telemetryGatewayForwardTLS struct {
	InsecureSkipTlsVerify bool   `json:"insecureSkipTlsVerify,omitempty"`
	CAFile                string `json:"caFile,omitempty"`
	CertFile              string `json:"certFile,omitempty"`
	KeyFile               string `json:"keyFile,omitempty"`
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
			Type:              "pgsql",
			Hostname:          "localhost",
			Port:              5432,
			Name:              "flightctl",
			User:              "flightctl_app",
			Password:          "adminpass",
			MigrationUser:     "flightctl_migrator",
			MigrationPassword: "adminpass",
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
			RenderedWaitTimeout:    util.Duration(2 * time.Minute),
			HealthChecks: &healthChecks{
				Enabled:          true,
				ReadinessPath:    "/readyz",
				LivenessPath:     "/healthz",
				ReadinessTimeout: util.Duration(2 * time.Second),
			},
			// Rate limiting is disabled by default - set RateLimit to enable
		},
		KV: &kvConfig{
			Hostname: "localhost",
			Port:     6379,
			Password: "adminpass",
		},
		Alertmanager: &alertmanagerConfig{
			Hostname:   "localhost",
			Port:       9093,
			MaxRetries: 3,
			BaseDelay:  "500ms",
			MaxDelay:   "10s",
		},
		TelemetryGateway: &telemetryGatewayConfig{
			LogLevel: "info",
			TLS: telemetryGatewayTLSConfig{
				CertFile: "/etc/telemetry-gateway/certs/server.crt",
				KeyFile:  "/etc/telemetry-gateway/certs/server.key",
				CACert:   "/etc/telemetry-gateway/certs/ca.crt",
			},
			Listen: telemetryGatewayListen{Device: "0.0.0.0:4317"},
			// Export: nil  (no Prom until explicitly set)
			// Forward: nil (no upstream until explicitly set)
		},
		Metrics: &metricsConfig{
			Enabled: true,
			Address: ":15690",
			SystemCollector: &systemCollectorConfig{
				periodicCollectorConfig: periodicCollectorConfig{
					Enabled:        true,
					TickerInterval: util.Duration(5 * time.Second),
				},
			},
			HttpCollector: &httpCollectorConfig{
				collectorConfig: collectorConfig{
					Enabled: true,
				},
			},
			DeviceCollector: &deviceCollectorConfig{
				periodicCollectorConfig: periodicCollectorConfig{
					Enabled:        true,
					TickerInterval: util.Duration(30 * time.Second),
				},
				GroupByFleet: true,
			},
			FleetCollector: &fleetCollectorConfig{
				periodicCollectorConfig: periodicCollectorConfig{
					Enabled:        true,
					TickerInterval: util.Duration(30 * time.Second),
				},
			},
			RepositoryCollector: &repositoryCollectorConfig{
				periodicCollectorConfig: periodicCollectorConfig{
					Enabled:        true,
					TickerInterval: util.Duration(30 * time.Second),
				},
			},
			ResourceSyncCollector: &resourceSyncCollectorConfig{
				periodicCollectorConfig: periodicCollectorConfig{
					Enabled:        true,
					TickerInterval: util.Duration(30 * time.Second),
				},
			},
		},
		GitOps: &gitOpsConfig{
			IgnoreResourceUpdates: []string{
				"/metadata/resourceVersion",
			},
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
		c.KV.Password = SecureString(kvPass)
	}
	if dbUser := os.Getenv("DB_USER"); dbUser != "" {
		c.Database.User = dbUser
	}
	if dbPass := os.Getenv("DB_PASSWORD"); dbPass != "" {
		c.Database.Password = SecureString(dbPass)
	}
	if dbMigrationUser := os.Getenv("DB_MIGRATION_USER"); dbMigrationUser != "" {
		c.Database.MigrationUser = dbMigrationUser
	}
	if dbMigrationPass := os.Getenv("DB_MIGRATION_PASSWORD"); dbMigrationPass != "" {
		c.Database.MigrationPassword = SecureString(dbMigrationPass)
	}
	// Handle rate limit environment variables - create config if env vars are set
	rateLimitRequests := os.Getenv("RATE_LIMIT_REQUESTS")
	rateLimitWindow := os.Getenv("RATE_LIMIT_WINDOW")
	authRateLimitRequests := os.Getenv("AUTH_RATE_LIMIT_REQUESTS")
	authRateLimitWindow := os.Getenv("AUTH_RATE_LIMIT_WINDOW")
	trustedProxies := os.Getenv("RATE_LIMIT_TRUSTED_PROXIES")

	if rateLimitRequests != "" || rateLimitWindow != "" || authRateLimitRequests != "" || authRateLimitWindow != "" || trustedProxies != "" {
		// Create rate limit config if it doesn't exist
		if c.Service.RateLimit == nil {
			c.Service.RateLimit = &RateLimitConfig{}
		}

		if rateLimitRequests != "" {
			if requests, err := strconv.Atoi(rateLimitRequests); err == nil {
				c.Service.RateLimit.Requests = requests
			}
		}
		if rateLimitWindow != "" {
			if window, err := time.ParseDuration(rateLimitWindow); err == nil {
				c.Service.RateLimit.Window = util.Duration(window)
			}
		}
		if authRateLimitRequests != "" {
			if requests, err := strconv.Atoi(authRateLimitRequests); err == nil {
				c.Service.RateLimit.AuthRequests = requests
			}
		}
		if authRateLimitWindow != "" {
			if window, err := time.ParseDuration(authRateLimitWindow); err == nil {
				c.Service.RateLimit.AuthWindow = util.Duration(window)
			}
		}
		if trustedProxies != "" {
			// Split by comma and trim whitespace
			proxies := strings.Split(trustedProxies, ",")
			for i, proxy := range proxies {
				proxies[i] = strings.TrimSpace(proxy)
			}
			c.Service.RateLimit.TrustedProxies = proxies
		}
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
	allowedGitOpsIgnoreResourceUpdates := map[string]struct{}{
		"/metadata/resourceVersion": {},
	}

	if cfg.GitOps != nil {
		for _, path := range cfg.GitOps.IgnoreResourceUpdates {
			if _, ok := allowedGitOpsIgnoreResourceUpdates[path]; !ok {
				return fmt.Errorf("invalid ignoreResourceUpdates value: %s", path)
			}
		}
	}

	if cfg.Service != nil && cfg.Service.HealthChecks != nil && cfg.Service.HealthChecks.Enabled {
		hc := cfg.Service.HealthChecks
		if strings.TrimSpace(hc.ReadinessPath) == "" {
			return fmt.Errorf("readinessPath must be non-empty")
		}
		if !strings.HasPrefix(hc.ReadinessPath, "/") {
			return fmt.Errorf("readinessPath must start with '/'")
		}
		if strings.TrimSpace(hc.LivenessPath) == "" {
			return fmt.Errorf("livenessPath must be non-empty")
		}
		if !strings.HasPrefix(hc.LivenessPath, "/") {
			return fmt.Errorf("livenessPath must start with '/'")
		}
		if hc.ReadinessTimeout <= 0 {
			return fmt.Errorf("readinessTimeout must be greater than 0")
		}
		if hc.ReadinessPath == hc.LivenessPath {
			return fmt.Errorf("readinessPath and livenessPath must not be identical")
		}
	}
	return nil
}

func (cfg *Config) String() string {
	contents, err := json.Marshal(cfg)
	if err != nil {
		return "<error>"
	}
	return string(contents)
}
