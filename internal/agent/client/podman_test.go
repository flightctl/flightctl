package client

import (
	"context"
	"testing"

	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/poll"
	"github.com/opencontainers/go-digest"
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
			mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "ls", "--format", PodmanImageDigestsListFormat).Return(tc.stdout, tc.stderr, tc.exit)

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

func TestPodman_InspectVolumeReturnsPublicProvenance(t *testing.T) {
	require := require.New(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	mockExec := executer.NewMockExecuter(ctrl)
	readWriter := fileio.NewReadWriter(fileio.NewReader(), fileio.NewWriter())
	volumeJSON := `{"Name":"app-data","Driver":"image","CreatedAt":"2026-10-12T12:00:00Z","Labels":{"io.flightctl.delta.digest":"sha256:observed"},"Options":{"image":"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"},"StorageID":"not-an-image-id"}`
	mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"volume", "inspect", "app-data", "--format", "{{json .}}"}).Return(volumeJSON, "", 0)
	podman := NewPodman(log.NewPrefixLogger("test"), mockExec, readWriter, poll.Config{})
	volume, err := podman.InspectVolume(context.Background(), "app-data")
	require.NoError(err)
	require.Equal("app-data", volume.Name)
	require.Equal("image", volume.Driver)
	require.Equal("2026-10-12T12:00:00Z", volume.CreatedAt)
	require.Equal("sha256:observed", volume.Labels[PodmanDeltaVolumeDigestLabel])
	require.Equal("0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", volume.Options["image"])
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

func TestPodmanArtifactReferenceCompatibility(t *testing.T) {
	const (
		taggedReference  = "quay.io/example/artifact:v1"
		digestValue      = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		otherDigestValue = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	)
	parsedDigest, err := digest.Parse(digestValue)
	require.NoError(t, err)
	digestReference := "quay.io/example/artifact@" + digestValue
	malformedReference := taggedReference + "@" + digestValue

	testCases := []struct {
		name      string
		setupMock func(*executer.MockExecuter)
		run       func(*testing.T, *Podman)
	}{
		{
			name: "When artifact reference is tagged it should return digest-pinned repository reference",
			setupMock: func(mockExec *executer.MockExecuter) {
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"--version"}).
					Return("podman version 5.5.0", "", 0)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"artifact", "inspect", taggedReference}).
					Return(`{"Digest":"`+digestValue+`"}`, "", 0)
			},
			run: func(t *testing.T, podman *Podman) {
				require := require.New(t)
				immutableReference, observedDigest, err := podman.ImmutableArtifactReference(context.Background(), taggedReference)
				require.NoError(err)
				require.Equal("quay.io/example/artifact@"+digestValue, immutableReference)
				require.Equal(digestValue, observedDigest)
			},
		},
		{
			name: "When artifact reference is digest pinned it should inspect encoded selector",
			setupMock: func(mockExec *executer.MockExecuter) {
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"--version"}).
					Return("podman version 5.5.0", "", 0)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"artifact", "inspect", parsedDigest.Encoded()}).
					Return(`{"Digest":"`+digestValue+`"}`, "", 0)
			},
			run: func(t *testing.T, podman *Podman) {
				require := require.New(t)
				observedDigest, err := podman.ArtifactDigest(context.Background(), digestReference)
				require.NoError(err)
				require.Equal(digestValue, observedDigest)
			},
		},
		{
			name: "When artifact digest is pinned it should reject mismatch",
			setupMock: func(mockExec *executer.MockExecuter) {
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"--version"}).
					Return("podman version 5.5.0", "", 0)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"artifact", "inspect", parsedDigest.Encoded()}).
					Return(`{"Digest":"`+otherDigestValue+`"}`, "", 0)
			},
			run: func(t *testing.T, podman *Podman) {
				require := require.New(t)
				_, err := podman.ArtifactDigest(context.Background(), digestReference)
				require.Error(err)
				require.Contains(err.Error(), "artifact digest mismatch")
			},
		},
		{
			name: "When artifact annotations are inspected it should use encoded selector",
			setupMock: func(mockExec *executer.MockExecuter) {
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"--version"}).
					Return("podman version 5.5.0", "", 0)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"artifact", "inspect", parsedDigest.Encoded()}).
					Return(`{"Manifest":{"layers":[{"annotations":{"appType":"compose"}}]}}`, "", 0)
			},
			run: func(t *testing.T, podman *Podman) {
				require := require.New(t)
				annotations, err := podman.InspectArtifactAnnotations(context.Background(), digestReference)
				require.NoError(err)
				require.Equal(map[string]string{"appType": "compose"}, annotations)
			},
		},
		{
			name: "When artifact reference is digest pinned it should exist by encoded selector",
			setupMock: func(mockExec *executer.MockExecuter) {
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"artifact", "inspect", parsedDigest.Encoded()}).
					Return("{}", "", 0)
			},
			run: func(t *testing.T, podman *Podman) {
				require := require.New(t)
				require.True(podman.ArtifactExists(context.Background(), digestReference))
			},
		},
		{
			name: "When artifact is digest pinned it should extract encoded selector",
			setupMock: func(mockExec *executer.MockExecuter) {
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"--version"}).
					Return("podman version 5.5.0", "", 0)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"artifact", "extract", parsedDigest.Encoded(), "/destination"}).
					Return("", "", 0)
			},
			run: func(t *testing.T, podman *Podman) {
				require := require.New(t)
				_, err := podman.ExtractArtifact(context.Background(), digestReference, "/destination")
				require.NoError(err)
			},
		},
		{
			name:      "When artifact reference combines tag and digest it should reject",
			setupMock: func(*executer.MockExecuter) {},
			run: func(t *testing.T, podman *Podman) {
				require := require.New(t)
				_, err := podman.ExtractArtifact(context.Background(), malformedReference, "/destination")
				require.Error(err)
			},
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			logger := log.NewPrefixLogger("test")
			mockExec := executer.NewMockExecuter(ctrl)
			readWriter := fileio.NewReadWriter(fileio.NewReader(), fileio.NewWriter())
			podman := NewPodman(logger, mockExec, readWriter, poll.Config{})

			testCase.setupMock(mockExec)
			testCase.run(t, podman)
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
