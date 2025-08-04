package apiserver

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"regexp"
	"strings"
	"time"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/api/server"
	fcmiddleware "github.com/flightctl/flightctl/internal/api_server/middleware"
	"github.com/flightctl/flightctl/internal/auth"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/console"
	"github.com/flightctl/flightctl/internal/crypto"
	"github.com/flightctl/flightctl/internal/instrumentation"
	"github.com/flightctl/flightctl/internal/kvstore"
	"github.com/flightctl/flightctl/internal/org"
	"github.com/flightctl/flightctl/internal/service"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/tasks_client"
	"github.com/flightctl/flightctl/internal/transport"
	"github.com/flightctl/flightctl/pkg/queues"
	"github.com/getkin/kin-openapi/openapi3"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	oapimiddleware "github.com/oapi-codegen/nethttp-middleware"
	"github.com/sirupsen/logrus"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

// customTransportHandler wraps the transport handler to exclude the auth validate endpoint
// since it's handled separately with stricter rate limiting
type customTransportHandler struct {
	*transport.TransportHandler
}

// AuthValidate is overridden to return 404 for this handler
// since the auth validate endpoint is handled separately
func (c *customTransportHandler) AuthValidate(w http.ResponseWriter, r *http.Request, params api.AuthValidateParams) {
	http.NotFound(w, r)
}

const (
	gracefulShutdownTimeout = 5 * time.Second
)

type Server struct {
	log                logrus.FieldLogger
	cfg                *config.Config
	store              store.Store
	ca                 *crypto.CAClient
	listener           net.Listener
	queuesProvider     queues.Provider
	metrics            *instrumentation.ApiMetrics
	consoleEndpointReg console.InternalSessionRegistration
	orgResolver        *org.Resolver
}

// New returns a new instance of a flightctl server.
func New(
	log logrus.FieldLogger,
	cfg *config.Config,
	st store.Store,
	ca *crypto.CAClient,
	listener net.Listener,
	queuesProvider queues.Provider,
	metrics *instrumentation.ApiMetrics,
	consoleEndpointReg console.InternalSessionRegistration,
) *Server {
	resolver := org.NewResolver(st.Organization(), 5*time.Minute)
	return &Server{
		log:                log,
		cfg:                cfg,
		store:              st,
		ca:                 ca,
		listener:           listener,
		queuesProvider:     queuesProvider,
		metrics:            metrics,
		consoleEndpointReg: consoleEndpointReg,
		orgResolver:        resolver,
	}
}

func oapiErrorHandler(w http.ResponseWriter, message string, statusCode int) {
	http.Error(w, fmt.Sprintf("API Error: %s", message), statusCode)
}

// If we got back multiple errors of the format:
// Error at "/path/to/invalid/input": ...
// then we don't want to return all of them because it will be too
// much information for the user. We assume that the error for the
// longest path will contain the most relevant error, so return only
// that one.
var pathRegex = regexp.MustCompile(`Error at \"/(.*)\":`)

func oapiMultiErrorHandler(errs openapi3.MultiError) (int, error) {
	if len(errs) == 0 {
		return http.StatusInternalServerError, nil
	}

	// Regex to extract the path inside quotes after "Error at "
	allMatchFormat := true
	var longestPathError error
	var longestPathErrorIndex int
	shortErrorMessages := make([]string, 0, len(errs))
	longestPathLength := -1

	for i, e := range errs {
		errMsg := e.Error()
		shortErr := strings.SplitN(errMsg, "\n", 2)[0] // Take everything until the first newline
		shortErrorMessages = append(shortErrorMessages, strings.TrimSpace(shortErr))

		matches := pathRegex.FindStringSubmatch(errMsg)
		if len(matches) < 2 {
			allMatchFormat = false
			break
		}

		// Extract the path and count the number of slashes
		path := matches[1]
		slashCount := strings.Count(path, "/")
		if slashCount > longestPathLength {
			longestPathError = e
			longestPathErrorIndex = i
			longestPathLength = slashCount
		}
	}

	if allMatchFormat && longestPathErrorIndex >= 0 {
		shortErrorMessages = append(shortErrorMessages[:longestPathErrorIndex], shortErrorMessages[longestPathErrorIndex+1:]...)
		response := fmt.Errorf("%d API errors found. The most relevant is likely:\n%s\nOther errors found were:\n%s",
			len(errs),
			longestPathError.Error(),
			strings.Join(shortErrorMessages, "\n"))
		return http.StatusBadRequest, response
	}

	// Default to returning the original errors
	return http.StatusBadRequest, errs
}

func (s *Server) Run(ctx context.Context) error {
	s.log.Println("Initializing async jobs")
	publisher, err := tasks_client.TaskQueuePublisher(s.queuesProvider)
	if err != nil {
		return err
	}
	kvStore, err := kvstore.NewKVStore(ctx, s.log, s.cfg.KV.Hostname, s.cfg.KV.Port, s.cfg.KV.Password)
	if err != nil {
		return err
	}
	callbackManager := tasks_client.NewCallbackManager(publisher, s.log)

	s.log.Println("Initializing API server")
	swagger, err := api.GetSwagger()
	if err != nil {
		return fmt.Errorf("failed loading swagger spec: %w", err)
	}
	// Skip server name validation
	swagger.Servers = nil

	oapiOpts := oapimiddleware.Options{
		ErrorHandler:      oapiErrorHandler,
		MultiErrorHandler: oapiMultiErrorHandler,
	}

	err = auth.InitAuth(s.cfg, s.log)
	if err != nil {
		return fmt.Errorf("failed initializing auth: %w", err)
	}

	router := chi.NewRouter()

	authMiddewares := []func(http.Handler) http.Handler{
		auth.CreateAuthNMiddleware(s.log),
		auth.CreateAuthZMiddleware(s.log),
	}

	// general middleware stack for all route groups
	// request size limits should come before logging to prevent DoS attacks from filling logs
	router.Use(
		middleware.RequestSize(int64(s.cfg.Service.HttpMaxRequestSize)),
		fcmiddleware.RequestSizeLimiter(s.cfg.Service.HttpMaxUrlLength, s.cfg.Service.HttpMaxNumHeaders),
		fcmiddleware.RequestID,
		fcmiddleware.AddEventMetadataToCtx,
		fcmiddleware.AddOrgIDToCtx(
			s.orgResolver,
			fcmiddleware.QueryOrgIDExtractor,
		),
		middleware.Logger,
		middleware.Recoverer,
	)

	serviceHandler := service.WrapWithTracing(service.NewServiceHandler(
		s.store, callbackManager, kvStore, s.ca, s.log, s.cfg.Service.BaseAgentEndpointUrl, s.cfg.Service.BaseUIUrl))

	// a group is a new mux copy, with its own copy of the middleware stack
	// this one handles the OpenAPI handling of the service (excluding auth validate endpoint)
	router.Group(func(r chi.Router) {
		//NOTE(majopela): keeping metrics middleware separate from the rest of the middleware stack
		// to avoid issues with websocket connections
		if s.metrics != nil {
			r.Use(s.metrics.ApiServerMiddleware)
		}
		r.Use(oapimiddleware.OapiRequestValidatorWithOptions(swagger, &oapiOpts))
		r.Use(authMiddewares...)
		// Add general rate limiting (only if configured)
		if s.cfg.Service.RateLimit != nil {
			trustedProxies := s.cfg.Service.RateLimit.TrustedProxies
			requests := 60        // Default requests limit
			window := time.Minute // Default window
			if s.cfg.Service.RateLimit.Requests > 0 {
				requests = s.cfg.Service.RateLimit.Requests
			}
			if s.cfg.Service.RateLimit.Window > 0 {
				window = time.Duration(s.cfg.Service.RateLimit.Window)
			}
			fcmiddleware.InstallRateLimiter(r, fcmiddleware.RateLimitOptions{
				Requests:       requests,
				Window:         window,
				Message:        "Rate limit exceeded, please try again later",
				TrustedProxies: trustedProxies,
			})
		}

		h := transport.NewTransportHandler(serviceHandler)

		// Register all other endpoints with general rate limiting (already applied at router level)
		// Create a custom handler that excludes the auth validate endpoint
		customHandler := &customTransportHandler{h}
		server.HandlerFromMux(customHandler, r)
	})

	// Register auth validate endpoint with stricter rate limiting (outside main API group)
	// This ensures it gets all the necessary middleware with stricter rate limiting
	router.Group(func(r chi.Router) {
		// Add conditional middleware
		if s.metrics != nil {
			r.Use(s.metrics.ApiServerMiddleware)
		}
		r.Use(oapimiddleware.OapiRequestValidatorWithOptions(swagger, &oapiOpts))
		r.Use(authMiddewares...)

		// Add auth-specific rate limiting (only if configured)
		if s.cfg.Service.RateLimit != nil {
			trustedProxies := s.cfg.Service.RateLimit.TrustedProxies
			authRequests := 10      // Default auth requests limit
			authWindow := time.Hour // Default auth window
			if s.cfg.Service.RateLimit.AuthRequests > 0 {
				authRequests = s.cfg.Service.RateLimit.AuthRequests
			}
			if s.cfg.Service.RateLimit.AuthWindow > 0 {
				authWindow = time.Duration(s.cfg.Service.RateLimit.AuthWindow)
			}
			fcmiddleware.InstallRateLimiter(r, fcmiddleware.RateLimitOptions{
				Requests:       authRequests,
				Window:         authWindow,
				Message:        "Login rate limit exceeded, please try again later",
				TrustedProxies: trustedProxies,
			})
		}

		h := transport.NewTransportHandler(serviceHandler)
		// Use the wrapper to handle the AuthValidate method signature
		wrapper := &server.ServerInterfaceWrapper{
			Handler:            h,
			HandlerMiddlewares: nil,
			ErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, err error) {
				oapiErrorHandler(w, err.Error(), http.StatusBadRequest)
			},
		}
		r.Get("/api/v1/auth/validate", wrapper.AuthValidate)
	})

	// ws handling
	router.Group(func(r chi.Router) {
		r.Use(fcmiddleware.CreateRouteExistsMiddleware(r))
		r.Use(authMiddewares...)
		// Add websocket rate limiting (only if configured)
		if s.cfg.Service.RateLimit != nil {
			trustedProxies := s.cfg.Service.RateLimit.TrustedProxies
			requests := 60        // Default requests limit
			window := time.Minute // Default window
			if s.cfg.Service.RateLimit.Requests > 0 {
				requests = s.cfg.Service.RateLimit.Requests
			}
			if s.cfg.Service.RateLimit.Window > 0 {
				window = time.Duration(s.cfg.Service.RateLimit.Window)
			}
			fcmiddleware.InstallRateLimiter(r, fcmiddleware.RateLimitOptions{
				Requests:       requests,
				Window:         window,
				Message:        "Rate limit exceeded, please try again later",
				TrustedProxies: trustedProxies,
			})
		}

		consoleSessionManager := console.NewConsoleSessionManager(serviceHandler, s.log, s.consoleEndpointReg)
		ws := transport.NewWebsocketHandler(s.ca, s.log, consoleSessionManager)
		ws.RegisterRoutes(r)
	})

	handler := otelhttp.NewHandler(router, "http-server")
	srv := fcmiddleware.NewHTTPServer(handler, s.log, s.cfg.Service.Address, s.cfg)

	go func() {
		<-ctx.Done()
		s.log.Println("Shutdown signal received:", ctx.Err())
		ctxTimeout, cancel := context.WithTimeout(context.Background(), gracefulShutdownTimeout)
		defer cancel()

		srv.SetKeepAlivesEnabled(false)
		_ = srv.Shutdown(ctxTimeout)
		kvStore.Close()
		s.orgResolver.Close()
		s.queuesProvider.Stop()
		s.queuesProvider.Wait()
	}()

	s.log.Printf("Listening on %s...", s.listener.Addr().String())
	if err := srv.Serve(s.listener); err != nil && !errors.Is(err, net.ErrClosed) {
		return err
	}

	return nil
}
