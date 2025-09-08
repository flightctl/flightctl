package provisioner

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/pkg/log"
)

func TestSelfSignedProvisioner_GeneratesCertAndKey(t *testing.T) {
	// Arrange: create an isolated root for file IO
	root, err := os.MkdirTemp("", "self-signed-prov-test-")
	if err != nil {
		t.Fatalf("mkdir temp root: %v", err)
	}
	defer os.RemoveAll(root)

	rw := fileio.NewReadWriter(fileio.WithTestRootDir(root))

	cfg := SelfSignedProvisionerConfig{
		CommonName:        "test-device-cn",
		ExpirationSeconds: 3600, // 1 hour
	}

	p, err := NewSelfSignedProvisioner(&cfg, rw, log.NewPrefixLogger("test"))
	if err != nil {
		t.Fatalf("NewSelfSignedProvisioner: %v", err)
	}

	// Act
	ready, cert, keyPEM, err := p.Provision(context.Background())

	// Assert
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if !ready {
		t.Fatalf("expected ready=true, got false")
	}
	if cert == nil {
		t.Fatalf("expected certificate, got nil")
		return
	}
	if len(keyPEM) == 0 {
		t.Fatalf("expected keyPEM, got empty")
	}
	if got, want := cert.Subject.CommonName, cfg.CommonName; got != want {
		t.Fatalf("common name mismatch: got %q want %q", got, want)
	}
	if !strings.Contains(string(keyPEM), "BEGIN") {
		t.Fatalf("expected PEM-encoded key, got: %q", string(keyPEM)[:min(40, len(keyPEM))])
	}

	// Validate approximate validity (~1h)
	dur := cert.NotAfter.Sub(cert.NotBefore)
	if dur < 50*time.Minute || dur > 2*time.Hour {
		t.Fatalf("unexpected cert validity: %s", dur)
	}

	// Ensure no temp self-signed-ca-* directories remain under our test root
	var foundTemp bool
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err == nil && d.IsDir() && strings.Contains(d.Name(), "self-signed-ca-") {
			foundTemp = true
		}
		return nil
	})
	if foundTemp {
		t.Fatalf("temporary self-signed-ca-* directory was not cleaned up under %s", root)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
