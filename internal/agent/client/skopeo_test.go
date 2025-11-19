package client

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestSkopeoInspectManifest(t *testing.T) {
	tests := []struct {
		name           string
		image          string
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
					ExecuteWithContext(gomock.Any(), "skopeo", "inspect", "--raw", "docker://quay.io/test/app:v1").
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
					ExecuteWithContext(gomock.Any(), "skopeo", "inspect", "--raw", "docker://quay.io/test/artifact:v1").
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
			name:  "inspect with authentication",
			image: "private-registry.io/test/image:v1",
			setupMocks: func(mockExec *executer.MockExecuter) {
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
					ExecuteWithContext(gomock.Any(), "skopeo", "inspect", "--raw", "docker://private-registry.io/test/image:v1", "--authfile", "/tmp/test-auth.json").
					Return(manifestJSON, "", 0)
			},
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
					ExecuteWithContext(gomock.Any(), "skopeo", "inspect", "--raw", "docker://ghcr.io/homebrew/core/sqlite:3.50.2").
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
					ExecuteWithContext(gomock.Any(), "skopeo", "inspect", "--raw", "docker://quay.io/test/nonexistent:v1").
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
					ExecuteWithContext(gomock.Any(), "skopeo", "inspect", "--raw", "docker://quay.io/test/invalid:v1").
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

			tt.setupMocks(mockExec)

			readWriter := fileio.NewReadWriter()
			skopeo := NewSkopeo(logger, mockExec, readWriter)

			var opts []ClientOption
			if tt.name == "inspect with authentication" {
				tmpFile := "/tmp/test-auth.json"
				_ = readWriter.WriteFile(tmpFile, []byte(`{"auths":{}}`), 0600)
				defer func() { _ = readWriter.RemoveAll(tmpFile) }()
				opts = append(opts, WithPullSecret(tmpFile))
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
