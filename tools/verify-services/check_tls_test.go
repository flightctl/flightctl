package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestQuadletMountsServerCert(t *testing.T) {
	cases := []struct {
		name    string
		service string
		content string
		want    bool
	}{
		{
			name:    "When volume present it should return true",
			service: "api",
			content: "Volume=/etc/flightctl/pki/flightctl-api/server.crt:/root/.flightctl/certs/server.crt:ro,z\n",
			want:    true,
		},
		{
			name:    "When no server cert it should return false",
			service: "imagebuilder-api",
			content: "Volume=/etc/flightctl/pki/ca-bundle.crt:/root/.flightctl/certs/ca-bundle.crt:ro,z\n",
			want:    false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			dir := filepath.Join(root, "deploy/podman/flightctl-"+tc.service)
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "flightctl-"+tc.service+".container"), []byte(tc.content), 0o644); err != nil {
				t.Fatal(err)
			}
			if got := quadletMountsServerCert(root, tc.service); got != tc.want {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
}
