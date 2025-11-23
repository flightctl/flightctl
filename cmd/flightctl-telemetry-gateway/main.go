package main

import (
	"context"
	"fmt"
	"syscall"
	"time"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/instrumentation/tracing"
	tg "github.com/flightctl/flightctl/internal/telemetry_gateway"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/shutdown"
	"github.com/sirupsen/logrus"
)

func main() {
	log := log.InitLogs()

	if err := runCmd(log); err != nil {
		log.Fatalf("Telemetry gateway error: %v", err)
	}
}

func runCmd(log *logrus.Logger) error {
	log.Info("Starting telemetry gateway")
	defer log.Info("Telemetry gateway stopped")

	// Create shutdown manager with explicit signals (no SIGHUP) and timeout for telemetry flushing
	shutdownMgr := shutdown.NewManager(log).
		WithSignals(syscall.SIGTERM, syscall.SIGINT, syscall.SIGQUIT).
		WithTimeout(shutdown.DefaultShutdownTimeout)

	cfg, err := config.LoadOrGenerate(config.ConfigFile())
	if err != nil {
		return fmt.Errorf("reading configuration: %w", err)
	}
	log.Printf("Using config: %s", cfg)

	logLvl, err := logrus.ParseLevel(cfg.Service.LogLevel)
	if err != nil {
		logLvl = logrus.InfoLevel
	}
	log.SetLevel(logLvl)

	// Setup tracer with cleanup
	tracerShutdown := tracing.InitTracer(log, cfg, "flightctl-telemetry-gateway")
	if tracerShutdown != nil {
		shutdownMgr.AddCleanup("tracer", func() error {
			log.Info("Shutting down tracer")
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			return tracerShutdown(ctx)
		})
	}

	// Add telemetry gateway server
	shutdownMgr.AddServer("telemetry-gateway", shutdown.NewServerFunc(func(ctx context.Context) error {
		log.Info("Starting telemetry gateway server")
		return tg.Run(ctx, cfg)
	}))

	// Run with coordinated shutdown
	return shutdownMgr.Run(context.Background())
}
