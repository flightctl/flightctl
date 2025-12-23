package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	api "github.com/flightctl/flightctl/api/v1beta1"
	"github.com/flightctl/flightctl/internal/config/ca"
	"github.com/flightctl/flightctl/internal/org"
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
	Enabled      bool          `json:"enabled,omitempty"`      // Enable/disable rate limiting
	Requests     int           `json:"requests,omitempty"`     // max requests per window
	Window       util.Duration `json:"window,omitempty"`       // e.g. "1m" for one minute
	AuthRequests int           `json:"authRequests,omitempty"` // max auth requests per window
	AuthWindow   util.Duration `json:"authWindow,omitempty"`   // e.g. "1h" for one hour
	// TrustedProxies specifies IP addresses/networks that are allowed to set proxy headers
	// If empty, proxy headers are ignored for security (only direct connection IPs are used)
	TrustedProxies []string `json:"trustedProxies,omitempty"`
}

type dbConfig struct {
	Type     string           `json:"type,omitempty"`
	Hostname string           `json:"hostname,omitempty"`
	Port     uint             `json:"port,omitempty"`
	Name     string           `json:"name,omitempty"`
	User     string           `json:"user,omitempty"`
	Password api.SecureString `json:"password,omitempty"`
	// Migration user configuration for schema changes
	MigrationUser     string           `json:"migrationUser,omitempty"`
	MigrationPassword api.SecureString `json:"migrationPassword,omitempty"`
	// SSL configuration
	SSLMode     string `json:"sslmode,omitempty"`
	SSLCert     string `json:"sslcert,omitempty"`
	SSLKey      string `json:"sslkey,omitempty"`
	SSLRootCert string `json:"sslrootcert,omitempty"`
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
	Hostname string           `json:"hostname,omitempty"`
	Port     uint             `json:"port,omitempty"`
	Password api.SecureString `json:"password,omitempty"`
}

type alertmanagerConfig struct {
	Hostname   string `json:"hostname,omitempty"`
	Port       uint   `json:"port,omitempty"`
	MaxRetries int    `json:"maxRetries,omitempty"`
	BaseDelay  string `json:"baseDelay,omitempty"`
	MaxDelay   string `json:"maxDelay,omitempty"`
}

type authConfig struct {
	K8s                     *api.K8sProviderSpec       `json:"k8s,omitempty"`
	OpenShift               *api.OpenShiftProviderSpec `json:"openshift,omitempty"`
	OIDC                    *api.OIDCProviderSpec      `json:"oidc,omitempty"`
	OAuth2                  *api.OAuth2ProviderSpec    `json:"oauth2,omitempty"`
	AAP                     *api.AapProviderSpec       `json:"aap,omitempty"`
	CACert                  string                     `json:"caCert,omitempty"`
	InsecureSkipTlsVerify   bool                       `json:"insecureSkipTlsVerify,omitempty"`
	PAMOIDCIssuer           *PAMOIDCIssuer             `json:"pamOidcIssuer,omitempty"`           // this is the issuer implementation configuration
	DynamicProviderCacheTTL util.Duration              `json:"dynamicProviderCacheTTL,omitempty"` // TTL for dynamic auth provider cache (default: 5s)
}

// PAMOIDCIssuer represents an OIDC issuer that uses Linux PAM for authentication
type PAMOIDCIssuer struct {
	// Address is the listen address for the PAM issuer service (e.g., ":8444")
	Address string `json:"address,omitempty"`
	// Issuer is the base URL for the OIDC issuer (e.g., "https://flightctl.example.com")
	Issuer string `json:"issuer,omitempty"`
	// ClientID is the OAuth2 client ID for this issuer
	ClientID string `json:"clientId,omitempty"`
	// ClientSecret is the OAuth2 client secret for this issuer
	ClientSecret string `json:"clientSecret,omitempty"`
	// Scopes are the supported OAuth2 scopes
	Scopes []string `json:"scopes,omitempty"`
	// RedirectURIs are the allowed redirect URIs for OAuth2 flows
	RedirectURIs []string `json:"redirectUris,omitempty"`
	// PAMService is the PAM service name to use for authentication (default: "flightctl")
	PAMService string `json:"pamService" validate:"required"`
	// AllowPublicClientWithoutPKCE allows public clients (no client secret) to skip PKCE
	// SECURITY WARNING: This should only be enabled for testing or backward compatibility
	// Default: false (PKCE required for public clients per OAuth 2.0 Security BCP)
	AllowPublicClientWithoutPKCE bool `json:"allowPublicClientWithoutPKCE,omitempty"`
	// AccessTokenExpiration is the expiration duration for access tokens and ID tokens
	// Default: 1 hour
	AccessTokenExpiration util.Duration `json:"accessTokenExpiration,omitempty"`
	// RefreshTokenExpiration is the expiration duration for refresh tokens
	// Default: 7 days
	RefreshTokenExpiration util.Duration `json:"refreshTokenExpiration,omitempty"`
	// PendingSessionCookieMaxAge is the MaxAge duration for pending session cookies
	// Default: 10 minutes
	PendingSessionCookieMaxAge util.Duration `json:"pendingSessionCookieMaxAge,omitempty"`
	// AuthenticatedSessionCookieMaxAge is the MaxAge duration for authenticated session cookies
	// Default: 30 minutes
	AuthenticatedSessionCookieMaxAge util.Duration `json:"authenticatedSessionCookieMaxAge,omitempty"`
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

func WithOIDCAuth(issuer, clientId string, enabled bool) ConfigOption {
	return func(c *Config) {
		if c.Auth == nil {
			c.Auth = &authConfig{
				DynamicProviderCacheTTL: util.Duration(5 * time.Second),
			}
		}
		c.Auth.OIDC = &api.OIDCProviderSpec{
			Issuer:       issuer,
			ClientId:     clientId,
			Enabled:      &enabled,
			ProviderType: api.Oidc,
		}
	}
}

func WithOAuth2Auth(authorizationUrl, tokenUrl, userinfoUrl, issuer, clientId string, enabled bool) ConfigOption {

	return func(c *Config) {
		if c.Auth == nil {
			c.Auth = &authConfig{
				DynamicProviderCacheTTL: util.Duration(5 * time.Second),
			}
		}
		c.Auth.OAuth2 = &api.OAuth2ProviderSpec{
			AuthorizationUrl: authorizationUrl,
			TokenUrl:         tokenUrl,
			UserinfoUrl:      userinfoUrl,
			Issuer:           &issuer,
			ClientId:         clientId,
			Enabled:          &enabled,
			ProviderType:     api.Oauth2,
		}
	}
}

func WithK8sAuth(apiUrl, rbacNs string) ConfigOption {
	return func(c *Config) {
		if c.Auth == nil {
			c.Auth = &authConfig{
				DynamicProviderCacheTTL: util.Duration(5 * time.Second),
			}
		}
		enabled := true
		c.Auth.K8s = &api.K8sProviderSpec{
			ApiUrl:       apiUrl,
			RbacNs:       &rbacNs,
			ProviderType: api.K8s,
			Enabled:      &enabled,
		}
	}
}

func WithAAPAuth(apiUrl, externalApiUrl string) ConfigOption {
	return func(c *Config) {
		if c.Auth == nil {
			c.Auth = &authConfig{
				DynamicProviderCacheTTL: util.Duration(5 * time.Second),
			}
		}
		enabled := true
		c.Auth.AAP = &api.AapProviderSpec{
			ApiUrl:       apiUrl,
			ProviderType: api.Aap,
			Enabled:      &enabled,
		}
	}
}

func WithPAMOIDCIssuer(issuer, clientId, clientSecret, pamService string) ConfigOption {
	return func(c *Config) {
		if c.Auth == nil {
			c.Auth = &authConfig{
				DynamicProviderCacheTTL: util.Duration(5 * time.Second),
			}
		}
		c.Auth.PAMOIDCIssuer = &PAMOIDCIssuer{
			Issuer:       issuer,
			ClientID:     clientId,
			ClientSecret: clientSecret,
			PAMService:   pamService,
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
			ServerCertValidityDays: 730,
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
		Auth: &authConfig{
			DynamicProviderCacheTTL: util.Duration(5 * time.Second),
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

	applyEnvVarOverrides(c)
	if err := applyAuthDefaults(c); err != nil {
		return nil, fmt.Errorf("applying auth defaults: %w", err)
	}

	return c, nil
}

func applyEnvVarOverrides(c *Config) {
	if kvPass := os.Getenv("KV_PASSWORD"); kvPass != "" {
		c.KV.Password = api.SecureString(kvPass)
	}
	if dbUser := os.Getenv("DB_USER"); dbUser != "" {
		c.Database.User = dbUser
	}
	if dbPass := os.Getenv("DB_PASSWORD"); dbPass != "" {
		c.Database.Password = api.SecureString(dbPass)
	}
	if dbMigrationUser := os.Getenv("DB_MIGRATION_USER"); dbMigrationUser != "" {
		c.Database.MigrationUser = dbMigrationUser
	}
	if dbMigrationPass := os.Getenv("DB_MIGRATION_PASSWORD"); dbMigrationPass != "" {
		c.Database.MigrationPassword = api.SecureString(dbMigrationPass)
	}
}

func applyAuthDefaults(c *Config) error {
	if c.Auth == nil {
		return nil
	}

	applyAuthProviderEnabledDefaults(c.Auth)
	applyPAMOIDCIssuerDefaults(c)
	applyOIDCClientDefaults(c)
	applyOpenShiftDefaults(c)
	if err := applyOAuth2Defaults(c); err != nil {
		return err
	}
	return nil
}

func applyAuthProviderEnabledDefaults(auth *authConfig) {
	if auth.OIDC != nil && auth.OIDC.Enabled == nil {
		enabled := true
		auth.OIDC.Enabled = &enabled
	}
	if auth.OpenShift != nil && auth.OpenShift.Enabled == nil {
		enabled := true
		auth.OpenShift.Enabled = &enabled
	}
	if auth.K8s != nil && auth.K8s.Enabled == nil {
		enabled := true
		auth.K8s.Enabled = &enabled
	}
	if auth.OAuth2 != nil && auth.OAuth2.Enabled == nil {
		enabled := true
		auth.OAuth2.Enabled = &enabled
	}
	if auth.AAP != nil && auth.AAP.Enabled == nil {
		enabled := true
		auth.AAP.Enabled = &enabled
	}
}

func applyPAMOIDCIssuerDefaults(c *Config) {
	if c.Auth.PAMOIDCIssuer == nil {
		return
	}

	if c.Auth.PAMOIDCIssuer.PAMService == "" {
		c.Auth.PAMOIDCIssuer.PAMService = "flightctl"
	}
	if c.Auth.PAMOIDCIssuer.Issuer == "" {
		c.Auth.PAMOIDCIssuer.Issuer = c.Service.BaseUrl
	}
	if c.Auth.PAMOIDCIssuer.ClientID == "" {
		c.Auth.PAMOIDCIssuer.ClientID = "flightctl-client"
	}
	if len(c.Auth.PAMOIDCIssuer.Scopes) == 0 {
		c.Auth.PAMOIDCIssuer.Scopes = []string{"openid", "profile", "email", "roles"}
	}
	if len(c.Auth.PAMOIDCIssuer.RedirectURIs) == 0 {
		applyPAMOIDCIssuerRedirectURIDefaults(c)
	}
	if c.Auth.PAMOIDCIssuer.AccessTokenExpiration == 0 {
		c.Auth.PAMOIDCIssuer.AccessTokenExpiration = util.Duration(1 * time.Hour)
	}
	if c.Auth.PAMOIDCIssuer.RefreshTokenExpiration == 0 {
		c.Auth.PAMOIDCIssuer.RefreshTokenExpiration = util.Duration(7 * 24 * time.Hour)
	}
	if c.Auth.PAMOIDCIssuer.PendingSessionCookieMaxAge == 0 {
		c.Auth.PAMOIDCIssuer.PendingSessionCookieMaxAge = util.Duration(10 * time.Minute)
	}
	if c.Auth.PAMOIDCIssuer.AuthenticatedSessionCookieMaxAge == 0 {
		c.Auth.PAMOIDCIssuer.AuthenticatedSessionCookieMaxAge = util.Duration(30 * time.Minute)
	}
}

func applyPAMOIDCIssuerRedirectURIDefaults(c *Config) {
	base := c.Service.BaseUIUrl
	if base == "" {
		base = c.Service.BaseUrl
	}
	if base != "" {
		c.Auth.PAMOIDCIssuer.RedirectURIs = []string{strings.TrimSuffix(base, "/") + "/callback"}
	}
}

func applyOIDCClientDefaults(c *Config) {
	if c.Auth.OIDC == nil {
		return
	}

	if c.Auth.OIDC.ClientId == "" {
		c.Auth.OIDC.ClientId = "flightctl-client"
	}
	if c.Auth.OIDC.Issuer == "" {
		c.Auth.OIDC.Issuer = c.Service.BaseUrl
	}
	if c.Auth.OIDC.UsernameClaim == nil {
		c.Auth.OIDC.UsernameClaim = &[]string{"preferred_username"}
	}

	applyOIDCRoleAssignmentDefaults(c.Auth.OIDC)
	applyOIDCOrganizationAssignmentDefaults(c.Auth.OIDC)
}

func applyOIDCRoleAssignmentDefaults(oidc *api.OIDCProviderSpec) {
	if _, err := oidc.RoleAssignment.Discriminator(); err != nil {
		dynamicRoleAssignment := api.AuthDynamicRoleAssignment{
			Type:      api.AuthDynamicRoleAssignmentTypeDynamic,
			ClaimPath: []string{"groups"},
		}
		_ = oidc.RoleAssignment.FromAuthDynamicRoleAssignment(dynamicRoleAssignment)
	}
}

func applyOIDCOrganizationAssignmentDefaults(oidc *api.OIDCProviderSpec) {
	if _, err := oidc.OrganizationAssignment.Discriminator(); err != nil {
		staticAssignment := api.AuthStaticOrganizationAssignment{
			OrganizationName: org.DefaultExternalID,
			Type:             api.AuthStaticOrganizationAssignmentTypeStatic,
		}
		_ = oidc.OrganizationAssignment.FromAuthStaticOrganizationAssignment(staticAssignment)
	}
}

func applyOpenShiftDefaults(c *Config) {
	if c.Auth.OpenShift == nil {
		return
	}

	// Use authorizationUrl as issuer if issuer is not provided
	if c.Auth.OpenShift.Issuer == nil || *c.Auth.OpenShift.Issuer == "" {
		if c.Auth.OpenShift.AuthorizationUrl != nil {
			c.Auth.OpenShift.Issuer = c.Auth.OpenShift.AuthorizationUrl
		}
	}
}

func applyOAuth2Defaults(c *Config) error {
	if c.Auth.OAuth2 == nil {
		return nil
	}

	// Infer introspection configuration if not provided
	if c.Auth.OAuth2.Introspection == nil {
		introspection, err := api.InferOAuth2IntrospectionConfig(*c.Auth.OAuth2)
		if err != nil {
			return fmt.Errorf("failed to infer OAuth2 introspection configuration: %w", err)
		}
		c.Auth.OAuth2.Introspection = introspection
	}
	return nil
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

	// Validate OIDC and OAuth2 provider role assignments
	if cfg.Auth != nil {
		if cfg.Auth.OIDC != nil {
			if err := validateAuthProviderRoleAssignment(cfg.Auth.OIDC.RoleAssignment, string(api.Oidc)); err != nil {
				return err
			}
		}
		if cfg.Auth.OAuth2 != nil {
			if err := validateAuthProviderRoleAssignment(cfg.Auth.OAuth2.RoleAssignment, string(api.Oauth2)); err != nil {
				return err
			}
		}
	}

	return nil
}

func validateAuthProviderRoleAssignment(roleAssignment api.AuthRoleAssignment, providerType string) error {
	discriminator, err := roleAssignment.Discriminator()
	if err != nil {
		// No role assignment configured, which is valid
		return nil
	}

	if discriminator != string(api.AuthStaticRoleAssignmentTypeStatic) {
		// Only validate static role assignments
		return nil
	}

	staticAssignment, err := roleAssignment.AsAuthStaticRoleAssignment()
	if err != nil {
		return fmt.Errorf("%s provider: invalid static role assignment: %w", providerType, err)
	}

	// Validate that all roles are in KnownExternalRoles
	for i, role := range staticAssignment.Roles {
		if role == "" {
			return fmt.Errorf("%s provider: role at index %d cannot be empty", providerType, i)
		}
		if !slices.Contains(api.KnownExternalRoles, role) {
			return fmt.Errorf("%s provider: role at index %d is not a valid role: %s (must be one of: %v)", providerType, i, role, api.KnownExternalRoles)
		}
	}

	return nil
}

func (cfg *Config) String() string {
	// Create a sanitized copy to avoid mutating the original config
	sanitized := cfg.sanitizeForLogging()
	contents, err := json.Marshal(sanitized)
	if err != nil {
		return "<error>"
	}
	return string(contents)
}

// sanitizeForLogging creates a copy of the config with sensitive fields redacted
func (cfg *Config) sanitizeForLogging() *Config {
	if cfg == nil {
		return nil
	}

	// Create a deep copy by marshaling and unmarshaling
	// This is safe because SecureString already handles redaction in MarshalJSON
	cfgJSON, err := json.Marshal(cfg)
	if err != nil {
		return cfg
	}

	var sanitized Config
	if err := json.Unmarshal(cfgJSON, &sanitized); err != nil {
		return cfg
	}

	// Redact client secrets in all auth providers
	if sanitized.Auth != nil {
		if sanitized.Auth.OIDC != nil && sanitized.Auth.OIDC.ClientSecret != nil {
			redacted := "[REDACTED]"
			sanitized.Auth.OIDC.ClientSecret = &redacted
		}
		if sanitized.Auth.OAuth2 != nil && sanitized.Auth.OAuth2.ClientSecret != nil {
			redacted := "[REDACTED]"
			sanitized.Auth.OAuth2.ClientSecret = &redacted
		}
		if sanitized.Auth.OpenShift != nil && sanitized.Auth.OpenShift.ClientSecret != nil {
			redacted := "[REDACTED]"
			sanitized.Auth.OpenShift.ClientSecret = &redacted
		}
		if sanitized.Auth.AAP != nil && sanitized.Auth.AAP.ClientSecret != nil {
			redacted := "[REDACTED]"
			sanitized.Auth.AAP.ClientSecret = &redacted
		}
		if sanitized.Auth.PAMOIDCIssuer != nil && sanitized.Auth.PAMOIDCIssuer.ClientSecret != "" {
			sanitized.Auth.PAMOIDCIssuer.ClientSecret = "[REDACTED]"
		}
	}

	return &sanitized
}
