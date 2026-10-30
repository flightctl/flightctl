package client

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/containerd/platforms"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/opencontainers/go-digest"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestSkopeoInspectManifest(t *testing.T) {
	tests := []struct {
		name           string
		image          string
		withAuth       bool
		setupMocks     func(*executer.MockExecuter)
		expectedResult *OCIManifest
		expectedError  bool
	}{
		{
			name:  "inspect OCI image manifest",
			image: "quay.io/test/app:v1",
			setupMocks: func(mockExec *executer.MockExecuter) {
				manifestJSON := `{
					"schemaVersion": 2,
					"mediaType": "application/vnd.oci.image.manifest.v1+json",
					"config": {
						"mediaType": "application/vnd.oci.image.config.v1+json",
						"digest": "sha256:abc123",
						"size": 677
					},
					"layers": [
						{
							"mediaType": "application/vnd.oci.image.layer.v1.tar+gzip",
							"digest": "sha256:def456",
							"size": 195
						}
					]
				}`
				mockExec.EXPECT().
					ExecuteWithContext(gomock.Any(), "skopeo", "inspect", "--raw", "docker://quay.io/test/app:v1", "--no-creds").
					Return(manifestJSON, "", 0)
			},
			expectedResult: &OCIManifest{
				MediaType: "application/vnd.oci.image.manifest.v1+json",
				Config: &OCIDescriptor{
					MediaType: "application/vnd.oci.image.config.v1+json",
					Digest:    "sha256:abc123",
					Size:      677,
				},
			},
			expectedError: false,
		},
		{
			name:  "inspect OCI artifact manifest",
			image: "quay.io/test/artifact:v1",
			setupMocks: func(mockExec *executer.MockExecuter) {
				manifestJSON := `{
					"schemaVersion": 2,
					"mediaType": "application/vnd.oci.image.manifest.v1+json",
					"artifactType": "application/vnd.example.artifact",
					"config": {
						"mediaType": "application/vnd.oci.empty.v1+json",
						"digest": "sha256:44136fa355b3678a1146ad16f7e8649e94fb4fc21fe77e8310c060f61caaff8a",
						"size": 2
					},
					"layers": [
						{
							"mediaType": "text/plain",
							"digest": "sha256:xyz789",
							"size": 20
						}
					]
				}`
				mockExec.EXPECT().
					ExecuteWithContext(gomock.Any(), "skopeo", "inspect", "--raw", "docker://quay.io/test/artifact:v1", "--no-creds").
					Return(manifestJSON, "", 0)
			},
			expectedResult: &OCIManifest{
				MediaType:    "application/vnd.oci.image.manifest.v1+json",
				ArtifactType: "application/vnd.example.artifact",
				Config: &OCIDescriptor{
					MediaType: "application/vnd.oci.empty.v1+json",
					Digest:    "sha256:44136fa355b3678a1146ad16f7e8649e94fb4fc21fe77e8310c060f61caaff8a",
					Size:      2,
				},
			},
			expectedError: false,
		},
		{
			name:     "inspect with authentication",
			image:    "private-registry.io/test/image:v1",
			withAuth: true,
			expectedResult: &OCIManifest{
				MediaType: "application/vnd.oci.image.manifest.v1+json",
				Config: &OCIDescriptor{
					MediaType: "application/vnd.oci.image.config.v1+json",
					Digest:    "sha256:abc123",
					Size:      500,
				},
			},
			expectedError: false,
		},
		{
			name:  "inspect manifest index (multi-platform)",
			image: "ghcr.io/homebrew/core/sqlite:3.50.2",
			setupMocks: func(mockExec *executer.MockExecuter) {
				manifestJSON := `{
					"schemaVersion": 2,
					"mediaType": "application/vnd.oci.image.index.v1+json",
					"manifests": [
						{
							"mediaType": "application/vnd.oci.image.manifest.v1+json",
							"digest": "sha256:aaa111",
							"size": 2567,
							"platform": {
								"architecture": "arm64",
								"os": "linux"
							}
						},
						{
							"mediaType": "application/vnd.oci.image.manifest.v1+json",
							"digest": "sha256:bbb222",
							"size": 2572,
							"platform": {
								"architecture": "amd64",
								"os": "linux"
							}
						}
					]
				}`
				mockExec.EXPECT().
					ExecuteWithContext(gomock.Any(), "skopeo", "inspect", "--raw", "docker://ghcr.io/homebrew/core/sqlite:3.50.2", "--no-creds").
					Return(manifestJSON, "", 0)
			},
			expectedResult: &OCIManifest{
				MediaType: "application/vnd.oci.image.index.v1+json",
				Manifests: json.RawMessage(`[
						{
							"mediaType": "application/vnd.oci.image.manifest.v1+json",
							"digest": "sha256:aaa111",
							"size": 2567,
							"platform": {
								"architecture": "arm64",
								"os": "linux"
							}
						},
						{
							"mediaType": "application/vnd.oci.image.manifest.v1+json",
							"digest": "sha256:bbb222",
							"size": 2572,
							"platform": {
								"architecture": "amd64",
								"os": "linux"
							}
						}
					]`),
			},
			expectedError: false,
		},
		{
			name:  "inspect fails with non-zero exit code",
			image: "quay.io/test/nonexistent:v1",
			setupMocks: func(mockExec *executer.MockExecuter) {
				mockExec.EXPECT().
					ExecuteWithContext(gomock.Any(), "skopeo", "inspect", "--raw", "docker://quay.io/test/nonexistent:v1", "--no-creds").
					Return("", "Error: manifest unknown", 1)
			},
			expectedResult: nil,
			expectedError:  true,
		},
		{
			name:  "inspect returns invalid JSON",
			image: "quay.io/test/invalid:v1",
			setupMocks: func(mockExec *executer.MockExecuter) {
				mockExec.EXPECT().
					ExecuteWithContext(gomock.Any(), "skopeo", "inspect", "--raw", "docker://quay.io/test/invalid:v1", "--no-creds").
					Return("not valid json", "", 0)
			},
			expectedResult: nil,
			expectedError:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockExec := executer.NewMockExecuter(ctrl)
			logger := log.NewPrefixLogger("test")
			logger.SetLevel(logrus.ErrorLevel)

			readWriter := fileio.NewReadWriter(fileio.NewReader(), fileio.NewWriter())
			skopeo := NewSkopeo(logger, mockExec, readWriter)

			var opts []ClientOption
			if tt.withAuth {
				tmpFile := filepath.Join(t.TempDir(), "auth.json")
				require.NoError(t, readWriter.WriteFile(tmpFile, []byte(`{"auths":{}}`), 0600))
				opts = append(opts, WithPullSecret(tmpFile))
				manifestJSON := `{
					"schemaVersion": 2,
					"mediaType": "application/vnd.oci.image.manifest.v1+json",
					"config": {
						"mediaType": "application/vnd.oci.image.config.v1+json",
						"digest": "sha256:abc123",
						"size": 500
					}
				}`
				mockExec.EXPECT().
					ExecuteWithContext(gomock.Any(), "skopeo", "inspect", "--raw", "docker://private-registry.io/test/image:v1", "--authfile", tmpFile).
					Return(manifestJSON, "", 0)
			} else {
				tt.setupMocks(mockExec)
			}

			ctx := context.Background()
			result, err := skopeo.InspectManifest(ctx, tt.image, opts...)

			if tt.expectedError {
				require.Error(t, err)
				require.Nil(t, result)
			} else {
				require.NoError(t, err)
				require.NotNil(t, result)
				require.Equal(t, tt.expectedResult.MediaType, result.MediaType)
				require.Equal(t, tt.expectedResult.ArtifactType, result.ArtifactType)

				if tt.expectedResult.Config != nil {
					require.NotNil(t, result.Config)
					require.Equal(t, tt.expectedResult.Config.MediaType, result.Config.MediaType)
					require.Equal(t, tt.expectedResult.Config.Digest, result.Config.Digest)
					require.Equal(t, tt.expectedResult.Config.Size, result.Config.Size)
				}

				if len(tt.expectedResult.Manifests) > 0 {
					require.NotEmpty(t, result.Manifests)
				}
			}
		})
	}
}

func TestSkopeoInspectDigest(t *testing.T) {
	tests := []struct {
		name          string
		image         string
		setupMocks    func(*executer.MockExecuter)
		want          string
		expectedError bool
	}{
		{
			name:  "When inspect succeeds it should return the digest",
			image: "quay.io/acme/os:latest",
			setupMocks: func(mockExec *executer.MockExecuter) {
				mockExec.EXPECT().
					ExecuteWithContext(gomock.Any(), "skopeo", "inspect", "--format", "{{.Digest}}", "docker://quay.io/acme/os:latest", "--no-creds").
					Return("sha256:abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789\n", "", 0)
			},
			want: "sha256:abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789",
		},
		{
			name:  "When inspect fails it should return an error",
			image: "quay.io/acme/os:missing",
			setupMocks: func(mockExec *executer.MockExecuter) {
				mockExec.EXPECT().
					ExecuteWithContext(gomock.Any(), "skopeo", "inspect", "--format", "{{.Digest}}", "docker://quay.io/acme/os:missing", "--no-creds").
					Return("", "Error: manifest unknown", 1)
			},
			expectedError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockExec := executer.NewMockExecuter(ctrl)
			logger := log.NewPrefixLogger("test")
			logger.SetLevel(logrus.ErrorLevel)
			tt.setupMocks(mockExec)

			skopeo := NewSkopeo(logger, mockExec, fileio.NewReadWriter(fileio.NewReader(), fileio.NewWriter()))
			got, err := skopeo.InspectDigest(context.Background(), tt.image)
			if tt.expectedError {
				require.Error(t, err)
				require.Empty(t, got)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestSkopeoListReferrers(t *testing.T) {
	const targetDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const targetImage = "quay.io/acme/os@" + targetDigest
	const indexJSON = `{"schemaVersion":2,"manifests":[{"digest":"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}]}`
	tests := []struct {
		name        string
		tag         bool
		auth        bool
		defaultAuth bool
		fail        bool
	}{
		{name: "When an image is pinned it should invoke the agent registry helper"},
		{name: "When an image has a tag it should resolve the digest before discovery", tag: true},
		{name: "When a pull secret is supplied it should pass its path to the helper", auth: true},
		{name: "When default auth is enabled it should use the executor user credentials", defaultAuth: true},
		{name: "When the registry helper fails it should propagate its error", fail: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()
			exec := executer.NewMockExecuter(ctrl)
			rw := fileio.NewReadWriter(fileio.NewReader(), fileio.NewWriter())
			skopeo := NewSkopeo(log.NewPrefixLogger("test"), exec, rw)
			image := targetImage
			if tc.tag {
				image = "quay.io/acme/os:latest"
				exec.EXPECT().ExecuteWithContext(gomock.Any(), "skopeo", "inspect", "--format", "{{.Digest}}", "docker://"+image, "--no-creds").Return(targetDigest, "", 0)
			}
			args := []any{gomock.Any(), "flightctl-agent", "registry-referrers", "--image", targetImage, "--timeout", "2m0s"}
			var opts []ClientOption
			if tc.auth {
				path := filepath.Join(t.TempDir(), "auth.json")
				require.NoError(t, rw.WriteFile(path, []byte(`{"auths":{}}`), 0600))
				opts = append(opts, WithPullSecret(path))
				args = append(args, "--authfile", path)
			} else if tc.defaultAuth {
				opts = append(opts, WithDefaultAuth())
			} else {
				args = append(args, "--no-creds")
			}
			if tc.fail {
				exec.EXPECT().ExecuteWithContext(args[0], args[1], args[2:]...).Return("", "registry request failed", 1)
			} else {
				exec.EXPECT().ExecuteWithContext(args[0], args[1], args[2:]...).Return(indexJSON, "", 0)
			}
			got, err := skopeo.ListReferrers(context.Background(), image, opts...)
			if tc.fail {
				require.ErrorContains(t, err, "registry request failed")
				require.Nil(t, got)
				return
			}
			require.NoError(t, err)
			require.Len(t, got.Manifests, 1)
		})
	}
}

func TestSkopeoListPlatformReferrers(t *testing.T) {
	require := require.New(t)
	const (
		targetImage    = "quay.io/acme/os:latest"
		platformDigest = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		deltaDigest    = "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	)
	hostPlatform := platforms.DefaultSpec()
	platform := map[string]string{
		"os":           hostPlatform.OS,
		"architecture": hostPlatform.Architecture,
	}
	if hostPlatform.Variant != "" {
		platform["variant"] = hostPlatform.Variant
	}
	targetIndex, err := json.Marshal(map[string]any{
		"schemaVersion": 2,
		"mediaType":     "application/vnd.oci.image.index.v1+json",
		"manifests": []map[string]any{{
			"mediaType": "application/vnd.oci.image.manifest.v1+json",
			"digest":    platformDigest,
			"size":      100,
			"platform":  platform,
		}},
	})
	require.NoError(err)
	referrers := `{"schemaVersion":2,"manifests":[{"digest":"` + deltaDigest + `","artifactType":"application/vnd.io.github.containers.oci-delta.v1"}]}`

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	mockExec := executer.NewMockExecuter(ctrl)
	logger := log.NewPrefixLogger("test")
	logger.SetLevel(logrus.ErrorLevel)
	skopeo := NewSkopeo(logger, mockExec, fileio.NewReadWriter(fileio.NewReader(), fileio.NewWriter()))
	mockExec.EXPECT().ExecuteWithContext(
		gomock.Any(),
		"skopeo", "inspect", "--raw", "docker://"+targetImage, "--no-creds",
	).Return(string(targetIndex), "", 0)
	mockExec.EXPECT().ExecuteWithContext(
		gomock.Any(),
		"flightctl-agent", "registry-referrers", "--image", "quay.io/acme/os@"+platformDigest, "--timeout", "2m0s", "--no-creds",
	).Return(referrers, "", 0)

	result, err := skopeo.ListPlatformReferrers(context.Background(), targetImage)
	require.NoError(err)
	require.NotNil(result)
	require.Equal(platformDigest, result.TargetDigest)
	require.NotNil(result.Referrers)
	require.Len(result.Referrers.Manifests, 1)
	require.Equal(deltaDigest, result.Referrers.Manifests[0].Digest)
}

func TestSkopeoInspectPlatformDigestHashesExactSingleManifestBytes(t *testing.T) {
	require := require.New(t)
	const image = "quay.io/acme/os:latest"
	manifest := `{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json"}`
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	mockExec := executer.NewMockExecuter(ctrl)
	skopeo := NewSkopeo(
		log.NewPrefixLogger("test"),
		mockExec,
		fileio.NewReadWriter(fileio.NewReader(), fileio.NewWriter()),
	)
	mockExec.EXPECT().ExecuteWithContext(
		gomock.Any(), "skopeo", "inspect", "--raw", "docker://"+image, "--no-creds",
	).Return(manifest+"\n", "", 0)

	got, err := skopeo.InspectPlatformDigest(context.Background(), image)
	require.NoError(err)
	require.Equal(digest.FromBytes([]byte(manifest+"\n")).String(), got)
}

func TestSkopeoInspectPlatformDigestUsesPinnedDigestAlgorithm(t *testing.T) {
	require := require.New(t)
	manifest := `{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json"}`
	pinnedDigest := digest.SHA512.FromBytes([]byte(manifest))
	image := "quay.io/acme/os@" + pinnedDigest.String()
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	mockExec := executer.NewMockExecuter(ctrl)
	skopeo := NewSkopeo(
		log.NewPrefixLogger("test"),
		mockExec,
		fileio.NewReadWriter(fileio.NewReader(), fileio.NewWriter()),
	)
	mockExec.EXPECT().ExecuteWithContext(
		gomock.Any(), "skopeo", "inspect", "--raw", "docker://"+image, "--no-creds",
	).Return(manifest, "", 0)

	got, err := skopeo.InspectPlatformDigest(context.Background(), image)
	require.NoError(err)
	require.Equal(pinnedDigest.String(), got)
}

func TestSkopeoCopyAuthentication(t *testing.T) {
	const source = "docker://quay.io/acme/delta:latest"
	const destination = "oci-archive:/tmp/delta.oci"
	for _, method := range []struct {
		name     string
		preserve bool
	}{
		{name: "Copy"},
		{name: "CopyPreservingDigests", preserve: true},
	} {
		t.Run(method.name, func(t *testing.T) {
			for _, tc := range []struct {
				name string
				opts []ClientOption
				args []string
			}{
				{name: "When default authentication is requested it should use the credential store", opts: []ClientOption{WithDefaultAuth()}},
				{name: "When authentication is not requested it should disable source credentials", args: []string{"--src-no-creds"}},
				{name: "When a pull secret is supplied it should use that source auth file", opts: []ClientOption{WithPullSecret("/auth.json")}, args: []string{"--src-authfile", "/auth.json"}},
			} {
				t.Run(tc.name, func(t *testing.T) {
					require := require.New(t)
					ctrl := gomock.NewController(t)
					defer ctrl.Finish()
					root := t.TempDir()
					rw := fileio.NewReadWriter(fileio.NewReader(fileio.WithReaderRootDir(root)), fileio.NewWriter(fileio.WithWriterRootDir(root)))
					require.NoError(rw.WriteFile("/auth.json", []byte(`{"auths":{}}`), fileio.DefaultFilePermissions))
					mockExec := executer.NewMockExecuter(ctrl)
					skopeo := NewSkopeo(log.NewPrefixLogger("test"), mockExec, rw)
					args := []any{gomock.Any(), "skopeo", "copy"}
					copyImage := skopeo.Copy
					if method.preserve {
						args = append(args, "--preserve-digests")
						copyImage = skopeo.CopyPreservingDigests
					}
					args = append(args, source, destination)
					for _, arg := range tc.args {
						args = append(args, arg)
					}
					mockExec.EXPECT().ExecuteWithContext(args[0], args[1], args[2:]...).Return("", "", 0)
					require.NoError(copyImage(t.Context(), source, destination, tc.opts...))
				})
			}
		})
	}
}
