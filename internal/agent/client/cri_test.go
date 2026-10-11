package client

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/poll"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestNormalizeImageRef(t *testing.T) {
	testCases := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "image with tag",
			input:    "registry.io/namespace/user/image:tag",
			expected: "registry.io/namespace/user/image",
		},
		{
			name:     "image with digest",
			input:    "registry.io/namespace/user/image@sha256:1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef",
			expected: "registry.io/namespace/user/image",
		},
		{
			name:     "image with oci scheme and tag",
			input:    "oci://registry.example.com/charts/mychart:1.0.0",
			expected: "registry.example.com/charts/mychart",
		},
		{
			name:     "image with docker scheme",
			input:    "docker://quay.io/myorg/myapp:v1",
			expected: "quay.io/myorg/myapp",
		},
		{
			name:     "localhost registry with port and tag",
			input:    "localhost:5000/test-image:v1.0",
			expected: "localhost:5000/test-image",
		},
		{
			name:     "image without tag",
			input:    "registry.io/namespace/image",
			expected: "registry.io/namespace/image",
		},
		{
			name:     "official docker hub image with tag",
			input:    "nginx:latest",
			expected: "docker.io/library/nginx",
		},
		{
			name:     "user docker hub image",
			input:    "user/nginx:latest",
			expected: "docker.io/user/nginx",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := normalizeImageRef(tc.input)
			require.Equal(t, tc.expected, result)
		})
	}
}

func TestBuildRegistryPaths(t *testing.T) {
	testCases := []struct {
		name     string
		input    string
		expected []string
	}{
		{
			name:  "full path with four levels",
			input: "registry.io/namespace/user/image",
			expected: []string{
				"registry.io/namespace/user/image",
				"registry.io/namespace/user",
				"registry.io/namespace",
				"registry.io",
			},
		},
		{
			name:  "short path with two levels",
			input: "registry.io/image",
			expected: []string{
				"registry.io/image",
				"registry.io",
			},
		},
		{
			name:  "single level",
			input: "registry.io",
			expected: []string{
				"registry.io",
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := buildRegistryPaths(tc.input)
			require.Equal(t, tc.expected, result)
		})
	}
}

func TestNormalizeAuthFileKey(t *testing.T) {
	testCases := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "registry-1.docker.io normalizes to docker.io",
			input:    "registry-1.docker.io",
			expected: "docker.io",
		},
		{
			name:     "index.docker.io normalizes to docker.io",
			input:    "index.docker.io",
			expected: "docker.io",
		},
		{
			name:     "docker.io stays as docker.io",
			input:    "docker.io",
			expected: "docker.io",
		},
		{
			name:     "https scheme stripped and path removed",
			input:    "https://registry.io/v2/",
			expected: "registry.io",
		},
		{
			name:     "http scheme stripped and path removed",
			input:    "http://registry.io/v2/",
			expected: "registry.io",
		},
		{
			name:     "https scheme stripped, no path",
			input:    "https://registry.io",
			expected: "registry.io",
		},
		{
			name:     "plain registry name unchanged",
			input:    "quay.io",
			expected: "quay.io",
		},
		{
			name:     "registry with namespace not affected by scheme stripping",
			input:    "registry.io/namespace",
			expected: "registry.io/namespace",
		},
		{
			name:     "registry with namespace and scheme gets path stripped",
			input:    "https://registry.io/namespace/image",
			expected: "registry.io",
		},
		{
			name:     "localhost with port",
			input:    "localhost:5000",
			expected: "localhost:5000",
		},
		{
			name:     "localhost with port and https scheme",
			input:    "https://localhost:5000/v2/",
			expected: "localhost:5000",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := normalizeAuthFileKey(tc.input)
			require.Equal(t, tc.expected, result)
		})
	}
}

func TestCRIImageRepoDigestsConfig(t *testing.T) {
	testCases := []struct {
		name      string
		config    string
		exists    bool
		pathError error
	}{
		{name: "When no config is specified it should use runtime defaults"},
		{name: "When the config exists it should use that config", config: "/etc/crictl.yaml", exists: true},
		{name: "When the config is missing it should use runtime defaults", config: "/etc/crictl.yaml"},
		{name: "When checking the config fails it should return the error", config: "/etc/crictl.yaml", pathError: fmt.Errorf("permission denied")},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()
			mockExec := executer.NewMockExecuter(ctrl)
			rw := fileio.NewMockReadWriter(ctrl)
			if tc.config != "" {
				rw.EXPECT().PathExists(tc.config).Return(tc.exists, tc.pathError).Times(2)
			}
			args := []any{}
			if tc.exists {
				args = append(args, "--config", tc.config)
			}
			image := "registry.example.com/app:v1"
			if tc.pathError == nil {
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "crictl", append(args, "images", image)...).Return("IMAGE TAG ID\napp v1 abc", "", 0)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "crictl", append(args, "inspecti", "--output", "json", image)...).Return(`{"status":{"repoDigests":["registry.example.com/app@sha256:abc"]}}`, "", 0)
			}
			cri := NewCRI(log.NewPrefixLogger("test"), mockExec, rw, poll.Config{})
			opts := []ClientOption{WithCRIConfig(tc.config)}
			require.Equal(tc.pathError == nil, cri.ImageExists(context.Background(), image, opts...))
			digests, err := cri.ImageRepoDigests(context.Background(), image, opts...)
			if tc.pathError != nil {
				require.ErrorIs(err, tc.pathError)
				require.Nil(digests)
			} else {
				require.NoError(err)
				require.Equal([]string{"registry.example.com/app@sha256:abc"}, digests)
			}
		})
	}
}

func TestCRIImageManifestDigest(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	ref := "quay.io/acme/app@" + digest
	for _, tt := range []struct {
		name, id, response string
		want               bool
	}{
		{name: "When qualified pull identity has a runtime prefix it should inspect the normalized reference", id: "docker-pullable://" + ref, response: fmt.Sprintf(`{"status":{"repoDigests":[%q]}}`, ref), want: true},
		{name: "When immutable qualified pull identity is confirmed it should return its digest", id: ref, response: fmt.Sprintf(`{"status":{"repoDigests":[%q]}}`, ref), want: true},
		{name: "When pull returns an opaque ID it should remain unknown", id: digest},
		{name: "When local aliases do not confirm the pull identity it should remain unknown", id: ref, response: `{"status":{"repoDigests":[]}}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()
			exec := executer.NewMockExecuter(ctrl)
			if tt.response != "" {
				exec.EXPECT().ExecuteWithContext(gomock.Any(), "crictl", "inspecti", "--output", "json", strings.TrimPrefix(tt.id, "docker-pullable://")).Return(tt.response, "", 0)
			}
			c := NewCRI(log.NewPrefixLogger("test"), exec, fileio.NewMockReadWriter(ctrl), poll.NewConfig(0, 1))
			value, err := c.ImageManifestDigest(context.Background(), tt.id)
			if tt.want {
				require.NoError(t, err)
				require.Equal(t, digest, value)
			} else {
				require.Error(t, err)
				require.Empty(t, value)
			}
		})
	}
}
func TestCRIImageServiceIdentity(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	rw := fileio.NewMockReadWriter(ctrl)
	rw.EXPECT().ReadFile("/etc/crictl.yaml").Return([]byte("runtime-endpoint: unix:///run/runtime.sock\nimage-endpoint: unix:///run/images.sock\n"), nil)
	c := NewCRI(log.NewPrefixLogger("test"), executer.NewMockExecuter(ctrl), rw, poll.NewConfig(0, 1))
	identity, err := c.ImageServiceIdentity()
	require.NoError(t, err)
	require.Equal(t, "unix:///run/images.sock#k8s.io", identity)
	rw.EXPECT().PathExists("/etc/crictl.yaml").Return(true, nil)
	rw.EXPECT().ReadFile("/etc/crictl.yaml").Return([]byte("runtime-endpoint: unix:///run/runtime.sock\nimage-endpoint: unix:///run/images.sock\n"), nil)
	endpoint, err := c.containerdEndpoint(&clientOptions{})
	require.NoError(t, err)
	require.Equal(t, "/run/images.sock", endpoint)
}

func TestCRIImageID(t *testing.T) {
	id := strings.Repeat("a", 64)
	for _, tt := range []struct {
		name, response string
		configured     bool
		code           int
		wantError      bool
	}{
		{name: "When CRI-O reports a storage image ID it should return the exact runtime identity", response: fmt.Sprintf(`{"status":{"id":%q}}`, id)},
		{name: "When the image service is configured it should inspect that service", configured: true, response: fmt.Sprintf(`{"status":{"id":%q}}`, id)},
		{name: "When the imported image is absent it should preserve unknown identity", response: `{"status":null}`, wantError: true},
		{name: "When CRI image status is malformed it should return the parse error", response: `{`, wantError: true},
		{name: "When inspecting the runtime fails it should return the original error", code: 1, wantError: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()
			exec, rw := executer.NewMockExecuter(ctrl), fileio.NewMockReadWriter(ctrl)
			var opts []ClientOption
			var args []any
			if tt.configured {
				configPath := "/etc/flightctl/crio.yaml"
				opts = []ClientOption{WithCRIConfig(configPath)}
				rw.EXPECT().PathExists(configPath).Return(true, nil)
				args = []any{"--config", configPath}
			}
			exec.EXPECT().ExecuteWithContext(gomock.Any(), "crictl", append(args, "inspecti", "--output", "json", id)...).Return(tt.response, "inspect failed", tt.code)
			cri := NewCRI(log.NewPrefixLogger("test"), exec, rw, poll.NewConfig(0, 1))
			actual, err := cri.ImageID(t.Context(), id, opts...)
			if tt.wantError {
				require.Error(err)
				require.Empty(actual)
			} else {
				require.NoError(err)
				require.Equal(id, actual)
			}
		})
	}
}
