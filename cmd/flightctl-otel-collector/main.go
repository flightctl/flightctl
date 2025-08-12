package main

import (
	"context"
	"flag"
	"os"
	"os/signal"
	"syscall"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/instrumentation"
	otel_collector "github.com/flightctl/flightctl/internal/otel-collector"
	"github.com/flightctl/flightctl/internal/otel-collector/cnauthenticator"
	"github.com/flightctl/flightctl/internal/otel-collector/deviceidprocessor"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/open-telemetry/opentelemetry-collector-contrib/exporter/prometheusexporter"
	"github.com/open-telemetry/opentelemetry-collector-contrib/processor/transformprocessor"
	"github.com/sirupsen/logrus"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/confmap"
	"go.opentelemetry.io/collector/confmap/provider/fileprovider"
	"go.opentelemetry.io/collector/confmap/provider/yamlprovider"
	"go.opentelemetry.io/collector/exporter"
	"go.opentelemetry.io/collector/extension"
	"go.opentelemetry.io/collector/otelcol"
	"go.opentelemetry.io/collector/processor"
	"go.opentelemetry.io/collector/receiver"
	"go.opentelemetry.io/collector/receiver/otlpreceiver"
	"gopkg.in/yaml.v3"
)

func main() {
	ctx := context.Background()

	// Parse command line arguments
	var configFile string
	flag.StringVar(&configFile, "config", "", "Path to the configuration file")
	flag.Parse()

	log := log.InitLogs()
	log.Println("Starting otel collector")

	// Load configuration using FlightCtl pattern
	configFilePath := config.ConfigFile()
	if configFile != "" {
		configFilePath = configFile
	}
	cfg, err := config.LoadOrGenerate(configFilePath)
	if err != nil {
		log.Fatalf("reading configuration: %v", err)
	}
	log.Printf("Using config: %s", cfg)

	logLvl, err := logrus.ParseLevel(cfg.Service.LogLevel)
	if err != nil {
		logLvl = logrus.InfoLevel
	}
	log.SetLevel(logLvl)

	tracerShutdown := instrumentation.InitTracer(log, cfg, "flightctl-otel-collector")
	defer func() {
		if err := tracerShutdown(ctx); err != nil {
			log.Fatalf("failed to shut down otel collector: %v", err)
		}
	}()

	// Convert FlightCtl config to OTEL config
	otelConfig := otel_collector.ToOTelYAMLConfig(cfg)
	if otelConfig == nil {
		log.Fatalf("failed to convert configuration to OTEL format")
	}

	// Convert to YAML
	otelYAML, err := yaml.Marshal(otelConfig)
	if err != nil {
		log.Fatalf("failed to marshal OTEL config to YAML: %v", err)
	}

	log.Printf("OTEL Config: %s", string(otelYAML))

	// Always create a temporary file for the transformed OTEL config
	tmpFile, err := os.CreateTemp("", "otel-config-*.yaml")
	if err != nil {
		log.Fatalf("failed to create temp config file: %v", err)
	}
	defer os.Remove(tmpFile.Name())
	configPath := tmpFile.Name()

	if _, err := tmpFile.Write(otelYAML); err != nil {
		log.Fatalf("failed to write config to temp file: %v", err)
	}
	tmpFile.Close()

	log.Printf("🔧 Config file location: %s", configPath)
	log.Printf("📄 OTEL YAML content:\n%s", string(otelYAML))
	log.Printf("🔧 About to create collector settings...")

	// Create collector settings

	// Set the configuration via environment variable
	os.Setenv("OTEL_CONFIG_YAML", string(otelYAML))

	settings := otelcol.CollectorSettings{
		BuildInfo: component.BuildInfo{
			Command:     "flightctl-otel-collector",
			Description: "FlightCtl OpenTelemetry Collector",
			Version:     "1.0.0",
		},
		ConfigProviderSettings: otelcol.ConfigProviderSettings{
			ResolverSettings: confmap.ResolverSettings{
				URIs:              []string{configPath},
				ProviderFactories: []confmap.ProviderFactory{fileprovider.NewFactory(), yamlprovider.NewFactory()},
			},
		},
		Factories: func() (otelcol.Factories, error) {
			// Create component factories
			factories := otelcol.Factories{
				Receivers: map[component.Type]receiver.Factory{
					component.MustNewType("otlp"): otlpreceiver.NewFactory(),
				},
				Processors: map[component.Type]processor.Factory{
					component.MustNewType("transform"): transformprocessor.NewFactory(),
					component.MustNewType("deviceid"):  deviceidprocessor.NewFactory(),
				},
				Exporters: map[component.Type]exporter.Factory{
					component.MustNewType("prometheus"): prometheusexporter.NewFactory(),
				},
				Extensions: map[component.Type]extension.Factory{
					component.MustNewType("cnauthenticator"): cnauthenticator.NewFactory(),
				},
			}
			return factories, nil
		},
	}

	log.Printf("🔧 URIs: %v", settings.ConfigProviderSettings.ResolverSettings.URIs)
	log.Printf("🔧 ProviderFactories count: %d", len(settings.ConfigProviderSettings.ResolverSettings.ProviderFactories))

	// Create and run collector
	collector, err := otelcol.NewCollector(settings)
	if err != nil {
		log.Fatalf("failed to create collector: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle graceful shutdown
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
		<-sigCh
		log.Println("Shutting down the collector...")
		collector.Shutdown()
		cancel()
	}()

	log.Println("Starting the collector...")
	if err := collector.Run(ctx); err != nil {
		log.Fatalf("collector run finished with error: %v", err)
	}
}
