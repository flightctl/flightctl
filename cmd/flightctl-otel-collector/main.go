package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/instrumentation"
	"github.com/flightctl/flightctl/internal/otel-collector/deviceattrs"
	"github.com/flightctl/flightctl/internal/otel-collector/deviceauth"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/version"
	"github.com/open-telemetry/opentelemetry-collector-contrib/exporter/prometheusexporter"
	"github.com/open-telemetry/opentelemetry-collector-contrib/exporter/prometheusremotewriteexporter"
	"github.com/open-telemetry/opentelemetry-collector-contrib/processor/transformprocessor"
	"github.com/open-telemetry/opentelemetry-collector-contrib/receiver/prometheusreceiver"
	"github.com/sirupsen/logrus"
	"github.com/spf13/pflag"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/confmap"
	"go.opentelemetry.io/collector/confmap/provider/envprovider"
	"go.opentelemetry.io/collector/exporter"
	"go.opentelemetry.io/collector/exporter/otlpexporter"
	"go.opentelemetry.io/collector/extension"
	"go.opentelemetry.io/collector/otelcol"
	"go.opentelemetry.io/collector/processor"
	"go.opentelemetry.io/collector/receiver"
	"go.opentelemetry.io/collector/receiver/otlpreceiver"
)

func main() {
	ctx := context.Background()
	log := log.InitLogs()

	log.Info("Starting otel collector")

	var otelConfigFile string
	pflag.StringVar(&otelConfigFile, "config", "", "Path to the OTEL config file")
	pflag.Parse()

	if len(otelConfigFile) == 0 {
		log.Info("No otel config specified")
		return
	}

	otelConfig, err := os.ReadFile(otelConfigFile)
	if err != nil {
		log.Fatalf("failed to read OTEL config file: %v", err)
	}

	raw := string(otelConfig)
	log.Infof("OTEL Config:\n%s", raw)

	cfg, err := config.LoadOrGenerate(config.ConfigFile())
	if err != nil {
		log.WithError(err).Fatal("reading configuration")
	}

	log.Infof("Using config: %s", cfg)

	logLvl, err := logrus.ParseLevel(cfg.Service.LogLevel)
	if err != nil {
		logLvl = logrus.InfoLevel
	}
	log.SetLevel(logLvl)

	tracerShutdown := instrumentation.InitTracer(log, cfg, "flightctl-otel-collector")
	defer func() {
		if err := tracerShutdown(ctx); err != nil {
			log.Fatalf("failed to shut down tracer: %v", err)
		}
	}()

	// Provide config via env var to the collector
	if err := os.Setenv("OTEL_CONFIG_YAML", raw); err != nil {
		log.Fatalf("failed to set OTEL_CONFIG_YAML env var: %v", err)
	}

	// Create collector settings
	settings := otelcol.CollectorSettings{
		BuildInfo: component.BuildInfo{
			Command:     "flightctl-otel-collector",
			Description: "FlightCtl OpenTelemetry Collector",
			Version:     version.Get().String(),
		},
		ConfigProviderSettings: otelcol.ConfigProviderSettings{
			ResolverSettings: confmap.ResolverSettings{
				URIs: []string{
					"env:OTEL_CONFIG_YAML",
				},
				ProviderFactories: []confmap.ProviderFactory{
					envprovider.NewFactory(),
				},
			},
		},
		Factories: func() (otelcol.Factories, error) {
			factories := otelcol.Factories{
				Receivers: map[component.Type]receiver.Factory{
					component.MustNewType("otlp"):       otlpreceiver.NewFactory(),
					component.MustNewType("prometheus"): prometheusreceiver.NewFactory(),
				},
				Processors: map[component.Type]processor.Factory{
					component.MustNewType("transform"):   transformprocessor.NewFactory(),
					component.MustNewType("deviceattrs"): deviceattrs.NewFactory(),
				},
				Exporters: map[component.Type]exporter.Factory{
					component.MustNewType("otlp"):                  otlpexporter.NewFactory(),
					component.MustNewType("prometheus"):            prometheusexporter.NewFactory(),
					component.MustNewType("prometheusremotewrite"): prometheusremotewriteexporter.NewFactory(),
				},
				Extensions: map[component.Type]extension.Factory{
					component.MustNewType("deviceauth"): deviceauth.NewFactory(cfg),
				},
			}
			return factories, nil
		},
	}

	collector, err := otelcol.NewCollector(settings)
	if err != nil {
		log.Fatalf("failed to create collector: %v", err)
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Handle graceful shutdown
	sigShutdown := make(chan os.Signal, 1)
	signal.Notify(sigShutdown, os.Interrupt, syscall.SIGHUP, syscall.SIGTERM, syscall.SIGQUIT)
	go func() {
		<-sigShutdown
		log.Info("Shutdown signal received")
		cancel()
		collector.Shutdown()
	}()

	if err := collector.Run(ctx); err != nil {
		log.Fatalf("collector finished with error: %v", err)
	}
}
