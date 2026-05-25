package auxiliary

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	"github.com/docker/docker/api/types/container"
	"github.com/sirupsen/logrus"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

const (
	fileServerImage         = "docker.io/busybox:latest"
	fileServerContainerName = "e2e-fileserver"
	fileServerPort          = "8088/tcp"
	fileServerPortNum       = "8088"
)

// FileServer holds connection info and the container for the aux HTTP file server.
type FileServer struct {
	URL         string
	InternalURL string
	Host        string
	Port        string
	DataDir     string
	container   testcontainers.Container
}

// Start starts the file server container and sets URL, Host, Port, DataDir.
func (f *FileServer) Start(ctx context.Context, network string, reuse bool) error {
	logrus.Infof("Starting file server container (reuse=%v)", reuse)

	dataDir, err := os.MkdirTemp("", "e2e-fileserver-data-")
	if err != nil {
		return fmt.Errorf("failed to create file server data dir: %w", err)
	}
	f.DataDir = dataDir

	if err := os.WriteFile(filepath.Join(dataDir, "index.html"), []byte("ok"), 0600); err != nil {
		return fmt.Errorf("failed to create index file: %w", err)
	}

	req := testcontainers.ContainerRequest{
		Image:        fileServerImage,
		Name:         fileServerContainerName,
		ExposedPorts: []string{fileServerPort},
		Cmd:          []string{"httpd", "-f", "-p", fileServerPortNum, "-h", "/data"},
		HostConfigModifier: func(hc *container.HostConfig) {
			hc.Binds = append(hc.Binds, dataDir+":/data:rw")
		},
		WaitingFor: wait.ForHTTP("/").WithPort(fileServerPortNum).WithStatusCodeMatcher(
			func(status int) bool { return status == 200 || status == 404 },
		),
		SkipReaper: reuse,
	}

	container, err := CreateContainer(ctx, req, reuse, WithNetwork(network), WithHostAccess())
	if err != nil {
		return fmt.Errorf("failed to start file server container: %w", err)
	}
	f.container = container

	inspect, err := container.Inspect(ctx)
	if err != nil {
		return fmt.Errorf("failed to inspect file server container: %w", err)
	}
	for _, m := range inspect.Mounts {
		if m.Destination == "/data" && m.Source != "" && m.Source != dataDir {
			_ = os.RemoveAll(dataDir)
			f.DataDir = m.Source
			break
		}
	}

	f.Host = GetHostIP()
	port, err := container.MappedPort(ctx, fileServerPortNum)
	if err != nil {
		return fmt.Errorf("failed to get file server port: %w", err)
	}
	f.Port = port.Port()
	f.URL = fmt.Sprintf("http://%s", net.JoinHostPort(f.Host, f.Port))
	f.InternalURL = f.URL
	logrus.Infof("File server container started: %s (internal: %s, data: %s)", f.URL, f.InternalURL, f.DataDir)
	return nil
}

// PushFile writes content to a file relative to the data directory, creating parent dirs as needed.
func (f *FileServer) PushFile(relativePath, content string) error {
	clean := filepath.Clean(relativePath)
	if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return fmt.Errorf("invalid relative path %q", relativePath)
	}
	fullPath := filepath.Join(f.DataDir, clean)
	if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
		return fmt.Errorf("failed to create directory for %s: %w", relativePath, err)
	}
	return os.WriteFile(fullPath, []byte(content), 0600)
}

// GetInternalURL returns the internal (cluster-reachable) URL of the file server.
func (f *FileServer) GetInternalURL() string {
	return f.InternalURL
}
