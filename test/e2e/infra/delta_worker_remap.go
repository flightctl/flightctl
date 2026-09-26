package infra

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	"github.com/flightctl/flightctl/test/util"
)

// DeltaWorkerRegistryRemapFiles returns registries.conf.d snippets that rewrite
// quay.io/flightctl image refs to registryURL (host:port).
func DeltaWorkerRegistryRemapFiles(registryURL string) (remap, insecure string) {
	host := registryURL
	if i := strings.LastIndex(registryURL, ":"); i >= 0 {
		host = registryURL[:i]
	}
	remap = fmt.Sprintf(`[[registry]]
prefix = "quay.io/flightctl/flightctl-device"
location = "%s/flightctl/flightctl-device"

[[registry]]
prefix = "quay.io/flightctl/sleep-app"
location = "%s/flightctl/sleep-app"

[[registry]]
prefix = "quay.io/flightctl/dummy-volume"
location = "%s/flightctl/dummy-volume"

[[registry]]
prefix = "quay.io/flightctl-private"
location = "%s:5002/flightctl"

[[registry]]
prefix = "quay.io/flightctl-tests"
location = "%s/flightctl-tests"
`, registryURL, registryURL, registryURL, host, registryURL)
	insecure = fmt.Sprintf(`[[registry]]
location = "%s"
insecure = true
`, registryURL)
	return remap, insecure
}

// DeltaWorkerRegistryCACert returns the E2E CA certificate used by the auxiliary registry.
func DeltaWorkerRegistryCACert() ([]byte, error) {
	path := filepath.Join(util.GetTopLevelDir(), "bin", "e2e-certs", "pki", "CA", "ca.crt")
	cert, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read E2E registry CA %s: %w", path, err)
	}
	return cert, nil
}

// DeltaWorkerRegistryCertDir returns the containers/image certs.d directory for the authenticated registry endpoint.
func DeltaWorkerRegistryCertDir(registryURL string) (string, error) {
	host, _, err := net.SplitHostPort(registryURL)
	if err != nil {
		return "", fmt.Errorf("parse registry URL %q: %w", registryURL, err)
	}
	return net.JoinHostPort(host, "5002"), nil
}
