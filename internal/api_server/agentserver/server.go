package agentserver

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"slices"
	"time"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	server "github.com/flightctl/flightctl/internal/api/server/agent"
	tlsmiddleware "github.com/flightctl/flightctl/internal/api_server/middleware"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/crypto"
	"github.com/flightctl/flightctl/internal/instrumentation"
	"github.com/flightctl/flightctl/internal/kvstore"
	"github.com/flightctl/flightctl/internal/service"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/tasks_client"
	transport "github.com/flightctl/flightctl/internal/transport/agent"
	"github.com/flightctl/flightctl/pkg/queues"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	oapimiddleware "github.com/oapi-codegen/nethttp-middleware"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc"
)

const (
	gracefulShutdownTimeout = 5 * time.Second
	cacheExpirationTime     = 10 * time.Minute
)

type AgentServer struct {
	log            logrus.FieldLogger
	cfg            *config.Config
	store          store.Store
	ca             *crypto.CAClient
	listener       net.Listener
	queuesProvider queues.Provider
	tlsConfig      *tls.Config
	metrics        *instrumentation.ApiMetrics
	grpcServer     *AgentGrpcServer
}

// New returns a new instance of a flightctl server.
func New(
	log logrus.FieldLogger,
	cfg *config.Config,
	store store.Store,
	ca *crypto.CAClient,
	listener net.Listener,
	queuesProvider queues.Provider,
	tlsConfig *tls.Config,
	metrics *instrumentation.ApiMetrics,
) *AgentServer {
	return &AgentServer{
		log:            log,
		cfg:            cfg,
		store:          store,
		ca:             ca,
		listener:       listener,
		queuesProvider: queuesProvider,
		tlsConfig:      tlsConfig,
		metrics:        metrics,
		grpcServer:     NewAgentGrpcServer(log, cfg),
	}
}

func oapiErrorHandler(w http.ResponseWriter, message string, statusCode int) {
	http.Error(w, fmt.Sprintf("API Error: %s", message), statusCode)
}

func (s *AgentServer) GetGRPCServer() *AgentGrpcServer {
	return s.grpcServer
}
func (s *AgentServer) Run(ctx context.Context) error {
	s.log.Println("Initializing Agent-side API server")

	publisher, err := tasks_client.TaskQueuePublisher(s.queuesProvider)
	if err != nil {
		return err
	}
	kvStore, err := kvstore.NewKVStore(ctx, s.log, s.cfg.KV.Hostname, s.cfg.KV.Port, s.cfg.KV.Password)
	if err != nil {
		return err
	}
	callbackManager := tasks_client.NewCallbackManager(publisher, s.log)

	serviceHandler := service.NewServiceHandler(s.store, callbackManager, kvStore, s.ca, s.log, s.cfg.Service.AgentEndpointAddress, s.cfg.Service.BaseUIUrl)

	httpAPIHandler, err := s.prepareHTTPHandler(serviceHandler)
	if err != nil {
		return err
	}

	grpcServer := s.grpcServer.PrepareGRPCService()

	handler := grpcMuxHandlerFunc(grpcServer, httpAPIHandler)
	srv := tlsmiddleware.NewHTTPServerWithTLSContext(handler, s.log, s.cfg.Service.AgentEndpointAddress, s.cfg)

	go func() {
		<-ctx.Done()
		s.log.Println("Shutdown signal received:", ctx.Err())
		ctxTimeout, cancel := context.WithTimeout(context.Background(), gracefulShutdownTimeout)
		defer cancel()

		srv.SetKeepAlivesEnabled(false)
		_ = srv.Shutdown(ctxTimeout)
	}()

	s.log.Printf("Listening on %s...", s.listener.Addr().String())
	srv.TLSConfig = s.tlsConfig
	if err := srv.ServeTLS(s.listener, "", ""); err != nil && !errors.Is(err, net.ErrClosed) {
		return err
	}

	return nil
}

func (s *AgentServer) prepareHTTPHandler(serviceHandler *service.ServiceHandler) (*chi.Mux, error) {
	swagger, err := api.GetSwagger()
	if err != nil {
		return nil, fmt.Errorf("prepareHTTPHandler: failed loading swagger spec: %w", err)
	}
	// Skip server name validation
	swagger.Servers = nil

	oapiOpts := oapimiddleware.Options{
		ErrorHandler: oapiErrorHandler,
	}

	// request size limits should come before logging to prevent DoS attacks from filling logs
	middlewares := [](func(http.Handler) http.Handler){
		middleware.RequestSize(int64(s.cfg.Service.HttpMaxRequestSize)),
		tlsmiddleware.RequestSizeLimiter(s.cfg.Service.HttpMaxUrlLength, s.cfg.Service.HttpMaxNumHeaders),
		middleware.RequestID,
		middleware.Logger,
		middleware.Recoverer,
		oapimiddleware.OapiRequestValidatorWithOptions(swagger, &oapiOpts),
	}

	if s.metrics != nil {
		middlewares = slices.Insert(middlewares, 0, s.metrics.AgentServerMiddleware)
	}

	router := chi.NewRouter()
	router.Use(middlewares...)

	h := transport.NewAgentTransportHandler(serviceHandler, s.ca, s.log)
	server.HandlerFromMux(h, router)
	return router, nil
}

// grpcMuxHandlerFunc dispatches requests to the gRPC server or the HTTP handler based on the request headers
func grpcMuxHandlerFunc(grpcServer *grpc.Server, otherHandler http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor == 2 && r.Header.Get("Content-Type") == "application/grpc" {
			grpcServer.ServeHTTP(w, r)
		} else {
			otherHandler.ServeHTTP(w, r)
		}
	})
}
