package satellite

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/sirupsen/logrus"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

const (
	gitServerContainerName  = "e2e-gitserver"
	gitServerPort           = "2222/tcp"
	gitServerImageRepo      = "localhost/git-server"
	gitServerImageTag       = "latest"
	gitServerKeyPathPrivate = "/home/user/.ssh/id_rsa"
	gitServerKeyPathPublic  = "/home/user/.ssh/id_rsa.pub"
)

func (s *Services) startGitServer(ctx context.Context) error {
	logrus.Infof("Starting git server container (reuse=%v)", s.reuse)
	projectRoot, err := getProjectRoot()
	if err != nil {
		return fmt.Errorf("failed to get project root: %w", err)
	}
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
		WaitingFor:   wait.ForListeningPort("2222"),
		SkipReaper:   s.reuse, // avoid Ryuk marking for removal when this process exits so next suite can reuse
	}
	// Key pair is generated inside the container (entrypoint); we copy the private key to the host
	// so the harness and Repository CR always use the key that matches the server. Reuse is safe.
	container, err := CreateContainer(ctx, req, s.reuse, WithNetwork(s.network), WithHostAccess())
	if err != nil {
		return fmt.Errorf("failed to start git server container: %w", err)
	}
	s.gitServer = container
	keyPath, err := copyGitServerKeysFromContainer(ctx, container)
	if err != nil {
		return fmt.Errorf("failed to copy git server SSH key from container: %w", err)
	}
	s.gitServerPrivateKeyPath = keyPath
	s.GitServerHost = GetHostIP()
	port, err := container.MappedPort(ctx, "2222")
	if err != nil {
		return fmt.Errorf("failed to get git server port: %w", err)
	}
	s.GitServerPort = port.Int()
	s.GitServerURL = fmt.Sprintf("ssh://user@%s:%d", s.GitServerHost, s.GitServerPort)
	s.GitServerInternalHost = s.GitServerHost
	s.GitServerInternalPort = s.GitServerPort
	logrus.Infof("Git server container started: %s", s.GitServerURL)
	return nil
}

// copyGitServerKeysFromContainer copies the container's SSH keys to a temp dir once; returns the path to id_rsa.
func copyGitServerKeysFromContainer(ctx context.Context, container testcontainers.Container) (string, error) {
	sshDir, err := os.MkdirTemp("", "e2e-gitserver-ssh-")
	if err != nil {
		return "", err
	}
	for _, pair := range []struct {
		containerPath string
		hostPath      string
		mode          os.FileMode
	}{
		{gitServerKeyPathPrivate, filepath.Join(sshDir, "id_rsa"), 0600},
		{gitServerKeyPathPublic, filepath.Join(sshDir, "id_rsa.pub"), 0644},
	} {
		rc, err := container.CopyFileFromContainer(ctx, pair.containerPath)
		if err != nil {
			return "", fmt.Errorf("copy %s from container: %w", pair.containerPath, err)
		}
		data, err := io.ReadAll(rc)
		_ = rc.Close()
		if err != nil {
			return "", fmt.Errorf("read %s from container: %w", pair.containerPath, err)
		}
		if err := os.WriteFile(pair.hostPath, data, pair.mode); err != nil {
			return "", fmt.Errorf("write %s: %w", pair.hostPath, err)
		}
	}
	return filepath.Join(sshDir, "id_rsa"), nil
}

// GetSSHPrivateKeyPath returns the path to the SSH private key for git operations.
// The key is read from the git server container once at start and kept in a temp file.
func GetSSHPrivateKeyPath() (string, error) {
	s := Get(context.Background())
	if s.gitServerPrivateKeyPath == "" {
		return "", fmt.Errorf("git server SSH key not available (git server may not be started)")
	}
	return s.gitServerPrivateKeyPath, nil
}
