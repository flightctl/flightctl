//go:build integration

package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/docker/docker/api/types/container"
	"github.com/flightctl/flightctl/internal/crypto"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

func TestWorkerEnrollmentContainerMount(t *testing.T) {
	pki, runtime := t.TempDir(), t.TempDir()
	require.NoError(t, os.Chmod(runtime, 0700))
	issuer := filepath.Join(pki, "flightctl-api")
	require.NoError(t, os.Mkdir(issuer, 0700))
	configFile := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(configFile, []byte("{}"), 0600))
	for _, available := range []bool{false, true, false} {
		if available {
			_, err := crypto.MakeSelfSignedCA(filepath.Join(issuer, "client-signer.crt"), filepath.Join(issuer, "client-signer.key"), "", "container-fixture", 1)
			require.NoError(t, err)
			cert, err := os.ReadFile(filepath.Join(issuer, "client-signer.crt"))
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(filepath.Join(pki, "ca-bundle.crt"), cert, 0600))
		} else {
			require.NoError(t, os.RemoveAll(filepath.Join(issuer, "client-signer.key")))
		}
		cmd := NewStandaloneCommand()
		cmd.SetArgs([]string{"prepare-worker-enrollment", "--pki-dir", pki, "--runtime-dir", runtime, "--config", configFile})
		require.NoError(t, cmd.Execute())
		check := "test ! -e /signing/client-signer.key"
		if available {
			check = "test -r /signing/client-signer.key && test -r /signing/client-signer.crt && test -r /signing/ca-bundle.crt"
		}
		check += " && test ! -e /signing/server.key && ! touch /signing/write-probe"
		ctx := context.Background()
		fixture, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
			ContainerRequest: testcontainers.ContainerRequest{
				Image:      "docker.io/library/postgres:16-alpine",
				Entrypoint: []string{"/bin/sh"},
				Cmd:        []string{"-c", check},
				HostConfigModifier: func(host *container.HostConfig) {
					host.NetworkMode = "none"
					host.Binds = []string{filepath.Join(runtime, "signing") + ":/signing:ro,z"}
				},
				WaitingFor: wait.ForExit(),
			}, Started: true,
		})
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, fixture.Terminate(context.Background())) })
		state, err := fixture.State(ctx)
		require.NoError(t, err)
		require.Equal(t, 0, state.ExitCode)
	}
}
