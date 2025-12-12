package agentserver

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	agent "github.com/flightctl/flightctl/api/v1beta1/agent"
	server "github.com/flightctl/flightctl/internal/api/server/agent"
	fcmiddleware "github.com/flightctl/flightctl/internal/api_server/middleware"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/crypto"
	"github.com/flightctl/flightctl/internal/healthchecker"
	"github.com/flightctl/flightctl/internal/kvstore"
	"github.com/flightctl/flightctl/internal/service"
	"github.com/flightctl/flightctl/internal/store"
	transport "github.com/flightctl/flightctl/internal/transport/agent"
	"github.com/flightctl/flightctl/internal/worker_client"
	"github.com/flightctl/flightctl/pkg/queues"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	oapimiddleware "github.com/oapi-codegen/nethttp-middleware"
	"github.com/sirupsen/logrus"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"google.golang.org/grpc"
)

const (
	gracefulShutdownTimeout = 5 * time.Second
	enrollmentRequestsPath  = "/api/v1/enrollmentrequests"
)

type AgentServer struct {
	log             logrus.FieldLogger
	cfg             *config.Config
	store           store.Store
	ca              *crypto.CAClient
	listener        net.Listener
	queuesProvider  queues.Provider
	tlsConfig       *tls.Config
	agentGrpcServer *AgentGrpcServer
	serviceHandler  service.Service
	kvStore         kvstore.KVStore
}

// New returns a new instance of a flightctl server.
func New(
	ctx context.Context,
	log logrus.FieldLogger,
	cfg *config.Config,
	st store.Store,
	ca *crypto.CAClient,
	listener net.Listener,
	queuesProvider queues.Provider,
	tlsConfig *tls.Config,
) (*AgentServer, error) {
	s := &AgentServer{
		log:            log,
		cfg:            cfg,
		store:          st,
		ca:             ca,
		listener:       listener,
		queuesProvider: queuesProvider,
		tlsConfig:      tlsConfig,
	}

	if err := s.init(ctx); err != nil {
		s.Stop()
		return nil, fmt.Errorf("initializing: %w", err)
	}

	return s, nil
}

// init initializes the agent server services including gRPC server
func (s *AgentServer) init(ctx context.Context) error {
	s.log.Println("Initializing Agent-side API server")

	healthchecker.HealthChecks.Initialize(ctx, s.store, s.log)
	publisher, err := worker_client.QueuePublisher(ctx, s.queuesProvider)

	if err != nil {
		return err
	}
	s.kvStore, err = kvstore.NewKVStore(ctx, s.log, s.cfg.KV.Hostname, s.cfg.KV.Port, s.cfg.KV.Password)
	if err != nil {
		return err
	}
	workerClient := worker_client.NewWorkerClient(publisher, s.log)

	s.serviceHandler = service.WrapWithTracing(
		service.NewServiceHandler(s.store, workerClient, s.kvStore, s.ca, s.log, s.cfg.Service.AgentEndpointAddress, s.cfg.Service.BaseUIUrl, s.cfg.Service.TPMCAPaths))

	s.agentGrpcServer = NewAgentGrpcServer(s.log, s.cfg, s.serviceHandler)
	return nil
}

// Stop cleans up all resources
func (s *AgentServer) Stop() {
	if s.agentGrpcServer != nil {
		s.agentGrpcServer.Close()
	}
	if s.kvStore != nil {
		s.kvStore.Close()
	}
	if s.queuesProvider != nil {
		s.queuesProvider.Stop()
		s.queuesProvider.Wait()
	}
	if s.listener != nil {
		_ = s.listener.Close()
	}
}

func (s *AgentServer) GetGRPCServer() *AgentGrpcServer {
	return s.agentGrpcServer
}

func oapiErrorHandler(w http.ResponseWriter, message string, statusCode int) {
	http.Error(w, fmt.Sprintf("API Error: %s", message), statusCode)
}

func (s *AgentServer) Run(ctx context.Context) error {
	s.log.Println("Starting Agent-side API server")

	httpAPIHandler, err := s.prepareHTTPHandler(ctx, s.serviceHandler)
	if err != nil {
		return err
	}

	handler := grpcMuxHandlerFunc(s.agentGrpcServer.server, httpAPIHandler, s.log)
	srv := fcmiddleware.NewHTTPServerWithTLSContext(handler, s.log, s.cfg.Service.AgentEndpointAddress, s.cfg)

	go func() {
		<-ctx.Done()
		s.log.Println("Shutdown signal received:", ctx.Err())
		ctxTimeout, cancel := context.WithTimeout(context.Background(), gracefulShutdownTimeout)
		defer cancel()

		srv.SetKeepAlivesEnabled(false)
		_ = srv.Shutdown(ctxTimeout)
		s.Stop()
	}()

	s.log.Printf("Listening on %s...", s.listener.Addr().String())
	srv.TLSConfig = s.tlsConfig
	if err := srv.ServeTLS(s.listener, "", ""); err != nil && !errors.Is(err, net.ErrClosed) && !errors.Is(err, http.ErrServerClosed) {
		return err
	}

	return nil
}

// Custom logger that logs only responses with status >= 400
func filteredLogger(log logrus.FieldLogger) func(next http.Handler) http.Handler {
	formatter := middleware.DefaultLogFormatter{Logger: log}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
			start := time.Now()

			defer func() {
				latency := time.Since(start)
				status := ww.Status()

				if status >= 400 {
					entry := formatter.NewLogEntry(r)
					entry.Write(status, ww.BytesWritten(), nil, latency, nil)
				}
			}()

			next.ServeHTTP(ww, r)
		})
	}
}

func addAgentContext(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Add userID into the context
		ctx := context.WithValue(r.Context(), consts.AgentCtxKey, "true")

		// Create a new request with the updated context
		r = r.WithContext(ctx)

		// Call next handler
		next.ServeHTTP(w, r)
	})
}

// isEnrollmentRequest checks if the request is for enrollment-related operations
func isEnrollmentRequest(r *http.Request) bool {
	// POST to /api/v1/enrollmentrequests (create enrollment request)
	if r.Method == "POST" && r.URL.Path == enrollmentRequestsPath {
		return true
	}
	// GET to /api/v1/enrollmentrequests/{name} (check enrollment status)
	if r.Method == "GET" && strings.HasPrefix(r.URL.Path, enrollmentRequestsPath+"/") {
		return true
	}
	return false
}

func (s *AgentServer) prepareHTTPHandler(ctx context.Context, serviceHandler service.Service) (http.Handler, error) {
	swagger, err := agent.GetSwagger()
	if err != nil {
		return nil, fmt.Errorf("prepareHTTPHandler: failed loading swagger spec: %w", err)
	}
	// Skip server name validation
	swagger.Servers = nil

	oapiOpts := oapimiddleware.Options{
		ErrorHandler: oapiErrorHandler,
	}

	// Create agent authentication middleware for device operations
	agentAuthMiddleware := fcmiddleware.NewAgentAuthMiddleware(s.ca, s.log)
	go agentAuthMiddleware.Start()

	// Create enrollment authentication middleware for enrollment/bootstrap operations
	enrollmentAuthMiddleware := fcmiddleware.NewEnrollmentAuthMiddleware(s.ca, s.log)
	go enrollmentAuthMiddleware.Start()

	// Create identity mapping middleware (handles both user and agent identities)
	identityMapper := service.NewIdentityMapper(s.store, s.log)
	go func() {
		identityMapper.Start(ctx)
		s.log.Warn("Identity mapper stopped unexpectedly")
	}()
	identityMappingMiddleware := fcmiddleware.NewIdentityMappingMiddleware(identityMapper, s.log)

	// Create organization extraction and validation middleware once
	orgMiddleware := fcmiddleware.ExtractAndValidateOrg(fcmiddleware.CertOrgIDExtractor, s.log)

	// Create authentication routing middleware once
	authRoutingMiddleware := func(next http.Handler) http.Handler {
		// --- Create these handlers ONCE ---
		// These are created when authRoutingMiddleware is called (at server setup),
		// not when a request comes in.
		enrollmentAuthHandler := enrollmentAuthMiddleware.AuthenticateEnrollment(next)
		agentAuthHandler := agentAuthMiddleware.AuthenticateAgent(next)
		// ------------------------------------

		// Return the handler that will run on every request
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// This closure "captures" the handlers created above.

			// Check if this is an enrollment request
			if isEnrollmentRequest(r) {
				// Route to the pre-built enrollment handler
				enrollmentAuthHandler.ServeHTTP(w, r)
				return
			}

			// For all other requests, route to the pre-built agent handler
			agentAuthHandler.ServeHTTP(w, r)
		})
	}

	// Create auth middlewares chain - flat and simple
	authMiddlewares := []func(http.Handler) http.Handler{
		authRoutingMiddleware,
		identityMappingMiddleware.MapIdentityToDB,
		orgMiddleware,
	}

	// request size limits should come before logging to prevent DoS attacks from filling logs
	middlewares := [](func(http.Handler) http.Handler){
		middleware.RequestSize(int64(s.cfg.Service.HttpMaxRequestSize)),
		fcmiddleware.RequestSizeLimiter(s.cfg.Service.HttpMaxUrlLength, s.cfg.Service.HttpMaxNumHeaders),
		fcmiddleware.SecurityHeaders,
		fcmiddleware.RequestID,
	}

	// Add auth middlewares
	middlewares = append(middlewares, authMiddlewares...)

	// Add remaining middlewares
	middlewares = append(middlewares, []func(http.Handler) http.Handler{
		filteredLogger(s.log),
		addAgentContext,
		middleware.Recoverer,
		oapimiddleware.OapiRequestValidatorWithOptions(swagger, &oapiOpts),
	}...)

	router := chi.NewRouter()
	router.Use(middlewares...)

	// Rate limiting middleware - applied before validation (only if configured and enabled)
	// Note: Agent server doesn't need trusted proxy validation since it's mTLS
	if s.cfg.Service.RateLimit != nil && s.cfg.Service.RateLimit.Enabled {
		requests := 300       // Default requests limit
		window := time.Minute // Default window
		if s.cfg.Service.RateLimit.Requests > 0 {
			requests = s.cfg.Service.RateLimit.Requests
		}
		if s.cfg.Service.RateLimit.Window > 0 {
			window = time.Duration(s.cfg.Service.RateLimit.Window)
		}
		fcmiddleware.InstallRateLimiter(router, fcmiddleware.RateLimitOptions{
			Requests:       requests,
			Window:         window,
			Message:        "Rate limit exceeded, please try again later",
			TrustedProxies: []string{}, // No proxy headers for mTLS
		})
	}

	h := transport.NewAgentTransportHandler(serviceHandler, s.ca, s.log)
	server.HandlerFromMux(h, router)

	return otelhttp.NewHandler(router, "agent-http-server"), nil
}

// grpcMuxHandlerFunc dispatches requests to the gRPC server or the HTTP handler based on the request headers
func grpcMuxHandlerFunc(grpcServer *grpc.Server, otherHandler http.Handler, log logrus.FieldLogger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor == 2 && r.Header.Get("Content-Type") == "application/grpc" {

			// Since gRPC is used for streaming, keeping the read and write timeouts, will
			// cause streaming connection to disconnected upon timeout expiration.  Therefore,
			// these timeouts are set to infinity for gRPC connections
			type readWriteTimeoutSetter interface {
				SetReadDeadline(deadline time.Time) error
				SetWriteDeadline(deadline time.Time) error
			}
			if rtw, ok := w.(readWriteTimeoutSetter); ok {
				// Set infinite read timeout
				if err := rtw.SetReadDeadline(time.Time{}); err != nil {
					log.Errorf("Couldn't set gRPC read timeout: %v", err)
				}

				// Set infinite write timeout
				if err := rtw.SetWriteDeadline(time.Time{}); err != nil {
					log.Errorf("Couldn't set gRPC write timeout: %v", err)
				}
			} else {
				log.Error("Cannot set gRPC timeout")
			}
			grpcServer.ServeHTTP(w, r)
		} else {
			otherHandler.ServeHTTP(w, r)
		}
	})
}
