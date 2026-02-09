package infra

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/sirupsen/logrus"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

const (
	gitServerContainerName = "e2e-gitserver"
	gitServerPort          = "2222/tcp"
	gitServerImageRepo     = "localhost/git-server"
	gitServerImageTag      = "latest"
)

// startGitServer starts a git server container for E2E tests.
// Builds the image from Containerfile.gitserver.
func (s *SatelliteServices) startGitServer(ctx context.Context) error {
	logrus.Infof("Starting git server container (reuse=%v)", s.reuse)

	// Get the project root directory
	projectRoot, err := getProjectRoot()
	if err != nil {
		return fmt.Errorf("failed to get project root: %w", err)
	}

	// Get SSH public key path
	sshPubKeyPath := filepath.Join(projectRoot, "bin", ".ssh", "id_rsa.pub")
	if _, err := os.Stat(sshPubKeyPath); os.IsNotExist(err) {
		return fmt.Errorf("SSH public key not found at %s - run 'make bin/e2e-certs/ca.pem' first", sshPubKeyPath)
	}

	// Build context is test/scripts which contains the Containerfile and required files
	buildContext := filepath.Join(projectRoot, "test", "scripts")

	req := testcontainers.ContainerRequest{
		FromDockerfile: testcontainers.FromDockerfile{
			Context:    buildContext,
			Dockerfile: "Containerfile.gitserver",
			Repo:       gitServerImageRepo,
			Tag:        gitServerImageTag,
			KeepImage:  true,
		},
		Name:         gitServerContainerName,
		ExposedPorts: []string{gitServerPort},
		Files: []testcontainers.ContainerFile{
			{
				HostFilePath:      sshPubKeyPath,
				ContainerFilePath: "/etc/ssh/authorized_keys/user",
				FileMode:          0644,
			},
		},
		WaitingFor: wait.ForListeningPort("2222"),
	}

	// Create container with appropriate provider and network
	container, err := CreateContainer(ctx, req, s.reuse,
		WithNetwork(s.network),
		WithHostAccess(),
	)
	if err != nil {
		return fmt.Errorf("failed to start git server container: %w", err)
	}

	s.gitServer = container

	// Get host and port
	host, err := container.Host(ctx)
	if err != nil {
		return fmt.Errorf("failed to get git server host: %w", err)
	}
	s.GitServerHost = host

	port, err := container.MappedPort(ctx, "2222")
	if err != nil {
		return fmt.Errorf("failed to get git server port: %w", err)
	}
	s.GitServerPort = port.Int()
	s.GitServerURL = fmt.Sprintf("ssh://user@%s:%d", s.GitServerHost, s.GitServerPort)

	// Set internal endpoints for container-to-container communication
	// Containers on the same network (e.g., kind) can access by container name
	s.GitServerInternalHost = gitServerContainerName
	s.GitServerInternalPort = 2222 // Internal container port, not the mapped port

	logrus.Infof("Git server container started: %s (internal: %s:%d)",
		s.GitServerURL, s.GitServerInternalHost, s.GitServerInternalPort)
	return nil
}

// getProjectRoot returns the project root directory.
func getProjectRoot() (string, error) {
	// Try to find the project root by looking for go.mod
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}

	// Walk up the directory tree looking for go.mod
	dir := cwd
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			// Reached root without finding go.mod
			break
		}
		dir = parent
	}

	// Fallback: try common relative paths from test directories
	for _, relPath := range []string{"../../..", "../..", ".."} {
		absPath, err := filepath.Abs(filepath.Join(cwd, relPath))
		if err != nil {
			continue
		}
		if _, err := os.Stat(filepath.Join(absPath, "go.mod")); err == nil {
			return absPath, nil
		}
	}

	return "", fmt.Errorf("could not find project root from %s", cwd)
}

// GetSSHPrivateKeyPath returns the path to the SSH private key for git operations.
func GetSSHPrivateKeyPath() (string, error) {
	projectRoot, err := getProjectRoot()
	if err != nil {
		return "", err
	}

	keyPath := filepath.Join(projectRoot, "bin", ".ssh", "id_rsa")
	if _, err := os.Stat(keyPath); os.IsNotExist(err) {
		return "", fmt.Errorf("SSH private key not found at %s", keyPath)
	}

	return keyPath, nil
}
