//go:build linux

package e2e

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEnrollmentServerFromConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		content   string
		want      string
		wantError string
	}{
		{
			name:    "When config uses an OCP enrollment endpoint it should return the endpoint",
			content: "enrollment-service:\n  service:\n    server: https://agent-api.flightctl.apps.example.com\n",
			want:    "https://agent-api.flightctl.apps.example.com",
		},
		{
			name:    "When config uses a quadlet enrollment endpoint it should return the endpoint",
			content: "enrollment-service:\n  service:\n    server: https://flightctl-vm.local:7443/\n",
			want:    "https://flightctl-vm.local:7443/",
		},
		{
			name:      "When config omits an enrollment endpoint it should return an error",
			content:   "enrollment-service:\n  service: {}\n",
			wantError: "does not define enrollment-service.service.server",
		},
		{
			name:      "When config is malformed it should return an error",
			content:   "enrollment-service: [\n",
			wantError: "parse agent config",
		},
		{
			name:      "When config contains an unknown field it should return an error",
			content:   "enrollment-service:\n  service:\n    server: https://agent-api.flightctl.apps.example.com\nunexpected-field: true\n",
			wantError: "parse agent config",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			configPath := filepath.Join(t.TempDir(), "config.yaml")
			require.NoError(t, os.WriteFile(configPath, []byte(test.content), 0o600))

			got, err := enrollmentServerFromConfig(configPath)
			if test.wantError != "" {
				require.ErrorContains(t, err, test.wantError)
				return
			}
			require.NoError(t, err)
			require.Equal(t, test.want, got)
		})
	}

	t.Run("When config file is missing it should return an error", func(t *testing.T) {
		t.Parallel()
		_, err := enrollmentServerFromConfig(filepath.Join(t.TempDir(), "missing.yaml"))
		require.ErrorContains(t, err, "read agent config")
	})
}

func TestEnrollmentHostMappingFromServer(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name               string
		server             string
		lookupIPs          []net.IP
		lookupErr          error
		wantLookupHostname string
		wantLookupCall     bool
		want               string
		wantError          string
	}{
		{
			name:               "When the enrollment host has an IPv4 address it should return a container host mapping",
			server:             "https://agent-api.flightctl.apps.example.com/api/v1/enrollmentrequests",
			lookupIPs:          []net.IP{net.ParseIP("192.168.130.10")},
			wantLookupHostname: "agent-api.flightctl.apps.example.com",
			wantLookupCall:     true,
			want:               "agent-api.flightctl.apps.example.com:192.168.130.10",
		},
		{
			name:               "When the enrollment host only has IPv6 it should return an error",
			server:             "https://agent-api.flightctl.apps.example.com",
			lookupIPs:          []net.IP{net.ParseIP("2001:db8::1")},
			wantLookupHostname: "agent-api.flightctl.apps.example.com",
			wantLookupCall:     true,
			wantError:          "has no IPv4 address",
		},
		{
			name:               "When the enrollment hostname cannot be resolved it should return an error",
			server:             "https://agent-api.flightctl.apps.example.com",
			lookupErr:          errors.New("lookup failed"),
			wantLookupHostname: "agent-api.flightctl.apps.example.com",
			wantLookupCall:     true,
			wantError:          "resolve enrollment hostname",
		},
		{
			name:      "When the enrollment server has no hostname it should return an error",
			server:    "https:///api/v1/enrollmentrequests",
			wantError: "does not define a hostname",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			lookupCalled := false
			lookupIP := func(_ context.Context, network, hostname string) ([]net.IP, error) {
				lookupCalled = true
				if network != "ip4" {
					t.Errorf("lookup network = %q, want ip4", network)
				}
				if hostname != test.wantLookupHostname {
					t.Errorf("lookup hostname = %q, want %q", hostname, test.wantLookupHostname)
				}
				return test.lookupIPs, test.lookupErr
			}

			got, err := enrollmentHostMappingFromServer(context.Background(), test.server, lookupIP)
			require.Equal(t, test.wantLookupCall, lookupCalled)
			if test.wantError != "" {
				require.ErrorContains(t, err, test.wantError)
				return
			}
			require.NoError(t, err)
			require.Equal(t, test.want, got)
		})
	}
}
