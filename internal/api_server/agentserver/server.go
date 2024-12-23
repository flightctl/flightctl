package agentserver

import (
	"context"
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
	service "github.com/flightctl/flightctl/internal/service/agent"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	oapimiddleware "github.com/oapi-codegen/nethttp-middleware"
	"github.com/sirupsen/logrus"
)

const (
	gracefulShutdownTimeout = 5 * time.Second
	cacheExpirationTime     = 10 * time.Minute
)

type AgentServer struct {
	log      logrus.FieldLogger
	cfg      *config.Config
	store    store.Store
	ca       *crypto.CA
	listener net.Listener
	metrics  *instrumentation.ApiMetrics
}

// New returns a new instance of a flightctl server.
func New(
	log logrus.FieldLogger,
	cfg *config.Config,
	store store.Store,
	ca *crypto.CA,
	listener net.Listener,
	metrics *instrumentation.ApiMetrics,
) *AgentServer {
	return &AgentServer{
		log:      log,
		cfg:      cfg,
		store:    store,
		ca:       ca,
		listener: listener,
		metrics:  metrics,
	}
}

func oapiErrorHandler(w http.ResponseWriter, message string, statusCode int) {
	http.Error(w, fmt.Sprintf("API Error: %s", message), statusCode)
}

func (s *AgentServer) Run(ctx context.Context) error {
	s.log.Println("Initializing Agent-side API server")
	swagger, err := api.GetSwagger()
	if err != nil {
		return fmt.Errorf("failed loading swagger spec: %w", err)
	}
	// Skip server name validation
	swagger.Servers = nil

	oapiOpts := oapimiddleware.Options{
		ErrorHandler: oapiErrorHandler,
	}

	middlewares := [](func(http.Handler) http.Handler){
		middleware.RequestID,
		middleware.Logger,
		middleware.RequestSize(int64(s.cfg.Service.HttpMaxRequestSize)),
		tlsmiddleware.RequestSizeLimiter(s.cfg.Service.HttpMaxUrlLength, s.cfg.Service.HttpMaxNumHeaders),
		middleware.Recoverer,
		oapimiddleware.OapiRequestValidatorWithOptions(swagger, &oapiOpts),
	}

	if s.metrics != nil {
		middlewares = slices.Insert(middlewares, 0, s.metrics.AgentServerMiddleware)
	}
	router := chi.NewRouter()
	router.Use(middlewares...)

	h := service.NewAgentServiceHandler(s.store, s.ca, s.log, s.cfg.Service.BaseAgentGrpcUrl)
	server.HandlerFromMux(server.NewStrictHandler(h, nil), router)

	srv := tlsmiddleware.NewHTTPServerWithTLSContext(router, s.log, s.cfg.Service.AgentEndpointAddress, s.cfg)

	go func() {
		<-ctx.Done()
		s.log.Println("Shutdown signal received:", ctx.Err())
		ctxTimeout, cancel := context.WithTimeout(context.Background(), gracefulShutdownTimeout)
		defer cancel()

		srv.SetKeepAlivesEnabled(false)
		_ = srv.Shutdown(ctxTimeout)
	}()

	s.log.Printf("Listening on %s...", s.listener.Addr().String())
	if err := srv.Serve(s.listener); err != nil && !errors.Is(err, net.ErrClosed) {
		return err
	}

	return nil
}
