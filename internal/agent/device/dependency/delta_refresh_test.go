package dependency

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDeltaRefreshReference(t *testing.T) {
	testCases := []struct {
		name      string
		image     string
		digest    string
		want      string
		wantError bool
	}{
		{name: "When the image has an unqualified tag it should preserve the short name", image: "nginx:latest", digest: podmanDeltaTargetDigest, want: "nginx@" + podmanDeltaTargetDigest},
		{name: "When the image has no tag it should preserve the short name", image: "nginx", digest: podmanDeltaTargetDigest, want: "nginx@" + podmanDeltaTargetDigest},
		{name: "When the image has an unqualified namespace it should preserve the namespace", image: "acme/app:latest", digest: podmanDeltaTargetDigest, want: "acme/app@" + podmanDeltaTargetDigest},
		{name: "When the image has an explicit registry it should preserve that registry", image: "quay.io/acme/app:latest", digest: podmanDeltaTargetDigest, want: "quay.io/acme/app@" + podmanDeltaTargetDigest},
		{name: "When the registry has a port it should remove only the image tag", image: "localhost:5000/app:latest", digest: podmanDeltaTargetDigest, want: "localhost:5000/app@" + podmanDeltaTargetDigest},
		{name: "When the image has a digest it should replace it with the target digest", image: "nginx@" + podmanDeltaSource, digest: podmanDeltaTargetDigest, want: "nginx@" + podmanDeltaTargetDigest},
		{name: "When the image has a tag and digest it should remove both", image: "nginx:latest@" + podmanDeltaSource, digest: podmanDeltaTargetDigest, want: "nginx@" + podmanDeltaTargetDigest},
		{name: "When the image reference is invalid it should return an error", image: "invalid image", digest: podmanDeltaTargetDigest, wantError: true},
		{name: "When the target digest is missing it should return an error", image: "nginx:latest", wantError: true},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			got, err := deltaRefreshReference(tc.image, tc.digest)
			if tc.wantError {
				require.Error(err)
				require.Empty(got)
				return
			}
			require.NoError(err)
			require.Equal(tc.want, got)
		})
	}
}
