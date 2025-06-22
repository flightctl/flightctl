package client

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
	"k8s.io/apimachinery/pkg/util/wait"
)

const (
	podmanCmd              = "podman"
	defaultPullLogInterval = 30 * time.Second
)

// PodmanInspect represents the overall structure of podman inspect output
type PodmanInspect struct {
	Restarts int                   `json:"RestartCount"`
	State    PodmanContainerState  `json:"State"`
	Config   PodmanContainerConfig `json:"Config"`
}

// ContainerState represents the container state part of the podman inspect output
type PodmanContainerState struct {
	OciVersion  string `json:"OciVersion"`
	Status      string `json:"Status"`
	Running     bool   `json:"Running"`
	Paused      bool   `json:"Paused"`
	Restarting  bool   `json:"Restarting"`
	OOMKilled   bool   `json:"OOMKilled"`
	Dead        bool   `json:"Dead"`
	Pid         int    `json:"Pid"`
	ExitCode    int    `json:"ExitCode"`
	Error       string `json:"Error"`
	StartedAt   string `json:"StartedAt"`
	FinishedAt  string `json:"FinishedAt"`
	Healthcheck string `json:"Healthcheck"`
}

type PodmanContainerConfig struct {
	Labels map[string]string `json:"Labels"`
}

type PodmanEvent struct {
	ContainerExitCode int               `json:"ContainerExitCode,omitempty"`
	ID                string            `json:"ID"`
	Image             string            `json:"Image"`
	Name              string            `json:"Name"`
	Status            string            `json:"Status"`
	Type              string            `json:"Type"`
	Attributes        map[string]string `json:"Attributes"`
}

type Podman struct {
	exec       executer.Executer
	log        *log.PrefixLogger
	timeout    time.Duration
	readWriter fileio.ReadWriter
	backoff    wait.Backoff
}

func NewPodman(log *log.PrefixLogger, exec executer.Executer, readWriter fileio.ReadWriter, backoff wait.Backoff) *Podman {
	return &Podman{
		log:        log,
		exec:       exec,
		timeout:    defaultPodmanTimeout,
		readWriter: readWriter,
		backoff:    backoff,
	}
}

// Pull pulls the image from the registry and the response. Users can pass in options to configure the client.
func (p *Podman) Pull(ctx context.Context, image string, opts ...ClientOption) (resp string, err error) {
	options := clientOptions{}
	for _, opt := range opts {
		opt(&options)
	}

	doneCh := make(chan struct{})
	defer close(doneCh)

	startTime := time.Now()
	go func() {
		ticker := time.NewTicker(defaultPullLogInterval)
		defer ticker.Stop()

		for {
			select {
			case <-doneCh:
				return
			case <-ticker.C:
				elapsed := time.Since(startTime)
				p.log.Infof("Pulling image, please wait... (elapsed: %v)", elapsed)
			}
		}
	}()

	if options.retry {
		err := wait.ExponentialBackoffWithContext(ctx, p.backoff, func(ctx context.Context) (bool, error) {
			resp, err = p.pullImage(ctx, image, options.pullSecretPath)
			if err != nil {
				// fail fast if the error is not retryable
				if !errors.IsRetryable(err) {
					p.log.Error(err)
					return false, err
				}
				p.log.Warnf("A retriable error occurred while pulling image %s: %v. Retrying…", image, err)
				return false, nil
			}
			return true, nil
		})

		if err != nil {
			return "", err
		}
		return resp, nil
	}

	// no retry
	resp, err = p.pullImage(ctx, image, options.pullSecretPath)
	if err != nil {
		return "", err
	}
	return resp, nil
}

func (p *Podman) pullImage(ctx context.Context, image string, pullSecretPath string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	args := []string{"pull", image}
	if pullSecretPath != "" {
		exists, err := p.readWriter.PathExists(pullSecretPath)
		if err != nil {
			return "", fmt.Errorf("check pull secret path: %w", err)
		}
		if !exists {
			p.log.Errorf("Pull secret path %s does not exist", pullSecretPath)
		} else {
			args = append(args, "--authfile", pullSecretPath)
		}
	}
	stdout, stderr, exitCode := p.exec.ExecuteWithContext(ctx, podmanCmd, args...)
	if exitCode != 0 {
		return "", fmt.Errorf("pull image: %w", errors.FromStderr(stderr, exitCode))
	}
	out := strings.TrimSpace(stdout)
	return out, nil
}

// Inspect returns the JSON output of the image inspection. The expectation is
// that the image exists in local container storage.
func (p *Podman) Inspect(ctx context.Context, image string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	args := []string{"inspect", image}
	stdout, stderr, exitCode := p.exec.ExecuteWithContext(ctx, podmanCmd, args...)
	if exitCode != 0 {
		return "", fmt.Errorf("inspect image: %s: %w", image, errors.FromStderr(stderr, exitCode))
	}
	out := strings.TrimSpace(stdout)
	return out, nil
}

func (p *Podman) ImageExists(ctx context.Context, image string) bool {
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	args := []string{"image", "exists", image}
	_, _, exitCode := p.exec.ExecuteWithContext(ctx, podmanCmd, args...)
	return exitCode == 0
}

// EventsSinceCmd returns a command to get podman events since the given time. After creating the command, it should be started with exec.Start().
// When the events are in sync with the current time a sync event is emitted.
func (p *Podman) EventsSinceCmd(ctx context.Context, events []string, sinceTime string) *exec.Cmd {
	args := []string{"events", "--format", "json", "--since", sinceTime}
	for _, event := range events {
		args = append(args, "--filter", fmt.Sprintf("event=%s", event))
	}

	return p.exec.CommandContext(ctx, podmanCmd, args...)
}

func (p *Podman) Mount(ctx context.Context, image string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	args := []string{
		"image",
		"mount",
		image,
	}
	stdout, stderr, exitCode := p.exec.ExecuteWithContext(ctx, podmanCmd, args...)
	if exitCode != 0 {
		return "", fmt.Errorf("mount image: %s: %w", image, errors.FromStderr(stderr, exitCode))
	}

	out := strings.TrimSpace(stdout)
	return out, nil
}

func (p *Podman) Unmount(ctx context.Context, image string) error {
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	args := []string{
		"image",
		"unmount",
		image,
	}
	_, stderr, exitCode := p.exec.ExecuteWithContext(ctx, podmanCmd, args...)
	if exitCode != 0 {
		return fmt.Errorf("unmount image: %s: %w", image, errors.FromStderr(stderr, exitCode))
	}
	return nil
}

func (p *Podman) Copy(ctx context.Context, src, dst string) error {
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	args := []string{"cp", src, dst}
	_, stderr, exitCode := p.exec.ExecuteWithContext(ctx, podmanCmd, args...)
	if exitCode != 0 {
		return fmt.Errorf("copy %s to %s: %w", src, dst, errors.FromStderr(stderr, exitCode))
	}
	return nil
}

func (p *Podman) InspectLabels(ctx context.Context, image string) (map[string]string, error) {
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	resp, err := p.Inspect(ctx, image)
	if err != nil {
		return nil, err
	}

	var inspectData []PodmanInspect
	if err := json.Unmarshal([]byte(resp), &inspectData); err != nil {
		return nil, fmt.Errorf("parse image inspect response: %w", err)
	}

	if len(inspectData) == 0 {
		return nil, fmt.Errorf("no image config found")
	}

	return inspectData[0].Config.Labels, nil
}

func (p *Podman) StopContainers(ctx context.Context, labels []string) error {
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	args := []string{"stop"}
	for _, label := range labels {
		args = append(args, "--filter", fmt.Sprintf("label=%s", label))
	}
	_, stderr, exitCode := p.exec.ExecuteWithContext(ctx, podmanCmd, args...)
	if exitCode != 0 {
		return fmt.Errorf("stop containers: %w", errors.FromStderr(stderr, exitCode))
	}
	return nil
}

func (p *Podman) RemoveContainer(ctx context.Context, labels []string) error {
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	args := []string{"rm"}
	for _, label := range labels {
		args = append(args, "--filter", fmt.Sprintf("label=%s", label))
	}
	_, stderr, exitCode := p.exec.ExecuteWithContext(ctx, podmanCmd, args...)
	if exitCode != 0 {
		return fmt.Errorf("remove containers: %w", errors.FromStderr(stderr, exitCode))
	}
	return nil
}

func (p *Podman) RemoveVolumes(ctx context.Context, labels []string) error {
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	args := []string{"volume", "rm"}
	_, stderr, exitCode := p.exec.ExecuteWithContext(ctx, podmanCmd, args...)
	if exitCode != 0 {
		return fmt.Errorf("remove volumes: %w", errors.FromStderr(stderr, exitCode))
	}
	return nil
}

func (p *Podman) ListNetworks(ctx context.Context, labels []string) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	args := []string{
		"network",
		"ls",
		"--format",
		"{{.Network.ID}}",
	}
	for _, label := range labels {
		args = append(args, "--filter", fmt.Sprintf("label=%s", label))
	}

	stdout, stderr, exitCode := p.exec.ExecuteWithContext(ctx, podmanCmd, args...)
	if exitCode != 0 {
		return nil, fmt.Errorf("list containers: %w", errors.FromStderr(stderr, exitCode))
	}

	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	networkSeen := make(map[string]struct{})
	for _, line := range lines {
		// handle multiple networks comma separated
		networks := strings.Split(line, ",")
		for _, network := range networks {
			network = strings.TrimSpace(network)
			if network != "" {
				networkSeen[network] = struct{}{}
			}
		}
	}

	var networks []string
	for network := range networkSeen {
		networks = append(networks, network)
	}
	return networks, nil
}

func (p *Podman) RemoveNetworks(ctx context.Context, networks ...string) error {
	for _, network := range networks {
		nctx, cancel := context.WithTimeout(ctx, p.timeout)
		args := []string{"network", "rm", network}
		_, stderr, exitCode := p.exec.ExecuteWithContext(nctx, podmanCmd, args...)
		cancel()
		if exitCode != 0 {
			return fmt.Errorf("remove networks: %w", errors.FromStderr(stderr, exitCode))
		}
		p.log.Infof("Removed network %s", network)
	}
	return nil
}

func (p *Podman) Unshare(ctx context.Context, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	args = append([]string{"unshare"}, args...)
	stdout, stderr, exitCode := p.exec.ExecuteWithContext(ctx, podmanCmd, args...)
	if exitCode != 0 {
		return "", fmt.Errorf("unshare: %w", errors.FromStderr(stderr, exitCode))
	}
	out := strings.TrimSpace(stdout)
	return out, nil
}

func (p *Podman) CopyContainerData(ctx context.Context, image, destPath string) error {
	return copyContainerData(ctx, p.log, p.readWriter, p, image, destPath)
}

func (p *Podman) Compose() *Compose {
	return &Compose{
		Podman: p,
	}
}

func IsPodmanRootless() bool {
	return os.Geteuid() != 0
}

func copyContainerData(ctx context.Context, log *log.PrefixLogger, writer fileio.Writer, podman *Podman, image, destPath string) (err error) {
	var mountPoint string

	rootless := IsPodmanRootless()
	if rootless {
		log.Warnf("Running in rootless mode this is for testing only")
		mountPoint, err = podman.Unshare(ctx, "podman", "image", "mount", image)
		if err != nil {
			return fmt.Errorf("failed to execute podman share: %w", err)
		}
	} else {
		mountPoint, err = podman.Mount(ctx, image)
		if err != nil {
			return fmt.Errorf("failed to mount image: %w", err)
		}
	}

	if err := writer.MkdirAll(destPath, fileio.DefaultDirectoryPermissions); err != nil {
		return fmt.Errorf("failed to dest create directory: %w", err)
	}

	defer func() {
		if err := podman.Unmount(ctx, image); err != nil {
			log.Errorf("failed to unmount image: %s %v", image, err)
		}
	}()

	// recursively copy image files to agent destination
	err = filepath.Walk(writer.PathFor(mountPoint), func(filePath string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if info.IsDir() {
			if info.Name() == "merged" {
				log.Debugf("Skipping merged directory: %s", filePath)
				return nil
			}
			log.Debugf("Creating directory: %s", info.Name())

			// ensure any directories in the image are also created
			return writer.MkdirAll(filepath.Join(destPath, info.Name()), fileio.DefaultDirectoryPermissions)
		}

		return copyContainerFile(filePath, writer.PathFor(destPath))
	})
	if err != nil {
		return fmt.Errorf("error during copy: %w", err)
	}

	return nil
}

func copyContainerFile(from, to string) error {
	// local writer ensures that the container from directory is correct.
	writer := fileio.NewWriter()
	if err := writer.CopyFile(from, to); err != nil {
		return fmt.Errorf("failed to copy file: %w", err)
	}

	return nil
}

// SanitizePodmanLabel sanitizes a string to be used as a label in Podman.
// Podman labels must be lowercase and can only contain alpha numeric
// characters, hyphens, and underscores. Any other characters are replaced with
// an underscore.
func SanitizePodmanLabel(name string) string {
	var result strings.Builder
	result.Grow(len(name))

	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		// lower case alpha numeric characters, hyphen, and underscore are allowed
		case (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' || c == '_':
			result.WriteByte(c)
		// upper case alpha characters are converted to lower case
		case c >= 'A' && c <= 'Z':
			// add 32 to ascii value convert to lower case
			result.WriteByte(c + 32)
		// any special characters are replaced with an underscore
		default:
			result.WriteByte('_')
		}
	}

	return result.String()
}
