package auxiliary

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"

	"github.com/flightctl/flightctl/test/util"
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

// GitServer holds connection info, SSH key path, and the container for the aux git server.
type GitServer struct {
	URL            string
	Host           string
	Port           int
	InternalHost   string
	InternalPort   int
	privateKeyPath string
	container      testcontainers.Container
}

// Start starts the git server container and sets URL, Host, Port, etc.
func (g *GitServer) Start(ctx context.Context, network string, reuse bool) error {
	logrus.Infof("Starting git server container (reuse=%v)", reuse)
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
		SkipReaper:   reuse,
	}
	container, err := CreateContainer(ctx, req, reuse, WithNetwork(network), WithHostAccess())
	if err != nil {
		return fmt.Errorf("failed to start git server container: %w", err)
	}
	g.container = container
	keyPath, err := copyGitServerKeysFromContainer(ctx, container)
	if err != nil {
		return fmt.Errorf("failed to copy git server SSH key from container: %w", err)
	}
	g.privateKeyPath = keyPath
	g.Host = GetHostIP()
	g.InternalHost = g.Host
	port, err := container.MappedPort(ctx, "2222")
	if err != nil {
		return fmt.Errorf("failed to get git server port: %w", err)
	}
	g.Port = port.Int()
	g.InternalPort = g.Port
	g.URL = fmt.Sprintf("ssh://user@%s", net.JoinHostPort(g.Host, strconv.Itoa(g.Port)))
	logrus.Infof("Git server container started: %s", g.URL)
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

// GetGitSSHPrivateKeyPath returns the path to the SSH private key for git operations.
// The key is always the one from the aux git server container (Podman); no deployment secrets.
func (s *Services) GetGitSSHPrivateKeyPath() (util.SSHPrivateKeyPath, error) {
	if s.GitServer == nil {
		return "", fmt.Errorf("git server SSH key not available (git server may not be started)")
	}
	return s.GitServer.GetGitSSHPrivateKeyPath()
}

// GetGitSSHPrivateKey returns the SSH private key content for git operations.
func (s *Services) GetGitSSHPrivateKey() (util.SSHPrivateKeyContent, error) {
	if s.GitServer == nil {
		return "", fmt.Errorf("git server may not be started")
	}
	return s.GitServer.GetGitSSHPrivateKey()
}

// GetGitSSHPrivateKeyPath returns the path to the SSH private key for the git server.
func (g *GitServer) GetGitSSHPrivateKeyPath() (util.SSHPrivateKeyPath, error) {
	if g.privateKeyPath == "" {
		return "", fmt.Errorf("git server SSH key not available")
	}
	return util.SSHPrivateKeyPath(g.privateKeyPath), nil
}

// GetGitSSHPrivateKey returns the SSH private key content for the git server.
func (g *GitServer) GetGitSSHPrivateKey() (util.SSHPrivateKeyContent, error) {
	path, err := g.GetGitSSHPrivateKeyPath()
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(string(path))
	if err != nil {
		return "", fmt.Errorf("failed to read SSH private key from %s: %w", path, err)
	}
	return util.SSHPrivateKeyContent(data), nil
}
