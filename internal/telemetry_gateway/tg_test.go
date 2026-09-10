package telemetrygateway

import (
	"context"
	"strings"
	"testing"

	"github.com/flightctl/flightctl/internal/config"
	"go.opentelemetry.io/collector/confmap"
	"sigs.k8s.io/yaml"
)

func TestIsHTTPEndpoint(t *testing.T) {
	tests := []struct {
		name     string
		endpoint string
		want     bool
	}{
		{"bare host:port is gRPC", "collector.example.com:4317", false},
		{"localhost:port is gRPC", "localhost:4317", false},
		{"https URL", "https://abc123.live.dynatrace.com/api/v2/otlp", true},
		{"http URL", "http://localhost:4318/v1/metrics", true},
		{"HTTPS uppercase", "HTTPS://example.com/otlp", true},
		{"mixed case", "Https://example.com", true},
		{"empty string", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isHTTPEndpoint(tt.endpoint); got != tt.want {
				t.Errorf("isHTTPEndpoint(%q) = %v, want %v", tt.endpoint, got, tt.want)
			}
		})
	}
}

func forwardCfg(t *testing.T, yamlSnippet string) *config.Config {
	t.Helper()
	cfg := config.NewDefault()
	if err := yaml.Unmarshal([]byte(yamlSnippet), cfg); err != nil {
		t.Fatalf("unmarshal config: %v", err)
	}
	return cfg
}

func TestBuildOTelConfigMap_GRPCForward(t *testing.T) {
	cfg := forwardCfg(t, `
telemetrygateway:
  forward:
    endpoint: "collector.example.com:4317"
`)

	root, err := buildOTelConfigMap(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	exporters, ok := root["exporters"].(map[string]any)
	if !ok {
		t.Fatal("exporters not found in config")
	}
	if _, ok := exporters["otlp"]; !ok {
		t.Error("expected 'otlp' exporter for bare host:port endpoint")
	}
	if _, ok := exporters["otlphttp"]; ok {
		t.Error("unexpected 'otlphttp' exporter for bare host:port endpoint")
	}
}

func TestBuildOTelConfigMap_HTTPForward(t *testing.T) {
	cfg := forwardCfg(t, `
telemetrygateway:
  forward:
    endpoint: "https://abc123.live.dynatrace.com/api/v2/otlp"
    headers:
      Authorization: "Api-Token dt0c01.xxxx"
`)

	root, err := buildOTelConfigMap(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	exporters, ok := root["exporters"].(map[string]any)
	if !ok {
		t.Fatal("exporters not found in config")
	}
	if _, ok := exporters["otlp"]; ok {
		t.Error("unexpected 'otlp' exporter for HTTPS endpoint")
	}

	httpExp, ok := exporters["otlphttp"].(map[string]any)
	if !ok {
		t.Fatal("expected 'otlphttp' exporter for HTTPS endpoint")
	}
	if httpExp["endpoint"] != "https://abc123.live.dynatrace.com/api/v2/otlp" {
		t.Errorf("unexpected endpoint: %v", httpExp["endpoint"])
	}

	headers, ok := httpExp["headers"].(map[string]string)
	if !ok {
		t.Fatal("expected headers in otlphttp exporter config")
	}
	if headers["Authorization"] != "Api-Token dt0c01.xxxx" {
		t.Errorf("unexpected Authorization header: got %q", headers["Authorization"])
	}
}

func TestBuildOTelConfigMap_HTTPForwardWithTLS(t *testing.T) {
	cfg := forwardCfg(t, `
telemetrygateway:
  forward:
    endpoint: "https://collector.example.com/v1/metrics"
    tls:
      insecureSkipTlsVerify: true
      caFile: "/etc/ssl/ca.crt"
`)

	root, err := buildOTelConfigMap(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	exporters := root["exporters"].(map[string]any)
	httpExp := exporters["otlphttp"].(map[string]any)
	tlsCfg, ok := httpExp["tls"].(map[string]any)
	if !ok {
		t.Fatal("expected tls config in otlphttp exporter")
	}
	if tlsCfg["insecure_skip_verify"] != true {
		t.Error("expected insecure_skip_verify=true")
	}
	if tlsCfg["ca_file"] != "/etc/ssl/ca.crt" {
		t.Errorf("unexpected ca_file: %v", tlsCfg["ca_file"])
	}
}

func TestBuildOTelConfigMap_GRPCWithHeadersRejected(t *testing.T) {
	cfg := forwardCfg(t, `
telemetrygateway:
  forward:
    endpoint: "collector.example.com:4317"
    headers:
      Authorization: "Api-Token test"
`)

	_, err := buildOTelConfigMap(cfg)
	if err == nil {
		t.Fatal("expected error when headers are set on a gRPC endpoint")
	}
	if !strings.Contains(err.Error(), "forward headers are only supported for http(s) endpoints") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestBuildOTelConfigMap_HTTPForwardEnvVarNotExpanded(t *testing.T) {
	cfg := forwardCfg(t, `
telemetrygateway:
  forward:
    endpoint: "https://abc123.live.dynatrace.com/api/v2/otlp"
    headers:
      Authorization: "Api-Token ${TEST_DT_TOKEN}"
`)

	root, err := buildOTelConfigMap(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	exporters := root["exporters"].(map[string]any)
	httpExp := exporters["otlphttp"].(map[string]any)
	headers := httpExp["headers"].(map[string]string)
	if headers["Authorization"] != "Api-Token ${TEST_DT_TOKEN}" {
		t.Errorf("header value should be left unresolved for Collector expansion, got %q", headers["Authorization"])
	}
}

func TestBuildOTelConfigMap_HTTPInPipelineExporters(t *testing.T) {
	cfg := forwardCfg(t, `
telemetrygateway:
  forward:
    endpoint: "https://ingest.example.com/v1/metrics"
`)

	root, err := buildOTelConfigMap(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	svc := root["service"].(map[string]any)
	pipelines := svc["pipelines"].(map[string]any)
	metrics := pipelines["metrics"].(map[string]any)
	exporterNames := metrics["exporters"].([]string)

	found := false
	for _, name := range exporterNames {
		if name == "otlphttp" {
			found = true
		}
		if name == "otlp" {
			t.Error("pipeline should use 'otlphttp', not 'otlp', for HTTPS endpoint")
		}
	}
	if !found {
		t.Error("expected 'otlphttp' in pipeline exporters")
	}
}

func TestStrictEnvProvider(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		envVars map[string]string
		wantErr bool
	}{
		{
			"defined variable resolves",
			`value: "${env:MY_TOKEN}"`,
			map[string]string{"MY_TOKEN": "secret123"},
			false,
		},
		{
			"undefined variable fails",
			`value: "${env:UNDEFINED_VAR}"`,
			nil,
			true,
		},
		{
			"bare ${VAR} syntax resolves via DefaultScheme",
			`value: "${MY_TOKEN}"`,
			map[string]string{"MY_TOKEN": "secret123"},
			false,
		},
		{
			"bare ${VAR} syntax fails when undefined",
			`value: "${MISSING_VAR}"`,
			nil,
			true,
		},
		{
			"empty but defined variable is allowed",
			`value: "${env:EMPTY_VAR}"`,
			map[string]string{"EMPTY_VAR": ""},
			false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for k, v := range tt.envVars {
				t.Setenv(k, v)
			}
			t.Setenv("TEST_YAML", tt.yaml)

			resolver, err := confmap.NewResolver(confmap.ResolverSettings{
				URIs:              []string{"env:TEST_YAML"},
				ProviderFactories: []confmap.ProviderFactory{newStrictEnvProviderFactory()},
				DefaultScheme:     "env",
			})
			if err != nil {
				t.Fatalf("creating resolver: %v", err)
			}

			_, err = resolver.Resolve(context.Background())
			if tt.wantErr && err == nil {
				t.Error("expected error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestBuildOTelConfigMap_HTTPForwardUndefinedEnvVarPassesThrough(t *testing.T) {
	cfg := forwardCfg(t, `
telemetrygateway:
  forward:
    endpoint: "https://abc123.live.dynatrace.com/api/v2/otlp"
    headers:
      Authorization: "Api-Token ${NONEXISTENT_TOKEN}"
`)

	root, err := buildOTelConfigMap(cfg)
	if err != nil {
		t.Fatalf("buildOTelConfigMap should not validate env vars, got: %v", err)
	}

	exporters := root["exporters"].(map[string]any)
	httpExp := exporters["otlphttp"].(map[string]any)
	headers := httpExp["headers"].(map[string]string)
	if !strings.Contains(headers["Authorization"], "${NONEXISTENT_TOKEN}") {
		t.Errorf("header should contain unresolved reference, got %q", headers["Authorization"])
	}
}

func TestBuildOTelConfigMap_HTTPForwardWithFullTLS(t *testing.T) {
	cfg := forwardCfg(t, `
telemetrygateway:
  forward:
    endpoint: "https://collector.example.com/api/v2/otlp"
    tls:
      certFile: "/etc/ssl/client.crt"
      keyFile: "/etc/ssl/client.key"
      caFile: "/etc/ssl/ca.crt"
`)

	root, err := buildOTelConfigMap(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	exporters := root["exporters"].(map[string]any)
	httpExp := exporters["otlphttp"].(map[string]any)
	tlsCfg, ok := httpExp["tls"].(map[string]any)
	if !ok {
		t.Fatal("expected tls config in otlphttp exporter")
	}
	if tlsCfg["cert_file"] != "/etc/ssl/client.crt" {
		t.Errorf("unexpected cert_file: %v", tlsCfg["cert_file"])
	}
	if tlsCfg["key_file"] != "/etc/ssl/client.key" {
		t.Errorf("unexpected key_file: %v", tlsCfg["key_file"])
	}
	if tlsCfg["ca_file"] != "/etc/ssl/ca.crt" {
		t.Errorf("unexpected ca_file: %v", tlsCfg["ca_file"])
	}
}

func TestBuildOTelConfigMap_GRPCForwardWithTLS(t *testing.T) {
	cfg := forwardCfg(t, `
telemetrygateway:
  forward:
    endpoint: "collector.example.com:4317"
    tls:
      certFile: "/etc/ssl/client.crt"
      keyFile: "/etc/ssl/client.key"
      caFile: "/etc/ssl/ca.crt"
`)

	root, err := buildOTelConfigMap(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	exporters := root["exporters"].(map[string]any)
	grpcExp, ok := exporters["otlp"].(map[string]any)
	if !ok {
		t.Fatal("expected 'otlp' exporter for gRPC endpoint")
	}
	tlsCfg, ok := grpcExp["tls"].(map[string]any)
	if !ok {
		t.Fatal("expected tls config in otlp exporter")
	}
	if tlsCfg["cert_file"] != "/etc/ssl/client.crt" {
		t.Errorf("unexpected cert_file: %v", tlsCfg["cert_file"])
	}
	if tlsCfg["key_file"] != "/etc/ssl/client.key" {
		t.Errorf("unexpected key_file: %v", tlsCfg["key_file"])
	}
	if tlsCfg["ca_file"] != "/etc/ssl/ca.crt" {
		t.Errorf("unexpected ca_file: %v", tlsCfg["ca_file"])
	}
}

func TestBuildOTelConfigMap_NoExportersError(t *testing.T) {
	cfg := forwardCfg(t, `
telemetrygateway:
  export: null
`)

	_, err := buildOTelConfigMap(cfg)
	if err == nil {
		t.Fatal("expected error when no exporters configured")
	}
	if !strings.Contains(err.Error(), "no exporters configured") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestBuildOTelConfigMap_InvalidHTTPEndpointError(t *testing.T) {
	cfg := forwardCfg(t, `
telemetrygateway:
  forward:
    endpoint: "https://host/path?key=val"
`)

	_, err := buildOTelConfigMap(cfg)
	if err == nil {
		t.Fatal("expected error for HTTP endpoint with query string")
	}
	if !strings.Contains(err.Error(), "forward endpoint") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestValidateHTTPEndpoint(t *testing.T) {
	tests := []struct {
		name     string
		endpoint string
		wantErr  bool
	}{
		{"valid https URL", "https://host/api/v2/otlp", false},
		{"valid with trailing slash", "https://host/api/v2/otlp/", false},
		{"valid with port", "https://host:443/api/v2/otlp", false},
		{"missing host", "https://", true},
		{"missing host with path", "https:///path", true},
		{"has query string", "https://host/path?key=val", true},
		{"has fragment", "https://host/path#section", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateHTTPEndpoint(tt.endpoint)
			if tt.wantErr && err == nil {
				t.Errorf("expected error for %q", tt.endpoint)
			}
			if !tt.wantErr && err != nil {
				t.Errorf("unexpected error for %q: %v", tt.endpoint, err)
			}
		})
	}
}
