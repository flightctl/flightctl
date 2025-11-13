package main

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"syscall"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/instrumentation/tracing"
	tg "github.com/flightctl/flightctl/internal/telemetry_gateway"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/shutdown"
	"github.com/sirupsen/logrus"
)

func main() {
	ctx := context.Background()

	// Handle graceful shutdown
	ctx, cancel := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGHUP, syscall.SIGTERM, syscall.SIGQUIT)
	defer cancel()

	if err := runCmd(ctx); err != nil {
		log.InitLogs().Fatalf("flightctl-telemetry-gateway: %v", err)
	}
}

func runCmd(ctx context.Context) error {
	log := log.InitLogs()

	log.Info("Starting telemetry gateway")
	defer log.Info("Telemetry gateway stopped")

	cfg, err := config.LoadOrGenerate(config.ConfigFile())
	if err != nil {
		return err
	}
	log.Printf("Using config: %s", cfg)

	logLvl, err := logrus.ParseLevel(cfg.Service.LogLevel)
	if err != nil {
		logLvl = logrus.InfoLevel
	}
	log.SetLevel(logLvl)

	tracerShutdown := tracing.InitTracer(log, cfg, "flightctl-telemetry-gateway")
	defer func() {
		if err := tracerShutdown(context.Background()); err != nil {
			log.Errorf("failed to shut down tracer: %v", err)
		}
	}()

	// Channel to collect server errors
	serverErrors := make(chan error, 1)

	// Start telemetry gateway
	go func() {
		log.Info("Starting telemetry gateway server")
		if err := tg.Run(ctx, cfg); err != nil {
			log.Errorf("Telemetry gateway error: %v", err)
			serverErrors <- err
		}
	}()

	// Wait for shutdown signal or server error
	log.Info("Telemetry gateway started, waiting for shutdown signal...")
	var serverErr error
	select {
	case <-ctx.Done():
		log.Info("Shutdown signal received, initiating graceful shutdown")
	case serverErr = <-serverErrors:
		log.Errorf("Server error received: %v", serverErr)
		log.Info("Initiating shutdown due to server error")
	}

	// Coordinated shutdown with timeout
	log.Infof("Starting coordinated shutdown (timeout: %v)", shutdown.DefaultGracefulShutdownTimeout)

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), shutdown.DefaultGracefulShutdownTimeout)
	defer shutdownCancel()

	// Wait for shutdown to complete or timeout
	// Telemetry gateway handles its own graceful shutdown when context is cancelled
	// We wait for the shutdown context timeout to allow it time to complete
	<-shutdownCtx.Done()
	if errors.Is(shutdownCtx.Err(), context.DeadlineExceeded) {
		log.WithField("timeout", shutdown.DefaultGracefulShutdownTimeout).Warn("Shutdown timeout exceeded")
	} else {
		log.Info("Graceful shutdown completed")
	}

	return serverErr
}
