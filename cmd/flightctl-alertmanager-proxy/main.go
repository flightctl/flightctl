// flightctl-alertmanager-proxy is a lightweight reverse proxy for Alertmanager that
// integrates with FlightControl's existing authentication and authorization system.
//
// It validates bearer tokens and authorizes requests before proxying them to
// Alertmanager running on localhost:9093. Users must have "get" access to the
// "alerts" resource to access the proxy.
//
// The proxy listens on port 8443 with HTTPS and requires:
// - Authorization: Bearer <token> header
//
// This works with all Flight Control auth types: OIDC, OpenShift, and AAP.
package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"time"

	"github.com/flightctl/flightctl/internal/api_server/middleware"
	"github.com/flightctl/flightctl/internal/auth"
	"github.com/flightctl/flightctl/internal/auth/authn"
	"github.com/flightctl/flightctl/internal/auth/common"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/crypto"
	"github.com/flightctl/flightctl/internal/instrumentation/tracing"
	"github.com/flightctl/flightctl/internal/org/cache"
	"github.com/flightctl/flightctl/internal/service"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/shutdown"
	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/sirupsen/logrus"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

const (
	proxyPort              = ":8443"
	defaultAlertmanagerURL = "http://localhost:9093"
	alertsResource         = "alerts"
	getAction              = "get"
	healthPath             = "/health"
	statusPath             = "/api/v2/status"
)

type AlertmanagerProxy struct {
	log    logrus.FieldLogger
	cfg    *config.Config
	proxy  *httputil.ReverseProxy
	target *url.URL
}

func NewAlertmanagerProxy(cfg *config.Config, log logrus.FieldLogger) (*AlertmanagerProxy, error) {
	// Get alertmanager URL from environment or use default
	alertmanagerURL := os.Getenv("ALERTMANAGER_URL")
	if alertmanagerURL == "" {
		alertmanagerURL = defaultAlertmanagerURL
	}

	target, err := url.Parse(alertmanagerURL)
	if err != nil {
		return nil, fmt.Errorf("failed to parse alertmanager URL: %w", err)
	}

	proxy := httputil.NewSingleHostReverseProxy(target)

	return &AlertmanagerProxy{
		log:    log,
		cfg:    cfg,
		proxy:  proxy,
		target: target,
	}, nil
}

func (p *AlertmanagerProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Health check endpoint - doesn't require auth and doesn't depend on Alertmanager
	if r.URL.Path == healthPath {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("OK"))
		return
	}

	// Status endpoint is not scoped to a specific org or user context, if the user has a valid token they can access it
	// (authentication and identity mapping are handled by middleware, but we skip org validation and authZ)
	if r.URL.Path == statusPath {
		p.proxy.ServeHTTP(w, r)
		return
	}

	// For all other endpoints, authentication, identity mapping, organization extraction/validation,
	// and authorization are handled by the middleware chain
	p.proxy.ServeHTTP(w, r)
}

// createConditionalAuthMiddleware creates a middleware that conditionally applies authentication
// and authorization based on the request path:
// - /health and /api/v2/status: skip auth (just continue)
// - all other endpoints: full auth chain (auth -> identity mapping -> org extract -> org validate -> authZ)
func createConditionalAuthMiddleware(
	authN common.MultiAuthNMiddleware,
	authZ auth.AuthZMiddleware,
	identityMappingMiddleware *middleware.IdentityMappingMiddleware,
	extractOrgMiddleware func(http.Handler) http.Handler,
	validateOrgMiddleware func(http.Handler) http.Handler,
	logger logrus.FieldLogger,
) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		// Pre-create the full auth chain once
		fullAuthHandler := auth.CreateAuthNMiddleware(authN, logger)(
			identityMappingMiddleware.MapIdentityToDB(
				extractOrgMiddleware(
					validateOrgMiddleware(
						http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
							// Check if user has permission to access alerts
							allowed, err := authZ.CheckPermission(r.Context(), alertsResource, getAction)
							if err != nil {
								logger.WithError(err).Error("Authorization check failed")
								http.Error(w, "Authorization service unavailable", http.StatusServiceUnavailable)
								return
							}

							if !allowed {
								logger.Warn("User denied access to alerts")
								http.Error(w, "Forbidden", http.StatusForbidden)
								return
							}

							next.ServeHTTP(w, r)
						}),
					),
				),
			),
		)

		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Skip auth for health and status endpoints
			if r.URL.Path == healthPath || r.URL.Path == statusPath {
				next.ServeHTTP(w, r)
				return
			}

			// For all other endpoints - apply full auth chain
			fullAuthHandler.ServeHTTP(w, r)
		})
	}
}

// setupCertificates initializes CA and TLS certificates following same pattern as API server
func setupCertificates(ctx context.Context, cfg *config.Config, logger logrus.FieldLogger) (*crypto.CAClient, *tls.Config, error) {
	// Initialize CA and TLS certificates (following same pattern as API server)
	ca, _, err := crypto.EnsureCA(cfg.CA)
	if err != nil {
		return nil, nil, fmt.Errorf("ensuring CA cert: %w", err)
	}

	var serverCerts *crypto.TLSCertificateConfig

	// Reuse the same server certificate as the API server
	srvCertFile := crypto.CertStorePath(cfg.Service.ServerCertName+".crt", cfg.Service.CertStore)
	srvKeyFile := crypto.CertStorePath(cfg.Service.ServerCertName+".key", cfg.Service.CertStore)

	// Check if existing certificate is available
	if canReadCertAndKey, _ := crypto.CanReadCertAndKey(srvCertFile, srvKeyFile); canReadCertAndKey {
		serverCerts, err = crypto.GetTLSCertificateConfig(srvCertFile, srvKeyFile)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to load existing certificate: %w", err)
		}
	} else {
		// Create new certificate with same alt names as API server
		altNames := cfg.Service.AltNames
		if len(altNames) == 0 {
			altNames = []string{"localhost"}
		}

		serverCerts, err = ca.MakeAndWriteServerCertificate(ctx, srvCertFile, srvKeyFile, altNames, cfg.CA.ServerCertValidityDays)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to create certificate: %w", err)
		}
	}

	// Check for expired certificate
	for _, x509Cert := range serverCerts.Certs {
		expired := time.Now().After(x509Cert.NotAfter)
		logger.Printf("checking certificate: subject='%s', issuer='%s', expiry='%v'",
			x509Cert.Subject.CommonName, x509Cert.Issuer.CommonName, x509Cert.NotAfter)

		if expired {
			logger.Warnf("server certificate for '%s' issued by '%s' has expired on: %v",
				x509Cert.Subject.CommonName, x509Cert.Issuer.CommonName, x509Cert.NotAfter)
		}
	}

	// Create TLS config
	tlsConfig, _, err := crypto.TLSConfigForServer(ca.GetCABundleX509(), serverCerts)
	if err != nil {
		return nil, nil, fmt.Errorf("failed creating TLS config: %w", err)
	}

	return ca, tlsConfig, nil
}

// createServers sets up all server specifications for multi-server coordination
func createServers(cfg *config.Config, logger logrus.FieldLogger, dataStore store.Store, orgCache *cache.OrganizationTTLCache, authN common.MultiAuthNMiddleware, authZ auth.AuthZMiddleware, identityMapper *service.IdentityMapper, tlsConfig *tls.Config) []shutdown.ServerSpec {
	var servers []shutdown.ServerSpec

	// Add organization cache server
	servers = append(servers, shutdown.ServerSpec{
		Name: "organization cache",
		Runner: func(shutdownCtx context.Context) error {
			orgCache.Start(shutdownCtx)
			return nil
		},
	})

	// Add auth provider loader if MultiAuth is configured
	if multiAuth, ok := authN.(*authn.MultiAuth); ok {
		servers = append(servers, shutdown.ServerSpec{
			Name: "auth provider loader",
			Runner: func(shutdownCtx context.Context) error {
				return multiAuth.Start(shutdownCtx)
			},
		})
	}

	// Add identity mapper server
	servers = append(servers, shutdown.ServerSpec{
		Name: "identity mapper",
		Runner: func(shutdownCtx context.Context) error {
			identityMapper.Start(shutdownCtx)
			return nil
		},
	})

	// Create main HTTP server
	servers = append(servers, shutdown.ServerSpec{
		Name: "alertmanager proxy server",
		Runner: func(shutdownCtx context.Context) error {
			return runAlertmanagerProxyServer(shutdownCtx, cfg, logger, dataStore, authN, authZ, identityMapper, tlsConfig)
		},
	})

	return servers
}

// runAlertmanagerProxyServer handles the main HTTP server logic
func runAlertmanagerProxyServer(shutdownCtx context.Context, cfg *config.Config, logger logrus.FieldLogger, dataStore store.Store, authN common.MultiAuthNMiddleware, authZ auth.AuthZMiddleware, identityMapper *service.IdentityMapper, tlsConfig *tls.Config) error {
	// Start multiAuthZ synchronously (it's not a continuous service)
	if multiAuthZ, ok := authZ.(*auth.MultiAuthZ); ok {
		multiAuthZ.Start(shutdownCtx)
		logger.Debug("Started MultiAuthZ with context-based cache lifecycle")
	}

	identityMappingMiddleware := middleware.NewIdentityMappingMiddleware(identityMapper, logger)

	// Create organization extraction and validation middlewares
	extractOrgMiddleware := middleware.ExtractOrgIDToCtx(middleware.QueryOrgIDExtractor, logger)
	validateOrgMiddleware := middleware.ValidateOrgMembership(logger)

	// Create proxy
	proxy, err := NewAlertmanagerProxy(cfg, logger)
	if err != nil {
		return fmt.Errorf("failed to create alertmanager proxy: %w", err)
	}

	// Check if auth is disabled
	authDisabled := false
	value, exists := os.LookupEnv(auth.DisableAuthEnvKey)
	if exists && value != "" {
		authDisabled = true
		logger.Warn("Auth is disabled")
	}

	// Create conditional auth middleware
	conditionalAuthMiddleware := createConditionalAuthMiddleware(
		authN,
		authZ,
		identityMappingMiddleware,
		extractOrgMiddleware,
		validateOrgMiddleware,
		logger,
	)

	// Create router with base middleware
	router := chi.NewRouter()
	router.Use(chimiddleware.RequestSize(int64(cfg.Service.HttpMaxRequestSize)))
	router.Use(middleware.RequestSizeLimiter(cfg.Service.HttpMaxUrlLength, cfg.Service.HttpMaxNumHeaders))
	router.Use(chimiddleware.RequestID)
	router.Use(chimiddleware.Recoverer)

	// Custom logging middleware that filters out health check noise
	router.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Skip logging for health checks to reduce noise
			if r.URL.Path == healthPath {
				next.ServeHTTP(w, r)
				return
			}
			// Use the default chi logger for all other requests
			chimiddleware.Logger(next).ServeHTTP(w, r)
		})
	})

	// Apply conditional auth middleware (unless auth is disabled)
	if !authDisabled {
		router.Use(conditionalAuthMiddleware)
	}

	// Add rate limiting (only if configured)
	// Alertmanager doesn't have built-in rate limiting, so we add it here to prevent abuse
	if cfg.Service.RateLimit != nil {
		trustedProxies := cfg.Service.RateLimit.TrustedProxies
		requests := 60        // Default requests limit
		window := time.Minute // Default window
		if cfg.Service.RateLimit.Requests > 0 {
			requests = cfg.Service.RateLimit.Requests
		}
		if cfg.Service.RateLimit.Window > 0 {
			window = time.Duration(cfg.Service.RateLimit.Window)
		}
		middleware.InstallRateLimiter(router, middleware.RateLimitOptions{
			Requests:       requests,
			Window:         window,
			Message:        "Alertmanager proxy rate limit exceeded, please try again later",
			TrustedProxies: trustedProxies,
		})
	}

	router.Mount("/", proxy)

	// Wrap router with OpenTelemetry handler to enable tracing spans
	handler := otelhttp.NewHandler(router, "alertmanager-proxy-http-server")
	// Create HTTPS server using FlightControl's TLS middleware
	server := middleware.NewHTTPServer(handler, logger, proxyPort, cfg)

	// Create TLS listener
	listener, err := middleware.NewTLSListener(proxyPort, tlsConfig)
	if err != nil {
		return fmt.Errorf("creating TLS listener: %w", err)
	}

	logger.Printf("Alertmanager proxy listening on port %s, proxying to %s", proxyPort[1:], proxy.target.String())

	// Handle graceful shutdown
	shutdownDone := make(chan error, 1)
	serverErr := make(chan error, 1)
	go func() {
		select {
		case <-shutdownCtx.Done():
		case <-serverErr:
			shutdownDone <- nil // Signal completion so main path doesn't block
			return
		}
		logger.Println("Shutdown signal received")

		serverShutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer shutdownCancel()

		if err := server.Shutdown(serverShutdownCtx); err != nil {
			shutdownDone <- fmt.Errorf("server shutdown error: %w", err)
		} else {
			shutdownDone <- nil
		}
	}()

	// Start server
	if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
		close(serverErr) // Signal goroutine to exit
		return fmt.Errorf("server error: %w", err)
	}

	// Wait for graceful shutdown completion
	return <-shutdownDone
}

func main() {
	ctx := context.Background()

	// Load configuration
	cfg, err := config.LoadOrGenerate(config.ConfigFile())
	if err != nil {
		log.InitLogs().WithError(err).Fatal("Failed to load configuration")
	}

	// Initialize logging
	logger := log.InitLogs(cfg.Service.LogLevel)
	logger.Println("Starting Alertmanager Proxy service")
	defer logger.Println("Alertmanager Proxy service stopped")
	logger.Infof("Using config: %s", cfg)

	// Initialize CA and TLS certificates
	_, tlsConfig, err := setupCertificates(ctx, cfg, logger)
	if err != nil {
		logger.Fatalf("Setting up certificates: %v", err)
	}

	// Initialize tracer
	tracerShutdown := tracing.InitTracer(logger, cfg, "flightctl-alertmanager-proxy")
	if tracerShutdown != nil {
		defer func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := tracerShutdown(ctx); err != nil {
				logger.WithError(err).Error("Error shutting down tracer")
			}
		}()
	}

	// Initialize data store
	db, err := store.InitDB(cfg, logger)
	if err != nil {
		logger.Fatalf("Initializing data store: %v", err)
	}

	dataStore := store.NewStore(db, logger.WithField("pkg", "store"))
	defer dataStore.Close()

	orgCache := cache.NewOrganizationTTL(cache.DefaultTTL)
	defer orgCache.Stop()

	// Create service handler for auth provider access
	baseServiceHandler := service.NewServiceHandler(dataStore, nil, nil, nil, logger, "", "", nil)
	serviceHandler := service.WrapWithTracing(baseServiceHandler)

	// Initialize auth system
	authN, err := auth.InitMultiAuth(cfg, logger, serviceHandler)
	if err != nil {
		logger.Fatalf("Failed to initialize auth: %v", err)
	}

	authZ, err := auth.InitMultiAuthZ(cfg, logger)
	if err != nil {
		logger.Fatalf("Failed to initialize authZ: %v", err)
	}

	identityMapper := service.NewIdentityMapper(dataStore, logger)
	defer identityMapper.Stop()

	// Create servers and run with multi-server coordination
	servers := createServers(cfg, logger, dataStore, orgCache, authN, authZ, identityMapper, tlsConfig)
	multiServerConfig := shutdown.NewMultiServerConfig("alertmanager proxy", logger)
	if err := multiServerConfig.RunMultiServer(ctx, servers); err != nil {
		logger.Fatalf("Alertmanager proxy error: %v", err)
	}
}
