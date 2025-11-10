package pam_issuer_server

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"time"

	pamapi "github.com/flightctl/flightctl/api/v1alpha1/pam-issuer"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/crypto"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
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
