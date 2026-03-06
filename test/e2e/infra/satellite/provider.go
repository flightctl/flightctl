package satellite

import (
	"context"
	"sync"

	"github.com/docker/docker/api/types/container"
	"github.com/testcontainers/testcontainers-go"
)

var (
	globalContainers []testcontainers.Container
	containersMu     sync.Mutex
)

// RegisterContainer adds a container to the cleanup registry.
func RegisterContainer(c testcontainers.Container) {
	containersMu.Lock()
	defer containersMu.Unlock()
	globalContainers = append(globalContainers, c)
}

// ShutdownAll terminates all registered containers.
func ShutdownAll(ctx context.Context) {
	containersMu.Lock()
	defer containersMu.Unlock()
	for _, c := range globalContainers {
		if c != nil {
			_ = c.Terminate(ctx)
		}
	}
	globalContainers = nil
}

// GetProviderType returns the testcontainers provider type (Podman).
func GetProviderType() testcontainers.ProviderType {
	return testcontainers.ProviderPodman
}

// ContainerRequestOption modifies a container request.
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

// WithHostAccess adds host.containers.internal for Podman.
func WithHostAccess() ContainerRequestOption {
	return func(req *testcontainers.ContainerRequest) {
		if isPodman() {
			req.HostConfigModifier = func(hc *container.HostConfig) {
				hc.ExtraHosts = append(hc.ExtraHosts, "host.containers.internal:host-gateway")
			}
		}
	}
}

// CreateContainer creates and starts a container, registering it for cleanup.
func CreateContainer(ctx context.Context, req testcontainers.ContainerRequest, reuse bool, opts ...ContainerRequestOption) (testcontainers.Container, error) {
	for _, opt := range opts {
		opt(&req)
	}
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ProviderType:     GetProviderType(),
		ContainerRequest: req,
		Started:          true,
		Reuse:            reuse,
	})
	if err != nil {
		return nil, err
	}
	RegisterContainer(c)
	return c, nil
}
