package server

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	cacheutil "github.com/argoproj/argo-cd/v2/util/cache"
	oapimiddleware "github.com/deepmap/oapi-codegen/pkg/chi-middleware"
	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/api/server"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/configprovider/git"
	"github.com/flightctl/flightctl/internal/crypto"
	"github.com/flightctl/flightctl/internal/service"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/tasks"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
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

func (s *Server) Run() error {
	s.log.Println("Initializing caching layer")
	cache := cacheutil.NewCache(cacheutil.NewInMemoryCache(cacheExpirationTime))

	s.log.Println("Initializing config providers")
	_ = git.NewGitConfigProvider(cache, cacheExpirationTime, cacheExpirationTime)

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

	router := chi.NewRouter()
	router.Use(
		middleware.RequestID,
		middleware.Logger,
		middleware.Recoverer,
		oapimiddleware.OapiRequestValidator(swagger),
	)

	h := service.NewServiceHandler(s.store, taskManager, s.ca, s.log)
	server.HandlerFromMux(server.NewStrictHandler(h, nil), router)

	srv := &http.Server{
		Addr:         s.cfg.Service.Address,
		Handler:      router,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 10 * time.Second,
	}

	sigShutdown := make(chan os.Signal, 1)
	signal.Notify(sigShutdown, os.Interrupt, syscall.SIGHUP, syscall.SIGTERM, syscall.SIGQUIT)
	go func() {
		<-sigShutdown
		s.log.Println("Shutdown signal received")
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

// NewTLSListener returns a new TLS listener. If the address is empty, it will
// listen on localhost's next available port.
func NewTLSListener(address string, tlsConfig *tls.Config) (net.Listener, error) {
	if address == "" {
		address = "localhost:0"
	}
	ln, err := net.Listen("tcp", address)
	if err != nil {
		return nil, err
	}
	return tls.NewListener(ln, tlsConfig), nil
}
