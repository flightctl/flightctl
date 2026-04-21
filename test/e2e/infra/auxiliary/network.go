package auxiliary

import (
	"github.com/flightctl/flightctl/test/harness/containers"
)

// ConfigureDockerHost sets up the container runtime environment for testcontainers.
func ConfigureDockerHost() {
	containers.ConfigureDockerHost()
}

func containerRuntimeCLIName() string {
	return containers.RuntimeCLIName()
}

// GetDockerNetwork returns the network name for testcontainers (kind, host, podman, bridge).
func GetDockerNetwork() string {
	return containers.GetDockerNetwork()
}

// E2EAuxHostEnv is the env var to override the host used for registry/git/prometheus
// (e.g. when the test VM has multiple NICs and the cluster is on a different interface).
const E2EAuxHostEnv = containers.E2EAuxHostEnv

// GetHostIP returns the host's external IP for container access.
func GetHostIP() string {
	return containers.GetHostIP()
}

// GetContainerHostname returns the hostname for host access from inside containers.
func GetContainerHostname() string {
	return containers.GetContainerHostname()
}
