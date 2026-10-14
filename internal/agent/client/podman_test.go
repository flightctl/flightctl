package client

import (
	"context"
	"testing"

	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/poll"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestPodman_ListImages(t *testing.T) {
	require := require.New(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	log := log.NewPrefixLogger("test")
	mockExec := executer.NewMockExecuter(ctrl)
	readWriter := fileio.NewReadWriter(fileio.NewReader(), fileio.NewWriter())
	backoff := poll.Config{}

	testCases := []struct {
		name      string
		setupMock func(*executer.MockExecuter)
		want      []string
		wantErr   bool
	}{
		{
			name: "success with tagged images",
			setupMock: func(mock *executer.MockExecuter) {
				mock.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"image", "ls", "--format", `{{if and .Repository (ne .Repository "<none>") .Tag (ne .Tag "<none>")}}{{.Repository}}:{{.Tag}}{{else}}{{.ID}}{{end}}`}).
					Return("quay.io/example/app:v1.0\nquay.io/example/app:v2.0\n", "", 0)
			},
			want:    []string{"quay.io/example/app:v1.0", "quay.io/example/app:v2.0"},
			wantErr: false,
		},
		{
			name: "success with mixed tagged and untagged images",
			setupMock: func(mock *executer.MockExecuter) {
				mock.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"image", "ls", "--format", `{{if and .Repository (ne .Repository "<none>") .Tag (ne .Tag "<none>")}}{{.Repository}}:{{.Tag}}{{else}}{{.ID}}{{end}}`}).
					Return("quay.io/example/app:v1.0\nabc123def456\nquay.io/example/app:v2.0\n", "", 0)
			},
			want:    []string{"quay.io/example/app:v1.0", "abc123def456", "quay.io/example/app:v2.0"},
			wantErr: false,
		},
		{
			name: "success with empty list",
			setupMock: func(mock *executer.MockExecuter) {
				mock.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"image", "ls", "--format", `{{if and .Repository (ne .Repository "<none>") .Tag (ne .Tag "<none>")}}{{.Repository}}:{{.Tag}}{{else}}{{.ID}}{{end}}`}).
					Return("", "", 0)
			},
			want:    []string{},
			wantErr: false,
		},
		{
			name: "handles duplicate images",
			setupMock: func(mock *executer.MockExecuter) {
				mock.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"image", "ls", "--format", `{{if and .Repository (ne .Repository "<none>") .Tag (ne .Tag "<none>")}}{{.Repository}}:{{.Tag}}{{else}}{{.ID}}{{end}}`}).
					Return("quay.io/example/app:v1.0\nquay.io/example/app:v1.0\n", "", 0)
			},
			want:    []string{"quay.io/example/app:v1.0"},
			wantErr: false,
		},
		{
			name: "handles whitespace",
			setupMock: func(mock *executer.MockExecuter) {
				mock.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"image", "ls", "--format", `{{if and .Repository (ne .Repository "<none>") .Tag (ne .Tag "<none>")}}{{.Repository}}:{{.Tag}}{{else}}{{.ID}}{{end}}`}).
					Return("  quay.io/example/app:v1.0  \n  quay.io/example/app:v2.0  \n", "", 0)
			},
			want:    []string{"quay.io/example/app:v1.0", "quay.io/example/app:v2.0"},
			wantErr: false,
		},
		{
			name: "handles untagged images with <none> repository",
			setupMock: func(mock *executer.MockExecuter) {
				// Format string should output image ID for <none> repository, not <none>:<none>
				mock.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"image", "ls", "--format", `{{if and .Repository (ne .Repository "<none>") .Tag (ne .Tag "<none>")}}{{.Repository}}:{{.Tag}}{{else}}{{.ID}}{{end}}`}).
					Return("quay.io/example/app:v1.0\nf67b988b348a\n", "", 0)
			},
			want:    []string{"quay.io/example/app:v1.0", "f67b988b348a"},
			wantErr: false,
		},
		{
			name: "error from podman command",
			setupMock: func(mock *executer.MockExecuter) {
				mock.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"image", "ls", "--format", `{{if and .Repository (ne .Repository "<none>") .Tag (ne .Tag "<none>")}}{{.Repository}}:{{.Tag}}{{else}}{{.ID}}{{end}}`}).
					Return("", "error: failed to list images", 1)
			},
			want:    nil,
			wantErr: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			tc.setupMock(mockExec)
			podman := NewPodman(log, mockExec, readWriter, backoff)
			got, err := podman.ListImages(context.Background())
			if tc.wantErr {
				require.Error(err)
				require.Nil(got)
			} else {
				require.NoError(err)
				require.Equal(tc.want, got)
			}
		})
	}
}

func TestPodman_ListImageDigests(t *testing.T) {
	tests := []struct {
		name    string
		stdout  string
		stderr  string
		exit    int
		want    []string
		wantErr bool
	}{
		{
			name:   "When the image listing contains multiple images it should return content and repository digests",
			stdout: "sha256:first\t[\"quay.io/example/first@sha256:repo-first\"]\nsha256:second\tnull",
			want:   []string{"sha256:first", "quay.io/example/first@sha256:repo-first", "sha256:second"},
		},
		{
			name: "When the listing is empty it should return no digests",
			want: []string{},
		},
		{
			name:   "When the content digest is empty it should preserve repository digests",
			stdout: "\t[\"quay.io/example/first@sha256:repo-first\"]",
			want:   []string{"quay.io/example/first@sha256:repo-first"},
		},
		{
			name:    "When an image row has no separator it should return an error",
			stdout:  "not tab delimited",
			wantErr: true,
		},
		{
			name:    "When repository digest output is invalid it should return an error",
			stdout:  "sha256:first\tnot json",
			wantErr: true,
		},
		{
			name:    "When Podman image listing fails it should return an error",
			stderr:  "image listing failed",
			exit:    1,
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()
			mockExec := executer.NewMockExecuter(ctrl)
			mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "ls", "--format", imageDigestsListFormat).Return(tc.stdout, tc.stderr, tc.exit)

			podman := NewPodman(log.NewPrefixLogger("test"), mockExec, fileio.NewReadWriter(fileio.NewReader(), fileio.NewWriter()), poll.Config{})
			got, err := podman.ListImageDigests(context.Background())
			if tc.wantErr {
				require.Error(err)
				require.Nil(got)
				return
			}
			require.NoError(err)
			require.Equal(tc.want, got)
		})
	}
}

func TestPodman_ListArtifacts(t *testing.T) {
	require := require.New(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	log := log.NewPrefixLogger("test")
	mockExec := executer.NewMockExecuter(ctrl)
	readWriter := fileio.NewReadWriter(fileio.NewReader(), fileio.NewWriter())
	backoff := poll.Config{}

	testCases := []struct {
		name      string
		setupMock func(*executer.MockExecuter)
		want      []string
		wantErr   bool
	}{
		{
			name: "success with artifacts",
			setupMock: func(mock *executer.MockExecuter) {
				// Version check (called first by EnsureArtifactSupport)
				mock.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"--version"}).
					Return("podman version 5.5.0", "", 0)
				// List artifacts
				mock.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"artifact", "ls", "--format", "{{.Name}}"}).
					Return("quay.io/example/artifact:v1.0\nquay.io/example/artifact:v2.0\n", "", 0)
			},
			want:    []string{"quay.io/example/artifact:v1.0", "quay.io/example/artifact:v2.0"},
			wantErr: false,
		},
		{
			name: "success with empty list",
			setupMock: func(mock *executer.MockExecuter) {
				// Version check (called first by EnsureArtifactSupport)
				mock.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"--version"}).
					Return("podman version 5.5.0", "", 0)
				// List artifacts
				mock.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"artifact", "ls", "--format", "{{.Name}}"}).
					Return("", "", 0)
			},
			want:    []string{},
			wantErr: false,
		},
		{
			name: "handles duplicate artifacts",
			setupMock: func(mock *executer.MockExecuter) {
				// Version check (called first by EnsureArtifactSupport)
				mock.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"--version"}).
					Return("podman version 5.5.0", "", 0)
				// List artifacts
				mock.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"artifact", "ls", "--format", "{{.Name}}"}).
					Return("quay.io/example/artifact:v1.0\nquay.io/example/artifact:v1.0\n", "", 0)
			},
			want:    []string{"quay.io/example/artifact:v1.0"},
			wantErr: false,
		},
		{
			name: "error from podman command",
			setupMock: func(mock *executer.MockExecuter) {
				// Version check (called first by EnsureArtifactSupport)
				mock.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"--version"}).
					Return("podman version 5.5.0", "", 0)
				// List artifacts
				mock.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"artifact", "ls", "--format", "{{.Name}}"}).
					Return("", "error: failed to list artifacts", 1)
			},
			want:    nil,
			wantErr: true,
		},
		{
			name: "error from unsupported podman version",
			setupMock: func(mock *executer.MockExecuter) {
				// Version check - old version (EnsureArtifactSupport fails here)
				mock.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"--version"}).
					Return("podman version 5.4.0", "", 0)
			},
			want:    nil,
			wantErr: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			tc.setupMock(mockExec)
			podman := NewPodman(log, mockExec, readWriter, backoff)
			got, err := podman.ListArtifacts(context.Background())
			if tc.wantErr {
				require.Error(err)
				require.Nil(got)
			} else {
				require.NoError(err)
				require.Equal(tc.want, got)
			}
		})
	}
}

func TestPodman_RemoveImage(t *testing.T) {
	require := require.New(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	log := log.NewPrefixLogger("test")
	mockExec := executer.NewMockExecuter(ctrl)
	readWriter := fileio.NewReadWriter(fileio.NewReader(), fileio.NewWriter())
	backoff := poll.Config{}

	testCases := []struct {
		name      string
		image     string
		setupMock func(*executer.MockExecuter)
		wantErr   bool
	}{
		{
			name:  "success removing image",
			image: "quay.io/example/app:v1.0",
			setupMock: func(mock *executer.MockExecuter) {
				mock.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"image", "rm", "quay.io/example/app:v1.0"}).
					Return("", "", 0)
			},
			wantErr: false,
		},
		{
			name:  "success when image does not exist",
			image: "quay.io/example/app:nonexistent",
			setupMock: func(mock *executer.MockExecuter) {
				mock.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"image", "rm", "quay.io/example/app:nonexistent"}).
					Return("", "Error: no such image quay.io/example/app:nonexistent", 1)
			},
			wantErr: false, // Should not error for non-existent images
		},
		{
			name:  "success when image not known",
			image: "quay.io/example/app:unknown",
			setupMock: func(mock *executer.MockExecuter) {
				mock.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"image", "rm", "quay.io/example/app:unknown"}).
					Return("", "Error: image not known", 1)
			},
			wantErr: false, // Should not error for unknown images
		},
		{
			name:  "error from podman command",
			image: "quay.io/example/app:v1.0",
			setupMock: func(mock *executer.MockExecuter) {
				mock.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"image", "rm", "quay.io/example/app:v1.0"}).
					Return("", "Error: image is in use", 1)
			},
			wantErr: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			tc.setupMock(mockExec)
			podman := NewPodman(log, mockExec, readWriter, backoff)
			err := podman.RemoveImage(context.Background(), tc.image)
			if tc.wantErr {
				require.Error(err)
			} else {
				require.NoError(err)
			}
		})
	}
}

func TestPodman_RemoveArtifact(t *testing.T) {
	require := require.New(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	log := log.NewPrefixLogger("test")
	mockExec := executer.NewMockExecuter(ctrl)
	readWriter := fileio.NewReadWriter(fileio.NewReader(), fileio.NewWriter())
	backoff := poll.Config{}

	testCases := []struct {
		name      string
		artifact  string
		setupMock func(*executer.MockExecuter)
		wantErr   bool
	}{
		{
			name:     "success removing artifact",
			artifact: "quay.io/example/artifact:v1.0",
			setupMock: func(mock *executer.MockExecuter) {
				// Version check (called first by EnsureArtifactSupport)
				mock.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"--version"}).
					Return("podman version 5.5.0", "", 0)
				// Remove artifact
				mock.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"artifact", "rm", "quay.io/example/artifact:v1.0"}).
					Return("", "", 0)
			},
			wantErr: false,
		},
		{
			name:     "success when artifact does not exist",
			artifact: "quay.io/example/artifact:nonexistent",
			setupMock: func(mock *executer.MockExecuter) {
				// Version check (called first by EnsureArtifactSupport)
				mock.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"--version"}).
					Return("podman version 5.5.0", "", 0)
				// Remove artifact
				mock.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"artifact", "rm", "quay.io/example/artifact:nonexistent"}).
					Return("", "Error: no such artifact quay.io/example/artifact:nonexistent", 1)
			},
			wantErr: false, // Should not error for non-existent artifacts
		},
		{
			name:     "success when artifact not known",
			artifact: "quay.io/example/artifact:unknown",
			setupMock: func(mock *executer.MockExecuter) {
				// Version check (called first by EnsureArtifactSupport)
				mock.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"--version"}).
					Return("podman version 5.5.0", "", 0)
				// Remove artifact
				mock.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"artifact", "rm", "quay.io/example/artifact:unknown"}).
					Return("", "Error: artifact not known", 1)
			},
			wantErr: false, // Should not error for unknown artifacts
		},
		{
			name:     "error from podman command",
			artifact: "quay.io/example/artifact:v1.0",
			setupMock: func(mock *executer.MockExecuter) {
				// Version check (called first by EnsureArtifactSupport)
				mock.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"--version"}).
					Return("podman version 5.5.0", "", 0)
				// Remove artifact
				mock.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"artifact", "rm", "quay.io/example/artifact:v1.0"}).
					Return("", "Error: artifact is in use", 1)
			},
			wantErr: true,
		},
		{
			name:     "error from unsupported podman version",
			artifact: "quay.io/example/artifact:v1.0",
			setupMock: func(mock *executer.MockExecuter) {
				// Version check - old version (EnsureArtifactSupport fails here)
				mock.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"--version"}).
					Return("podman version 5.4.0", "", 0)
			},
			wantErr: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			tc.setupMock(mockExec)
			podman := NewPodman(log, mockExec, readWriter, backoff)
			err := podman.RemoveArtifact(context.Background(), tc.artifact)
			if tc.wantErr {
				require.Error(err)
			} else {
				require.NoError(err)
			}
		})
	}
}
