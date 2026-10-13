package kubeflowmodelregistrysource

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"os"
	"strings"

	catalogcollector "github.com/flightctl/flightctl/pkg/catalogcollector"
	"github.com/flightctl/flightctl/pkg/catalogcollector/config"
	"github.com/flightctl/flightctl/pkg/catalogcollector/extension/extensionauth"
	"github.com/flightctl/flightctl/pkg/catalogcollector/source/pollsource"
	mrapi "github.com/kubeflow/hub/pkg/openapi"
)

// Type is the component type for the Kubeflow Model Registry source.
const Type catalogcollector.ComponentType = "kubeflowmodelregistry"

type factory struct{}

// NewFactory returns a SourceFactory for the Kubeflow Model Registry source.
func NewFactory() catalogcollector.SourceFactory {
	return &factory{}
}

func (f *factory) Type() catalogcollector.ComponentType {
	return Type
}

func (f *factory) CreateDefaultConfig() catalogcollector.ComponentConfig {
	return &Config{
		Backoff: pollsource.DefaultBackoffConfig(),
	}
}

func (f *factory) CreateSource(
	_ context.Context,
	settings catalogcollector.Settings,
	cfg catalogcollector.ComponentConfig,
	next catalogcollector.Consumer,
) (catalogcollector.Source, error) {
	c, ok := cfg.(*Config)
	if !ok {
		return nil, fmt.Errorf(
			"source %q: unexpected config type %T",
			settings.ID,
			cfg,
		)
	}
	if err := c.Validate(); err != nil {
		return nil, fmt.Errorf("source %q: %w", settings.ID, err)
	}
	if settings.Logger == nil {
		return nil, fmt.Errorf("source %q: logger must not be nil", settings.ID)
	}
	if settings.MeterProvider == nil {
		return nil, fmt.Errorf(
			"source %q: meter provider must not be nil",
			settings.ID,
		)
	}
	if next == nil {
		return nil, fmt.Errorf(
			"source %q: downstream consumer must not be nil",
			settings.ID,
		)
	}

	if c.InsecureSkipVerify {
		// Emit exactly one warning at construction time. Repeating this per
		// request would flood the log without adding information. The warning
		// names only the configuration option; no credential material is
		// logged.
		settings.Logger.Warn(
			"insecureSkipVerify is enabled: Model Registry TLS certificates " +
				"are not verified. This development-only option exposes the " +
				"connection to interception; do not enable it in production.",
		)
	}

	baseTransport, err := buildTransport(c)
	if err != nil {
		return nil, fmt.Errorf(
			"source %q: building HTTP transport: %w",
			settings.ID,
			err,
		)
	}

	transport, err := buildAuthTransport(settings, c, baseTransport)
	if err != nil {
		return nil, err
	}

	apiCfg := mrapi.NewConfiguration()

	// The generated client adds the Model Registry v1alpha3 API path to every
	// request. The configured server URL therefore contains only the registry
	// endpoint and any deployment-specific reverse-proxy prefix.
	apiCfg.Servers = mrapi.ServerConfigurations{{
		URL: strings.TrimRight(c.Endpoint, "/"),
	}}
	apiCfg.HTTPClient = &http.Client{
		Transport: transport,
		Timeout:   c.requestTimeout(),
	}

	client := &openapiClient{
		api:           mrapi.NewAPIClient(apiCfg).ModelRegistryServiceAPI,
		pageSize:      fmt.Sprintf("%d", c.pageSize()),
		modelFilter:   c.modelFilter(),
		versionFilter: c.versionFilter(),
	}

	metrics, err := newMetrics(
		settings.ID.String(),
		settings.MeterProvider,
	)
	if err != nil {
		return nil, fmt.Errorf(
			"source %q: creating metrics: %w",
			settings.ID,
			err,
		)
	}

	poller := pollsource.NewHelper(
		settings.ID.String(),
		c.pollInterval(),
		c.Backoff,
		settings.Logger,
		nil, // Use the production clock.
		nil, // Use production random jitter.
	)
	// The helper invokes OnCollect once per collection attempt, before the
	// snapshot reaches the downstream consumer, so the source counters,
	// duration histogram, and last-success timestamp describe Model Registry
	// collection alone. Downstream consumer failures still drive the helper's
	// backoff, but are never recorded as collection failures.
	poller.OnCollect = metrics.recordCollection

	return &source{
		catalog:           c.Catalog,
		collectionTimeout: c.collectionTimeout(),
		client:            client,
		poller:            poller,
		next:              next,
		log:               settings.Logger,
	}, nil
}

func buildAuthTransport(
	settings catalogcollector.Settings,
	cfg *Config,
	base http.RoundTripper,
) (http.RoundTripper, error) {
	if cfg.Auth == nil {
		return base, nil
	}
	if settings.Host == nil {
		return nil, fmt.Errorf(
			"source %q: auth.authenticator %q requires a host but none is available",
			settings.ID,
			cfg.Auth.Authenticator,
		)
	}

	authID, err := catalogcollector.ParseComponentID(
		cfg.Auth.Authenticator,
	)
	if err != nil {
		return nil, fmt.Errorf(
			"source %q: invalid auth.authenticator %q: %w",
			settings.ID,
			cfg.Auth.Authenticator,
			err,
		)
	}

	ext, err := settings.Host.GetExtension(authID)
	if err != nil {
		return nil, fmt.Errorf(
			"source %q: resolving authenticator %q: %w",
			settings.ID,
			authID,
			err,
		)
	}

	authClient, ok := ext.(extensionauth.HTTPClient)
	if !ok {
		return nil, fmt.Errorf(
			"source %q: extension %q does not implement extensionauth.HTTPClient",
			settings.ID,
			authID,
		)
	}

	transport, err := authClient.RoundTripper(base)
	if err != nil {
		return nil, fmt.Errorf(
			"source %q: building transport for authenticator %q: %w",
			settings.ID,
			authID,
			err,
		)
	}

	return transport, nil
}

// buildTransport constructs the TLS-aware base HTTP transport used by the
// source. Authentication, when configured, is layered on top of this
// transport.
func buildTransport(cfg *Config) (*http.Transport, error) {
	tlsConfig := &tls.Config{
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: cfg.InsecureSkipVerify, //nolint:gosec
	}

	if cfg.CertificateAuthority != "" {
		caCert, err := os.ReadFile(cfg.CertificateAuthority)
		if err != nil {
			return nil, fmt.Errorf(
				"reading CA certificate: %w",
				err,
			)
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

	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = tlsConfig
	transport.Proxy = http.ProxyFromEnvironment
	transport.ForceAttemptHTTP2 = true
	transport.MaxIdleConnsPerHost = 10

	return transport, nil
}

var (
	_ catalogcollector.SourceFactory = (*factory)(nil)
	_ config.Validator               = (*Config)(nil)
)
