package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"

	"github.com/flightctl/flightctl/test/e2e/infra/auxiliary"
	"github.com/flightctl/flightctl/test/e2e/infra/setup"
	"github.com/sirupsen/logrus"
)

const registryEndpointEnv = "REGISTRY_ENDPOINT"

var errInvalidRegistryEndpoint = errors.New("invalid registry endpoint")

// main starts the auxiliary registry after aligning its host with the registry endpoint.
func main() {
	ctx := context.Background()
	// Image pushing can start the registry before run_e2e_tests.sh exports E2E_AUX_HOST.
	// Use the already-resolved registry endpoint so the certificate SAN matches the endpoint used by agents.
	if err := configureAuxHost(); err != nil {
		logrus.Fatalf("push e2e images: configure auxiliary host: %v", err)
	}
	if err := setup.EnsureDefaultProviders(nil); err != nil {
		logrus.Fatalf("push e2e images: providers: %v", err)
	}
	if _, err := auxiliary.StartServices(ctx, []auxiliary.Service{auxiliary.ServiceRegistry}); err != nil {
		logrus.Fatalf("push e2e images: %v", err)
	}
	os.Exit(0)
}

// configureAuxHost sets E2E_AUX_HOST from REGISTRY_ENDPOINT when no explicit host was provided.
func configureAuxHost() error {
	if strings.TrimSpace(os.Getenv(auxiliary.E2EAuxHostEnv)) != "" {
		return nil
	}
	endpoint := strings.TrimSpace(os.Getenv(registryEndpointEnv))
	if endpoint == "" {
		return nil
	}
	host, err := hostFromRegistryEndpoint(endpoint)
	if err != nil {
		return err
	}
	if err := os.Setenv(auxiliary.E2EAuxHostEnv, host); err != nil {
		return fmt.Errorf("set %s: %w", auxiliary.E2EAuxHostEnv, err)
	}
	logrus.Infof("Using registry endpoint host for %s: %s", auxiliary.E2EAuxHostEnv, host)
	return nil
}

// hostFromRegistryEndpoint extracts the host from a registry endpoint.
func hostFromRegistryEndpoint(endpoint string) (string, error) {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return "", errInvalidRegistryEndpoint
	}
	if ip := net.ParseIP(endpoint); ip != nil {
		return ip.String(), nil
	}
	if host, _, err := net.SplitHostPort(endpoint); err == nil && validRegistryHost(host) {
		return host, nil
	}
	if !strings.Contains(endpoint, ":") && validRegistryHost(endpoint) {
		return endpoint, nil
	}
	return "", errInvalidRegistryEndpoint
}

// validRegistryHost reports whether host contains only hostname or IP data.
func validRegistryHost(host string) bool {
	if host == "" || strings.TrimSpace(host) != host || strings.ContainsAny(host, "@/\\") {
		return false
	}
	if net.ParseIP(host) != nil {
		return true
	}
	if strings.Contains(host, ":") || len(host) > 253 {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, char := range label {
			if (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') && (char < '0' || char > '9') && char != '-' {
				return false
			}
		}
	}
	return true
}
