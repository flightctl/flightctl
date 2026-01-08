package pamissuer

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/flightctl/flightctl/internal/config/ca"
	"github.com/flightctl/flightctl/internal/config/common"
	"github.com/flightctl/flightctl/internal/crypto"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/sirupsen/logrus"
	"sigs.k8s.io/yaml"
)

var (
	ErrCheckingServerCerts = errors.New("error checking for server certs")
	ErrServerCertsNotFound = errors.New("server certificates not found")
)

// Config holds the configuration for the flightctl-pam-issuer service.
type Config struct {
	Service       *ServiceConfig        `json:"service,omitempty"`
	PAMOIDCIssuer *PAMOIDCIssuer        `json:"pamOidcIssuer,omitempty"`
	Tracing       *common.TracingConfig `json:"tracing,omitempty"`
	CA            *ca.Config            `json:"ca,omitempty"`
}

// ServiceConfig holds PAM issuer service-specific configuration.
type ServiceConfig struct {
	LogLevel       string                  `json:"logLevel,omitempty"`
	CertStore      string                  `json:"cert,omitempty"`
	BaseUrl        string                  `json:"baseUrl,omitempty"`
	BaseUIUrl      string                  `json:"baseUIUrl,omitempty"`
	ServerCertName string                  `json:"serverCertName,omitempty"`
	SrvCertFile    string                  `json:"srvCertificateFile,omitempty"`
	SrvKeyFile     string                  `json:"srvKeyFile,omitempty"`
	RateLimit      *common.RateLimitConfig `json:"rateLimit,omitempty"`
}

// PAMOIDCIssuer represents an OIDC issuer that uses Linux PAM for authentication.
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

// NewDefault returns a default PAM issuer configuration.
func NewDefault() *Config {
	certDir := common.CertificateDir()
	return &Config{
		Service: &ServiceConfig{
			LogLevel:       "info",
			CertStore:      certDir,
			ServerCertName: "server",
		},
		CA: ca.NewDefault(certDir),
	}
}

// ConfigFile returns the default config file path.
func ConfigFile() string {
	return common.ConfigFile()
}

// Load loads the PAM issuer configuration from a file.
func Load(cfgFile string) (*Config, error) {
	contents, err := os.ReadFile(cfgFile)
	if err != nil {
		return nil, fmt.Errorf("reading config file: %w", err)
	}

	c := NewDefault()
	if err := yaml.Unmarshal(contents, c); err != nil {
		return nil, fmt.Errorf("decoding config: %w", err)
	}

	c.applyDefaults()
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

func (c *Config) applyDefaults() {
	if c.PAMOIDCIssuer == nil {
		return
	}

	baseUrl := ""
	baseUIUrl := ""
	if c.Service != nil {
		baseUrl = c.Service.BaseUrl
		baseUIUrl = c.Service.BaseUIUrl
	}

	if c.PAMOIDCIssuer.PAMService == "" {
		c.PAMOIDCIssuer.PAMService = "flightctl"
	}
	if c.PAMOIDCIssuer.Issuer == "" {
		c.PAMOIDCIssuer.Issuer = baseUrl
	}
	if c.PAMOIDCIssuer.ClientID == "" {
		c.PAMOIDCIssuer.ClientID = "flightctl-client"
	}
	if len(c.PAMOIDCIssuer.Scopes) == 0 {
		c.PAMOIDCIssuer.Scopes = []string{"openid", "profile", "email", "roles"}
	}
	if len(c.PAMOIDCIssuer.RedirectURIs) == 0 {
		c.applyRedirectURIDefaults(baseUrl, baseUIUrl)
	}
	if c.PAMOIDCIssuer.AccessTokenExpiration == 0 {
		c.PAMOIDCIssuer.AccessTokenExpiration = util.Duration(1 * time.Hour)
	}
	if c.PAMOIDCIssuer.RefreshTokenExpiration == 0 {
		c.PAMOIDCIssuer.RefreshTokenExpiration = util.Duration(7 * 24 * time.Hour)
	}
	if c.PAMOIDCIssuer.PendingSessionCookieMaxAge == 0 {
		c.PAMOIDCIssuer.PendingSessionCookieMaxAge = util.Duration(10 * time.Minute)
	}
	if c.PAMOIDCIssuer.AuthenticatedSessionCookieMaxAge == 0 {
		c.PAMOIDCIssuer.AuthenticatedSessionCookieMaxAge = util.Duration(30 * time.Minute)
	}
}

func (c *Config) applyRedirectURIDefaults(baseUrl, baseUIUrl string) {
	base := baseUIUrl
	if base == "" {
		base = baseUrl
	}
	if base != "" {
		c.PAMOIDCIssuer.RedirectURIs = []string{strings.TrimSuffix(base, "/") + "/callback"}
	}
}

// LogLevel returns the configured log level.
func (c *Config) LogLevel() string {
	if c.Service != nil && c.Service.LogLevel != "" {
		return c.Service.LogLevel
	}
	return "info"
}

// GetPAMOIDCIssuer returns the PAM OIDC issuer configuration.
func (c *Config) GetPAMOIDCIssuer() *PAMOIDCIssuer {
	return c.PAMOIDCIssuer
}

// String returns a JSON representation of the config with sensitive fields redacted.
func (c *Config) String() string {
	sanitized := c.sanitizeForLogging()
	contents, err := json.Marshal(sanitized)
	if err != nil {
		return "<error>"
	}
	return string(contents)
}

func (c *Config) sanitizeForLogging() *Config {
	if c == nil {
		return nil
	}

	// Create a shallow copy
	sanitized := *c
	if sanitized.PAMOIDCIssuer != nil {
		pamCopy := *sanitized.PAMOIDCIssuer
		pamCopy.ClientSecret = "[REDACTED]"
		sanitized.PAMOIDCIssuer = &pamCopy
	}

	return &sanitized
}

// TracingConfig returns the tracing configuration.
func (c *Config) TracingConfig() *common.TracingConfig {
	return c.Tracing
}

// CAConfig returns the CA configuration.
func (c *Config) CAConfig() *ca.Config {
	return c.CA
}

// LoadServerCertificates loads the server certificates for the PAM issuer.
func LoadServerCertificates(cfg *Config, log *logrus.Logger) (*crypto.TLSCertificateConfig, error) {
	var keyFile, certFile string
	if cfg.Service.SrvCertFile != "" || cfg.Service.SrvKeyFile != "" {
		certFile = cfg.Service.SrvCertFile
		keyFile = cfg.Service.SrvKeyFile
	} else {
		certFile = crypto.CertStorePath(cfg.Service.ServerCertName+".crt", cfg.Service.CertStore)
		keyFile = crypto.CertStorePath(cfg.Service.ServerCertName+".key", cfg.Service.CertStore)
	}

	canReadCertAndKey, err := crypto.CanReadCertAndKey(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCheckingServerCerts, err)
	}
	if !canReadCertAndKey {
		return nil, ErrServerCertsNotFound
	}

	serverCerts, err := crypto.GetTLSCertificateConfig(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("loading TLS certificate: %w", err)
	}

	// Check for expired certificate
	for _, x509Cert := range serverCerts.Certs {
		expired := time.Now().After(x509Cert.NotAfter)
		log.Printf("checking certificate: subject='%s', issuer='%s', expiry='%v'",
			x509Cert.Subject.CommonName, x509Cert.Issuer.CommonName, x509Cert.NotAfter)

		if expired {
			log.Warnf("server certificate for '%s' issued by '%s' has expired on: %v",
				x509Cert.Subject.CommonName, x509Cert.Issuer.CommonName, x509Cert.NotAfter)
		}
	}

	return serverCerts, nil
}
