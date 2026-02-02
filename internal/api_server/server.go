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

	corev1alpha1 "github.com/flightctl/flightctl/api/core/v1alpha1"
	corev1beta1 "github.com/flightctl/flightctl/api/core/v1beta1"
	convertv1alpha1 "github.com/flightctl/flightctl/internal/api/convert/v1alpha1"
	convertv1beta1 "github.com/flightctl/flightctl/internal/api/convert/v1beta1"
	"github.com/flightctl/flightctl/internal/api/server"
	serverv1alpha1 "github.com/flightctl/flightctl/internal/api/server/v1alpha1"
	fcmiddleware "github.com/flightctl/flightctl/internal/api_server/middleware"
	"github.com/flightctl/flightctl/internal/api_server/versioning"
	"github.com/flightctl/flightctl/internal/auth"
	"github.com/flightctl/flightctl/internal/auth/authn"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/console"
	"github.com/flightctl/flightctl/internal/crypto"
	"github.com/flightctl/flightctl/internal/kvstore"
	"github.com/flightctl/flightctl/internal/service"
	"github.com/flightctl/flightctl/internal/store"
	transportv1alpha1 "github.com/flightctl/flightctl/internal/transport/v1alpha1"
	transportv1beta1 "github.com/flightctl/flightctl/internal/transport/v1beta1"
	"github.com/flightctl/flightctl/internal/worker_client"
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
	*transportv1beta1.TransportHandler
}

// AuthValidate is overridden to return 404 for this handler
// since the auth validate endpoint is handled separately
func (c *customTransportHandler) AuthValidate(w http.ResponseWriter, r *http.Request, params corev1beta1.AuthValidateParams) {
	http.NotFound(w, r)
}

type Server struct {
	log                logrus.FieldLogger
	cfg                *config.Config
	store              store.Store
	ca                 *crypto.CAClient
	listener           net.Listener
	queuesProvider     queues.Provider
	consoleEndpointReg console.InternalSessionRegistration
	authN              *authn.MultiAuth
	authZ              auth.AuthZMiddleware
}

// New returns a new instance of a flightctl server.
func New(
	log logrus.FieldLogger,
	cfg *config.Config,
	st store.Store,
	ca *crypto.CAClient,
	listener net.Listener,
	queuesProvider queues.Provider,
	consoleEndpointReg console.InternalSessionRegistration,
) *Server {
	return &Server{
		log:                log,
		cfg:                cfg,
		store:              st,
		ca:                 ca,
		listener:           listener,
		queuesProvider:     queuesProvider,
		consoleEndpointReg: consoleEndpointReg,
	}
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

	if allMatchFormat && longestPathError != nil {
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
	publisher, err := worker_client.QueuePublisher(ctx, s.queuesProvider)
	if err != nil {
		return err
	}
	kvStore, err := kvstore.NewKVStore(ctx, s.log, s.cfg.KV.Hostname, s.cfg.KV.Port, s.cfg.KV.Password)
	if err != nil {
		return err
	}
	workerClient := worker_client.NewWorkerClient(publisher, s.log)

	s.log.Println("Initializing API server")

	// Create service handler and wrap with tracing
	baseServiceHandler := service.NewServiceHandler(
		s.store, workerClient, kvStore, s.ca, s.log, s.cfg.Service.BaseAgentEndpointUrl, s.cfg.Service.BaseUIUrl, s.cfg.Service.TPMCAPaths)
	serviceHandler := service.WrapWithTracing(baseServiceHandler)

	// Initialize auth with traced service handler for OIDC provider access
	authN, err := auth.InitMultiAuth(s.cfg, s.log, serviceHandler)
	if err != nil {
		return fmt.Errorf("failed initializing auth: %w", err)
	}
	s.authN = authN

	// Create auth proxies (token and userinfo)
	authTokenProxy := service.NewAuthTokenProxy(authN)
	authUserInfoProxy := service.NewAuthUserInfoProxy(authN)

	// Start auth provider loader
	go func() {
		if err := authN.Start(ctx); err != nil {
			s.log.Errorf("Failed to start auth provider loader: %v", err)
			return
		}
		s.log.Warn("Auth provider loader stopped unexpectedly")
	}()

	s.authZ, err = auth.InitMultiAuthZ(s.cfg, s.log)
	if err != nil {
		return fmt.Errorf("failed initializing authZ: %w", err)
	}

	// Start multiAuthZ to initialize cache lifecycle management
	if multiAuthZ, ok := s.authZ.(*auth.MultiAuthZ); ok {
		multiAuthZ.Start(ctx)
		s.log.Debug("Started MultiAuthZ with context-based cache lifecycle")
	}

	router := chi.NewRouter()

	// Create identity mapping middleware
	identityMapper := service.NewIdentityMapper(s.store, s.log)
	go func() {
		identityMapper.Start(ctx)
		s.log.Warn("Identity mapper stopped unexpectedly")
	}()
	identityMappingMiddleware := fcmiddleware.NewIdentityMappingMiddleware(identityMapper, s.log)

	// Create organization extraction and validation middleware once
	orgMiddleware := fcmiddleware.ExtractAndValidateOrg(fcmiddleware.QueryOrgIDExtractor, s.log)
	userAgentMiddleware := fcmiddleware.UserAgentLogger(s.log)

	authMiddewares := []func(http.Handler) http.Handler{
		auth.CreateAuthNMiddleware(s.authN, s.log),
		identityMappingMiddleware.MapIdentityToDB,
		orgMiddleware,
		auth.CreateAuthZMiddleware(s.authZ, s.log),
	}

	// general middleware stack for all route groups
	// request size limits should come before logging to prevent DoS attacks from filling logs
	router.Use(
		middleware.RequestSize(int64(s.cfg.Service.HttpMaxRequestSize)),
		fcmiddleware.RequestSizeLimiter(s.cfg.Service.HttpMaxUrlLength, s.cfg.Service.HttpMaxNumHeaders),
		fcmiddleware.SecurityHeaders,
		fcmiddleware.RequestID,
		fcmiddleware.AddEventMetadataToCtx,
		fcmiddleware.ChiLoggerWithAPIVersionTag(),
		middleware.Recoverer,
		userAgentMiddleware,
	)

	// Create version negotiator with v1beta1 as default
	negotiator := versioning.NewNegotiator(versioning.V1Beta1)

	// Create v1beta1 transport handler
	handlerV1Beta1 := transportv1beta1.NewTransportHandler(
		serviceHandler, convertv1beta1.NewConverter(),
		s.authN, authTokenProxy, authUserInfoProxy, s.authZ,
	)

	// Create v1beta1 router with OpenAPI validation
	v1beta1Swagger, err := corev1beta1.GetSwagger()
	if err != nil {
		return fmt.Errorf("failed loading v1beta1 swagger spec: %w", err)
	}
	v1beta1OapiMiddleware := oapimiddleware.OapiRequestValidatorWithOptions(v1beta1Swagger, &oapimiddleware.Options{
		ErrorHandler:          OapiErrorHandler,
		MultiErrorHandler:     oapiMultiErrorHandler,
		SilenceServersWarning: true, // Suppress Host header mismatch warnings
	})

	routerV1Beta1 := versioning.NewRouter(versioning.RouterConfig{
		Middlewares: []versioning.Middleware{v1beta1OapiMiddleware},
		RegisterRoutes: func(r chi.Router) {
			server.HandlerFromMux(&customTransportHandler{handlerV1Beta1}, r)
		},
	})

	// Create v1alpha1 router with OpenAPI validation (for alpha-stage resources like Catalog)
	v1alpha1Swagger, err := corev1alpha1.GetSwagger()
	if err != nil {
		return fmt.Errorf("failed loading v1alpha1 swagger spec: %w", err)
	}
	v1alpha1OapiMiddleware := oapimiddleware.OapiRequestValidatorWithOptions(v1alpha1Swagger, &oapimiddleware.Options{
		ErrorHandler:          OapiErrorHandler,
		MultiErrorHandler:     oapiMultiErrorHandler,
		SilenceServersWarning: true,
	})

	// Create v1alpha1 transport handler for alpha-stage resources (Catalog)
	handlerV1Alpha1 := transportv1alpha1.NewTransportHandler(
		serviceHandler, convertv1alpha1.NewConverter(),
	)

	routerV1Alpha1 := versioning.NewRouter(versioning.RouterConfig{
		Middlewares: []versioning.Middleware{v1alpha1OapiMiddleware},
		RegisterRoutes: func(r chi.Router) {
			serverv1alpha1.HandlerFromMux(handlerV1Alpha1, r)
		},
	})

	// Create negotiated router with version-specific sub-routers
	negotiatedRouter, err := versioning.NewNegotiatedRouter(
		negotiator.NegotiateMiddleware,
		map[versioning.Version]chi.Router{
			versioning.V1Beta1:  routerV1Beta1,
			versioning.V1Alpha1: routerV1Alpha1,
		},
		versioning.V1Beta1,
	)
	if err != nil {
		return fmt.Errorf("failed to create negotiated router: %w", err)
	}

	// a group is a new mux copy, with its own copy of the middleware stack
	// this one handles the OpenAPI handling of the service (excluding auth validate endpoint)
	// Versioned API endpoints at /api/v1
	router.Route(server.ServerUrlApiv1, func(r chi.Router) {
		// Add general rate limiting (only if configured and enabled)
		ConfigureRateLimiterFromConfig(
			r,
			s.cfg.Service.RateLimit,
			RateLimitScopeGeneral,
		)

		// Auth middlewares (shared by all versions)
		r.Use(authMiddewares...)

		// Auth validate with stricter rate limiting (separate group)
		// This ensures it gets all the necessary middleware with stricter rate limiting
		r.Group(func(r chi.Router) {
			// Add conditional middleware
			r.Use(v1beta1OapiMiddleware)
			r.Use(identityMappingMiddleware.MapIdentityToDB) // Map identity to DB objects AFTER authentication
			ConfigureRateLimiterFromConfig(
				r,
				s.cfg.Service.RateLimit,
				RateLimitScopeAuth,
			)

			wrapper := &server.ServerInterfaceWrapper{
				Handler:            handlerV1Beta1,
				HandlerMiddlewares: nil,
				ErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, err error) {
					OapiErrorHandler(w, err.Error(), http.StatusBadRequest)
				},
			}
			r.Get("/auth/validate", wrapper.AuthValidate)
		})

		// Register all other endpoints with general rate limiting (already applied at router level)
		// Negotiated API endpoints
		r.Mount("/", negotiatedRouter)
	})

	// Backward-compatible /corev1beta1/version endpoint (redirects to same handler as /corev1beta1/v1/version)
	router.Get("/api/version", handlerV1Beta1.GetVersion)

	// health endpoints: bypass OpenAPI + auth, but keep global safety middlewares
	router.Group(func(r chi.Router) {
		if s.cfg != nil && s.cfg.Service != nil && s.cfg.Service.HealthChecks != nil && s.cfg.Service.HealthChecks.Enabled {
			hc := s.cfg.Service.HealthChecks
			r.Method(http.MethodGet, hc.ReadinessPath,
				ReadyzHandler(time.Duration(hc.ReadinessTimeout), s.store, s.queuesProvider))
			r.Method(http.MethodGet, hc.LivenessPath, HealthzHandler())
		}
	})

	// ws handling
	router.Group(func(r chi.Router) {
		r.Use(fcmiddleware.CreateRouteExistsMiddleware(r))
		r.Use(authMiddewares...)
		r.Use(identityMappingMiddleware.MapIdentityToDB) // Map identity to DB objects AFTER authentication
		// Add websocket rate limiting (only if configured and enabled)
		ConfigureRateLimiterFromConfig(
			r,
			s.cfg.Service.RateLimit,
			RateLimitScopeGeneral,
		)

		consoleSessionManager := console.NewConsoleSessionManager(serviceHandler, s.log, s.consoleEndpointReg)
		ws := transportv1beta1.NewWebsocketHandler(s.ca, s.log, consoleSessionManager)
		ws.RegisterRoutes(r)
	})

	handler := otelhttp.NewHandler(router, "http-server")
	srv := fcmiddleware.NewHTTPServer(handler, s.log, s.cfg.Service.Address, s.cfg)

	go func() {
		<-ctx.Done()
		s.log.Println("Shutdown signal received:", ctx.Err())
		ctxTimeout, cancel := context.WithTimeout(context.Background(), GracefulShutdownTimeout)
		defer cancel()

		srv.SetKeepAlivesEnabled(false)
		_ = srv.Shutdown(ctxTimeout)
		identityMapper.Stop()
		kvStore.Close()
		s.queuesProvider.Stop()
		s.queuesProvider.Wait()
	}()

	s.log.Printf("Listening on %s...", s.listener.Addr().String())
	if err := srv.Serve(s.listener); err != nil && !errors.Is(err, net.ErrClosed) && !errors.Is(err, http.ErrServerClosed) {
		return err
	}

	return nil
}
