package pam_issuer_server

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"time"

	pamapi "github.com/flightctl/flightctl/api/v1beta1/pam-issuer"
	fcmiddleware "github.com/flightctl/flightctl/internal/api_server/middleware"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/crypto"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/httprate"
	oapimiddleware "github.com/oapi-codegen/nethttp-middleware"
	"github.com/sirupsen/logrus"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

const (
	gracefulShutdownTimeout = 5 * time.Second
)

type Server struct {
	log      logrus.FieldLogger
	cfg      *config.Config
	ca       *crypto.CAClient
	listener net.Listener
	handler  *Handler
}

// New returns a new instance of a PAM issuer server.
func New(
	log logrus.FieldLogger,
	cfg *config.Config,
	ca *crypto.CAClient,
	listener net.Listener,
) *Server {
	return &Server{
		log:      log,
		cfg:      cfg,
		ca:       ca,
		listener: listener,
	}
}

func oapiErrorHandler(w http.ResponseWriter, message string, statusCode int) {
	http.Error(w, fmt.Sprintf("API Error: %s", message), statusCode)
}

// installOAuth2RateLimiter installs a rate limiter that returns OAuth2-compliant error responses
func installOAuth2RateLimiter(r chi.Router, opts fcmiddleware.RateLimitOptions) {
	// 1) Only trust X-Forwarded-For/X-Real-IP when the immediate peer is in one of these CIDRs
	if len(opts.TrustedProxies) > 0 {
		r.Use(fcmiddleware.TrustedRealIP(opts.TrustedProxies))
	}

	// 2) Build a limiter: N req per window, keyed by client IP
	limiter := httprate.Limit(
		opts.Requests, // e.g. 300
		opts.Window,   // e.g. time.Minute
		httprate.WithKeyFuncs( // bucket by r.RemoteAddr (after RealIP)
			httprate.KeyByIP,
		),
		httprate.WithLimitHandler(func(w http.ResponseWriter, r *http.Request) {
			// Return OAuth2 error format (RFC 6749 Section 5.2)
			oauth2Error := &pamapi.OAuth2Error{
				Code:             pamapi.TemporarilyUnavailable,
				ErrorDescription: &opts.Message,
			}

			// emit headers + JSON
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Retry-After", strconv.Itoa(int(opts.Window.Seconds())))
			// 429 is semantically correct for rate limiting (RFC 6585)
			// OAuth2 RFC 6749 allows "appropriate HTTP status codes"
			w.WriteHeader(http.StatusTooManyRequests)
			_ = json.NewEncoder(w).Encode(oauth2Error)
		}),
	)

	// 3) Register it for all routes
	r.Use(limiter)
}

func (s *Server) Run(ctx context.Context) error {
	s.log.Println("Initializing PAM issuer server")

	// Load swagger spec
	swagger, err := pamapi.GetSwagger()
	if err != nil {
		return fmt.Errorf("failed loading swagger spec: %w", err)
	}
	// Skip server name validation
	swagger.Servers = nil
	// Skip OpenAPI security validation - the AuthUserInfo handler validates tokens itself
	swagger.Components.SecuritySchemes = nil
	// Remove security requirements from all paths so middleware doesn't enforce them
	for _, pathItem := range swagger.Paths.Map() {
		if pathItem.Get != nil {
			pathItem.Get.Security = nil
		}
		if pathItem.Post != nil {
			pathItem.Post.Security = nil
		}
	}

	oapiOpts := oapimiddleware.Options{
		ErrorHandler: oapiErrorHandler,
	}

	// Create PAM OIDC provider
	handler, err := NewHandler(s.log, s.cfg, s.ca)
	if err != nil {
		return fmt.Errorf("failed to create PAM issuer handler: %w", err)
	}
	s.handler = handler

	// Start background cleanup goroutine
	if err := handler.Run(ctx); err != nil {
		return fmt.Errorf("failed to start handler: %w", err)
	}

	router := chi.NewRouter()

	// Add middlewares
	router.Use(
		middleware.RequestID,
		middleware.Logger,
		middleware.Recoverer,
		middleware.Timeout(60*time.Second),
	)

	// OpenAPI validation middleware
	router.Use(oapimiddleware.OapiRequestValidatorWithOptions(swagger, &oapiOpts))

	// Add rate limiting (only if configured and enabled)
	// Uses OAuth2-compliant error format (RFC 6749 Section 5.2)
	if s.cfg.Service.RateLimit != nil && s.cfg.Service.RateLimit.Enabled {
		trustedProxies := s.cfg.Service.RateLimit.TrustedProxies
		requests := 300       // Default requests limit
		window := time.Minute // Default window
		if s.cfg.Service.RateLimit.Requests > 0 {
			requests = s.cfg.Service.RateLimit.Requests
		}
		if s.cfg.Service.RateLimit.Window > 0 {
			window = time.Duration(s.cfg.Service.RateLimit.Window)
		}
		installOAuth2RateLimiter(router, fcmiddleware.RateLimitOptions{
			Requests:       requests,
			Window:         window,
			Message:        "Rate limit exceeded, please try again later",
			TrustedProxies: trustedProxies,
		})
	}

	// Register PAM issuer handler
	pamapi.HandlerFromMux(handler, router)

	// Wrap with OpenTelemetry
	httpHandler := otelhttp.NewHandler(router, "pam-issuer")

	httpServer := &http.Server{
		Addr:              s.listener.Addr().String(),
		Handler:           httpHandler,
		ReadHeaderTimeout: 32 * time.Second,
	}

	idleConnsClosed := make(chan struct{})
	go func() {
		<-ctx.Done()
		s.log.Println("Shutting down PAM issuer server")

		ctx, cancel := context.WithTimeout(context.Background(), gracefulShutdownTimeout)
		defer cancel()

		if err := httpServer.Shutdown(ctx); err != nil {
			s.log.Printf("HTTP server shutdown error: %v", err)
		}

		// Cleanup
		if s.handler != nil {
			s.handler.Close()
		}

		close(idleConnsClosed)
	}()

	s.log.Printf("PAM issuer server listening on %s", s.listener.Addr().String())
	if err := httpServer.Serve(s.listener); err != http.ErrServerClosed {
		return fmt.Errorf("HTTP server error: %w", err)
	}

	<-idleConnsClosed
	s.log.Println("PAM issuer server stopped")
	return nil
}
