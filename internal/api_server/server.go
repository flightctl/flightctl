package apiserver

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/api/server"
	tlsmiddleware "github.com/flightctl/flightctl/internal/api_server/middleware"
	"github.com/flightctl/flightctl/internal/auth"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/console"
	"github.com/flightctl/flightctl/internal/crypto"
	"github.com/flightctl/flightctl/internal/instrumentation"
	"github.com/flightctl/flightctl/internal/kvstore"
	"github.com/flightctl/flightctl/internal/service"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/tasks"
	"github.com/flightctl/flightctl/pkg/queues"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	oapimiddleware "github.com/oapi-codegen/nethttp-middleware"
	"github.com/sirupsen/logrus"
)

const (
	gracefulShutdownTimeout = 5 * time.Second
)

type Server struct {
	log                logrus.FieldLogger
	cfg                *config.Config
	store              store.Store
	ca                 *crypto.CA
	listener           net.Listener
	provider           queues.Provider
	metrics            *instrumentation.ApiMetrics
	consoleEndpointReg console.InternalSessionRegistration
}

// New returns a new instance of a flightctl server.
func New(
	log logrus.FieldLogger,
	cfg *config.Config,
	store store.Store,
	ca *crypto.CA,
	listener net.Listener,
	provider queues.Provider,
	metrics *instrumentation.ApiMetrics,
	consoleEndpointReg console.InternalSessionRegistration,
) *Server {
	return &Server{
		log:                log,
		cfg:                cfg,
		store:              store,
		ca:                 ca,
		listener:           listener,
		provider:           provider,
		metrics:            metrics,
		consoleEndpointReg: consoleEndpointReg,
	}
}

func oapiErrorHandler(w http.ResponseWriter, message string, statusCode int) {
	http.Error(w, fmt.Sprintf("API Error: %s", message), statusCode)
}

func (s *Server) Run(ctx context.Context) error {
	s.log.Println("Initializing async jobs")
	publisher, err := tasks.TaskQueuePublisher(s.provider)
	if err != nil {
		return err
	}
	kvStore, err := kvstore.NewKVStore(s.cfg.KV.Hostname, s.cfg.KV.Port, s.cfg.KV.Password)
	if err != nil {
		return err
	}
	callbackManager := tasks.NewCallbackManager(publisher, s.log)

	s.log.Println("Initializing API server")
	swagger, err := api.GetSwagger()
	if err != nil {
		return fmt.Errorf("failed loading swagger spec: %w", err)
	}
	// Skip server name validation
	swagger.Servers = nil

	oapiOpts := oapimiddleware.Options{
		ErrorHandler: oapiErrorHandler,
	}

	authMiddleware, err := auth.CreateAuthMiddleware(s.cfg, s.log)
	if err != nil {
		return err
	}

	router := chi.NewRouter()

	// general middleware stack for all route groups
	router.Use(
		middleware.RequestID,
		middleware.Logger,
		middleware.RequestSize(int64(s.cfg.Service.HttpMaxRequestSize)),
		tlsmiddleware.RequestSizeLimiter(s.cfg.Service.HttpMaxUrlLength, s.cfg.Service.HttpMaxNumHeaders),
		middleware.Recoverer,
		authMiddleware,
	)

	// a group is a new mux copy, with it's own copy of the middleware stack
	// this one handles the OpenAPI handling of the service
	router.Group(func(r chi.Router) {
		//NOTE(majopela): keeping metrics middleware separate from the rest of the middleware stack
		// to avoid issues with websocket connections
		if s.metrics != nil {
			r.Use(s.metrics.ApiServerMiddleware)
		}
		r.Use(oapimiddleware.OapiRequestValidatorWithOptions(swagger, &oapiOpts))

		h := service.NewServiceHandler(s.store, callbackManager, kvStore, s.ca, s.log, s.cfg.Service.BaseAgentGrpcUrl, s.cfg.Service.BaseAgentEndpointUrl, s.cfg.Service.BaseUIUrl)
		server.HandlerFromMux(server.NewStrictHandler(h, nil), r)
	})

	consoleSessionManager := console.NewConsoleSessionManager(s.store, callbackManager, kvStore, s.log, s.consoleEndpointReg)
	ws := service.NewWebsocketHandler(s.store, s.ca, s.log, consoleSessionManager)
	ws.RegisterRoutes(router)

	srv := tlsmiddleware.NewHTTPServer(router, s.log, s.cfg.Service.Address, s.cfg)

	go func() {
		<-ctx.Done()
		s.log.Println("Shutdown signal received:", ctx.Err())
		ctxTimeout, cancel := context.WithTimeout(context.Background(), gracefulShutdownTimeout)
		defer cancel()

		srv.SetKeepAlivesEnabled(false)
		_ = srv.Shutdown(ctxTimeout)
		kvStore.Close()
		s.provider.Stop()
		s.provider.Wait()
	}()

	s.log.Printf("Listening on %s...", s.listener.Addr().String())
	if err := srv.Serve(s.listener); err != nil && !errors.Is(err, net.ErrClosed) {
		return err
	}

	return nil
}
