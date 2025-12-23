package client

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	grpc_v1 "github.com/flightctl/flightctl/api/grpc/v1"
	api "github.com/flightctl/flightctl/api/v1beta1"
	"github.com/flightctl/flightctl/internal/api/client"
	"github.com/flightctl/flightctl/internal/auth/common"
	"github.com/flightctl/flightctl/internal/crypto"
	"github.com/flightctl/flightctl/internal/org"
	"github.com/flightctl/flightctl/pkg/reqid"
	"github.com/flightctl/flightctl/pkg/version"
	"github.com/go-chi/chi/v5/middleware"
	"golang.org/x/net/http2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/keepalive"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	certutil "k8s.io/client-go/util/cert"
	"sigs.k8s.io/yaml"
)

const (
	// TestRootDirEnvKey is the environment variable key used to set the file system root when testing.
	TestRootDirEnvKey = "FLIGHTCTL_TEST_ROOT_DIR"

	http2ReadIdleTimeout = 45 * time.Second
)

// HTTPClientOption is a functional option for configuring HTTP client behavior.
type HTTPClientOption func(*http.Client) error

// Config holds the information needed to connect to a Flight Control API server
type Config struct {
	Service      Service  `json:"service"`
	AuthInfo     AuthInfo `json:"authentication"`
	Organization string   `json:"organization,omitempty"`

	// HTTPOptions contains HTTP client configuration options
	HTTPOptions []HTTPClientOption `json:"-"`

	// baseDir is used to resolve relative paths
	// If baseDir is empty, the current working directory is used.
	baseDir string `json:"-"`
	// TestRootDir is the root directory for test files.
	testRootDir string `json:"-"`
}

// Service contains information how to connect to and authenticate the Flight Control API server.
type Service struct {
	// Server is the URL of the Flight Control API server (the part before /api/v1/...).
	Server string `json:"server,omitempty"`
	// TLSServerName is passed to the server for SNI and is used in the client to check server certificates against.
	// If TLSServerName is empty, the hostname used to contact the server is used.
	// +optional
	TLSServerName string `json:"tls-server-name,omitempty"`
	// CertificateAuthority is the path to a cert file for the certificate authority.
	CertificateAuthority string `json:"certificate-authority,omitempty"`
	// CertificateAuthorityData contains PEM-encoded certificate authority certificates. Overrides CertificateAuthority
	CertificateAuthorityData []byte `json:"certificate-authority-data,omitempty"`
	InsecureSkipVerify       bool   `json:"insecureSkipVerify,omitempty"`
}

type TokenToUseType string

const (
	TokenToUseAccessToken TokenToUseType = "access"
	TokenToUseIdToken     TokenToUseType = "id"
)

// AuthInfo contains information for authenticating Flight Control API clients.
type AuthInfo struct {
	// ClientCertificate is the path to a client cert file for TLS.
	// +optional
	ClientCertificate string `json:"client-certificate,omitempty"`
	// ClientCertificateData contains PEM-encoded data from a client cert file for TLS. Overrides ClientCertificate.
	// +optional
	ClientCertificateData []byte `json:"client-certificate-data,omitempty"`
	// ClientKey is the path to a client key file for TLS.
	// +optional
	ClientKey string `json:"client-key,omitempty"`
	// ClientKeyData contains PEM-encoded data from a client key file for TLS. Overrides ClientKey.
	// +optional
	ClientKeyData []byte `json:"client-key-data,omitempty" datapolicy:"security-key"`
	// AccessToken is the OAuth2/OIDC access token for API authentication
	// +optional
	AccessToken string `json:"access-token,omitempty"`
	// AccessTokenExpiry is the expiration time of the access token (RFC3339 format)
	// +optional
	AccessTokenExpiry string `json:"access-token-expiry,omitempty"`
	// RefreshToken is the OAuth2/OIDC refresh token for obtaining new access tokens
	// +optional
	RefreshToken string `json:"refresh-token,omitempty"`
	// IdToken is the OIDC ID token containing user identity information
	// +optional
	IdToken string `json:"id-token,omitempty"`
	// TokenToUse is the type of token to use for API authentication
	// +optional
	TokenToUse TokenToUseType `json:"token-to-use,omitempty"`
	// The authentication provider (i.e. OIDC, AAP, OAuth2, OpenShift)
	// +optional
	AuthProvider *AuthProviderConfig `json:"auth-provider,omitempty"`
	// Organizations indicates the configured IdP supports organizations.
	// +optional
	OrganizationsEnabled bool `json:"organizations-enabled,omitempty"`
}

type AuthProviderConfig struct {
	// AuthProvider is the authentication provider from the API
	AuthProvider api.AuthProvider `json:"auth-provider"`
	// CAFile is the path to a cert file for the certificate authority of the auth provider.
	CAFile string `json:"ca-file,omitempty"`
	// InsecureSkipVerify skips TLS verification when connecting to the auth provider
	InsecureSkipVerify bool `json:"insecureSkipVerify,omitempty"`
}

func (c *Config) Equal(c2 *Config) bool {
	if c == c2 {
		return true
	}
	if c == nil || c2 == nil {
		return false
	}
	return c.Service.Equal(&c2.Service) && c.AuthInfo.Equal(&c2.AuthInfo)
}

func (s *Service) Equal(s2 *Service) bool {
	if s == s2 {
		return true
	}
	if s == nil || s2 == nil {
		return false
	}
	return s.Server == s2.Server && s.TLSServerName == s2.TLSServerName &&
		s.CertificateAuthority == s2.CertificateAuthority &&
		bytes.Equal(s.CertificateAuthorityData, s2.CertificateAuthorityData)
}

func (a *AuthInfo) Equal(a2 *AuthInfo) bool {
	if a == a2 {
		return true
	}
	if a == nil || a2 == nil {
		return false
	}
	// Compare AuthProvider presence and equality
	if a.AuthProvider == nil && a2.AuthProvider == nil {
		// Both nil, continue with other fields
	} else if a.AuthProvider == nil || a2.AuthProvider == nil {
		// One nil, one not nil => not equal
		return false
	} else if !a.AuthProvider.Equal(a2.AuthProvider) {
		// Both non-nil, use Equal method
		return false
	}
	return a.ClientCertificate == a2.ClientCertificate && a.ClientKey == a2.ClientKey &&
		bytes.Equal(a.ClientCertificateData, a2.ClientCertificateData) &&
		bytes.Equal(a.ClientKeyData, a2.ClientKeyData) &&
		a.AccessToken == a2.AccessToken &&
		a.AccessTokenExpiry == a2.AccessTokenExpiry &&
		a.RefreshToken == a2.RefreshToken &&
		a.IdToken == a2.IdToken &&
		a.TokenToUse == a2.TokenToUse &&
		a.OrganizationsEnabled == a2.OrganizationsEnabled
}

func (a *AuthProviderConfig) Equal(a2 *AuthProviderConfig) bool {
	if a == a2 {
		return true
	}
	if a == nil || a2 == nil {
		return false
	}

	// Compare AuthProvider using YAML marshaling
	a1Yaml, err1 := yaml.Marshal(a.AuthProvider)
	a2Yaml, err2 := yaml.Marshal(a2.AuthProvider)
	if err1 != nil || err2 != nil || !bytes.Equal(a1Yaml, a2Yaml) {
		return false
	}

	return a.CAFile == a2.CAFile &&
		a.InsecureSkipVerify == a2.InsecureSkipVerify
}

func (c *Config) DeepCopy() *Config {
	if c == nil {
		return nil
	}
	return &Config{
		Service:      *c.Service.DeepCopy(),
		AuthInfo:     *c.AuthInfo.DeepCopy(),
		Organization: c.Organization,
		HTTPOptions:  slices.Clone(c.HTTPOptions),
		baseDir:      c.baseDir,
		testRootDir:  c.testRootDir,
	}
}

func (s *Service) DeepCopy() *Service {
	if s == nil {
		return nil
	}
	s2 := *s
	s2.CertificateAuthorityData = bytes.Clone(s.CertificateAuthorityData)
	return &s2
}

func (a *AuthInfo) DeepCopy() *AuthInfo {
	if a == nil {
		return nil
	}
	a2 := *a
	a2.ClientCertificateData = bytes.Clone(a.ClientCertificateData)
	a2.ClientKeyData = bytes.Clone(a.ClientKeyData)
	if a.AuthProvider != nil {
		a2.AuthProvider = a.AuthProvider.DeepCopy()
	}
	return &a2
}

func (a *AuthProviderConfig) DeepCopy() *AuthProviderConfig {
	if a == nil {
		return nil
	}
	a2 := *a
	return &a2
}

func (c *Config) HasCredentials() bool {
	return (len(c.AuthInfo.ClientCertificate) > 0 || len(c.AuthInfo.ClientCertificateData) > 0) &&
		(len(c.AuthInfo.ClientKey) > 0 || len(c.AuthInfo.ClientKeyData) > 0)
}

func (c *Config) GetClientKeyPath() string {
	return resolvePath(c.AuthInfo.ClientKey, c.baseDir)
}

func (c *Config) GetClientCertificatePath() string {
	return resolvePath(c.AuthInfo.ClientCertificate, c.baseDir)
}

func (c *Config) SetBaseDir(baseDir string) {
	c.baseDir = baseDir
}

// AddHTTPOptions adds HTTP client options to the config
func (c *Config) AddHTTPOptions(opts ...HTTPClientOption) {
	c.HTTPOptions = append(c.HTTPOptions, opts...)
}

func NewDefault() *Config {
	c := &Config{}

	if value := os.Getenv(TestRootDirEnvKey); value != "" {
		c.testRootDir = filepath.Clean(value)
	}

	return c
}

// WithQueryParam returns a ClientOption that appends a request editor which
// sets (or overrides) the given query parameter. If value is empty, the editor
// is a no-op so callers can pass it unconditionally.
func WithQueryParam(key, value string) client.ClientOption {
	return client.WithRequestEditorFn(func(ctx context.Context, req *http.Request) error {
		if value == "" {
			return nil
		}
		q := req.URL.Query()
		q.Set(key, value)
		req.URL.RawQuery = q.Encode()
		return nil
	})
}

// WithOrganization sets the organization ID in the request query parameters.
func WithOrganization(orgID string) client.ClientOption {
	return WithQueryParam("org_id", orgID)
}

// WithHeader returns a ClientOption that appends a request editor which
// sets the given HTTP header. If value is empty, the editor is a no-op
// so callers can pass it unconditionally.
func WithHeader(key, value string) client.ClientOption {
	return client.WithRequestEditorFn(func(ctx context.Context, req *http.Request) error {
		if value == "" {
			return nil
		}
		req.Header.Set(key, value)
		return nil
	})
}

// WithUserAgentHeader returns a ClientOption that sets the User-Agent header.
// The component parameter specifies the component name (e.g., "flightctl-cli")
// to include in the User-Agent string.
func WithUserAgentHeader(component string) client.ClientOption {
	info := version.Get()
	userAgent := fmt.Sprintf("%s/%s (%s/%s)", component, info.String(), runtime.GOOS, runtime.GOARCH)
	return WithHeader("User-Agent", userAgent)
}

// NewFromConfig returns a new Flight Control API client from the given config.
func NewFromConfig(config *Config, configFilePath string, opts ...client.ClientOption) (*client.ClientWithResponses, error) {

	httpClient, err := NewHTTPClientFromConfig(config)
	if err != nil {
		return nil, fmt.Errorf("NewFromConfig: creating HTTP client %w", err)
	}
	ref := client.WithRequestEditorFn(func(ctx context.Context, req *http.Request) error {
		req.Header.Set(middleware.RequestIDHeader, reqid.NextRequestID())
		accessToken := GetAccessToken(config, configFilePath)
		if accessToken != "" {
			req.Header.Set(common.AuthHeader, fmt.Sprintf("Bearer %s", accessToken))
		}
		return nil
	})
	defaultOpts := []client.ClientOption{client.WithHTTPClient(httpClient), ref, WithOrganization(config.Organization)}
	defaultOpts = append(defaultOpts, opts...)
	return client.NewClientWithResponses(config.Service.Server, defaultOpts...)
}

// NewFromConfigFile returns a new Flight Control API client using the config
// read from the given file. Additional client options may be supplied and will
// be appended after the defaults.
func NewFromConfigFile(filename string, opts ...client.ClientOption) (*client.ClientWithResponses, error) {
	config, err := ParseConfigFile(filename)
	if err != nil {
		return nil, err
	}
	return NewFromConfig(config, filename, opts...)
}

// NewHTTPClientFromConfig returns a new HTTP Client from the given config.
func NewHTTPClientFromConfig(config *Config) (*http.Client, error) {
	config = config.DeepCopy()
	if err := config.Flatten(); err != nil {
		return nil, err
	}

	tlsServerName := config.Service.TLSServerName
	if len(tlsServerName) == 0 {
		u, err := url.Parse(config.Service.Server)
		if err != nil {
			return nil, fmt.Errorf("NewHTTPClientFromConfig: parsing server url: %w", err)
		}
		tlsServerName = u.Hostname()
	}

	tlsConfig, err := CreateTLSConfigFromConfig(config)
	if err != nil {
		return nil, fmt.Errorf("NewHTTPClientFromConfig: creating TLS config: %w", err)
	}
	tlsConfig.ServerName = tlsServerName

	// Configure transport for HTTP/2 support
	transport := &http.Transport{
		TLSClientConfig: tlsConfig,
		// Enable HTTP/2
		ForceAttemptHTTP2: true,
	}

	// Configure HTTP/2
	t2, err := http2.ConfigureTransports(transport)
	if err != nil {
		return nil, fmt.Errorf("NewHTTPClientFromConfig: configuring HTTP/2 transport: %w", err)
	}
	if t2 != nil {
		t2.ReadIdleTimeout = http2ReadIdleTimeout
	}
	httpClient := &http.Client{
		Transport: transport,
	}

	for _, opt := range config.HTTPOptions {
		if err = opt(httpClient); err != nil {
			return nil, fmt.Errorf("NewHTTPClientFromConfig: applying HTTP option: %w", err)
		}
	}

	return httpClient, nil
}

func CreateTLSConfigFromConfig(config *Config) (*tls.Config, error) {
	tlsConfig := tls.Config{
		MinVersion:         tls.VersionTLS13,
		InsecureSkipVerify: config.Service.InsecureSkipVerify, //nolint:gosec
	}

	if err := addServiceCAToTLSConfig(&tlsConfig, config); err != nil {
		return nil, fmt.Errorf("NewHTTPClientFromConfig: parsing CA certs: %w", err)
	}

	if err := addClientCertToTLSConfig(&tlsConfig, config); err != nil {
		return nil, fmt.Errorf("NewHTTPClientFromConfig: parsing client cert and key: %w", err)
	}
	return &tlsConfig, nil
}

func addServiceCAToTLSConfig(tlsConfig *tls.Config, config *Config) error {
	if len(config.Service.CertificateAuthorityData) > 0 {
		caPool, err := certutil.NewPoolFromBytes(config.Service.CertificateAuthorityData)
		if err != nil {
			return err
		}
		tlsConfig.RootCAs = caPool
	}
	return nil
}

func addClientCertToTLSConfig(tlsConfig *tls.Config, config *Config) error {
	if len(config.AuthInfo.ClientCertificateData) > 0 {
		clientCert, err := tls.X509KeyPair(config.AuthInfo.ClientCertificateData, config.AuthInfo.ClientKeyData)
		if err != nil {
			return err
		}
		tlsConfig.Certificates = []tls.Certificate{clientCert}
	}
	return nil
}

// NewGRPCClientFromConfig returns a new gRPC Client from the given config.
func NewGRPCClientFromConfig(config *Config, endpoint string) (grpc_v1.RouterServiceClient, error) {
	grpcEndpoint := config.Service.Server
	if endpoint != "" {
		grpcEndpoint = endpoint
	}

	config = config.DeepCopy()
	if err := config.Flatten(); err != nil {
		return nil, err
	}

	tlsConfig := tls.Config{
		MinVersion:         tls.VersionTLS13,
		InsecureSkipVerify: config.Service.InsecureSkipVerify, //nolint:gosec
	}

	if string(config.Service.CertificateAuthorityData) != "" {
		caPool, err := certutil.NewPoolFromBytes(config.Service.CertificateAuthorityData)
		if err != nil {
			return nil, fmt.Errorf("NewHTTPClientFromConfig: parsing CA certs: %w", err)
		}
		tlsConfig.RootCAs = caPool
	}

	u, err := url.Parse(grpcEndpoint)
	if err != nil {
		return nil, fmt.Errorf("NewHTTPClientFromConfig: parsing CA certs: %w", err)
	}
	tlsServerName := u.Hostname()
	tlsConfig.ServerName = tlsServerName

	if len(config.AuthInfo.ClientCertificateData) > 0 {
		clientCert, err := tls.X509KeyPair(config.AuthInfo.ClientCertificateData, config.AuthInfo.ClientKeyData)
		if err != nil {
			return nil, fmt.Errorf("NewHTTPClientFromConfig: parsing client cert and key: %w", err)
		}
		tlsConfig.Certificates = []tls.Certificate{clientCert}
	}
	// our transport is http, but the grpc library has special encoding for the endpoint
	grpcEndpoint = strings.TrimPrefix(grpcEndpoint, "http://")
	grpcEndpoint = strings.TrimPrefix(grpcEndpoint, "https://")
	grpcEndpoint = strings.TrimSuffix(grpcEndpoint, "/")

	client, err := grpc.NewClient(grpcEndpoint,
		grpc.WithTransportCredentials(credentials.NewTLS(&tlsConfig)),
		grpc.WithKeepaliveParams(keepalive.ClientParameters{
			Time:                30 * time.Second, // Send keepalive ping every 30s
			Timeout:             10 * time.Second, // Wait 10s for server response
			PermitWithoutStream: true,             // Send even if no active RPCs
		}))

	if err != nil {
		return nil, fmt.Errorf("NewGRPCClientFromConfig: creating gRPC client: %w", err)
	}

	router := grpc_v1.NewRouterServiceClient(client)

	return router, nil
}

// DefaultFlightctlClientConfigPath returns the default path to the Flight Control client config file.
func DefaultFlightctlClientConfigPath() (string, error) {
	baseDir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}

	return filepath.Join(baseDir, "flightctl", "client.yaml"), nil
}

func ParseConfigFile(filename string) (*Config, error) {
	contents, err := os.ReadFile(filename)
	if err != nil {
		return nil, fmt.Errorf("reading config: %w", err)
	}
	config := NewDefault()
	if err := yaml.Unmarshal(contents, config); err != nil {
		return nil, fmt.Errorf("decoding config: %w", err)
	}
	config.SetBaseDir(filepath.Dir(filename))
	if err := config.Validate(); err != nil {
		return nil, err
	}
	return config, config.Flatten()
}

// NewFromConfigFile returns a new Flight Control API client using the config read from the given file.
func NewGrpcClientFromConfigFile(filename string, endpoint string) (grpc_v1.RouterServiceClient, error) {
	contents, err := os.ReadFile(filename)
	if err != nil {
		return nil, fmt.Errorf("reading config: %v", err)
	}
	config := NewDefault()
	if err := yaml.Unmarshal(contents, config); err != nil {
		return nil, fmt.Errorf("decoding config: %v", err)
	}
	config.SetBaseDir(filepath.Dir(filename))
	if err := config.Validate(); err != nil {
		return nil, err
	}
	if err := config.Flatten(); err != nil {
		return nil, err
	}
	return NewGRPCClientFromConfig(config, endpoint)
}

// WriteConfig writes a client config file using the given parameters.
func WriteConfig(filename string, server string, tlsServerName string, caCertPEM []byte, client *crypto.TLSCertificateConfig) error {

	config := NewDefault()
	config.Service = Service{
		Server:                   server,
		TLSServerName:            tlsServerName,
		CertificateAuthorityData: caCertPEM,
	}

	if client != nil {
		clientCertPEM, clientKeyPEM, err := client.GetPEMBytes()
		if err != nil {
			return fmt.Errorf("PEM-encoding client cert and key: %w", err)
		}
		config.AuthInfo = AuthInfo{
			ClientCertificateData: clientCertPEM,
			ClientKeyData:         clientKeyPEM,
		}
	}

	return config.Persist(filename)
}

func (c *Config) Persist(filename string) error {
	contents, err := yaml.Marshal(c)
	if err != nil {
		return fmt.Errorf("encoding config: %w", err)
	}
	directory := filepath.Dir(filename)
	if err := os.MkdirAll(directory, 0700); err != nil {
		return fmt.Errorf("writing config: %w", err)
	}
	if err := os.WriteFile(filename, contents, 0600); err != nil {
		return fmt.Errorf("writing config: %w", err)
	}
	return nil
}

func (c *Config) Validate() error {
	validationErrors := make([]error, 0)
	validationErrors = append(validationErrors, validateService(c.Service, c.baseDir, c.testRootDir)...)
	validationErrors = append(validationErrors, validateAuthInfo(c.AuthInfo, c.baseDir, c.testRootDir)...)
	validationErrors = append(validationErrors, validateOrganization(c.Organization, c.baseDir, c.testRootDir)...)
	if len(validationErrors) > 0 {
		return fmt.Errorf("invalid configuration: %v", utilerrors.NewAggregate(validationErrors).Error())
	}
	return nil
}

func validateService(service Service, baseDir string, testRootDir string) []error {
	validationErrors := make([]error, 0)
	// Make sure the server is specified and well-formed
	if len(service.Server) == 0 {
		validationErrors = append(validationErrors, fmt.Errorf("no server found"))
	} else {
		u, err := url.Parse(service.Server)
		if err != nil {
			validationErrors = append(validationErrors, fmt.Errorf("invalid server format %q: %w", service.Server, err))
		} else {
			if len(u.Hostname()) == 0 {
				validationErrors = append(validationErrors, fmt.Errorf("invalid server format %q: no hostname", service.Server))
			}
		}
	}
	// Make sure CA data and CA file aren't both specified
	if len(service.CertificateAuthority) != 0 && len(service.CertificateAuthorityData) != 0 {
		validationErrors = append(validationErrors, fmt.Errorf("certificate-authority-data and certificate-authority are both specified. certificate-authority-data will override"))
	}
	if len(service.CertificateAuthority) != 0 {
		clientCertCA, err := os.Open(filepath.Join(testRootDir, resolvePath(service.CertificateAuthority, baseDir)))
		if err != nil {
			validationErrors = append(validationErrors, fmt.Errorf("unable to read certificate-authority %v due to %w", service.CertificateAuthority, err))
		} else {
			defer clientCertCA.Close()
		}
	}
	return validationErrors
}

func validateAuthInfo(authInfo AuthInfo, baseDir string, testRootDir string) []error {
	validationErrors := make([]error, 0)
	if len(authInfo.ClientCertificate) != 0 || len(authInfo.ClientCertificateData) != 0 {
		// Make sure cert data and file aren't both specified
		if len(authInfo.ClientCertificate) != 0 && len(authInfo.ClientCertificateData) != 0 {
			validationErrors = append(validationErrors, fmt.Errorf("client-cert-data and client-cert are both specified. client-cert-data will override"))
		}
		// Make sure key data and file aren't both specified
		if len(authInfo.ClientKey) != 0 && len(authInfo.ClientKeyData) != 0 {
			validationErrors = append(validationErrors, fmt.Errorf("client-key-data and client-key are both specified; client-key-data will override"))
		}
		// Make sure a key is specified
		if len(authInfo.ClientKey) == 0 && len(authInfo.ClientKeyData) == 0 {
			validationErrors = append(validationErrors, fmt.Errorf("client-key-data or client-key must be specified to use the clientCert authentication method"))
		}

		if len(authInfo.ClientCertificate) != 0 {
			clientCertFile, err := os.Open(filepath.Join(testRootDir, resolvePath(authInfo.ClientCertificate, baseDir)))
			if err != nil {
				validationErrors = append(validationErrors, fmt.Errorf("unable to read client-cert %v due to %w", authInfo.ClientCertificate, err))
			} else {
				defer clientCertFile.Close()
			}
		}
		if len(authInfo.ClientKey) != 0 {
			clientKeyFile, err := os.Open(filepath.Join(testRootDir, resolvePath(authInfo.ClientKey, baseDir)))
			if err != nil {
				validationErrors = append(validationErrors, fmt.Errorf("unable to read client-key %v due to %w", authInfo.ClientKey, err))
			} else {
				defer clientKeyFile.Close()
			}
		}
	}
	return validationErrors
}

func validateOrganization(organization string, baseDir string, testRootDir string) []error {
	validationErrors := make([]error, 0)
	if organization == "" {
		return validationErrors
	}
	if _, err := org.Parse(organization); err != nil {
		validationErrors = append(validationErrors, err)
	}
	return validationErrors
}

// Reads the contents of all referenced files and embeds them in the config.
func (c *Config) Flatten() error {
	if err := flatten(&c.Service.CertificateAuthority, &c.Service.CertificateAuthorityData, c.baseDir, c.testRootDir); err != nil {
		return err
	}
	if err := flatten(&c.AuthInfo.ClientCertificate, &c.AuthInfo.ClientCertificateData, c.baseDir, c.testRootDir); err != nil {
		return err
	}
	if err := flatten(&c.AuthInfo.ClientKey, &c.AuthInfo.ClientKeyData, c.baseDir, c.testRootDir); err != nil {
		return err
	}
	return nil
}

func flatten(path *string, contents *[]byte, baseDir string, testRootDir string) error {
	if len(*path) != 0 {
		if len(*contents) > 0 {
			return errors.New("cannot have values for both path and contents")
		}

		var err error
		absPath := resolvePath(*path, baseDir)
		*contents, err = os.ReadFile(filepath.Join(testRootDir, absPath))
		if err != nil {
			return err
		}

		*path = ""
	}
	return nil
}

func resolvePath(path string, baseDir string) string {
	// Don't resolve empty paths
	if len(path) > 0 {
		// Don't resolve absolute paths
		if !filepath.IsAbs(path) {
			return filepath.Join(baseDir, path)
		}
	}
	return path
}

func resolveTransport(client *http.Client) (*http.Transport, error) {
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		return nil, fmt.Errorf("transport is an unknown type: %T", client.Transport)
	}

	return transport.Clone(), nil
}

// WithDialer configures the HTTP client to use the specified dialer.
func WithDialer(dialer *net.Dialer) HTTPClientOption {
	return func(client *http.Client) error {
		transport, err := resolveTransport(client)
		if err != nil {
			return fmt.Errorf("WithDialer: %w", err)
		}
		transport.DialContext = dialer.DialContext
		client.Transport = transport
		return nil
	}
}

// WithMaxIdleConnsPerHost configures the HTTP client to use the specified number of IdleConnsPerHost
// Also increases the MaxIdleConns configuration if the current setting is less than new configuration
// for IdleConnsPerHost
func WithMaxIdleConnsPerHost(conns int) HTTPClientOption {
	return func(client *http.Client) error {
		transport, err := resolveTransport(client)
		if err != nil {
			return fmt.Errorf("WithMaxIdleConnsPerHost: %w", err)
		}
		transport.MaxIdleConnsPerHost = conns
		if transport.MaxIdleConns < conns {
			transport.MaxIdleConns = conns
		}
		client.Transport = transport
		return nil
	}
}

// WithCachedTransport caches the first transport it sees and replaces all future invocations with this transport.
// The purpose of this option is to reuse connection pools across areas that may be hard to wire together.
func WithCachedTransport() HTTPClientOption {
	var mux sync.Mutex
	var cached *http.Transport
	return func(client *http.Client) error {
		mux.Lock()
		defer mux.Unlock()
		if cached == nil {
			transport, err := resolveTransport(client)
			if err != nil {
				return fmt.Errorf("WithCachedTransport: %w", err)
			}
			cached = transport
		}
		if _, ok := client.Transport.(*http.Transport); !ok {
			return fmt.Errorf("WithCachedTransport: transport is an unknown type: %T", client.Transport)
		}
		client.Transport = cached
		return nil
	}
}
