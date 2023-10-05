package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	oapimiddleware "github.com/deepmap/oapi-codegen/pkg/chi-middleware"
	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/service"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/pkg/server"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

const (
	gracefulShutdownTimeout     = 5 * time.Second
	caCertValidityDays          = 365 * 10
	serverCertValidityDays      = 365 * 1
	clientBootStrapValidityDays = 365 * 1
	signerCertName              = "csr-signer-ca"
	serverCertName              = "server"
	clientBootstrapCertName     = "client-bootstrap"
)

func main() {
	log.Println("Starting device management service")
	defer log.Println("Device management service stopped")

	cfg, err := config.LoadOrGenerate(config.ConfigFile())
	if err != nil {
		log.Fatalf("reading configuration: %v", err)
	}
	log.Printf("Using config: %s", cfg)

	log.Println("Initializing data store...")
	db, err := store.InitDB(cfg)
	if err != nil {
		log.Fatalf("initializing data store: %v", err)
	}
	store := store.NewStore(db)
	if err := store.InitialMigration(); err != nil {
		log.Fatalf("running initial migration: %v", err)
	}

	swagger, err := api.GetSwagger()
	if err != nil {
		log.Fatalf("loading swagger spec: %v", err)
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

	h := service.NewServiceHandler(store)
	server.HandlerFromMux(server.NewStrictHandler(h, nil), router)

	srv := &http.Server{
		Addr:         cfg.Service.Address,
		Handler:      router,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 10 * time.Second,
	}

	sigShutdown := make(chan os.Signal, 1)
	signal.Notify(sigShutdown, os.Interrupt, syscall.SIGHUP, syscall.SIGTERM, syscall.SIGQUIT)
	go func() {
		<-sigShutdown
		log.Println("Shutdown signal received")

		ctxTimeout, cancel := context.WithTimeout(context.Background(), gracefulShutdownTimeout)
		defer cancel()

		srv.SetKeepAlivesEnabled(false)
		srv.Shutdown(ctxTimeout)
	}()

	log.Printf("Listening on %s...", srv.Addr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}
