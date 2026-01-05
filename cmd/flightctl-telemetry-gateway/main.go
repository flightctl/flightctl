package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	tgconfig "github.com/flightctl/flightctl/internal/config/telemetrygateway"
	"github.com/flightctl/flightctl/internal/instrumentation/tracing"
	tg "github.com/flightctl/flightctl/internal/telemetry_gateway"
	"github.com/flightctl/flightctl/pkg/log"
)

func main() {
	ctx := context.Background()

	cfg, err := tgconfig.LoadOrGenerate(tgconfig.ConfigFile())
	if err != nil {
		log.InitLogs().Fatalf("reading configuration: %v", err)
	}

	log := log.InitLogs(cfg.LogLevel())
	log.Info("Starting telemetry gateway")
	log.Printf("Using config: %s", cfg)

	tracerShutdown := tracing.InitTracer(log, cfg.TracingConfig(), "flightctl-telemetry-gateway")
	defer func() {
		if err := tracerShutdown(ctx); err != nil {
			log.Fatalf("failed to shut down tracer: %v", err)
		}
	}()

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Handle graceful shutdown
	sigShutdown := make(chan os.Signal, 1)
	signal.Notify(sigShutdown, os.Interrupt, syscall.SIGHUP, syscall.SIGTERM, syscall.SIGQUIT)
	go func() {
		<-sigShutdown
		log.Info("Shutdown signal received")
		cancel()
	}()

	if err := tg.Run(ctx, cfg); err != nil {
		log.Fatalf("failed to create telemetry gateway: %v", err)
	}
}
