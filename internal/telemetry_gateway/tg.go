package telemetrygateway

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/telemetry_gateway/deviceattrs"
	"github.com/flightctl/flightctl/internal/telemetry_gateway/deviceauth"
	"github.com/flightctl/flightctl/pkg/version"
	"github.com/goccy/go-yaml"
	"github.com/open-telemetry/opentelemetry-collector-contrib/exporter/prometheusexporter"
	"github.com/open-telemetry/opentelemetry-collector-contrib/exporter/prometheusremotewriteexporter"
	"github.com/open-telemetry/opentelemetry-collector-contrib/receiver/prometheusreceiver"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/confmap"
	"go.opentelemetry.io/collector/confmap/provider/envprovider"
	"go.opentelemetry.io/collector/exporter"
	"go.opentelemetry.io/collector/exporter/otlpexporter"
	"go.opentelemetry.io/collector/exporter/otlphttpexporter"
	"go.opentelemetry.io/collector/extension"
	"go.opentelemetry.io/collector/otelcol"
	"go.opentelemetry.io/collector/processor"
	"go.opentelemetry.io/collector/receiver"
	"go.opentelemetry.io/collector/receiver/otlpreceiver"
)

// Option configures how Run builds both the Collector and the OTEL config.
type Option func(*runOptions)

type runOptions struct {
	settingsMutators []func(*otelcol.CollectorSettings)
	cfgMutators      []OTelConfigMutator
}

// WithCollectorSettings lets callers tweak CollectorSettings before NewCollector.
func WithCollectorSettings(mut func(*otelcol.CollectorSettings)) Option {
	return func(ro *runOptions) { ro.settingsMutators = append(ro.settingsMutators, mut) }
}

// WithSkipSettingGRPCLogger avoids setting the grpc logger
func WithSkipSettingGRPCLogger(skip bool) Option {
	return WithCollectorSettings(func(s *otelcol.CollectorSettings) { s.SkipSettingGRPCLogger = skip })
}

// WithOTelYAMLOverlay merges a YAML snippet into the generated config (deep-merge).
func WithOTelYAMLOverlay(snippet string) Option {
	return WithOTelConfigMutator(func(root map[string]any) error {
		var overlay map[string]any
		if err := yaml.Unmarshal([]byte(snippet), &overlay); err != nil {
			return fmt.Errorf("overlay unmarshal: %w", err)
		}
		deepMerge(root, overlay)
		return nil
	})
}

// deepMerge overlays src onto dst recursively for map[string]any.
func deepMerge(dst, src map[string]any) {
	for k, sv := range src {
		if dv, ok := dst[k]; ok {
			dm, okd := dv.(map[string]any)
			sm, oks := sv.(map[string]any)
			if okd && oks {
				deepMerge(dm, sm)
				continue
			}
		}
		dst[k] = sv
	}
}

// --- OTEL config mutators ---

// OTelConfigMutator receives the root OTEL conf map and may mutate it.
type OTelConfigMutator func(root map[string]any) error

// WithOTelConfigMutator registers a raw mutator (maximum flexibility).
func WithOTelConfigMutator(m OTelConfigMutator) Option {
	return func(ro *runOptions) { ro.cfgMutators = append(ro.cfgMutators, m) }
}

func Run(ctx context.Context, cfg *config.Config, opts ...Option) error {
	// collect options
	ro := &runOptions{}
	for _, opt := range opts {
		opt(ro)
	}

	root, err := buildOTelConfigMap(cfg)
	if err != nil {
		return fmt.Errorf("failed to build OTEL config: %w", err)
	}
	for _, m := range ro.cfgMutators {
		if err := m(root); err != nil {
			return fmt.Errorf("OTEL config mutation failed: %w", err)
		}
	}

	ymlBytes, err := yaml.Marshal(root)
	if err != nil {
		return fmt.Errorf("OTEL config marshal: %w", err)
	}
	yml := string(ymlBytes)

	// Provide config via env var to the collector
	if err := os.Setenv("OTEL_CONFIG_YAML", yml); err != nil {
		return fmt.Errorf("failed to set OTEL_CONFIG_YAML env var: %w", err)
	}

	// Create collector settings
	settings := otelcol.CollectorSettings{
		BuildInfo: component.BuildInfo{
			Command:     "flightctl-telemetry-gateway",
			Description: "Flight Control Telemetry Gateway",
			Version:     version.Get().String(),
		},
		ConfigProviderSettings: otelcol.ConfigProviderSettings{
			ResolverSettings: confmap.ResolverSettings{
				URIs: []string{
					"env:OTEL_CONFIG_YAML",
				},
				ProviderFactories: []confmap.ProviderFactory{
					newStrictEnvProviderFactory(),
				},
				DefaultScheme: "env",
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
					component.MustNewType("otlphttp"):              otlphttpexporter.NewFactory(),
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

	for _, sm := range ro.settingsMutators {
		sm(&settings)
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

func isHTTPEndpoint(endpoint string) bool {
	u, err := url.Parse(endpoint)
	if err != nil {
		return false
	}
	return u.Scheme == "http" || u.Scheme == "https"
}

func validateHTTPEndpoint(endpoint string) error {
	u, err := url.Parse(endpoint)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}
	if u.Host == "" {
		return fmt.Errorf("missing host in endpoint %q", endpoint)
	}
	if u.RawQuery != "" {
		return fmt.Errorf("query strings are not supported in endpoint %q", endpoint)
	}
	if u.Fragment != "" {
		return fmt.Errorf("fragments are not supported in endpoint %q", endpoint)
	}
	return nil
}

// strictEnvProvider wraps the OTel envprovider but fails on undefined
// environment variables instead of logging a warning and continuing
// with an empty string.
type strictEnvProvider struct {
	inner confmap.Provider
}

func newStrictEnvProviderFactory() confmap.ProviderFactory {
	return confmap.NewProviderFactory(func(ps confmap.ProviderSettings) confmap.Provider {
		return &strictEnvProvider{inner: envprovider.NewFactory().Create(ps)}
	})
}

func (p *strictEnvProvider) Retrieve(ctx context.Context, uri string, watcher confmap.WatcherFunc) (*confmap.Retrieved, error) {
	if !strings.HasPrefix(uri, "env:") {
		return nil, fmt.Errorf("%q uri is not supported by env provider", uri)
	}
	name := strings.TrimPrefix(uri, "env:")
	if idx := strings.Index(name, ":-"); idx >= 0 {
		name = name[:idx]
	}
	if _, ok := os.LookupEnv(name); !ok {
		return nil, fmt.Errorf("undefined environment variable: %s", name)
	}
	return p.inner.Retrieve(ctx, uri, watcher)
}

func (p *strictEnvProvider) Scheme() string                     { return "env" }
func (p *strictEnvProvider) Shutdown(ctx context.Context) error { return p.inner.Shutdown(ctx) }

// buildOTelConfigMap builds the OTEL collector config.
func buildOTelConfigMap(cfg *config.Config) (map[string]any, error) {
	exporterNames := []string{}
	exporters := map[string]any{}

	if cfg.TelemetryGateway.Export != nil && cfg.TelemetryGateway.Export.Prometheus != "" {
		exporters["prometheus"] = map[string]any{
			"endpoint": cfg.TelemetryGateway.Export.Prometheus,
		}
		exporterNames = append(exporterNames, "prometheus")
	}
	if cfg.TelemetryGateway.Forward != nil && cfg.TelemetryGateway.Forward.Endpoint != "" {
		fwd := cfg.TelemetryGateway.Forward
		exporterCfg := map[string]any{
			"endpoint": fwd.Endpoint,
		}
		if fwd.TLS != nil {
			tls := map[string]any{}
			if fwd.TLS.InsecureSkipTlsVerify {
				tls["insecure_skip_verify"] = true
			}
			if v := fwd.TLS.CertFile; v != "" {
				tls["cert_file"] = v
			}
			if v := fwd.TLS.KeyFile; v != "" {
				tls["key_file"] = v
			}
			if v := fwd.TLS.CAFile; v != "" {
				tls["ca_file"] = v
			}
			if len(tls) > 0 {
				exporterCfg["tls"] = tls
			}
		}
		exporterKey := "otlp"
		if isHTTPEndpoint(fwd.Endpoint) {
			exporterKey = "otlphttp"
			if err := validateHTTPEndpoint(fwd.Endpoint); err != nil {
				return nil, fmt.Errorf("forward endpoint: %w", err)
			}
			if len(fwd.Headers) > 0 {
				headers := make(map[string]string, len(fwd.Headers))
				for k, v := range fwd.Headers {
					headers[k] = v.Value()
				}
				exporterCfg["headers"] = headers
			}
		}
		exporters[exporterKey] = exporterCfg
		exporterNames = append(exporterNames, exporterKey)
	}
	if len(exporterNames) == 0 {
		return nil, fmt.Errorf("no exporters configured")
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
	return root, nil
}
