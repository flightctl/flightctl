package containers

import (
	"context"
	"os"
	"os/exec"
)

func RuntimeCommandContext(ctx context.Context, args ...string) *exec.Cmd {
	cli := RuntimeCLIName()
	endpoint := os.Getenv("DOCKER_HOST")
	if endpoint != "" {
		if cli == "podman" {
			args = append([]string{"--remote", "--url", endpoint}, args...)
		} else {
			args = append([]string{"--host", endpoint}, args...)
		}
	}
	if cli == "podman" {
		return exec.CommandContext(ctx, "podman", args...)
	}
	return exec.CommandContext(ctx, "docker", args...)
}
