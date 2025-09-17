package telemetrygateway

import (
	"context"
	"fmt"
	"os"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/telemetry_gateway/deviceattrs"
	"github.com/flightctl/flightctl/internal/telemetry_gateway/deviceauth"
	"github.com/flightctl/flightctl/pkg/version"
	"github.com/goccy/go-yaml"
	"github.com/open-telemetry/opentelemetry-collector-contrib/exporter/prometheusexporter"
	"github.com/open-telemetry/opentelemetry-collector-contrib/exporter/prometheusremotewriteexporter"
	"github.com/open-telemetry/opentelemetry-collector-contrib/receiver/prometheusreceiver"
	"github.com/sirupsen/logrus"
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

func Run(ctx context.Context, cfg *config.Config, log logrus.FieldLogger) error {
	yml, err := ToOTelYAMLConfig(cfg)
	if err != nil {
		return fmt.Errorf("failed to build OTEL config: %w", err)
	}

	// Provide config via env var to the collector
	if err := os.Setenv("OTEL_CONFIG_YAML", yml); err != nil {
		return fmt.Errorf("failed to set OTEL_CONFIG_YAML env var: %w", err)
	}

	// Create collector settings
	settings := otelcol.CollectorSettings{
		BuildInfo: component.BuildInfo{
			Command:     "flightctl-telemetry-gateway",
			Description: "FlightCtl Telemetry Gateway",
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
		return fmt.Errorf("failed to create otel collector: %w", err)
	}

	if err := collector.Run(ctx); err != nil {
		return fmt.Errorf("otel collector finished with error: %w", err)
	}

	return nil
}

// ToOTelYAMLConfig converts the OpenTelemetry collector configuration to YAML format
func ToOTelYAMLConfig(cfg *config.Config) (string, error) {
	exporterNames := []string{}
	exporters := map[string]any{}

	if cfg.TelemetryGateway.Export != nil && cfg.TelemetryGateway.Export.Prometheus != "" {
		exporters["prometheus"] = map[string]any{
			"endpoint": cfg.TelemetryGateway.Export.Prometheus,
		}
		exporterNames = append(exporterNames, "prometheus")
	}
	if cfg.TelemetryGateway.Forward != nil && cfg.TelemetryGateway.Forward.Endpoint != "" {
		otlp := map[string]any{
			"endpoint": cfg.TelemetryGateway.Forward.Endpoint,
		}
		if cfg.TelemetryGateway.Forward.TLS != nil {
			tls := map[string]any{}
			if cfg.TelemetryGateway.Forward.TLS.InsecureSkipTlsVerify {
				tls["insecure_skip_verify"] = true
			}
			if cfg.TelemetryGateway.Forward.TLS.CertFile != "" {
				tls["cert_file"] = cfg.TelemetryGateway.Forward.TLS.CertFile
			}
			if cfg.TelemetryGateway.Forward.TLS.KeyFile != "" {
				tls["key_file"] = cfg.TelemetryGateway.Forward.TLS.KeyFile
			}
			if cfg.TelemetryGateway.Forward.TLS.CAFile != "" {
				tls["ca_file"] = cfg.TelemetryGateway.Forward.TLS.CAFile
			}
			if len(tls) > 0 {
				otlp["tls"] = tls
			}
		}
		exporters["otlp"] = otlp
		exporterNames = append(exporterNames, "otlp")
	}
	if len(exporterNames) == 0 {
		return "", fmt.Errorf("no exporters configured")
	}

	root := map[string]any{
		"receivers": map[string]any{
			"otlp/device": map[string]any{
				"protocols": map[string]any{
					"grpc": map[string]any{
						"endpoint": cfg.TelemetryGateway.Listen.Device,
						"tls": map[string]any{
							"cert_file":      cfg.TelemetryGateway.TLS.CertFile,
							"key_file":       cfg.TelemetryGateway.TLS.KeyFile,
							"client_ca_file": cfg.TelemetryGateway.TLS.CACert,
						},
						"auth": map[string]any{"authenticator": "deviceauth"},
					},
				},
			},
		},
		"processors": map[string]any{
			"deviceattrs": map[string]any{},
		},
		"exporters":  exporters,
		"extensions": map[string]any{"deviceauth": map[string]any{}},
		"service": map[string]any{
			"extensions": []string{"deviceauth"},
			"pipelines": map[string]any{
				"metrics": map[string]any{
					"receivers":  []string{"otlp/device"},
					"processors": []string{"deviceattrs"},
					"exporters":  exporterNames,
				},
			},
			"telemetry": map[string]any{
				"logs": map[string]any{"level": cfg.TelemetryGateway.LogLevel},
			},
		},
	}

	b, err := yaml.Marshal(root)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
