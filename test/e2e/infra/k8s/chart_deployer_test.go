package k8s

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// podmanDockerHost and dockerDockerHost select the runtime CLI that
// containers.RuntimeCLIName derives from DOCKER_HOST.
const (
	podmanDockerHost = "unix:///run/user/1000/podman/podman.sock"
	dockerDockerHost = "unix:///var/run/docker.sock"
)

// exitError builds an *exec.ExitError carrying the given status, which is what
// runLookupCommand reports for a command that ran and failed.
func exitError(t *testing.T, code int) error {
	t.Helper()
	// Running a real process is the only portable way to obtain an
	// *exec.ExitError with a chosen status; ProcessState cannot be forged.
	err := exec.Command("sh", "-c", fmt.Sprintf("exit %d", code)).Run() //nolint:gosec // G204: literal status from the test table
	var exitErr *exec.ExitError
	require.ErrorAs(t, err, &exitErr, "sh must report a non-zero exit as *exec.ExitError")
	require.Equal(t, code, exitErr.ExitCode())
	return err
}

func TestLocalImageExists(t *testing.T) {
	const image = "localhost/flightctl-catalog-collector-el9:latest"

	tests := []struct {
		name string
		// dockerHost picks the runtime CLI under test.
		dockerHost string
		// result is what the stubbed command reports.
		result func(t *testing.T) commandResult
		// wantArgs is the subcommand the deployer must use for this runtime.
		wantArgs []string

		wantExists bool
		wantErr    bool
		// wantErrContains is checked against the returned error message.
		wantErrContains string
	}{
		{
			name:       "When podman reports the image is present it should return true",
			dockerHost: podmanDockerHost,
			result:     func(*testing.T) commandResult { return commandResult{exited: true} },
			wantArgs:   []string{"image", "exists", image},
			wantExists: true,
		},
		{
			name:       "When podman exits 1 it should report the image as absent without an error",
			dockerHost: podmanDockerHost,
			result: func(t *testing.T) commandResult {
				return commandResult{exited: true, exitCode: 1, err: exitError(t, 1)}
			},
			wantArgs:   []string{"image", "exists", image},
			wantExists: false,
		},
		{
			name:       "When podman exits 125 because the runtime failed it should return an error",
			dockerHost: podmanDockerHost,
			result: func(t *testing.T) commandResult {
				return commandResult{
					exited:   true,
					exitCode: 125,
					stderr:   "Error: unable to connect to Podman socket",
					err:      exitError(t, 125),
				}
			},
			wantArgs:        []string{"image", "exists", image},
			wantErr:         true,
			wantErrContains: "unable to connect to Podman socket",
		},
		{
			name:       "When the runtime executable is missing it should return an error",
			dockerHost: podmanDockerHost,
			result: func(*testing.T) commandResult {
				return commandResult{err: exec.ErrNotFound}
			},
			wantArgs:        []string{"image", "exists", image},
			wantErr:         true,
			wantErrContains: "executable file not found",
		},
		{
			name:       "When docker reports the image is present it should return true",
			dockerHost: dockerDockerHost,
			result:     func(*testing.T) commandResult { return commandResult{exited: true} },
			wantArgs:   []string{"image", "inspect", image},
			wantExists: true,
		},
		{
			name:       "When docker exits 1 with a no such image message it should report the image as absent",
			dockerHost: dockerDockerHost,
			result: func(t *testing.T) commandResult {
				return commandResult{
					exited:   true,
					exitCode: 1,
					stderr:   "Error: No such image: " + image,
					err:      exitError(t, 1),
				}
			},
			wantArgs:   []string{"image", "inspect", image},
			wantExists: false,
		},
		{
			name:       "When docker exits 1 because the daemon is unreachable it should return an error",
			dockerHost: dockerDockerHost,
			result: func(t *testing.T) commandResult {
				return commandResult{
					exited:   true,
					exitCode: 1,
					stderr:   "Cannot connect to the Docker daemon at unix:///var/run/docker.sock",
					err:      exitError(t, 1),
				}
			},
			wantArgs:        []string{"image", "inspect", image},
			wantErr:         true,
			wantErrContains: "Cannot connect to the Docker daemon",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("DOCKER_HOST", tt.dockerHost)

			var gotArgs []string
			deployer := &ChartDeployer{
				runCommand: func(_ context.Context, _ string, args ...string) commandResult {
					gotArgs = args
					return tt.result(t)
				},
			}

			exists, err := deployer.LocalImageExists(context.Background(), image)

			require.Equal(t, tt.wantArgs, gotArgs, "the runtime subcommand must match the runtime in use")
			if tt.wantErr {
				require.Error(t, err)
				require.Contains(t, err.Error(), tt.wantErrContains)
				require.False(t, exists, "a failed lookup must never claim the image is present")
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.wantExists, exists)
		})
	}
}

func TestLocalImageExistsCancellation(t *testing.T) {
	const image = "localhost/flightctl-catalog-collector-el9:latest"
	t.Setenv("DOCKER_HOST", podmanDockerHost)

	tests := []struct {
		name string
		// newContext returns an already-finished context, as the deployer sees
		// it after exec kills the child process.
		newContext func(t *testing.T) context.Context
		wantErr    error
	}{
		{
			name: "When the context is cancelled it should return the cancellation, not image absent",
			newContext: func(*testing.T) context.Context {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx
			},
			wantErr: context.Canceled,
		},
		{
			name: "When the context deadline passed it should return the deadline, not image absent",
			newContext: func(t *testing.T) context.Context {
				ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Hour))
				t.Cleanup(cancel)
				return ctx
			},
			wantErr: context.DeadlineExceeded,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			deployer := &ChartDeployer{
				runCommand: func(_ context.Context, _ string, _ ...string) commandResult {
					// exec kills the child on context end, which surfaces as a
					// signalled process rather than a clean exit status.
					return commandResult{err: errors.New("signal: killed")}
				},
			}

			exists, err := deployer.LocalImageExists(tt.newContext(t), image)

			require.Error(t, err)
			require.ErrorIs(t, err, tt.wantErr)
			require.False(t, exists)
		})
	}
}

func TestLocalImageExistsRejectsEmptyImage(t *testing.T) {
	deployer := &ChartDeployer{
		runCommand: func(_ context.Context, _ string, _ ...string) commandResult {
			t.Fatal("the runtime must not be invoked for an empty image")
			return commandResult{}
		},
	}

	exists, err := deployer.LocalImageExists(context.Background(), "   ")

	require.Error(t, err)
	require.False(t, exists)
}
