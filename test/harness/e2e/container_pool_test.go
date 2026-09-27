package e2e

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/flightctl/flightctl/test/harness/e2e/vm"
)

func TestContainerDeviceImageCache(t *testing.T) {
	t.Run("When multiple workers resolve the image concurrently it should resolve once", func(t *testing.T) {
		var resolveCalls atomic.Int32
		cache := containerDeviceImageCache{
			resolve: func() (string, error) {
				resolveCalls.Add(1)
				return "localhost:5000/flightctl/flightctl-device:base-cs10", nil
			},
		}

		type result struct {
			image string
			err   error
		}
		const requests = 32
		start := make(chan struct{})
		results := make(chan result, requests)
		var wg sync.WaitGroup
		for range requests {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				image, err := cache.get()
				results <- result{image: image, err: err}
			}()
		}
		close(start)
		wg.Wait()
		close(results)

		for result := range results {
			if result.err != nil {
				t.Errorf("cache.get() error = %v", result.err)
			}
			if result.image != "localhost:5000/flightctl/flightctl-device:base-cs10" {
				t.Errorf("cache.get() image = %q", result.image)
			}
		}
		if got := resolveCalls.Load(); got != 1 {
			t.Errorf("image resolver called %d times, want 1", got)
		}
	})
}

func TestContainerDeviceNameIsUniqueForConcurrentRequests(t *testing.T) {
	t.Run("When concurrent requests target one worker it should assign unique names", func(t *testing.T) {
		const requests = 100
		names := make(chan string, requests)
		var wg sync.WaitGroup

		for range requests {
			wg.Add(1)
			go func() {
				defer wg.Done()
				names <- containerDeviceName(1)
			}()
		}
		wg.Wait()
		close(names)

		seen := make(map[string]struct{}, requests)
		for name := range names {
			if _, exists := seen[name]; exists {
				t.Errorf("containerDeviceName() returned duplicate name %q", name)
			}
			seen[name] = struct{}{}
		}
	})
}

func TestContainerDeviceRegistryAccess(t *testing.T) {
	tests := []struct {
		name           string
		hostIP         string
		wantHost       string
		wantExtraHosts []string
	}{
		{
			name:     "When the host IP is IPv4 it should use the IP directly",
			hostIP:   "192.0.2.10",
			wantHost: "192.0.2.10",
		},
		{
			name:           "When the host IP is IPv6 it should use the VM registry hostname mapped to the host IP",
			hostIP:         "2001:db8::10",
			wantHost:       registryHostname,
			wantExtraHosts: []string{"e2e-registry:2001:db8::10"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			host, extraHosts := containerDeviceRegistryAccess(tt.hostIP)
			if host != tt.wantHost {
				t.Fatalf("containerDeviceRegistryAccess() host = %q, want %q", host, tt.wantHost)
			}
			if !reflect.DeepEqual(extraHosts, tt.wantExtraHosts) {
				t.Fatalf("containerDeviceRegistryAccess() extra hosts = %v, want %v", extraHosts, tt.wantExtraHosts)
			}

			remap := string(buildRegistryRemapFile(host).Content)
			for _, endpoint := range []string{
				"location = \"" + host + ":5000/flightctl\"",
				"location = \"" + host + ":5002/flightctl\"",
				"prefix = \"quay.io/flightctl-tests\"",
				"location = \"" + host + ":5000/flightctl-tests\"",
			} {
				if !strings.Contains(remap, endpoint) {
					t.Errorf("registry remap %q does not contain %q", remap, endpoint)
				}
			}
		})
	}
}

func TestValidateContainerIdentityFiles(t *testing.T) {
	t.Run("When a certificate path is a dangling symlink it should fail preflight", func(t *testing.T) {
		missingPath := filepath.Join(t.TempDir(), "missing-cert.pem")
		identityPath := filepath.Join(t.TempDir(), "agent-cert.pem")
		if err := os.Symlink(missingPath, identityPath); err != nil {
			t.Fatalf("create dangling symlink: %v", err)
		}

		err := validateContainerIdentityFiles([]vm.ContainerFile{{
			HostPath:      identityPath,
			ContainerPath: "/etc/flightctl/certs/agent-cert.pem",
		}})
		if err == nil {
			t.Fatal("validateContainerIdentityFiles() error = nil, want an error for the dangling symlink")
		}
		if !strings.Contains(err.Error(), identityPath) {
			t.Errorf("error %q does not identify the invalid path %q", err, identityPath)
		}
	})
}
