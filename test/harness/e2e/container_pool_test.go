package e2e

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flightctl/flightctl/test/harness/e2e/vm"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

func TestLazyVMSetupAbort(t *testing.T) {
	if abortMode := os.Getenv("TEST_LAZY_VM_ABORT"); abortMode != "" {
		gomega.RegisterFailHandler(ginkgo.Fail)
		ginkgo.BeforeEach(func() {
			ginkgo.DeferCleanup(func() {
				fmt.Fprintln(os.Stdout, "VM_ABORT_CLEANUP_RAN")
			})
			if abortMode == "container" {
				t.Setenv("E2E_REQUIRE_CONTAINER_DEVICE", "true")
				globalContainerDeviceImageCache.resolve = func() (string, error) {
					return "", errors.New("container image missing")
				}
				RequireContainerDeviceImage()
			} else {
				abortVMSetup(7, errors.New("VM boot failed"))
			}
		})
		ginkgo.It("should not run after setup aborts", func() {
			t.Error("spec ran after setup aborted")
		})
		ginkgo.RunSpecs(t, "Lazy VM setup abort regression")
		return
	}
	for _, abortMode := range []string{"vm", "container"} {
		t.Run("When "+abortMode+" setup aborts it should preserve cleanup and JUnit", func(t *testing.T) {
			testDeviceSetupAbort(t, abortMode)
		})
	}
}

func testDeviceSetupAbort(t *testing.T, abortMode string) {
	t.Helper()
	reportPath := filepath.Join(t.TempDir(), "junit.xml")
	command := exec.Command(os.Args[0], "-test.run=^TestLazyVMSetupAbort$", "-ginkgo.junit-report="+reportPath)
	command.Env = append(os.Environ(), "TEST_LAZY_VM_ABORT="+abortMode)
	output, err := command.CombinedOutput()
	var exitError *exec.ExitError
	if !errors.As(err, &exitError) || exitError.ExitCode() != 1 {
		t.Fatalf("VM abort exit = %v, want Ginkgo failure exit 1; output: %s", err, output)
	}
	if abortMode == "vm" && !strings.Contains(string(output), E2ESetupAbortStderrMarker) {
		t.Fatalf("VM abort output %q is missing the CI failure marker", output)
	}
	if !strings.Contains(string(output), "VM_ABORT_CLEANUP_RAN") {
		t.Fatalf("VM abort output %q is missing the cleanup marker", output)
	}
	report, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatalf("VM abort JUnit report: %v", err)
	}
	expectedError := "VM boot failed"
	if abortMode == "container" {
		expectedError = "container device image required but not available"
	}
	if !strings.Contains(string(report), expectedError) || !strings.Contains(string(report), `status="aborted"`) {
		t.Fatalf("VM abort JUnit report did not record the suite abort: %s", report)
	}
}

func TestContainerDeviceImageRef(t *testing.T) {
	for _, testCase := range []struct {
		name  string
		image string
		want  string
	}{
		{"When the bundle has a custom repository it should preserve its path", "quay.io/custom/device:base-test", "192.0.2.10:5000/custom/device:base-test"},
		{"When the source registry has a port it should preserve the repository and tag", "registry.test:8443/custom/device:base-test", "192.0.2.10:5000/custom/device:base-test"},
		{"When repository metadata is absent it should use the default repository", "device:base-test", "192.0.2.10:5000/flightctl/flightctl-device:base-test"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if got := containerDeviceImageRef("192.0.2.10", testCase.image); got != testCase.want {
				t.Fatalf("containerDeviceImageRef() = %q, want %q", got, testCase.want)
			}
		})
	}
}

func TestGetContainerForWorkerReturnsNilOnError(t *testing.T) {
	t.Setenv("PWD", filepath.Join(t.TempDir(), "test"))
	originalResolve := globalContainerDeviceImageCache.resolve
	originalDone := globalContainerDeviceImageCache.done
	originalImage := globalContainerDeviceImageCache.image
	t.Cleanup(func() {
		globalContainerDeviceImageCache.resolve = originalResolve
		globalContainerDeviceImageCache.done = originalDone
		globalContainerDeviceImageCache.image = originalImage
	})
	globalContainerDeviceImageCache.done = false
	globalContainerDeviceImageCache.resolve = func() (string, error) {
		return "", errors.New("container device image unavailable")
	}
	for _, testCase := range []struct {
		name string
		get  func(int) (vm.TestVMInterface, error)
	}{
		{"When the pool fails it should return a nil interface", (&ContainerPool{}).GetContainerForWorker},
		{"When the harness fails it should return a nil interface", (&Harness{}).GetContainerFromPool},
		{"When worker setup fails it should return a nil interface", SetupContainerForWorker},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			device, err := testCase.get(1)
			if err == nil || device != nil {
				t.Fatalf("container getter = (%v, %v), want (nil, error)", device, err)
			}
		})
	}
}

func TestContainerDeviceEndpointHosts(t *testing.T) {
	t.Run("When Quadlets are remote it should map runner and deployment aliases separately", func(t *testing.T) {
		binDir := t.TempDir()
		if err := os.WriteFile(filepath.Join(binDir, "hostname"), []byte("#!/bin/sh\nif [ \"$1\" = -f ]; then echo runner.example.test; else echo runner; fi\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
		t.Setenv("QUADLET_HOST", "192.0.2.20")
		aliases, err := containerDeviceEndpointHosts(context.Background(), "192.0.2.10")
		if err != nil {
			t.Fatal(err)
		}
		want := []string{"runner.example.test:192.0.2.10", "runner:192.0.2.10", "flightctl-vm.local:192.0.2.20"}
		if !reflect.DeepEqual(aliases, want) {
			t.Fatalf("aliases = %v, want %v", aliases, want)
		}
	})
	t.Run("When endpoint hosts are DNS names it should use resolved IP addresses", func(t *testing.T) {
		t.Setenv("QUADLET_HOST", "localhost.")
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		aliases, err := containerDeviceEndpointHosts(ctx, "192.0.2.10")
		if err != nil {
			t.Fatal(err)
		}
		foundQuadlet := false
		for _, alias := range aliases {
			_, address, found := strings.Cut(alias, ":")
			if !found || net.ParseIP(address) == nil {
				t.Fatalf("alias %q does not contain an IP address", alias)
			}
			if net.ParseIP(address).IsLoopback() {
				t.Fatalf("alias %q points to the device's loopback", alias)
			}
			if strings.HasPrefix(alias, "flightctl-vm.local:") && address != "192.0.2.10" {
				t.Fatalf("loopback endpoint alias %q does not use the host-visible IP", alias)
			}
			foundQuadlet = foundQuadlet || strings.HasPrefix(alias, "flightctl-vm.local:")
		}
		if !foundQuadlet {
			t.Fatal("resolved Quadlet host alias missing")
		}
	})
	t.Run("When both hosts resolve to loopback it should use the runtime host gateway", func(t *testing.T) {
		t.Setenv("QUADLET_HOST", "127.0.0.2")
		aliases, err := containerDeviceEndpointHosts(context.Background(), "::1")
		if err != nil {
			t.Fatal(err)
		}
		for _, alias := range aliases {
			if alias == "flightctl-vm.local:host-gateway" {
				return
			}
		}
		t.Fatalf("aliases = %v, want the runtime host gateway", aliases)
	})
	t.Run("When an endpoint hostname is invalid it should skip the alias", func(t *testing.T) {
		t.Setenv("QUADLET_HOST", "invalid host name")
		aliases, err := containerDeviceEndpointHosts(context.Background(), "192.0.2.10")
		if err != nil {
			t.Fatal(err)
		}
		for _, alias := range aliases {
			if strings.HasPrefix(alias, "flightctl-vm.local:") {
				t.Fatalf("unresolvable host produced alias %q", alias)
			}
		}
	})
}

func TestPreferredContainerHostIP(t *testing.T) {
	for _, testCase := range []struct {
		name      string
		addresses []string
		want      string
	}{
		{"When DNS returns both families it should prefer IPv4", []string{"2001:db8::1", "192.0.2.10", "192.0.2.20"}, "192.0.2.10"},
		{"When DNS returns only IPv6 it should keep the first address", []string{"2001:db8::1", "2001:db8::2"}, "2001:db8::1"},
		{"When DNS returns loopback and reachable addresses it should ignore loopback", []string{"127.0.0.2", "::1", "192.0.2.10"}, "192.0.2.10"},
		{"When DNS returns IPv4 loopback and reachable IPv6 it should use IPv6", []string{"127.0.0.1", "2001:db8::1"}, "2001:db8::1"},
		{"When DNS returns only loopback it should require a host gateway", []string{"127.0.0.2", "::1"}, ""},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if got := preferredContainerHostIP(testCase.addresses); got != testCase.want {
				t.Fatalf("preferredContainerHostIP() = %q, want %q", got, testCase.want)
			}
		})
	}
}

func TestContainerDeviceRequired(t *testing.T) {
	for _, value := range []string{"", "false", "0", "true", "1"} {
		t.Run("When require-container is "+value+" it should honor boolean semantics", func(t *testing.T) {
			t.Setenv("E2E_REQUIRE_CONTAINER_DEVICE", value)
			if got, want := containerDeviceRequired(), value == "true" || value == "1"; got != want {
				t.Fatalf("containerDeviceRequired() = %v, want %v", got, want)
			}
		})
	}
}

func TestContainerDeviceImageCache(t *testing.T) {
	t.Run("When image resolution fails it should retry and cache only success", func(t *testing.T) {
		resolveCalls := 0
		resolveErr := errors.New("bundle not ready")
		cache := containerDeviceImageCache{resolve: func() (string, error) {
			resolveCalls++
			if resolveCalls == 1 {
				return "", resolveErr
			}
			return "test-image", nil
		}}
		if _, err := cache.get(); !errors.Is(err, resolveErr) {
			t.Fatalf("cache.get() error = %v, want %v", err, resolveErr)
		}
		for range 2 {
			if image, err := cache.get(); err != nil || image != "test-image" {
				t.Fatalf("cache.get() = %q, %v, want test-image, nil", image, err)
			}
		}
		if resolveCalls != 2 {
			t.Fatalf("resolver called %d times, want 2", resolveCalls)
		}
	})
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
		{
			name:           "When host detection returns localhost it should map the registry to the host gateway",
			hostIP:         "localhost",
			wantHost:       registryHostname,
			wantExtraHosts: []string{"e2e-registry:host-gateway"},
		},
		{
			name:           "When host detection returns IPv4 loopback it should map the registry to the host gateway",
			hostIP:         "127.0.0.1",
			wantHost:       registryHostname,
			wantExtraHosts: []string{"e2e-registry:host-gateway"},
		},
		{
			name:           "When host detection returns IPv6 loopback it should map the registry to the host gateway",
			hostIP:         "::1",
			wantHost:       registryHostname,
			wantExtraHosts: []string{"e2e-registry:host-gateway"},
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
