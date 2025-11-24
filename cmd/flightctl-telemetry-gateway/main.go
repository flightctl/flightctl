package main

import (
	"context"
	"errors"
	"fmt"
	"os/signal"
	"syscall"
	"time"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/instrumentation/tracing"
	tg "github.com/flightctl/flightctl/internal/telemetry_gateway"
	"github.com/flightctl/flightctl/pkg/log"
)

func main() {
	cfg, err := config.LoadOrGenerate(config.ConfigFile())
	if err != nil {
		log.InitLogs().WithError(err).Fatal("error reading configuration")
	}

	if err = runCmd(cfg); err != nil {
		log.InitLogs().WithError(err).Fatal("Telemetry gateway error")
	}
}

func runCmd(cfg *config.Config) error {
	logger := log.InitLogs(cfg.Service.LogLevel)

	logger.Info("Starting telemetry gateway")
	defer logger.Info("Telemetry gateway stopped")
	logger.Infof("Using config: %s", cfg)

	// Single context with signal handling - OS signal cancels context
	ctx, cancel := signal.NotifyContext(context.Background(),
		syscall.SIGTERM, syscall.SIGINT, syscall.SIGQUIT)

	// Build cleanup functions incrementally as resources are created
	var cleanupFuncs []func() error
	defer func() {
		// First cancel context to signal all goroutines to stop
		logger.Info("Cancelling context to stop all servers")
		cancel()

		// Then run cleanup in reverse order after goroutines have stopped
		logger.Info("Starting cleanup")
		for i := len(cleanupFuncs) - 1; i >= 0; i-- {
			if err := cleanupFuncs[i](); err != nil {
				logger.WithError(err).Error("Cleanup error")
			}
		}
		logger.Info("Cleanup completed")
	}()

	tracerShutdown := tracing.InitTracer(logger, cfg, "flightctl-telemetry-gateway")
	if tracerShutdown != nil {
		cleanupFuncs = append(cleanupFuncs, func() error {
			logger.Info("Shutting down tracer")
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			return tracerShutdown(ctx)
		})
	}

	// Start telemetry gateway and wait for completion or signal
	logger.Info("Starting telemetry gateway server")
	err := tg.Run(ctx, cfg)
	if err != nil {
		err = fmt.Errorf("telemetry gateway server: %w", err)
	}

	// Handle shutdown reason
	if errors.Is(err, context.Canceled) {
		logger.Info("Server stopped due to shutdown signal")
		return nil // Normal shutdown
	} else if err != nil {
		logger.WithError(err).Error("Server stopped with error")
		return err // Error shutdown
	}

	logger.Info("Server stopped normally")
	return nil // Normal completion
}
