package containers

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"

	"github.com/sirupsen/logrus"
)

// E2EAuxHostEnv is the env var to override the host used for registry/git/prometheus
// (e.g. when the test VM has multiple NICs and the cluster is on a different interface).
const E2EAuxHostEnv = "E2E_AUX_HOST"

// GetDockerNetwork returns the network name for testcontainers (kind, host, podman, bridge).
func GetDockerNetwork() string {
	ConfigureDockerHost()
	if os.Getenv("FLIGHTCTL_QUADLETS") != "" {
		return "host"
	}
	if isKindCluster() {
		return "kind"
	}
	if IsPodman() {
		return "podman"
	}
	return "bridge"
}

func GetDeviceNetwork() string {
	network := GetDockerNetwork()
	if network == "host" {
		if IsPodman() {
			return "podman"
		}
		return "bridge"
	}
	return network
}

// IsPodman reports whether the selected container runtime is Podman (same rule as RuntimeCLIName / DOCKER_HOST).
func IsPodman() bool {
	return RuntimeCLIName() == "podman"
}

var (
	kindClusterOnce    sync.Once
	kindClusterPresent bool
)

func isKindCluster() bool {
	kindClusterOnce.Do(func() {
		cmd := exec.Command("kind", "get", "clusters")
		out, err := cmd.Output()
		kindClusterPresent = err == nil && strings.TrimSpace(string(out)) != ""
	})
	return kindClusterPresent
}

// GetHostIP returns the host's external IP for container access.
func GetHostIP() string {
	if override := os.Getenv(E2EAuxHostEnv); override != "" {
		return override
	}
	conn, err := net.Dial("udp", "1.1.1.1:80")
	if err != nil {
		return "localhost"
	}
	defer conn.Close()
	return conn.LocalAddr().(*net.UDPAddr).IP.String()
}

// GetContainerHostname returns the hostname for host access from inside containers.
func GetContainerHostname() string {
	ConfigureDockerHost()
	if isKindCluster() {
		return GetHostIP()
	}
	if IsPodman() {
		return "host.containers.internal"
	}
	return GetHostIP()
}

// ContainerExistsByName returns true if a container with the given name exists (running or stopped).
func ContainerExistsByName(name string) bool {
	exists, err := ContainerExistsByNameContext(context.Background(), name)
	return err == nil && exists
}

// ContainerExistsByNameContext checks for a container by name without allowing a stalled runtime
// CLI to outlive the caller's deadline.
func ContainerExistsByNameContext(ctx context.Context, name string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	cli := RuntimeCLIName()
	filter := NamePSFilter(cli, name)
	cmd := RuntimeCommandContext(ctx, "ps", "-a", "--filter", filter, "--format", "{{.Names}}")
	out, err := cmd.CombinedOutput()
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return false, ctxErr
		}
		logrus.Debugf("containerExistsByName %s: %v %s", name, err, string(out))
		return false, fmt.Errorf("failed to check whether container %s exists: %w", name, err)
	}
	for _, existingName := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if strings.TrimPrefix(existingName, "/") == name {
			return true, nil
		}
	}
	return false, nil
}

// ContainerRunningByName returns true if a container with the given name exists and is running.
// Uses the same CLI selection as ContainerExistsByName (see auxiliary.Registry.Reused / E2E aux).
func ContainerRunningByName(name string) bool {
	if !ContainerExistsByName(name) {
		return false
	}
	cmd := RuntimeCommandContext(context.Background(), "inspect", "-f", "{{.State.Running}}", name)
	out, err := cmd.Output()
	if err != nil {
		logrus.Debugf("containerRunningByName %s: %v", name, err)
		return false
	}
	return strings.TrimSpace(string(out)) == "true"
}

// RemoveContainerByName force-removes a container by name (best effort).
func RemoveContainerByName(name string) error {
	cmd := RuntimeCommandContext(context.Background(), "rm", "-f", "-v", name)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// RemoveContainerByNameContext force-removes a container by name, bounded by the caller's context.
func RemoveContainerByNameContext(ctx context.Context, name string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	cmd := RuntimeCommandContext(ctx, "rm", "-f", "-v", name)
	out, err := cmd.CombinedOutput()
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return fmt.Errorf("failed to remove container %s: %w: %s", name, err, strings.TrimSpace(string(out)))
	}
	return nil
}
