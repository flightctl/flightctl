package client

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/flightctl/flightctl/internal/agent/device/errors"
)

const defaultPodmanTimeout = time.Minute

type Compose struct {
	*Podman
}

// UpFromWorkDir runs `docker-compose up -d` or `podman-compose up -d` from the
// given workDir. The third argument is a flag to prevent recreation of existing
// containers.
func (p *Compose) UpFromWorkDir(ctx context.Context, workDir string, noRecreate bool) error {
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	args := []string{
		"compose",
		"up",
		"-d",
	}

	if noRecreate {
		args = append(args, "--no-recreate")
	}

	_, stderr, exitCode := p.exec.ExecuteWithContextFromDir(ctx, workDir, podmanCmd, args)
	if exitCode != 0 {
		return fmt.Errorf("podman compose up: %w", errors.FromStderr(stderr, exitCode))
	}
	return nil
}

func (p *Compose) Up(ctx context.Context, path string) error {
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	args := []string{
		"-f",
		path,
		"up",
		"-d",
	}
	_, stderr, exitCode := p.exec.ExecuteWithContext(ctx, podmanCmd, args...)
	if exitCode != 0 {
		return fmt.Errorf("podman compose up: %w", errors.FromStderr(stderr, exitCode))
	}
	return nil
}

func (p *Compose) Down(ctx context.Context, path string) error {
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	args := []string{
		"-f",
		path,
		"down",
	}
	_, stderr, exitCode := p.exec.ExecuteWithContext(ctx, podmanCmd, args...)
	if exitCode == 0 {
		return nil
	}
	_, err := os.Stat(path)
	if !os.IsNotExist(err) {
		return fmt.Errorf("podman-compose down: %w", errors.FromStderr(stderr, exitCode))
	}
	psStdout, psStderr, psExitCode := p.exec.ExecuteWithContext(ctx, "podman", "ps", "-a", "--format=json")
	if psExitCode != 0 {
		p.log.Errorf("podman ps --all failed: %s", psStderr)
		return fmt.Errorf("podman compose down: %w", errors.FromStderr(stderr, exitCode))
	}
	type psRecord struct {
		Labels map[string]string `json:"Labels"`
	}
	var psRecords []psRecord
	if err = json.Unmarshal([]byte(psStdout), &psRecords); err != nil {
		p.log.WithError(err).Errorf("json unmarshal failed:")
		return fmt.Errorf("podman-compose down failed for path %s with exit code %d: %s", path, exitCode, stderr)
	}

	for _, p := range psRecords {
		if p.Labels != nil && p.Labels["com.docker.compose.project.config_files"] == path {
			return fmt.Errorf("podman-compose down failed for path %s but container created by compose file exists: %s", path, stderr)
		}
	}
	return nil
}
