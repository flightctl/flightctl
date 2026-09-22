package auxiliary

import (
	"archive/tar"
	"bytes"
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
	contextArchive, err := gitServerContextArchive(buildContext)
	if err != nil {
		return fmt.Errorf("failed to prepare git server build context: %w", err)
	}
	req := testcontainers.ContainerRequest{
		FromDockerfile: testcontainers.FromDockerfile{
			ContextArchive: contextArchive,
			Dockerfile:     "Containerfile.gitserver",
			Repo:           gitServerImageRepo,
			Tag:            gitServerImageTag,
			KeepImage:      true,
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

// gitServerContextArchive creates a minimal build context with normalized root
// ownership. The Podman Docker-compatible build API otherwise preserves the
// host UID in the context tar, which can be unmapped in a rootless user namespace.
func gitServerContextArchive(contextDir string) (io.ReadSeeker, error) {
	var archiveBuffer bytes.Buffer
	tarWriter := tar.NewWriter(&archiveBuffer)

	if err := addGitServerArchiveEntry(tarWriter, contextDir, "Containerfile.gitserver"); err != nil {
		return nil, err
	}
	if err := addGitServerArchiveEntry(tarWriter, contextDir, "git-server-entrypoint.sh"); err != nil {
		return nil, err
	}

	commandDir := filepath.Join(contextDir, "git-server-cmds")
	entries, err := os.ReadDir(commandDir)
	if err != nil {
		return nil, fmt.Errorf("read git server commands: %w", err)
	}
	if err := addGitServerArchiveDirectory(tarWriter, "git-server-cmds"); err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			return nil, fmt.Errorf("unexpected directory in git server commands: %s", entry.Name())
		}
		name := filepath.Join("git-server-cmds", entry.Name())
		if err := addGitServerArchiveEntry(tarWriter, contextDir, name); err != nil {
			return nil, err
		}
	}

	if err := tarWriter.Close(); err != nil {
		return nil, fmt.Errorf("close git server build context: %w", err)
	}
	return bytes.NewReader(archiveBuffer.Bytes()), nil
}

func addGitServerArchiveDirectory(writer *tar.Writer, name string) error {
	return writer.WriteHeader(&tar.Header{
		Name:     name + "/",
		Mode:     0755,
		Typeflag: tar.TypeDir,
		Uname:    "root",
		Gname:    "root",
	})
}

func addGitServerArchiveEntry(writer *tar.Writer, contextDir, name string) error {
	path := filepath.Join(contextDir, filepath.FromSlash(name))
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("stat git server build file %q: %w", name, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("git server build file %q is not regular", name)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read git server build file %q: %w", name, err)
	}
	header := &tar.Header{
		Name:  name,
		Mode:  int64(info.Mode().Perm()),
		Size:  int64(len(data)),
		Uname: "root",
		Gname: "root",
	}
	if err := writer.WriteHeader(header); err != nil {
		return fmt.Errorf("write git server build file %q header: %w", name, err)
	}
	if _, err := writer.Write(data); err != nil {
		return fmt.Errorf("write git server build file %q: %w", name, err)
	}
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
