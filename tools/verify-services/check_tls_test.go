package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestQuadletMountsServerCert_WhenVolumePresent_itShouldReturnTrue(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "deploy/podman/flightctl-api")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "Volume=/etc/flightctl/pki/flightctl-api/server.crt:/root/.flightctl/certs/server.crt:ro,z\n"
	if err := os.WriteFile(filepath.Join(dir, "flightctl-api.container"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if !quadletMountsServerCert(root, "api") {
		t.Fatal("expected true")
	}
}

func TestQuadletMountsServerCert_WhenNoServerCert_itShouldReturnFalse(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "deploy/podman/flightctl-imagebuilder-api")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "Volume=/etc/flightctl/pki/ca-bundle.crt:/root/.flightctl/certs/ca-bundle.crt:ro,z\n"
	if err := os.WriteFile(filepath.Join(dir, "flightctl-imagebuilder-api.container"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if quadletMountsServerCert(root, "imagebuilder-api") {
		t.Fatal("expected false for gateway-terminated HTTP service")
	}
}
