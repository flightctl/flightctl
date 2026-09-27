package auxiliary

import "testing"

func TestTelemetryCollectorImage(t *testing.T) {
	tests := []struct {
		name string
		env  string
		want string
	}{
		{name: "default image", want: telemetryHTTPCollectorImage},
		{name: "configured image", env: "registry.example.test/ubi9/python-312:latest", want: "registry.example.test/ubi9/python-312:latest"},
		{name: "blank override", env: "  ", want: telemetryHTTPCollectorImage},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(telemetryHTTPCollectorImageEnv, tt.env)
			if got := telemetryCollectorImage(); got != tt.want {
				t.Fatalf("telemetryCollectorImage() = %q, want %q", got, tt.want)
			}
		})
	}
}
