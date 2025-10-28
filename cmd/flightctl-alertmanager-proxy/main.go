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
	"strings"
	"syscall"
	"time"

	"github.com/flightctl/flightctl/internal/api_server/middleware"
	"github.com/flightctl/flightctl/internal/auth"
	"github.com/flightctl/flightctl/internal/auth/common"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/crypto"
	"github.com/flightctl/flightctl/internal/instrumentation"
	"github.com/flightctl/flightctl/internal/org"
	"github.com/flightctl/flightctl/internal/org/cache"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/util"
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
	log          logrus.FieldLogger
	cfg          *config.Config
	proxy        *httputil.ReverseProxy
	target       *url.URL
	authDisabled bool
	authN        common.AuthNMiddleware
	authZ        auth.AuthZMiddleware
}

func NewAlertmanagerProxy(cfg *config.Config, log logrus.FieldLogger, authN common.AuthNMiddleware, authZ auth.AuthZMiddleware) (*AlertmanagerProxy, error) {
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

	authDisabled := false
	value, exists := os.LookupEnv(auth.DisableAuthEnvKey)
	if exists && value != "" {
		authDisabled = true
	}

	return &AlertmanagerProxy{
		log:          log,
		cfg:          cfg,
		proxy:        proxy,
		target:       target,
		authDisabled: authDisabled,
		authN:        authN,
		authZ:        authZ,
	}, nil
}

func ensureOrgIDFilter(r *http.Request) (uuid.UUID, error) {
	filters := r.URL.Query()["filter"]

	for _, filter := range filters {
		parts := strings.SplitN(filter, "=", 2)
		if len(parts) == 2 {
			key := strings.TrimSpace(parts[0])
			value := strings.TrimSpace(parts[1])

			if key == "org_id" {
				orgID, err := uuid.Parse(value)
				if err != nil {
					return uuid.Nil, fmt.Errorf("invalid org_id format %s: %w", orgID, err)
				}
				return orgID, nil
			}
		}
	}

	// If no org_id filter is found, inject the default into the query
	orgID := org.DefaultID

	q := r.URL.Query()
	q.Add("filter", fmt.Sprintf("org_id=%s", orgID.String()))
	r.URL.RawQuery = q.Encode()

	return orgID, nil
}

func (p *AlertmanagerProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Health check endpoint - doesn't require auth and doesn't depend on Alertmanager
	if r.URL.Path == healthPath {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("OK"))
		return
	}

	if p.authDisabled {
		p.proxy.ServeHTTP(w, r)
		return
	}

	// Extract bearer token from Authorization header using FlightControl's utility
	token, err := common.ExtractBearerToken(r)
	if err != nil {
		p.log.WithError(err).Error("Failed to extract bearer token")
		http.Error(w, "Invalid Authorization header", http.StatusUnauthorized)
		return
	}

	// Validate token using FlightControl's auth system
	if err := p.authN.ValidateToken(r.Context(), token); err != nil {
		p.log.WithError(err).Error("Token validation failed")
		http.Error(w, "Invalid token", http.StatusUnauthorized)
		return
	}

	// Status endpoint is not scoped to a specific org or user context, if the user has a valid token they can access it
	if r.URL.Path == statusPath {
		p.proxy.ServeHTTP(w, r)
		return
	}

	orgID, err := ensureOrgIDFilter(r)
	if err != nil {
		p.log.WithError(err).Error("Failed to validate organization ID")
		http.Error(w, "Invalid organization ID", http.StatusBadRequest)
		return
	}

	// Create context with necessary values for authorization checks
	ctx := context.WithValue(r.Context(), consts.TokenCtxKey, token)
	identity, err := p.authN.GetIdentity(ctx, token)
	if err != nil {
		p.log.WithError(err).Error("Failed to get identity")
		http.Error(w, "Invalid identity", http.StatusUnauthorized)
		return
	}
	ctx = context.WithValue(ctx, consts.IdentityCtxKey, identity)
	ctx = util.WithOrganizationID(ctx, orgID)

	// Check if user has permission to access alerts
	allowed, err := p.authZ.CheckPermission(ctx, alertsResource, getAction)
	if err != nil {
		p.log.WithError(err).Error("Authorization check failed")
		http.Error(w, "Authorization service unavailable", http.StatusServiceUnavailable)
		return
	}

	if !allowed {
		p.log.Warn("User denied access to alerts")
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	// Proxy the request to Alertmanager
	p.proxy.ServeHTTP(w, r)
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

	tracerShutdown := instrumentation.InitTracer(logger, cfg, "flightctl-alertmanager-proxy")
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

	orgCache := cache.NewOrganizationTTL(cache.DefaultTTL)
	go orgCache.Start()
	defer orgCache.Stop()

	// Initialize auth system
	authN, authZ, err := auth.InitMultiAuth(cfg, logger, nil)
	if err != nil {
		logger.Fatalf("Failed to initialize auth: %v", err)
	}

	// Create proxy
	proxy, err := NewAlertmanagerProxy(cfg, logger, authN, authZ)
	if err != nil {
		logger.Fatalf("Failed to create alertmanager proxy: %v", err)
	}

	if proxy.authDisabled {
		logger.Warn("Auth is disabled")
	}

	// Create router with logging middleware
	router := chi.NewRouter()
	router.Use(chimiddleware.RequestSize(int64(cfg.Service.HttpMaxRequestSize)))
	router.Use(middleware.RequestSizeLimiter(cfg.Service.HttpMaxUrlLength, cfg.Service.HttpMaxNumHeaders))
	router.Use(chimiddleware.RequestID)
	router.Use(chimiddleware.Recoverer)

	// Custom logging middleware that filters out health check noise
	router.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Skip logging for health checks to reduce noise
			if r.URL.Path == "/health" {
				next.ServeHTTP(w, r)
				return
			}
			// Use the default chi logger for all other requests
			chimiddleware.Logger(next).ServeHTTP(w, r)
		})
	})

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

	// Handle graceful shutdown
	ctx, cancel := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM, syscall.SIGQUIT)
	defer cancel()

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
