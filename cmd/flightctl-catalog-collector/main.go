package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	catalogcollector "github.com/flightctl/flightctl/pkg/catalogcollector"
	"github.com/flightctl/flightctl/pkg/catalogcollector/config"
	"github.com/flightctl/flightctl/pkg/catalogcollector/service"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/sirupsen/logrus"
)

func main() {
	configFile := flag.String(
		"config",
		"",
		"path to the collector configuration file",
	)
	flag.Parse()

	if *configFile == "" {
		fmt.Fprintln(os.Stderr, "error: --config is required")
		os.Exit(1)
	}

	logger := log.InitLogs()
	logger.Info("starting catalog collector")

	ctx, cancel := signal.NotifyContext(
		context.Background(),
		os.Interrupt,
		syscall.SIGHUP,
		syscall.SIGTERM,
		syscall.SIGQUIT,
	)
	defer cancel()

	if err := run(
		ctx,
		*configFile,
		components(),
		service.Settings{Logger: logger},
	); err != nil {
		logger.Fatalf("catalog collector failed: %v", err)
	}
}

func run(
	ctx context.Context,
	configPath string,
	factories catalogcollector.Factories,
	settings service.Settings,
) error {
	if settings.Logger == nil {
		return fmt.Errorf("service settings: logger must not be nil")
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("loading configuration: %w", err)
	}

	settings.Logger.WithFields(logrus.Fields{
		"config_path":                  configPath,
		"configured_source_count":      len(cfg.Sources),
		"configured_processor_count":   len(cfg.Processors),
		"configured_destination_count": len(cfg.Destinations),
		"configured_extension_count":   len(cfg.Extensions),
		"configured_pipeline_count":    len(cfg.Pipelines),
	}).Info("configuration loaded successfully")

	svc, err := service.New(ctx, cfg, factories, settings)
	if err != nil {
		return fmt.Errorf("building pipelines: %w", err)
	}

	return svc.Run(ctx)
}
