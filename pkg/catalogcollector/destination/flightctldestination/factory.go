package flightctldestination

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/flightctl/flightctl/api/versioning"
	v1alpha1client "github.com/flightctl/flightctl/internal/api/client/v1alpha1"
	catalogcollector "github.com/flightctl/flightctl/pkg/catalogcollector"
	"github.com/flightctl/flightctl/pkg/catalogcollector/extension/extensionauth"
)

// Type is the component type for the Flightctl destination.
const Type catalogcollector.ComponentType = "flightctl"

type factory struct{}

// NewFactory returns a DestinationFactory for the Flightctl destination.
func NewFactory() catalogcollector.DestinationFactory {
	return &factory{}
}

func (f *factory) Type() catalogcollector.ComponentType {
	return Type
}

func (f *factory) CreateDefaultConfig() catalogcollector.ComponentConfig {
	return &Config{}
}

func (f *factory) CreateDestination(
	_ context.Context,
	settings catalogcollector.Settings,
	cfg catalogcollector.ComponentConfig,
) (catalogcollector.Destination, error) {
	c, ok := cfg.(*Config)
	if !ok {
		return nil, fmt.Errorf(
			"destination %q: unexpected config type %T",
			settings.ID,
			cfg,
		)
	}
	if err := c.Validate(); err != nil {
		return nil, fmt.Errorf("destination %q: %w", settings.ID, err)
	}

	baseTransport, err := buildTLSTransport(c)
	if err != nil {
		return nil, fmt.Errorf("destination %q: %w", settings.ID, err)
	}

	var transport http.RoundTripper = baseTransport

	if c.Auth != nil {
		authID, err := catalogcollector.ParseComponentID(
			c.Auth.Authenticator,
		)
		if err != nil {
			return nil, fmt.Errorf(
				"destination %q: invalid auth.authenticator %q: %w",
				settings.ID,
				c.Auth.Authenticator,
				err,
			)
		}

		if settings.Host == nil {
			return nil, fmt.Errorf(
				"destination %q: auth.authenticator %q requires a host but none is available",
				settings.ID,
				authID,
			)
		}

		extension, err := settings.Host.GetExtension(authID)
		if err != nil {
			return nil, fmt.Errorf(
				"destination %q: resolving authenticator %q: %w",
				settings.ID,
				authID,
				err,
			)
		}

		authenticator, ok := extension.(extensionauth.HTTPClient)
		if !ok {
			return nil, fmt.Errorf(
				"destination %q: extension %q does not implement extensionauth.HTTPClient",
				settings.ID,
				authID,
			)
		}

		transport, err = authenticator.RoundTripper(transport)
		if err != nil {
			return nil, fmt.Errorf(
				"destination %q: building transport for authenticator %q: %w",
				settings.ID,
				authID,
				err,
			)
		}
		if transport == nil {
			return nil, fmt.Errorf(
				"destination %q: authenticator %q returned a nil HTTP transport",
				settings.ID,
				authID,
			)
		}
	}

	if c.OrgID != "" {
		transport = &organizationRoundTripper{orgID: c.OrgID, base: transport}
	}

	transport = versioning.NewTransport(
		transport,
		versioning.WithAPIVersion(versioning.V1Alpha1),
	)

	apiHTTPClient := &http.Client{
		Transport: transport,
		Timeout:   c.timeoutDuration(),
	}

	serverURL := strings.TrimSuffix(c.Server, "/") +
		v1alpha1client.ServerUrlApiv1

	apiClient, err := v1alpha1client.NewClientWithResponses(
		serverURL,
		v1alpha1client.WithHTTPClient(apiHTTPClient),
	)
	if err != nil {
		return nil, fmt.Errorf(
			"destination %q: creating API client: %w",
			settings.ID,
			err,
		)
	}

	return &reconciler{
		client: apiClient,
		id:     settings.ID,
		log:    settings.Logger,
	}, nil
}

func buildTLSTransport(cfg *Config) (*http.Transport, error) {
	tlsConfig := &tls.Config{
		MinVersion:         tls.VersionTLS13,
		InsecureSkipVerify: cfg.InsecureSkipVerify, //nolint:gosec
	}

	if cfg.CertificateAuthority != "" {
		caCert, err := os.ReadFile(cfg.CertificateAuthority)
		if err != nil {
			return nil, fmt.Errorf("reading CA certificate: %w", err)
		}

		pool, err := x509.SystemCertPool()
		if err != nil || pool == nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM(caCert) {
			return nil, fmt.Errorf(
				"CA certificate file contains no valid PEM certificates",
			)
		}
		tlsConfig.RootCAs = pool
	}

	return &http.Transport{
		TLSClientConfig:   tlsConfig,
		Proxy:             http.ProxyFromEnvironment,
		ForceAttemptHTTP2: true,
	}, nil
}
