package client

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
)

const (
	podmanCmd = "podman"
)

type Podman struct {
	exec    executer.Executer
	log     *log.PrefixLogger
	timeout time.Duration
}

type ImageConfig struct {
	Labels map[string]string `json:"Labels"`
}

func NewPodman(log *log.PrefixLogger, exec executer.Executer) *Podman {
	return &Podman{
		log:     log,
		exec:    exec,
		timeout: defaultPodmanTimeout,
	}
}

// Pull pulls the image from the registry.
func (p *Podman) Pull(ctx context.Context, image string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	args := []string{"pull", image}
	stdout, stderr, exitCode := p.exec.ExecuteWithContext(ctx, podmanCmd, args...)
	if exitCode != 0 {
		return "", fmt.Errorf("failed to pull image:%s  %d: %s", image, exitCode, stderr)
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
		return "", fmt.Errorf("failed to inspect image: %s  %d: %s", image, exitCode, stderr)
	}
	out := strings.TrimSpace(stdout)
	return out, nil
}

func (p *Podman) EventsCmd(ctx context.Context, events []string) *exec.Cmd {
	args := []string{"events", "--format", "json"}
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
		return "", fmt.Errorf("failed to mount image: %s  %d: %s", image, exitCode, stderr)
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
		return fmt.Errorf("failed to unmount image: %s  %d: %s", image, exitCode, stderr)
	}
	return nil
}

func (p *Podman) Copy(ctx context.Context, src, dst string) error {
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	args := []string{"cp", src, dst}
	_, stderr, exitCode := p.exec.ExecuteWithContext(ctx, podmanCmd, args...)
	if exitCode != 0 {
		return fmt.Errorf("failed to copy %s to %s: %d: %s", src, dst, exitCode, stderr)
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
		return fmt.Errorf("failed to stop containers %d: %s", exitCode, stderr)
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
		return fmt.Errorf("failed to remove containers %d: %s", exitCode, stderr)
	}
	return nil
}

func (p *Podman) RemoveVolumes(ctx context.Context, labels []string) error {
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	args := []string{"volume", "rm"}
	_, stderr, exitCode := p.exec.ExecuteWithContext(ctx, podmanCmd, args...)
	if exitCode != 0 {
		return fmt.Errorf("failed to remove volumes %d: %s", exitCode, stderr)
	}
	return nil
}

func (p *Podman) RemoveNetworks(ctx context.Context, labels []string) error {
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	args := []string{"network", "rm"}
	for _, label := range labels {
		args = append(args, "--filter", fmt.Sprintf("label=%s", label))
	}
	_, stderr, exitCode := p.exec.ExecuteWithContext(ctx, podmanCmd, args...)
	if exitCode != 0 {
		return fmt.Errorf("failed to remove networks %d: %s", exitCode, stderr)
	}
	return nil
}

func (p *Podman) Unshare(ctx context.Context, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	args = append([]string{"unshare"}, args...)
	stdout, stderr, exitCode := p.exec.ExecuteWithContext(ctx, podmanCmd, args...)
	if exitCode != 0 {
		return "", fmt.Errorf("failed to execute podman unshare %d: %s", exitCode, stderr)
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
