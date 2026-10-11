package applications

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDigestFromKubernetesImageID(t *testing.T) {
	testCases := []struct {
		name     string
		image    string
		imageID  string
		expected string
	}{
		{
			name:     "When the image ID is a pullable digest for the declared repository it should return the digest",
			image:    "quay.io/acme/app:latest",
			imageID:  "docker-pullable://quay.io/acme/app@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			expected: "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		},
		{
			name:     "When the image ID is a raw repository digest for the declared repository it should return the digest",
			image:    "10.100.102.128:5000/flightctl-tests/alpine:latest",
			imageID:  "10.100.102.128:5000/flightctl-tests/alpine@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			expected: "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		},
		{
			name:     "When the declared image is immutable it should use its digest",
			image:    "quay.io/acme/app@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			imageID:  "containerd://sha256:abcdef",
			expected: "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		},
		{
			name:    "When the pullable image ID is for a different repository it should remain unknown",
			image:   "quay.io/acme/app:latest",
			imageID: "docker-pullable://quay.io/other/app@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		},
		{
			name:    "When the raw image ID is for a different repository it should remain unknown",
			image:   "quay.io/acme/app:latest",
			imageID: "quay.io/other/app@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		},
		{
			name:    "When the runtime image ID is opaque it should remain unknown",
			image:   "quay.io/acme/app:latest",
			imageID: "containerd://sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.expected, digestFromKubernetesImageID(tc.image, tc.imageID))
		})
	}
}

func TestDigestFromRepoDigests(t *testing.T) {
	const digest = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

	testCases := []struct {
		name        string
		image       string
		repoDigests []string
		expected    string
	}{
		{
			name:        "When a repository digest matches the declared image it should return the digest",
			image:       "quay.io/acme/app:latest",
			repoDigests: []string{"quay.io/acme/app@" + digest},
			expected:    digest,
		},
		{
			name:        "When several repository digests are reported it should return the matching repository",
			image:       "quay.io/acme/app:latest",
			repoDigests: []string{"quay.io/other/app@sha256:ffff", "quay.io/acme/app@" + digest},
			expected:    digest,
		},
		{
			name:        "When no repository digest matches the declared image it should remain unknown",
			image:       "quay.io/acme/app:latest",
			repoDigests: []string{"quay.io/other/app@" + digest},
		},
		{
			name:        "When the runtime reports no repository digests it should remain unknown",
			image:       "quay.io/acme/app:latest",
			repoDigests: nil,
		},
		{
			name:        "When a repository digest is unparseable it should skip it",
			image:       "quay.io/acme/app:latest",
			repoDigests: []string{"not a reference", "quay.io/acme/app@" + digest},
			expected:    digest,
		},
		{
			name:        "When the declared image is unparseable it should remain unknown",
			image:       "not a reference",
			repoDigests: []string{"quay.io/acme/app@" + digest},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.expected, digestFromRepoDigests(tc.image, tc.repoDigests))
		})
	}
}
