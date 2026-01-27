package client

import (
	"context"
	"testing"

	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
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

func TestCRI_RemoveImage(t *testing.T) {
	testCases := []struct {
		name          string
		image         string
		setupMocks    func(*executer.MockExecuter)
		wantErr       bool
		wantErrSubstr string
	}{
		{
			name:  "success - image removed",
			image: "quay.io/example/app:v1.0",
			setupMocks: func(mockExec *executer.MockExecuter) {
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "crictl", []string{"rmi", "quay.io/example/app:v1.0"}).
					Return("Deleted: sha256:1234...", "", 0)
			},
			wantErr: false,
		},
		{
			name:  "failure - image in use",
			image: "quay.io/example/app:v1.0",
			setupMocks: func(mockExec *executer.MockExecuter) {
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "crictl", []string{"rmi", "quay.io/example/app:v1.0"}).
					Return("", "error removing image: image is in use by container", 1)
			},
			wantErr:       true,
			wantErrSubstr: "crictl rmi",
		},
		{
			name:  "failure - image not found",
			image: "quay.io/example/nonexistent:v1.0",
			setupMocks: func(mockExec *executer.MockExecuter) {
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "crictl", []string{"rmi", "quay.io/example/nonexistent:v1.0"}).
					Return("", "no such image", 1)
			},
			wantErr:       true,
			wantErrSubstr: "crictl rmi",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockExec := executer.NewMockExecuter(ctrl)
			tc.setupMocks(mockExec)

			logger := log.NewPrefixLogger("test")
			readWriter := fileio.NewReadWriter(fileio.NewReader(), fileio.NewWriter())
			criClient := NewCRI(logger, mockExec, readWriter)

			err := criClient.RemoveImage(context.Background(), tc.image)
			if tc.wantErr {
				require.Error(err)
				if tc.wantErrSubstr != "" {
					require.Contains(err.Error(), tc.wantErrSubstr)
				}
			} else {
				require.NoError(err)
			}
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
