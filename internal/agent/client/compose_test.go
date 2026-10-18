package client

import (
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/api/common"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/poll"
	"github.com/stretchr/testify/require"
)

func TestParseComposeSpecFromDir(t *testing.T) {
	require := require.New(t)
	tests := []struct {
		name          string
		files         map[string][]byte
		expectedError error
		expectedSpec  common.ComposeSpec
	}{
		{
			name: "single compose.yaml file",
			files: map[string][]byte{
				"docker-compose.yaml": []byte(`version: "3"
services:
  web:
    image: nginx
`),
			},
			expectedSpec: common.ComposeSpec{
				Services: map[string]common.ComposeService{
					"web": {Image: "nginx"},
				},
				Volumes: map[string]common.ComposeVolume{},
			},
		},
		{
			name: "single compose.yml file with yml override",
			files: map[string][]byte{
				"docker-compose.yml": []byte(`version: "3"
services:
  web:
    image: nginx
`),
				"docker-compose.override.yml": []byte(`version: "3"
services:
  web:
    image: nginx:latest
`),
			},
			expectedSpec: common.ComposeSpec{
				Services: map[string]common.ComposeService{
					"web": {Image: "nginx:latest"},
				},
				Volumes: map[string]common.ComposeVolume{},
			},
		},
		{
			name: "multiple compose files priority .yaml",
			files: map[string][]byte{
				"docker-compose.yaml": []byte(`version: "3"
services:
  web:
    image: nginx
`),
				"docker-compose.yml": []byte(`version: "3"
services:
  web:
    image: apache
`),
			},
			expectedSpec: common.ComposeSpec{
				Services: map[string]common.ComposeService{
					"web": {Image: "nginx"},
				},
				Volumes: map[string]common.ComposeVolume{},
			},
		},
		{
			name: "no compose files",
			files: map[string][]byte{
				"random-file.txt": []byte("not a compose file"),
			},
			expectedError: errors.ErrNoComposeFile,
			expectedSpec:  common.ComposeSpec{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			readerWriter := fileio.NewReadWriter(
				fileio.NewReader(fileio.WithReaderRootDir(tmpDir)),
				fileio.NewWriter(fileio.WithWriterRootDir(tmpDir)),
			)
			for filename, content := range tt.files {
				if err := readerWriter.WriteFile(filename, content, fileio.DefaultFilePermissions); err != nil {
					require.NoError(err)
				}
			}
			spec, err := ParseComposeSpecFromDir(readerWriter, "/")
			if tt.expectedError != nil {
				require.ErrorIs(err, tt.expectedError)
				return
			}
			require.NoError(err)
			require.Equal(tt.expectedSpec, *spec)
		})
	}
}

func TestWhenComposeStartsItShouldCaptureCreateEventsInTheOwningUserEnvironment(t *testing.T) {
	current, err := user.Current()
	require.NoError(t, err)
	for _, owner := range []v1beta1.Username{v1beta1.CurrentProcessUsername, v1beta1.Username(current.Username)} {
		t.Run("When runAs is "+owner.String()+" it should configure the creator", func(t *testing.T) {
			require := require.New(t)
			if !owner.IsCurrentProcessUser() && os.Geteuid() != 0 {
				t.Skip("Executing with explicit user credentials requires root")
			}
			directory := t.TempDir()
			// A process boundary probe stands in for Podman; no container runtime is required.
			script := `#!/bin/sh
printf '%s\n' "$HOME" "$XDG_RUNTIME_DIR" "$CONTAINERS_CONF_OVERRIDE" > ../invocation
/bin/cat "$CONTAINERS_CONF_OVERRIDE" > ../captured-config
`
			require.NoError(os.WriteFile(filepath.Join(directory, "podman"), []byte(script), 0o700)) //nolint:gosec // This test fixture must be executable.
			workDir := filepath.Join(directory, "manifests")
			require.NoError(os.MkdirAll(workDir, 0o755))
			require.NoError(os.WriteFile(filepath.Join(workDir, "docker-compose.yaml"), []byte("services: {}"), 0o600))
			// Embedded Compose definitions can be on the read-only OS filesystem.
			require.NoError(os.Chmod(workDir, 0o555))
			t.Cleanup(func() { _ = os.Chmod(workDir, 0o755) })
			t.Setenv("PATH", directory+":"+os.Getenv("PATH"))
			t.Setenv("HOME", "/agent-home")
			executor, err := ExecuterForUser(owner)
			require.NoError(err)
			rw := fileio.NewReadWriter(fileio.NewReader(), fileio.NewWriter())
			podman := NewPodman(log.NewPrefixLogger("test"), executor, rw, poll.Config{})
			require.NoError(podman.Compose().UpFromWorkDir(t.Context(), workDir, "app", false))
			contents, err := os.ReadFile(filepath.Join(directory, "captured-config"))
			require.NoError(err)
			require.Equal("[engine]\nevents_container_create_inspect_data = true\n", string(contents))
			invocation, err := os.ReadFile(filepath.Join(directory, "invocation"))
			require.NoError(err)
			environment := strings.Split(strings.TrimSuffix(string(invocation), "\n"), "\n")
			require.Len(environment, 3)
			if owner.IsCurrentProcessUser() {
				require.Equal("/agent-home", environment[0])
			} else {
				require.Equal(current.HomeDir, environment[0])
				uid, err := strconv.Atoi(current.Uid)
				require.NoError(err)
				require.Equal("/run/user/"+strconv.Itoa(uid), environment[1])
			}
			require.NotEqual(workDir, filepath.Dir(environment[2]))
			_, err = os.Stat(environment[2])
			require.ErrorIs(err, os.ErrNotExist, "temporary creator configuration should be removed after compose up")
		})
	}
}
