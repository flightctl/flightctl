package client

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
	"k8s.io/apimachinery/pkg/util/wait"
)

const (
	podmanCmd = "podman"
)

type Podman struct {
	exec    executer.Executer
	log     *log.PrefixLogger
	timeout time.Duration
	backoff wait.Backoff
}

type ImageConfig struct {
	Labels map[string]string `json:"Labels"`
}

func NewPodman(log *log.PrefixLogger, exec executer.Executer, backoff wait.Backoff) *Podman {
	return &Podman{
		log:     log,
		exec:    exec,
		timeout: defaultPodmanTimeout,
		backoff: backoff,
	}
}

// Pull pulls the image from the registry and the response. Users can pass in options to configure the client.
func (p *Podman) Pull(ctx context.Context, image string, opts ...ClientOption) (resp string, err error) {
	options := clientOptions{}
	for _, opt := range opts {
		opt(&options)
	}

	if options.retry {
		err := wait.ExponentialBackoffWithContext(ctx, p.backoff, func() (bool, error) {
			resp, err = p.pullImage(ctx, image)
			if err != nil {
				p.log.Warnf("Failed to pull os image %q: %v", image, err)
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
	resp, err = p.pullImage(ctx, image)
	if err != nil {
		return "", err
	}
	return resp, nil
}

func (p *Podman) pullImage(ctx context.Context, image string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	args := []string{"pull", image}
	stdout, stderr, exitCode := p.exec.ExecuteWithContext(ctx, podmanCmd, args...)
	if exitCode != 0 {
		return "", fmt.Errorf("pull image: %w", errors.FromStderr(stderr, exitCode))
	}
	out := strings.TrimSpace(stdout)
	return out, nil
}

// Inspect returns the JSON output of the image inspection.
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

	type inspect struct {
		Config ImageConfig `json:"Config"`
	}

	var inspectData []inspect
	if err := json.Unmarshal([]byte(resp), &inspectData); err != nil {
		return nil, fmt.Errorf("failed to parse image config: %w", err)
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

func (p *Podman) Compose() *Compose {
	return &Compose{
		Podman: p,
	}
}

func IsPodmanRootless() bool {
	return os.Geteuid() != 0
}
