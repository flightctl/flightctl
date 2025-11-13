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
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"syscall"
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
	fclog "github.com/flightctl/flightctl/pkg/log"
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
	authN common.AuthNMiddleware,
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

func main() {
	ctx := context.Background()

	// Initialize logging
	logger := fclog.InitLogs()
	logger.Println("Starting Alertmanager Proxy service")
	defer logger.Println("Alertmanager Proxy service stopped")

	// Load configuration
	cfg, err := config.LoadOrGenerate(config.ConfigFile())
	if err != nil {
		logger.Fatalf("Failed to load configuration: %v", err)
	}

	// Set log level
	logLvl, err := logrus.ParseLevel(cfg.Service.LogLevel)
	if err != nil {
		logLvl = logrus.InfoLevel
	}
	logger.SetLevel(logLvl)

	// Initialize CA and TLS certificates (following same pattern as API server)
	ca, _, err := crypto.EnsureCA(cfg.CA)
	if err != nil {
		logger.Fatalf("ensuring CA cert: %v", err)
	}

	var serverCerts *crypto.TLSCertificateConfig

	// Reuse the same server certificate as the API server
	srvCertFile := crypto.CertStorePath(cfg.Service.ServerCertName+".crt", cfg.Service.CertStore)
	srvKeyFile := crypto.CertStorePath(cfg.Service.ServerCertName+".key", cfg.Service.CertStore)

	// Check if existing certificate is available
	if canReadCertAndKey, _ := crypto.CanReadCertAndKey(srvCertFile, srvKeyFile); canReadCertAndKey {
		serverCerts, err = crypto.GetTLSCertificateConfig(srvCertFile, srvKeyFile)
		if err != nil {
			logger.Fatalf("failed to load existing certificate: %v", err)
		}
	} else {
		// Create new certificate with same alt names as API server
		altNames := cfg.Service.AltNames
		if len(altNames) == 0 {
			altNames = []string{"localhost"}
		}

		serverCerts, err = ca.MakeAndWriteServerCertificate(ctx, srvCertFile, srvKeyFile, altNames, cfg.CA.ServerCertValidityDays)
		if err != nil {
			logger.Fatalf("failed to create certificate: %v", err)
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
		logger.Fatalf("failed creating TLS config: %v", err)
	}

	tracerShutdown := tracing.InitTracer(logger, cfg, "flightctl-alertmanager-proxy")
	defer func() {
		if err := tracerShutdown(ctx); err != nil {
			logger.Fatalf("Failed to shut down tracer: %v", err)
		}
	}()

	// Initialize data store
	db, err := store.InitDB(cfg, logger)
	if err != nil {
		logger.Fatalf("Initializing data store: %v", err)
	}

	dataStore := store.NewStore(db, logger.WithField("pkg", "store"))
	defer dataStore.Close()

	// Handle graceful shutdown
	ctx, cancel := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM, syscall.SIGQUIT)
	defer cancel()

	orgCache := cache.NewOrganizationTTL(cache.DefaultTTL)
	go func() {
		orgCache.Start(ctx)
		cancel() // Trigger coordinated shutdown if cache exits
	}()
	defer orgCache.Stop()

	// Create service handler for auth provider access
	serviceHandler := service.NewServiceHandler(dataStore, nil, nil, nil, logger, "", "", nil)

	// Initialize auth system
	authN, err := auth.InitMultiAuth(cfg, logger, serviceHandler)
	if err != nil {
		logger.Fatalf("Failed to initialize auth: %v", err)
	}

	// Start auth provider loader if MultiAuth is configured (not NilAuth)
	if multiAuth, ok := authN.(*authn.MultiAuth); ok {
		go func() {
			multiAuth.Start(ctx)
			cancel() // Trigger coordinated shutdown if auth loader exits
		}()
	}

	authZ, err := auth.InitMultiAuthZ(cfg, logger)
	if err != nil {
		logger.Fatalf("Failed to initialize authZ: %v", err)
	}

	// Create identity mapper for mapping identities to database objects
	identityMapper := service.NewIdentityMapper(dataStore, logger)
	go func() {
		identityMapper.Start(ctx)
		cancel() // Trigger coordinated shutdown if identity mapper exits
	}()
	defer identityMapper.Stop()
	identityMappingMiddleware := middleware.NewIdentityMappingMiddleware(identityMapper, logger)

	// Create organization extraction and validation middlewares
	extractOrgMiddleware := middleware.ExtractOrgIDToCtx(middleware.QueryOrgIDExtractor, logger)
	validateOrgMiddleware := middleware.ValidateOrgMembership(logger)

	// Create proxy
	proxy, err := NewAlertmanagerProxy(cfg, logger)
	if err != nil {
		logger.Fatalf("Failed to create alertmanager proxy: %v", err)
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
		logger.Fatalf("creating TLS listener: %v", err)
	}

	go func() {
		<-ctx.Done()
		logger.Println("Shutdown signal received")

		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer shutdownCancel()

		if err := server.Shutdown(shutdownCtx); err != nil {
			logger.Errorf("Server shutdown error: %v", err)
		}
	}()

	logger.Printf("Alertmanager proxy listening on https://%s, proxying to %s", proxyPort, proxy.target.String())
	if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
		logger.Fatalf("Server error: %v", err)
	}
}
