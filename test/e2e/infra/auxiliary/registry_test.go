package auxiliary

import (
	"path/filepath"
	"testing"
)

func TestRegistryCertsDir(t *testing.T) {
	tests := []struct {
		name        string
		certsRoot   string
		registryURL string
		want        string
		wantErr     bool
	}{
		{
			name:        "When the registry URL is host and port, it should return the matching directory",
			certsRoot:   "/etc/containers/certs.d",
			registryURL: "10.0.0.1:5000",
			want:        filepath.Join("/etc/containers/certs.d", "10.0.0.1:5000"),
		},
		{
			name:        "When the registry URL is IPv6, it should preserve the bracketed host",
			certsRoot:   "/etc/containers/certs.d",
			registryURL: "[::1]:5000",
			want:        filepath.Join("/etc/containers/certs.d", "[::1]:5000"),
		},
		{
			name:        "When the registry URL contains a path separator, it should be rejected",
			certsRoot:   "/etc/containers/certs.d",
			registryURL: "../registry:5000",
			wantErr:     true,
		},
		{
			name:        "When the registry URL has no port, it should be rejected",
			certsRoot:   "/etc/containers/certs.d",
			registryURL: "registry.example.test",
			wantErr:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := registryCertsDir(tt.certsRoot, tt.registryURL)
			if tt.wantErr {
				if err == nil {
					t.Fatal("registryCertsDir() expected an error")
				}
				return
			}
			if err != nil {
				t.Fatalf("registryCertsDir() error = %v", err)
			}
			if got != tt.want {
				t.Errorf("registryCertsDir() = %q, want %q", got, tt.want)
			}
		})
	}
}
