package server

import (
	"context"
	"crypto/tls"
	"fmt"
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
	device_updater "github.com/flightctl/flightctl/internal/monitors/device-updater"
	"github.com/flightctl/flightctl/internal/monitors/repotester"
	"github.com/flightctl/flightctl/internal/monitors/resourcesync"
	"github.com/flightctl/flightctl/internal/service"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/pkg/thread"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

const (
	gracefulShutdownTimeout = 5 * time.Second
	cacheExpirationTime     = 10 * time.Minute
)

type Server struct {
	log       logrus.FieldLogger
	cfg       *config.Config
	store     store.Store
	db        *gorm.DB
	tlsConfig *tls.Config
	ca        *crypto.CA
}

// New returns a new instance of a flightctl server.
func New(
	log logrus.FieldLogger,
	cfg *config.Config,
	store store.Store,
	db *gorm.DB,
	tlsConfig *tls.Config,
	ca *crypto.CA,
) *Server {
	return &Server{
		log:       log,
		cfg:       cfg,
		store:     store,
		db:        db,
		tlsConfig: tlsConfig,
		ca:        ca,
	}
}

func (s *Server) Run() error {
	s.log.Println("Initializing caching layer")
	cache := cacheutil.NewCache(cacheutil.NewInMemoryCache(cacheExpirationTime))

	s.log.Println("Initializing config providers")
	_ = git.NewGitConfigProvider(cache, cacheExpirationTime, cacheExpirationTime)

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

	h := service.NewServiceHandler(s.store, s.ca, s.log)
	server.HandlerFromMux(server.NewStrictHandler(h, nil), router)

	srv := &http.Server{
		Addr:         s.cfg.Service.Address,
		Handler:      router,
		TLSConfig:    s.tlsConfig,
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
	}()

	repoTester := repotester.NewRepoTester(s.log, s.db, s.store)
	repoTesterThread := thread.New(
		s.log.WithField("pkg", "repository-tester"), "Repository tester", threadIntervalMinute(2), repoTester.TestRepo)
	repoTesterThread.Start()
	defer repoTesterThread.Stop()

	deviceUpdater := device_updater.NewDeviceUpdater(s.log, s.db, s.store)
	deviceUpdaterThread := thread.New(
		s.log.WithField("pkg", "device-updater"), "Device updater", threadIntervalMinute(2), deviceUpdater.UpdateDevices)
	deviceUpdaterThread.Start()
	defer deviceUpdaterThread.Stop()

	resourceSync := resourcesync.NewResourceSync(s.log, s.db, s.store)
	resourceSyncThread := thread.New(
		s.log.WithField("pkg", "resourcesync"), "ResourceSync", threadIntervalMinute(2), resourceSync.Poll)
	resourceSyncThread.Start()
	defer resourceSyncThread.Stop()

	s.log.Printf("Listening on %s...", srv.Addr)
	if err := srv.ListenAndServeTLS("", ""); err != nil && err != http.ErrServerClosed {
		return err
	}

	return nil
}

func threadIntervalMinute(min float64) time.Duration {
	return time.Duration(min * float64(time.Minute))
}
