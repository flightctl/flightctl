package infra

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

// init configures the environment for testcontainers at package load time.
// This runs before any testcontainers operations.
func init() {
	ConfigureDockerHost()
}

// ConfigureDockerHost sets up the container runtime environment.
// It detects Docker or Podman sockets and configures testcontainers appropriately.
func ConfigureDockerHost() {
	logrus.Info("[infra] ConfigureDockerHost called")

	// If DOCKER_HOST is already set, use it
	if existing := os.Getenv("DOCKER_HOST"); existing != "" {
		logrus.Infof("[infra] Using existing DOCKER_HOST: %s", existing)
		configureProviderSettings(existing)
		return
	}

	logrus.Info("[infra] DOCKER_HOST not set, auto-detecting socket...")

	// Try to detect the socket path
	socketPath := detectContainerSocket()
	if socketPath == "" {
		logrus.Warn("[infra] Could not detect container socket - testcontainers may fail")
		return
	}

	dockerHost := fmt.Sprintf("unix://%s", socketPath)
	if err := os.Setenv("DOCKER_HOST", dockerHost); err != nil {
		logrus.Errorf("[infra] Failed to set DOCKER_HOST: %v", err)
		return
	}
	logrus.Infof("[infra] Auto-configured DOCKER_HOST: %s", dockerHost)

	configureProviderSettings(dockerHost)
}

// configureProviderSettings sets additional environment variables based on the provider.
func configureProviderSettings(dockerHost string) {
	if strings.Contains(dockerHost, "podman") {
		// Disable Ryuk (cleanup sidecar) - we use our own cleanup registry
		os.Setenv("TESTCONTAINERS_RYUK_DISABLED", "true")

		// Set Docker API version for Podman compatibility
		if os.Getenv("DOCKER_API_VERSION") == "" {
			os.Setenv("DOCKER_API_VERSION", "1.43")
		}

		logrus.Info("[infra] Detected Podman - configured for compatibility (Ryuk disabled)")
	}
}

// detectContainerSocket finds the Docker or Podman socket path.
// It checks multiple locations to support various environments:
// - Rootless Podman (user socket)
// - Rootful Podman
// - Docker
// - Podman Desktop (macOS/Windows)
func detectContainerSocket() string {
	var socketPaths []string

	// Get user info for constructing paths
	uid := os.Getuid()
	currentUser, _ := user.Current()
	var homeDir string
	if currentUser != nil {
		homeDir = currentUser.HomeDir
	}

	logrus.Infof("[infra] Detecting socket: UID=%d, XDG_RUNTIME_DIR=%s, HOME=%s",
		uid, os.Getenv("XDG_RUNTIME_DIR"), homeDir)

	// 1. XDG_RUNTIME_DIR - highest priority for rootless Podman
	if xdgRuntime := os.Getenv("XDG_RUNTIME_DIR"); xdgRuntime != "" {
		socketPaths = append(socketPaths, filepath.Join(xdgRuntime, "podman", "podman.sock"))
	}

	// 2. User-specific Podman socket (standard location)
	if uid != 0 {
		socketPaths = append(socketPaths, fmt.Sprintf("/run/user/%d/podman/podman.sock", uid))
	}

	// 3. Podman Desktop locations (macOS/Linux)
	if homeDir != "" {
		socketPaths = append(socketPaths,
			filepath.Join(homeDir, ".local", "share", "containers", "podman", "machine", "podman.sock"),
			filepath.Join(homeDir, ".local", "share", "containers", "podman", "machine", "qemu", "podman.sock"),
		)
	}

	// 4. Docker socket (standard location)
	socketPaths = append(socketPaths, "/var/run/docker.sock")

	// 5. Rootful Podman socket
	socketPaths = append(socketPaths, "/run/podman/podman.sock")

	logrus.Infof("[infra] Checking %d socket paths...", len(socketPaths))

	// Find first existing socket
	for _, path := range socketPaths {
		if isSocket(path) {
			logrus.Infof("[infra] Found container socket: %s", path)
			return path
		}
		logrus.Infof("[infra] Socket not found or not valid: %s", path)
	}

	// Log debug info for troubleshooting
	logrus.Warn("[infra] No Docker/Podman socket found!")
	return ""
}

// isSocket checks if the given path is an existing Unix socket.
func isSocket(path string) bool {
	fi, err := os.Stat(path)
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeSocket != 0
}

// GetDockerNetwork determines which network to use based on the environment.
// Priority:
// 1. KIND cluster network (if KIND is running)
// 2. Host network (for quadlets environments)
// 3. Podman default network (for rootless Podman)
// 4. Docker bridge network (default)
func GetDockerNetwork() string {
	// KIND takes highest priority - containers must be on same network as cluster
	if isKindCluster() {
		logrus.Debug("[infra] Using KIND network")
		return "kind"
	}

	// For quadlets, use host network to communicate with root podman containers
	if os.Getenv("FLIGHTCTL_QUADLETS") != "" {
		logrus.Debug("[infra] Using host network for quadlets")
		return "host"
	}

	// For Podman, use the default "podman" network (not "bridge")
	if isPodman() {
		logrus.Debug("[infra] Using Podman default network")
		return "podman"
	}

	// Docker default
	logrus.Debug("[infra] Using Docker bridge network")
	return "bridge"
}

// isPodman returns true - we always use Podman for E2E tests.
func isPodman() bool {
	return true
}

// isKindCluster checks if a KIND cluster is running.
func isKindCluster() bool {
	cmd := exec.Command("kind", "get", "clusters")
	output, err := cmd.Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(output)) != ""
}

// GetHostIP returns the external IP address of the host.
// This is used for services that need to be accessible from containers.
func GetHostIP() string {
	conn, err := net.Dial("udp", "1.1.1.1:80")
	if err != nil {
		logrus.Warnf("[infra] Failed to determine host IP: %v, using localhost", err)
		return "localhost"
	}
	defer conn.Close()

	localAddr := conn.LocalAddr().(*net.UDPAddr)
	return localAddr.IP.String()
}

// GetContainerHostname returns the hostname to use for accessing the host from within containers.
func GetContainerHostname() string {
	// In KIND, containers can reach the host via the host's external IP
	if isKindCluster() {
		return GetHostIP()
	}

	// For Podman, use host.containers.internal (requires WithHostAccess option)
	if isPodman() {
		return "host.containers.internal"
	}

	// Default to host IP
	return GetHostIP()
}
