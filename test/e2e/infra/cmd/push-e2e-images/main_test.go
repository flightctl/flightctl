package main

import (
	"os"
	"strings"
	"testing"

	"github.com/flightctl/flightctl/test/e2e/infra/auxiliary"
)

const (
	registryEndpointIPv4 = "192.168.129.73:5000"
	registryEndpointIPv6 = "[2001:db8::73]:5000"
)

var registryEndpointCases = []struct {
	name     string
	endpoint string
	wantHost string
	wantErr  bool
}{
	{name: "When endpoint is an IPv4 address and port it should return the IPv4 host", endpoint: registryEndpointIPv4, wantHost: "192.168.129.73"},
	{name: "When endpoint is an IPv6 address and port it should return the IPv6 host", endpoint: registryEndpointIPv6, wantHost: "2001:db8::73"},
	{name: "When endpoint is a hostname and port it should return the hostname", endpoint: "registry.example.test:5000", wantHost: "registry.example.test"},
	{name: "When endpoint is an IPv4 address without a port it should return the address", endpoint: "192.168.129.73", wantHost: "192.168.129.73"},
	{name: "When endpoint is empty it should return an error", endpoint: "", wantErr: true},
	{name: "When endpoint has malformed IPv6 and port it should return an error", endpoint: "2001:db8::73:5000:bad:z", wantErr: true},
	{name: "When endpoint contains userinfo it should return an error", endpoint: "user:secret@registry.example.test:5000", wantErr: true},
}

// TestHostFromRegistryEndpointRedactsInvalidEndpoint verifies that validation errors do not expose endpoint contents.
func TestHostFromRegistryEndpointRedactsInvalidEndpoint(t *testing.T) {
	const endpoint = "user:secret@registry.example.test:5000"

	if _, err := hostFromRegistryEndpoint(endpoint); err == nil {
		t.Fatal("hostFromRegistryEndpoint() error = nil, want error")
	} else if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), endpoint) {
		t.Errorf("hostFromRegistryEndpoint() error = %q, contains endpoint credentials", err)
	}
}

// TestHostFromRegistryEndpoint verifies endpoint parsing for IPv4, IPv6, hostnames, and invalid input.
func TestHostFromRegistryEndpoint(t *testing.T) {
	for _, tc := range registryEndpointCases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := hostFromRegistryEndpoint(tc.endpoint)
			if tc.wantErr {
				if err == nil {
					t.Errorf("hostFromRegistryEndpoint(%q) error = nil, want error", tc.endpoint)
				}
				return
			}
			if err != nil {
				t.Errorf("hostFromRegistryEndpoint(%q) error = %v", tc.endpoint, err)
				return
			}
			if got != tc.wantHost {
				t.Errorf("hostFromRegistryEndpoint(%q) = %q, want %q", tc.endpoint, got, tc.wantHost)
			}
		})
	}
}

// TestConfigureAuxHostUsesRegistryEndpoint verifies that the registry endpoint supplies the auxiliary host when needed.
func TestConfigureAuxHostUsesRegistryEndpoint(t *testing.T) {
	t.Run("When registry endpoint is valid it should set the auxiliary host", func(t *testing.T) {
		t.Setenv(registryEndpointEnv, registryEndpointIPv4)
		t.Setenv(auxiliary.E2EAuxHostEnv, "")

		if err := configureAuxHost(); err != nil {
			t.Fatalf("configureAuxHost() error = %v", err)
		}
		if got := auxiliary.GetHostIP(); got != "192.168.129.73" {
			t.Errorf("auxiliary.GetHostIP() = %q, want %q", got, "192.168.129.73")
		}
	})

	t.Run("When registry endpoint is malformed it should return an error and leave the auxiliary host unset", func(t *testing.T) {
		t.Setenv(registryEndpointEnv, "2001:db8::73:5000:bad:z")
		t.Setenv(auxiliary.E2EAuxHostEnv, "")
		if err := os.Unsetenv(auxiliary.E2EAuxHostEnv); err != nil {
			t.Fatalf("unset %s: %v", auxiliary.E2EAuxHostEnv, err)
		}

		if err := configureAuxHost(); err == nil {
			t.Fatal("configureAuxHost() error = nil, want error")
		}
		if _, ok := os.LookupEnv(auxiliary.E2EAuxHostEnv); ok {
			t.Errorf("%s is set after configureAuxHost returned an error", auxiliary.E2EAuxHostEnv)
		}
	})
}

// TestConfigureAuxHostPreservesExplicitHost verifies that an explicit auxiliary host takes precedence.
func TestConfigureAuxHostPreservesExplicitHost(t *testing.T) {
	t.Setenv(registryEndpointEnv, registryEndpointIPv4)
	t.Setenv(auxiliary.E2EAuxHostEnv, "192.168.124.72")

	if err := configureAuxHost(); err != nil {
		t.Fatalf("configureAuxHost() error = %v", err)
	}
	if got := auxiliary.GetHostIP(); got != "192.168.124.72" {
		t.Errorf("auxiliary.GetHostIP() = %q, want %q", got, "192.168.124.72")
	}
}
