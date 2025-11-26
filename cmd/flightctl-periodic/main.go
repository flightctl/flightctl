package main

import (
	"context"
	"fmt"
	"time"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/instrumentation/tracing"
	periodic "github.com/flightctl/flightctl/internal/periodic_checker"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/shutdown"
)

func main() {
	cfg, err := config.LoadOrGenerate(config.ConfigFile())
	if err != nil {
		log.InitLogs().WithError(err).Fatal("reading configuration")
	}

	if err := runCmd(cfg); err != nil {
		log.InitLogs().WithError(err).Fatal("Periodic service error")
	}
}

func runCmd(cfg *config.Config) error {
	logger := log.InitLogs(cfg.Service.LogLevel)

	logger.Info("Starting periodic service")
	defer logger.Info("Periodic service stopped")
	logger.Infof("Using config: %s", cfg)

	tracerShutdown := tracing.InitTracer(logger, cfg, "flightctl-periodic")
	if tracerShutdown != nil {
		defer func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := tracerShutdown(ctx); err != nil {
				logger.WithError(err).Error("Error shutting down tracer")
			}
		}()
	}

	logger.Info("Initializing data store")
	db, err := store.InitDB(cfg, logger)
	if err != nil {
		return fmt.Errorf("initializing data store: %w", err)
	}

	store := store.NewStore(db, logger.WithField("pkg", "store"))
	defer store.Close()

	server := periodic.New(cfg, logger, store)

	// Use single server shutdown coordination
	singleServerConfig := shutdown.NewSingleServerConfig("periodic service", logger)
	return singleServerConfig.RunSingleServer(server.Run)
}
