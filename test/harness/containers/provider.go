package containers

import (
	"context"

	"github.com/docker/docker/api/types/container"
	"github.com/testcontainers/testcontainers-go"
)

// GetProviderType returns the testcontainers provider type based on the detected runtime.
func GetProviderType() testcontainers.ProviderType {
	if IsPodman() {
		return testcontainers.ProviderPodman
	}
	return testcontainers.ProviderDocker
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
		if !IsPodman() {
			return
		}
		old := req.HostConfigModifier
		req.HostConfigModifier = func(hc *container.HostConfig) {
			if old != nil {
				old(hc)
			}
			hc.ExtraHosts = append(hc.ExtraHosts, "host.containers.internal:host-gateway")
		}
	}
}

// GenericStart starts a container with the Flight Control testcontainers provider defaults.
func GenericStart(ctx context.Context, req testcontainers.ContainerRequest, reuse bool, opts ...ContainerRequestOption) (testcontainers.Container, error) {
	ConfigureDockerHost()
	for _, opt := range opts {
		opt(&req)
	}
	return testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ProviderType:     GetProviderType(),
		ContainerRequest: req,
		Started:          true,
		Reuse:            reuse,
	})
}
