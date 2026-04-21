package auxiliary

import (
	"context"
	"sync"

	"github.com/flightctl/flightctl/test/harness/containers"
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

// GetProviderType returns the testcontainers provider type based on the detected runtime.
func GetProviderType() testcontainers.ProviderType {
	return containers.GetProviderType()
}

// ContainerRequestOption is an alias for the shared containers package option type.
type ContainerRequestOption = containers.ContainerRequestOption

// WithNetwork sets the container network.
var WithNetwork = containers.WithNetwork

// WithHostAccess adds host.containers.internal for Podman.
var WithHostAccess = containers.WithHostAccess

// CreateContainer creates and starts a container, registering it for cleanup.
func CreateContainer(ctx context.Context, req testcontainers.ContainerRequest, reuse bool, opts ...ContainerRequestOption) (testcontainers.Container, error) {
	c, err := containers.GenericStart(ctx, req, reuse, opts...)
	if err != nil {
		return nil, err
	}
	RegisterContainer(c)
	return c, nil
}
