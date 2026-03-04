package satellite

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"

	"github.com/sirupsen/logrus"
)

func init() {
	ConfigureDockerHost()
}

// ConfigureDockerHost sets up the container runtime environment for testcontainers.
func ConfigureDockerHost() {
	dockerHost := os.Getenv("DOCKER_HOST")
	if dockerHost == "" {
		socketPath := detectContainerSocket()
		if socketPath == "" {
			logrus.Warn("[satellite] Could not detect container socket")
			return
		}
		dockerHost = fmt.Sprintf("unix://%s", socketPath)
		_ = os.Setenv("DOCKER_HOST", dockerHost)
	}
	configureProviderSettings(dockerHost)
	logContainerRuntime(dockerHost)
}

// logContainerRuntime logs the detected runtime so CI/local logs show whether we use Docker or Podman.
func logContainerRuntime(dockerHost string) {
	runtime := "docker"
	if strings.Contains(dockerHost, "podman") {
		runtime = "podman"
	}
	ryuk := "enabled"
	if os.Getenv("TESTCONTAINERS_RYUK_DISABLED") == "true" {
		ryuk = "disabled"
	}
	logrus.Infof("[satellite] Container runtime: %s (DOCKER_HOST=%s), Ryuk %s", runtime, dockerHost, ryuk)
}

func configureProviderSettings(dockerHost string) {
	// Disable Ryuk so reused satellite containers (registry, git, prometheus) are not reaped when
	// one suite process exits; later suites (different process) can reuse them. We already set
	// SkipReaper on those containers, but disabling Ryuk avoids any cross-process reaping.
	if os.Getenv("TESTCONTAINERS_RYUK_DISABLED") == "" {
		os.Setenv("TESTCONTAINERS_RYUK_DISABLED", "true")
	}
	// Podman's API version negotiation can report 1.40; testcontainers and some ops (e.g. platform) need 1.41+.
	// Force 1.43 so the Docker client uses a compatible API with Podman.
	if strings.Contains(dockerHost, "podman") && os.Getenv("DOCKER_API_VERSION") == "" {
		os.Setenv("DOCKER_API_VERSION", "1.43")
	}
}

func detectContainerSocket() string {
	uid := os.Getuid()
	currentUser, _ := user.Current()
	var homeDir string
	if currentUser != nil {
		homeDir = currentUser.HomeDir
	}
	var paths []string
	if xdg := os.Getenv("XDG_RUNTIME_DIR"); xdg != "" {
		paths = append(paths, filepath.Join(xdg, "podman", "podman.sock"))
	}
	if uid != 0 {
		paths = append(paths, fmt.Sprintf("/run/user/%d/podman/podman.sock", uid))
	}
	if homeDir != "" {
		paths = append(paths,
			filepath.Join(homeDir, ".local", "share", "containers", "podman", "machine", "podman.sock"),
		)
	}
	paths = append(paths, "/var/run/docker.sock", "/run/podman/podman.sock")
	for _, p := range paths {
		if isSocket(p) {
			return p
		}
	}
	return ""
}

func isSocket(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.Mode()&os.ModeSocket != 0
}

// GetDockerNetwork returns the network name for testcontainers (kind, host, podman, bridge).
func GetDockerNetwork() string {
	if isKindCluster() {
		return "kind"
	}
	if os.Getenv("FLIGHTCTL_QUADLETS") != "" {
		return "host"
	}
	if isPodman() {
		return "podman"
	}
	return "bridge"
}

func isPodman() bool { return true }

func isKindCluster() bool {
	cmd := exec.Command("kind", "get", "clusters")
	out, err := cmd.Output()
	return err == nil && strings.TrimSpace(string(out)) != ""
}

// E2ESatelliteHostEnv is the env var to override the host used for registry/git/prometheus
// (e.g. when the test VM has multiple NICs and the cluster is on a different interface).
const E2ESatelliteHostEnv = "E2E_SATELLITE_HOST"

// GetHostIP returns the host's external IP for container access.
// If E2E_SATELLITE_HOST is set (e.g. for a two-NIC test VM on shared OCP network), that value is returned.
func GetHostIP() string {
	if override := os.Getenv(E2ESatelliteHostEnv); override != "" {
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
	if isKindCluster() {
		return GetHostIP()
	}
	if isPodman() {
		return "host.containers.internal"
	}
	return GetHostIP()
}
