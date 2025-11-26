package main

import (
	"context"
	"time"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/instrumentation/tracing"
	tg "github.com/flightctl/flightctl/internal/telemetry_gateway"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/shutdown"
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

	tracerShutdown := tracing.InitTracer(logger, cfg, "flightctl-telemetry-gateway")
	if tracerShutdown != nil {
		defer func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := tracerShutdown(ctx); err != nil {
				logger.WithError(err).Error("Error shutting down tracer")
			}
		}()
	}

	// Use single server shutdown coordination
	singleServerConfig := shutdown.NewSingleServerConfig("telemetry gateway", logger)
	return singleServerConfig.RunSingleServer(func(shutdownCtx context.Context) error {
		return tg.Run(shutdownCtx, cfg)
	})
}
