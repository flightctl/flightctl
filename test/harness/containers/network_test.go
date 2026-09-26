package containers

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRuntimeCLIContextHelpersHonorDeadline(t *testing.T) {
	binDir := t.TempDir()
	fakePodman := filepath.Join(binDir, "podman")
	if err := os.WriteFile(fakePodman, []byte("#!/bin/sh\nexec sleep 10\n"), 0o755); err != nil { // #nosec G306 -- the temporary fake runtime must be executable by the test process.
		t.Fatalf("write fake podman: %v", err)
	}
	t.Setenv("DOCKER_HOST", "unix:///run/user/1000/podman/podman.sock")
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	tests := []struct {
		name string
		run  func(context.Context) error
	}{
		{
			name: "When checking for a container it should stop at the context deadline",
			run: func(ctx context.Context) error {
				_, err := ContainerExistsByNameContext(ctx, "slow-device")
				return err
			},
		},
		{
			name: "When removing a container it should stop at the context deadline",
			run: func(ctx context.Context) error {
				return RemoveContainerByNameContext(ctx, "slow-device")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()

			started := time.Now()
			err := tt.run(ctx)
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("runtime CLI helper error = %v, want context.DeadlineExceeded", err)
			}
			if elapsed := time.Since(started); elapsed > time.Second {
				t.Errorf("runtime CLI helper took %v after a 100ms deadline", elapsed)
			}
		})
	}

	if got := RuntimeCLIName(); !strings.Contains(got, "podman") {
		t.Fatalf("RuntimeCLIName() = %q, want podman", got)
	}

	fakeDocker := filepath.Join(binDir, "docker")
	if err := os.WriteFile(fakeDocker, []byte("#!/bin/sh\nprintf 'slow-device-suffix\\n'\n"), 0o755); err != nil { // #nosec G306 -- the temporary fake runtime must be executable by the test process.
		t.Fatalf("write fake docker: %v", err)
	}
	t.Setenv("DOCKER_HOST", "unix:///var/run/docker.sock")
	if got := RuntimeCLIName(); got != "docker" {
		t.Fatalf("RuntimeCLIName() = %q, want docker", got)
	}
	exists, err := ContainerExistsByNameContext(context.Background(), "slow-device")
	if err != nil {
		t.Fatalf("ContainerExistsByNameContext() error = %v", err)
	}
	if exists {
		t.Fatal("ContainerExistsByNameContext() matched a longer container name by substring")
	}
}
