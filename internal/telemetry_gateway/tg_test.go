package telemetrygateway

import (
	"strings"
	"testing"

	"github.com/flightctl/flightctl/internal/config"
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
		t.Errorf("unexpected Authorization header: %v", headers["Authorization"])
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

func TestBuildOTelConfigMap_GRPCWithHeadersIgnored(t *testing.T) {
	cfg := forwardCfg(t, `
telemetrygateway:
  forward:
    endpoint: "collector.example.com:4317"
    headers:
      Authorization: "Api-Token test"
`)

	root, err := buildOTelConfigMap(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	exporters := root["exporters"].(map[string]any)
	if _, ok := exporters["otlp"]; !ok {
		t.Error("expected 'otlp' exporter for gRPC endpoint even with headers set")
	}
}

func TestBuildOTelConfigMap_HTTPForwardEnvVarExpansion(t *testing.T) {
	t.Setenv("TEST_DT_TOKEN", "dt0c01.secret-from-env")

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
	if headers["Authorization"] != "Api-Token dt0c01.secret-from-env" {
		t.Errorf("env var not expanded in header: got %q", headers["Authorization"])
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

func TestExpandEnvStrict(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		envVars map[string]string
		want    string
		wantErr bool
	}{
		{"no variables", "static-value", nil, "static-value", false},
		{"single defined variable", "Api-Token ${MY_TOKEN}", map[string]string{"MY_TOKEN": "secret123"}, "Api-Token secret123", false},
		{"empty but defined variable is allowed", "Bearer ${EMPTY_VAR}", map[string]string{"EMPTY_VAR": ""}, "Bearer ", false},
		{"undefined variable returns error", "Api-Token ${UNDEFINED_VAR}", nil, "", true},
		{"multiple variables with one undefined", "${DEFINED}-${ALSO_UNDEFINED}", map[string]string{"DEFINED": "ok"}, "", true},
		{"bare $VAR is not expanded", "Api-Token $BARE_VAR", map[string]string{"BARE_VAR": "secret"}, "Api-Token $BARE_VAR", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for k, v := range tt.envVars {
				t.Setenv(k, v)
			}
			got, err := expandEnvStrict(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Errorf("expected error, got %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestBuildOTelConfigMap_HTTPForwardUndefinedEnvVar(t *testing.T) {
	cfg := forwardCfg(t, `
telemetrygateway:
  forward:
    endpoint: "https://abc123.live.dynatrace.com/api/v2/otlp"
    headers:
      Authorization: "Api-Token ${NONEXISTENT_TOKEN}"
`)

	_, err := buildOTelConfigMap(cfg)
	if err == nil {
		t.Fatal("expected error for undefined env var, got nil")
	}
	if !strings.Contains(err.Error(), "NONEXISTENT_TOKEN") {
		t.Errorf("error should identify the missing variable, got: %v", err)
	}
	if !strings.Contains(err.Error(), "Authorization") {
		t.Errorf("error should identify the header name, got: %v", err)
	}
}

func TestValidateHTTPEndpoint(t *testing.T) {
	tests := []struct {
		name    string
		endpoint string
		wantErr bool
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
