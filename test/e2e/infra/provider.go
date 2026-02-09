// Package infra provides testcontainers-based infrastructure for E2E tests.
package infra

import (
	"context"
	"sync"

	"github.com/docker/docker/api/types/container"
	"github.com/sirupsen/logrus"
	"github.com/testcontainers/testcontainers-go"
)

var (
	// Global container registry for cleanup when Ryuk is disabled
	globalContainers []testcontainers.Container
	containersMu     sync.Mutex
)

// RegisterContainer adds a container to the global cleanup registry.
// This is essential when Ryuk is disabled to prevent zombie containers.
func RegisterContainer(c testcontainers.Container) {
	containersMu.Lock()
	defer containersMu.Unlock()
	globalContainers = append(globalContainers, c)
}

// ShutdownAll terminates all registered containers.
// Call this in AfterSuite or TestMain cleanup to ensure no zombie containers.
func ShutdownAll(ctx context.Context) {
	containersMu.Lock()
	defer containersMu.Unlock()

	logrus.Infof("Shutting down %d registered containers", len(globalContainers))
	for _, c := range globalContainers {
		if c != nil {
			if err := c.Terminate(ctx); err != nil {
				logrus.Warnf("Failed to terminate container: %v", err)
			}
		}
	}
	globalContainers = nil
}

// GetProviderType returns the Podman provider for testcontainers.
func GetProviderType() testcontainers.ProviderType {
	return testcontainers.ProviderPodman
}

// ContainerRequestOption is a function that modifies a ContainerRequest.
type ContainerRequestOption func(*testcontainers.ContainerRequest)

// WithNetwork sets the container network.
func WithNetwork(network string) ContainerRequestOption {
	return func(req *testcontainers.ContainerRequest) {
		if network != "" && network != "host" {
			req.Networks = []string{network}
		}
		if network == "host" {
			req.NetworkMode = "host"
		}
	}
}

// WithHostAccess configures the container for host access.
// This adds the host.containers.internal DNS entry for Podman compatibility.
func WithHostAccess() ContainerRequestOption {
	return func(req *testcontainers.ContainerRequest) {
		if isPodman() {
			req.HostConfigModifier = func(hc *container.HostConfig) {
				hc.ExtraHosts = append(hc.ExtraHosts, "host.containers.internal:host-gateway")
			}
		}
	}
}

// CreateContainer creates a container with the appropriate provider and options.
// It automatically registers the container for cleanup.
func CreateContainer(ctx context.Context, req testcontainers.ContainerRequest, reuse bool, opts ...ContainerRequestOption) (testcontainers.Container, error) {
	// Apply options
	for _, opt := range opts {
		opt(&req)
	}

	// Create with appropriate provider
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ProviderType:     GetProviderType(),
		ContainerRequest: req,
		Started:          true,
		Reuse:            reuse,
	})
	if err != nil {
		return nil, err
	}

	// Register for cleanup (important when Ryuk is disabled)
	RegisterContainer(c)

	return c, nil
}
