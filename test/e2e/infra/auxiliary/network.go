package auxiliary

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

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

// alreadyConnectedMessage reports whether a failed "network connect" means the
// container is already attached, which is the normal outcome when a reused
// container is started again.
//
// Podman reports `container <id> is already connected to network <name>:
// network is already connected` and exits 125; Docker reports `endpoint with
// name <name> already exists in network <network>`. Every other wording is a
// genuine failure to attach.
func alreadyConnectedMessage(stderr string) bool {
	lowered := strings.ToLower(stderr)
	for _, marker := range []string{"already connected", "already exists in network"} {
		if strings.Contains(lowered, marker) {
			return true
		}
	}
	return false
}

// connectToNetwork attaches container to network, treating an existing
// attachment as success. Any other failure is returned: callers publish an
// internal address that only resolves once the container is on the network, so
// a silent miss here surfaces much later as an unexplained DNS error.
func connectToNetwork(ctx context.Context, network, container string) error {
	cli := containerRuntimeCLIName()
	//nolint:gosec // G204: the runtime CLI name and container name are package constants; network comes from the aux network config
	cmd := exec.CommandContext(ctx, cli, "network", "connect", network, container)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if alreadyConnectedMessage(stderr.String()) {
			return nil
		}
		return fmt.Errorf("connect container %s to network %s: %w: %s",
			container, network, err, strings.TrimSpace(stderr.String()))
	}
	return nil
}
