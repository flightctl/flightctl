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
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/flightctl/flightctl/internal/api_server/middleware"
	"github.com/flightctl/flightctl/internal/auth"
	"github.com/flightctl/flightctl/internal/auth/common"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/instrumentation/tracing"
	"github.com/flightctl/flightctl/internal/org/cache"
	"github.com/flightctl/flightctl/internal/service"
	"github.com/flightctl/flightctl/internal/store"
	fclog "github.com/flightctl/flightctl/pkg/log"
	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
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

// extractOrgIDFromFiltersQuery extracts organization ID from the filter query parameter.
// It looks for filter parameters in the format "org_id=<uuid>".
// Returns (orgID, true, nil) if found, (uuid.Nil, false, nil) if not found, or (uuid.Nil, false, error) on parse error.
func extractOrgIDFromFiltersQuery(ctx context.Context, r *http.Request) (uuid.UUID, bool, error) {
	filters := r.URL.Query()["filter"]

	for _, filter := range filters {
		parts := strings.SplitN(filter, "=", 2)
		if len(parts) == 2 {
			key := strings.TrimSpace(parts[0])
			value := strings.TrimSpace(parts[1])

			if key == "org_id" {
				orgID, err := uuid.Parse(value)
				if err != nil {
					return uuid.Nil, false, fmt.Errorf("invalid org_id format in filter: %w", err)
				}
				return orgID, true, nil
			}
		}
	}

	return uuid.Nil, false, nil
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
// - all other endpoints: full auth chain (auth -> identity mapping -> org -> authZ)
func createConditionalAuthMiddleware(
	authN common.MultiAuthNMiddleware,
	authZ auth.AuthZMiddleware,
	identityMappingMiddleware *middleware.IdentityMappingMiddleware,
	orgMiddleware func(http.Handler) http.Handler,
	logger logrus.FieldLogger,
) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		// Pre-create the full auth chain once
		fullAuthHandler := auth.CreateAuthNMiddleware(authN, logger)(
			identityMappingMiddleware.MapIdentityToDB(
				orgMiddleware(
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

	// Load configuration
	cfg, err := config.LoadOrGenerate(config.ConfigFile())
	if err != nil {
		logger := fclog.InitLogs()
		logger.Fatalf("Failed to load configuration: %v", err)
	}

	// Initialize logging with level from config
	logger := fclog.InitLogs(cfg.Service.LogLevel)
	logger.Println("Starting Alertmanager Proxy service")
	defer logger.Println("Alertmanager Proxy service stopped")

	serverCerts, err := config.LoadServerCertificates(cfg, logger)
	if err != nil {
		logger.Fatalf("loading server certificates: %v", err)
	}

	certBytes, keyBytes, err := serverCerts.GetPEMBytes()
	if err != nil {
		logger.Fatalf("failed getting certificate bytes: %v", err)
	}

	cert, err := tls.X509KeyPair(certBytes, keyBytes)
	if err != nil {
		logger.Fatalf("failed creating certificate pair: %v", err)
	}

	tlsConfig := &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS13,
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
	baseServiceHandler := service.NewServiceHandler(dataStore, nil, nil, nil, logger, "", "", nil)
	serviceHandler := service.WrapWithTracing(baseServiceHandler)

	// Initialize auth system
	authN, err := auth.InitMultiAuth(cfg, logger, serviceHandler)
	if err != nil {
		logger.Fatalf("Failed to initialize auth: %v", err)
	}

	// Start auth provider loader
	go func() {
		if err := authN.Start(ctx); err != nil {
			logger.Errorf("Failed to start auth provider loader: %v", err)
		}
		cancel() // Trigger coordinated shutdown if auth loader exits
	}()

	authZ, err := auth.InitMultiAuthZ(cfg, logger)
	if err != nil {
		logger.Fatalf("Failed to initialize authZ: %v", err)
	}

	// Start multiAuthZ to initialize cache lifecycle management
	if multiAuthZ, ok := authZ.(*auth.MultiAuthZ); ok {
		multiAuthZ.Start(ctx)
		logger.Debug("Started MultiAuthZ with context-based cache lifecycle")
	}

	// Create identity mapper for mapping identities to database objects
	identityMapper := service.NewIdentityMapper(dataStore, logger)
	go func() {
		identityMapper.Start(ctx)
		cancel() // Trigger coordinated shutdown if identity mapper exits
	}()
	defer identityMapper.Stop()
	identityMappingMiddleware := middleware.NewIdentityMappingMiddleware(identityMapper, logger)

	// Create organization extraction and validation middleware
	orgMiddleware := middleware.ExtractAndValidateOrg(extractOrgIDFromFiltersQuery, logger)

	// Create proxy
	proxy, err := NewAlertmanagerProxy(cfg, logger)
	if err != nil {
		logger.Fatalf("Failed to create alertmanager proxy: %v", err)
	}

	// Create conditional auth middleware
	conditionalAuthMiddleware := createConditionalAuthMiddleware(
		authN,
		authZ,
		identityMappingMiddleware,
		orgMiddleware,
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

	// Apply auth middleware
	router.Use(conditionalAuthMiddleware)

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

	logger.Printf("Alertmanager proxy listening on port %s, proxying to %s", proxyPort[1:], proxy.target.String())
	if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
		logger.Fatalf("Server error: %v", err)
	}
}
