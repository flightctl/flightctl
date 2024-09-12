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

const podmanCommandDuration = time.Minute

func PodmanComposeUp(ctx context.Context, path string, exec executer.Executer, _ *log.PrefixLogger) error {
	ctx, cancel := context.WithTimeout(ctx, podmanCommandDuration)
	defer cancel()

	_, stderr, exitCode := exec.ExecuteWithContext(ctx, "podman-compose", "-f", path, "up", "-d")
	if exitCode != 0 {
		return fmt.Errorf("podman-compose up hook for path %s failed with exitCode %d: %s", path, exitCode, stderr)
	}
	return nil
}

func PodmanComposeDown(ctx context.Context, path string, exec executer.Executer, log *log.PrefixLogger) error {
	ctx, cancel := context.WithTimeout(ctx, podmanCommandDuration)
	defer cancel()

	_, stderr, exitCode := exec.ExecuteWithContext(ctx, "podman-compose", "-f", path, "down")
	if exitCode == 0 {
		return nil
	}
	_, err := os.Stat(path)
	if !os.IsNotExist(err) {
		return fmt.Errorf("podman-compose down failed for existing path %s with exit code %d: %s", path, exitCode, stderr)
	}
	psStdout, psStderr, psExitCode := exec.ExecuteWithContext(ctx, "podman", "ps", "-a", "--format=json")
	if psExitCode != 0 {
		log.Errorf("podman ps --all failed: %s", psStderr)
		return fmt.Errorf("podman-compose down failed for path %s with exit code %d: %s", path, exitCode, stderr)
	}
	type psRecord struct {
		Labels map[string]string `json:"Labels"`
	}
	var psRecords []psRecord
	if err = json.Unmarshal([]byte(psStdout), &psRecords); err != nil {
		log.WithError(err).Errorf("json unmarshal failed:")
		return fmt.Errorf("podman-compose down failed for path %s with exit code %d: %s", path, exitCode, stderr)
	}

	for _, p := range psRecords {
		if p.Labels != nil && p.Labels["com.docker.compose.project.config_files"] == path {
			return fmt.Errorf("podman-compose down failed for path %s but container created by compose file exists: %s", path, stderr)
		}
	}
	return nil
}
