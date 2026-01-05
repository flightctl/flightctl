package common

// TracingConfig holds OpenTelemetry tracing configuration.
type TracingConfig struct {
	Enabled  bool   `json:"enabled,omitempty"`
	Endpoint string `json:"endpoint,omitempty"`
	Insecure bool   `json:"insecure,omitempty"`
}

// NewDefaultTracingConfig returns a default tracing configuration (disabled).
func NewDefaultTracingConfig() *TracingConfig {
	return &TracingConfig{
		Enabled: false,
	}
}
