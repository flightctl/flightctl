package util

import (
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// ContainerInfo holds information about a podman container
type ContainerInfo struct {
	ID     string
	Name   string
	Image  string
	Status string
}

// ListPodmanContainers lists all running podman containers
func ListPodmanContainers() ([]ContainerInfo, error) {
	cmd := exec.Command("sudo", "podman", "ps", "--format", "{{.ID}}|{{.Names}}|{{.Image}}|{{.Status}}")
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to list podman containers: %w", err)
	}

	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	containers := []ContainerInfo{}
	for _, line := range lines {
		if line != "" {
			parts := strings.Split(line, "|")
			if len(parts) >= 4 {
				containers = append(containers, ContainerInfo{
					ID:     parts[0],
					Name:   parts[1],
					Image:  parts[2],
					Status: parts[3],
				})
			}
		}
	}

	return containers, nil
}

// WaitForContainerReady waits for a container to be in the "Up" state
func WaitForContainerReady(containerName string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		containers, err := ListPodmanContainers()
		if err != nil {
			return fmt.Errorf("failed to list containers: %w", err)
		}

		for _, container := range containers {
			if container.Name == containerName && strings.Contains(container.Status, "Up") {
				return nil
			}
		}

		time.Sleep(2 * time.Second)
	}

	return fmt.Errorf("timeout waiting for container %s to be ready", containerName)
}

// GetContainerLogs retrieves logs from a specific container
func GetContainerLogs(containerName string, lines int) (string, error) {
	cmd := exec.Command("sudo", "podman", "logs", "--tail", fmt.Sprintf("%d", lines), containerName)
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to get container logs: %w", err)
	}

	return string(output), nil
}

// CheckContainerHealth checks if a container is healthy
func CheckContainerHealth(containerName string) (bool, error) {
	cmd := exec.Command("sudo", "podman", "inspect", "--format", "{{.State.Health.Status}}", containerName)
	output, err := cmd.Output()
	if err != nil {
		// If health check is not configured, check if container is running
		cmd = exec.Command("sudo", "podman", "inspect", "--format", "{{.State.Running}}", containerName)
		output, err = cmd.Output()
		if err != nil {
			return false, fmt.Errorf("failed to inspect container: %w", err)
		}
		return strings.TrimSpace(string(output)) == "true", nil
	}

	status := strings.TrimSpace(string(output))
	return status == "healthy" || status == "", nil
}

// GetServicePort retrieves the exposed port for a specific service
func GetServicePort(containerName string, internalPort string) (string, error) {
	cmd := exec.Command("sudo", "podman", "port", containerName, internalPort)
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to get service port: %w", err)
	}

	// Output format: 0.0.0.0:PORT
	portInfo := strings.TrimSpace(string(output))
	parts := strings.Split(portInfo, ":")
	if len(parts) < 2 {
		return "", fmt.Errorf("unexpected port format: %s", portInfo)
	}

	return parts[len(parts)-1], nil
}

// RestartContainer restarts a specific container
func RestartContainer(containerName string) error {
	cmd := exec.Command("sudo", "podman", "restart", containerName)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to restart container %s: %w", containerName, err)
	}

	return nil
}

// GetPodmanSecrets lists all podman secrets
func GetPodmanSecrets() ([]string, error) {
	cmd := exec.Command("sudo", "podman", "secret", "ls", "--format", "{{.Name}}")
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to list podman secrets: %w", err)
	}

	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	secrets := []string{}
	for _, line := range lines {
		if line != "" {
			secrets = append(secrets, line)
		}
	}

	return secrets, nil
}

// ExecInContainer executes a command inside a container
func ExecInContainer(containerName string, command []string) (string, error) {
	args := append([]string{"podman", "exec", containerName}, command...)
	allArgs := append([]string{"sudo"}, args...)
	cmd := exec.Command(allArgs[0], allArgs[1:]...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("failed to exec in container %s: %w", containerName, err)
	}

	return string(output), nil
}

// GetDatabaseStatus checks if the database container is ready to accept connections
func GetDatabaseStatus() (bool, error) {
	// Try to connect to PostgreSQL using podman exec
	cmd := exec.Command("sudo", "podman", "exec", "flightctl-db",
		"pg_isready", "-U", "admin", "-d", "flightctl")
	err := cmd.Run()
	if err != nil {
		return false, nil
	}

	return true, nil
}

// GetRedisStatus checks if Redis is responding
func GetRedisStatus() (bool, error) {
	cmd := exec.Command("sudo", "podman", "exec", "flightctl-kv",
		"redis-cli", "ping")
	output, err := cmd.Output()
	if err != nil {
		return false, nil
	}

	return strings.TrimSpace(string(output)) == "PONG", nil
}

// PodmanNetworkExists checks if a podman network exists
func PodmanNetworkExists(networkName string) (bool, error) {
	cmd := exec.Command("sudo", "podman", "network", "exists", networkName)
	err := cmd.Run()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return false, nil
		}
		return false, fmt.Errorf("failed to check podman network: %w", err)
	}

	return true, nil
}

// ListPodmanVolumes lists all podman volumes
func ListPodmanVolumes() ([]string, error) {
	cmd := exec.Command("sudo", "podman", "volume", "ls", "--format", "{{.Name}}")
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to list podman volumes: %w", err)
	}

	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	volumes := []string{}
	for _, line := range lines {
		if line != "" {
			volumes = append(volumes, line)
		}
	}

	return volumes, nil
}

// InspectPodmanNetwork inspects a podman network and returns its configuration
func InspectPodmanNetwork(networkName string) (string, error) {
	cmd := exec.Command("sudo", "podman", "network", "inspect", networkName)
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to inspect podman network: %w", err)
	}

	return string(output), nil
}
