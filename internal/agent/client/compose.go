package client

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
)

const defaultPodmanTimeout = time.Minute

type ComposeType string

const (
	DockerCompose ComposeType = "docker-compose"
	PodmanCompose ComposeType = "podman-compose"
)

type Compose struct {
	exec        executer.Executer
	log         *log.PrefixLogger
	composeType ComposeType
	timeout     time.Duration
}

func NewComposeClient(log *log.PrefixLogger, exec executer.Executer) (*Compose, error) {
	c := &Compose{
		exec:    exec,
		log:     log,
		timeout: defaultPodmanTimeout,
	}

	if IsCommandAvailable("podman-compose") {
		c.composeType = PodmanCompose
	} else if IsCommandAvailable("docker-compose") {
		c.composeType = DockerCompose
	} else {
		return nil, fmt.Errorf("podman-compose or docker-compose is required")
	}

	return c, nil
}

// UpFromWorkDir runs `docker-compose up -d` or `podman-compose up -d` from the specified workDir.
func (p *Compose) UpFromWorkDir(ctx context.Context, workDir string) error {
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()
	args := []string{"up", "-d"}

	_, stderr, exitCode := p.exec.ExecuteWithContextFromDir(ctx, workDir, string(p.composeType), args)
	if exitCode != 0 {
		return fmt.Errorf("%s up from workDir %s failed with exitCode %d: %s", p.composeType, workDir, exitCode, stderr)
	}
	return nil
}

func (p *Compose) Up(ctx context.Context, path string) error {
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	_, stderr, exitCode := p.exec.ExecuteWithContext(ctx, string(p.composeType), "-f", path, "up", "-d")
	if exitCode != 0 {
		return fmt.Errorf("%s up for path %s failed with exitCode %d: %s", p.composeType, path, exitCode, stderr)
	}
	return nil
}

func (p *Compose) Down(ctx context.Context, path string) error {
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	_, stderr, exitCode := p.exec.ExecuteWithContext(ctx, string(p.composeType), "-f", path, "down")
	if exitCode == 0 {
		return nil
	}
	_, err := os.Stat(path)
	if !os.IsNotExist(err) {
		return fmt.Errorf("podman-compose down failed for existing path %s with exit code %d: %s", path, exitCode, stderr)
	}
	psStdout, psStderr, psExitCode := p.exec.ExecuteWithContext(ctx, "podman", "ps", "-a", "--format=json")
	if psExitCode != 0 {
		p.log.Errorf("podman ps --all failed: %s", psStderr)
		return fmt.Errorf("podman-compose down failed for path %s with exit code %d: %s", path, exitCode, stderr)
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
