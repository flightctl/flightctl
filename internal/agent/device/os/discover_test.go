package os

import (
	"testing"

	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
)

const (
	testTargetImage  = "quay.io/acme/os@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	testTargetRepo   = "quay.io/acme/os"
	testSourceDigest = "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	testDeltaDigest  = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	testHintedDelta  = "quay.io/acme-deltas/os@sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
)

func TestSelectOSDeltaCandidate(t *testing.T) {
	matching := &client.OCIIndex{
		Manifests: []client.OCIReferrer{
			{
				Digest:       testDeltaDigest,
				ArtifactType: ociDeltaArtifactType,
				Annotations: map[string]string{
					ociDeltaSourceAnnotation: testSourceDigest,
				},
			},
		},
	}

	testCases := []struct {
		name         string
		hint         *string
		targetImage  string
		sourceDigest string
		index        *client.OCIIndex
		want         string
	}{
		{
			name:        "When hint is set it should return the hint and ignore the index",
			hint:        lo.ToPtr(testHintedDelta),
			targetImage: testTargetImage,
			index:       matching,
			want:        testHintedDelta,
		},
		{
			name:         "When no hint and a matching referrer exists it should return targetRepo@digest",
			targetImage:  testTargetImage,
			sourceDigest: testSourceDigest,
			index:        matching,
			want:         testTargetRepo + "@" + testDeltaDigest,
		},
		{
			name:         "When source annotation does not match it should return empty",
			targetImage:  testTargetImage,
			sourceDigest: testSourceDigest,
			index: &client.OCIIndex{
				Manifests: []client.OCIReferrer{
					{
						Digest:       testDeltaDigest,
						ArtifactType: ociDeltaArtifactType,
						Annotations: map[string]string{
							ociDeltaSourceAnnotation: "sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee",
						},
					},
				},
			},
		},
		{
			name:         "When artifactType does not match it should return empty",
			targetImage:  testTargetImage,
			sourceDigest: testSourceDigest,
			index: &client.OCIIndex{
				Manifests: []client.OCIReferrer{
					{
						Digest:       testDeltaDigest,
						ArtifactType: "application/vnd.example.other",
						Annotations: map[string]string{
							ociDeltaSourceAnnotation: testSourceDigest,
						},
					},
				},
			},
		},
		{
			name:         "When digest is selected it should never join a different repository",
			targetImage:  "quay.io/acme/os:latest",
			sourceDigest: testSourceDigest,
			index:        matching,
			want:         testTargetRepo + "@" + testDeltaDigest,
		},
		{
			name:         "When index is nil it should return empty",
			targetImage:  testTargetImage,
			sourceDigest: testSourceDigest,
		},
		{
			name:         "When index has no manifests it should return empty",
			targetImage:  testTargetImage,
			sourceDigest: testSourceDigest,
			index:        &client.OCIIndex{},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got := selectOSDeltaCandidate(tc.hint, tc.targetImage, tc.sourceDigest, tc.index)
			require.Equal(t, tc.want, got)
		})
	}
}
