package oauth2clientauthextension

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"

	catalogcollector "github.com/flightctl/flightctl/pkg/catalogcollector"
	"github.com/flightctl/flightctl/pkg/catalogcollector/extension/extensionauth"
	"github.com/sirupsen/logrus"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/clientcredentials"
)

var (
	_ catalogcollector.Extension = (*extension)(nil)
	_ extensionauth.HTTPClient   = (*extension)(nil)
)

type extension struct {
	log             *logrus.Entry
	tokenSource     oauth2.TokenSource
	tokenHTTPClient *http.Client
}

func (e *extension) Start(
	_ context.Context,
	_ catalogcollector.Host,
) error {
	e.log.Info("extension started")
	return nil
}

func (e *extension) Shutdown(_ context.Context) error {
	e.log.Info("shutting down extension")
	if e.tokenHTTPClient != nil {
		e.tokenHTTPClient.CloseIdleConnections()
		e.log.Info("idle token HTTP connections closed")
	}
	e.log.Info("extension stopped")
	return nil
}

// RoundTripper returns an HTTP transport that attaches OAuth2 bearer tokens to
// outgoing requests. Tokens are cached and refreshed automatically.
func (e *extension) RoundTripper(
	base http.RoundTripper,
) (http.RoundTripper, error) {
	if e.tokenSource == nil {
		return nil, fmt.Errorf("OAuth2 token source is not configured")
	}
	if base == nil {
		base = http.DefaultTransport
	}

	return &oauth2.Transport{
		Source: e.tokenSource,
		Base:   base,
	}, nil
}

// fileCredentialTokenSource resolves credentials and acquires a new access
// token whenever its Token method is called.
//
// oauth2.ReuseTokenSourceWithExpiry wraps this source and handles caching.
// Credential files are therefore read only when a new token is required.
type fileCredentialTokenSource struct {
	mu sync.Mutex

	cfg             *Config
	oauthConfig     *clientcredentials.Config
	tokenHTTPClient *http.Client
	ctx             context.Context
}

func (s *fileCredentialTokenSource) Token() (*oauth2.Token, error) {
	// Preserve the clientcredentials.Config auth-style cache and prevent
	// concurrent token requests from mutating its credential fields.
	s.mu.Lock()
	defer s.mu.Unlock()

	clientID, err := s.resolveClientID()
	if err != nil {
		return nil, err
	}

	clientSecret, err := s.resolveClientSecret()
	if err != nil {
		return nil, err
	}

	s.oauthConfig.ClientID = clientID
	s.oauthConfig.ClientSecret = clientSecret

	tokenContext := context.WithValue(
		context.WithoutCancel(s.ctx),
		oauth2.HTTPClient,
		s.tokenHTTPClient,
	)

	token, err := s.oauthConfig.Token(tokenContext)
	if err != nil {
		return nil, sanitizeOAuthError(err)
	}

	return token, nil
}

func (s *fileCredentialTokenSource) resolveClientID() (string, error) {
	if s.cfg.ClientID != "" {
		return s.cfg.ClientID, nil
	}

	return readCredentialFile(
		s.cfg.ClientIDFile,
		"client ID file",
	)
}

func (s *fileCredentialTokenSource) resolveClientSecret() (string, error) {
	if value := s.cfg.ClientSecret.Value(); value != "" {
		return value, nil
	}

	return readCredentialFile(
		s.cfg.ClientSecretFile,
		"client secret file",
	)
}

func readCredentialFile(path string, label string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("reading %s: %w", label, err)
	}

	value := strings.TrimSpace(string(data))
	if value == "" {
		return "", fmt.Errorf("%s is empty", label)
	}

	return value, nil
}

// sanitizeOAuthError prevents token endpoint response bodies from appearing in
// caller-visible errors. Providers may include sensitive request information in
// those bodies.
func sanitizeOAuthError(err error) error {
	var retrieveError *oauth2.RetrieveError
	if errors.As(err, &retrieveError) {
		if retrieveError.Response == nil {
			return fmt.Errorf("token endpoint rejected the token request")
		}

		status := retrieveError.Response.Status
		if status == "" {
			status = fmt.Sprintf(
				"HTTP %d",
				retrieveError.Response.StatusCode,
			)
		}

		return fmt.Errorf("token endpoint returned %s", status)
	}

	return fmt.Errorf("acquiring OAuth2 access token: %w", err)
}

func newTokenHTTPClient(cfg *Config) (*http.Client, error) {
	tlsConfig := &tls.Config{
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: cfg.InsecureSkipVerify, //nolint:gosec
	}

	if cfg.CertificateAuthority != "" {
		caCertificate, err := os.ReadFile(cfg.CertificateAuthority)
		if err != nil {
			return nil, fmt.Errorf(
				"reading token endpoint CA certificate: %w",
				err,
			)
		}

		certificatePool, err := x509.SystemCertPool()
		if err != nil || certificatePool == nil {
			certificatePool = x509.NewCertPool()
		}
		if !certificatePool.AppendCertsFromPEM(caCertificate) {
			return nil, fmt.Errorf(
				"token endpoint CA certificate contains no valid PEM certificates",
			)
		}

		tlsConfig.RootCAs = certificatePool
	}

	defaultTransport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return nil, fmt.Errorf(
			"default HTTP transport has unexpected type %T",
			http.DefaultTransport,
		)
	}

	transport := defaultTransport.Clone()
	transport.TLSClientConfig = tlsConfig

	return &http.Client{
		Transport: transport,
		Timeout:   cfg.timeoutDuration(),
	}, nil
}

func newOAuthConfig(cfg *Config) *clientcredentials.Config {
	endpointParams := make(url.Values, len(cfg.EndpointParams))
	for name, value := range cfg.EndpointParams {
		endpointParams.Set(name, value)
	}

	scopes := append([]string(nil), cfg.Scopes...)

	return &clientcredentials.Config{
		TokenURL:       cfg.TokenURL,
		Scopes:         scopes,
		EndpointParams: endpointParams,
		AuthStyle:      oauth2.AuthStyleAutoDetect,
	}
}

// newExtension constructs an extension from validated configuration and the
// factory context. It performs no network requests.
func newExtension(
	ctx context.Context,
	cfg *Config,
	log *logrus.Entry,
) (*extension, error) {
	tokenHTTPClient, err := newTokenHTTPClient(cfg)
	if err != nil {
		return nil, err
	}

	tokenSource := &fileCredentialTokenSource{
		cfg:             cfg,
		oauthConfig:     newOAuthConfig(cfg),
		tokenHTTPClient: tokenHTTPClient,
		ctx:             ctx,
	}

	cachedTokenSource := oauth2.ReuseTokenSourceWithExpiry(
		nil,
		tokenSource,
		cfg.expiryBufferDuration(),
	)

	return &extension{
		log:             log,
		tokenSource:     cachedTokenSource,
		tokenHTTPClient: tokenHTTPClient,
	}, nil
}
