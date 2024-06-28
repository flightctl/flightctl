package server

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/api/server"
	"github.com/flightctl/flightctl/internal/auth"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/crypto"
	tlsmiddleware "github.com/flightctl/flightctl/internal/server/middleware"
	"github.com/flightctl/flightctl/internal/service"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/tasks"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	oapimiddleware "github.com/oapi-codegen/nethttp-middleware"
	"github.com/sirupsen/logrus"
)

const (
	gracefulShutdownTimeout = 5 * time.Second
	cacheExpirationTime     = 10 * time.Minute
)

type Server struct {
	log      logrus.FieldLogger
	cfg      *config.Config
	store    store.Store
	ca       *crypto.CA
	listener net.Listener
}

// New returns a new instance of a flightctl server.
func New(
	log logrus.FieldLogger,
	cfg *config.Config,
	store store.Store,
	ca *crypto.CA,
	listener net.Listener,
) *Server {
	return &Server{
		log:      log,
		cfg:      cfg,
		store:    store,
		ca:       ca,
		listener: listener,
	}
}

func oapiErrorHandler(w http.ResponseWriter, message string, statusCode int) {
	http.Error(w, fmt.Sprintf("API Error: %s", message), statusCode)
}

func (s *Server) Run(ctx context.Context) error {
	s.log.Println("Initializing async jobs")
	taskManager := tasks.Init(s.log, s.store)
	taskManager.Start()

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
	router.Use(
		middleware.RequestID,
		middleware.Logger,
		middleware.Recoverer,
		tlsmiddleware.AdminTLSValidator,
		authMiddleware.AuthHandler,
		oapimiddleware.OapiRequestValidatorWithOptions(swagger, &oapiOpts),
	)

	h := service.NewServiceHandler(s.store, taskManager, s.ca, s.log)
	server.HandlerFromMux(server.NewStrictHandler(h, nil), router)

	srv := tlsmiddleware.NewHTTPServerWithTLSContext(router, s.log, s.cfg.Service.Address)

	go func() {
		<-ctx.Done()
		s.log.Println("Shutdown signal received:", ctx.Err())
		ctxTimeout, cancel := context.WithTimeout(context.Background(), gracefulShutdownTimeout)
		defer cancel()

		srv.SetKeepAlivesEnabled(false)
		_ = srv.Shutdown(ctxTimeout)
		taskManager.Stop()
	}()

	s.log.Printf("Listening on %s...", s.listener.Addr().String())
	if err := srv.Serve(s.listener); err != nil && !errors.Is(err, net.ErrClosed) {
		return err
	}

	return nil
}
