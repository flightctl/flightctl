package applications

import (
	"testing"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/device/applications/provider"
	"github.com/flightctl/flightctl/internal/agent/device/dependency"
	"github.com/stretchr/testify/require"
)

func TestAddCachedParentImageDigests(t *testing.T) {
	const (
		appID          = "app-id"
		parentImage    = "quay.io/example/parent:v1"
		parentDigest   = "sha256:parent"
		workloadImage  = "quay.io/example/workload:v1"
		workloadDigest = "sha256:workload"
	)

	imageDigests := func(digests ...v1beta1.ApplicationImageDigest) *[]v1beta1.ApplicationImageDigest {
		return &digests
	}
	cacheEntry := func(id, image, digest string) provider.CacheEntry {
		return provider.CacheEntry{
			Name: id,
			Parent: dependency.OCIPullTarget{
				Reference: image,
				Digest:    digest,
			},
		}
	}

	testCases := []struct {
		name         string
		result       AppStatusResult
		cacheEntries []provider.CacheEntry
		want         []v1beta1.ApplicationImageDigest
	}{
		{
			name: "When the status list is nil, it should include the cached parent image",
			result: AppStatusResult{
				ID:     appID,
				Status: v1beta1.DeviceApplicationStatus{Name: "parent-app"},
			},
			cacheEntries: []provider.CacheEntry{cacheEntry(appID, parentImage, parentDigest)},
			want:         []v1beta1.ApplicationImageDigest{{Image: parentImage, Digest: parentDigest}},
		},
		{
			name: "When the parent ref is absent, it should append the cached parent image",
			result: AppStatusResult{
				ID: appID,
				Status: v1beta1.DeviceApplicationStatus{
					Name:         "parent-app",
					ImageDigests: imageDigests(v1beta1.ApplicationImageDigest{Image: workloadImage, Digest: workloadDigest}),
				},
			},
			cacheEntries: []provider.CacheEntry{cacheEntry(appID, parentImage, parentDigest)},
			want: []v1beta1.ApplicationImageDigest{
				{Image: parentImage, Digest: parentDigest},
				{Image: workloadImage, Digest: workloadDigest},
			},
		},
		{
			name: "When the parent digest is empty, it should fill the cached digest",
			result: AppStatusResult{
				ID: appID,
				Status: v1beta1.DeviceApplicationStatus{
					Name:         "parent-app",
					ImageDigests: imageDigests(v1beta1.ApplicationImageDigest{Image: parentImage}),
				},
			},
			cacheEntries: []provider.CacheEntry{cacheEntry(appID, parentImage, parentDigest)},
			want:         []v1beta1.ApplicationImageDigest{{Image: parentImage, Digest: parentDigest}},
		},
		{
			name: "When the cached digest is empty, it should leave other status entries unchanged",
			result: AppStatusResult{
				ID: appID,
				Status: v1beta1.DeviceApplicationStatus{
					Name:         "parent-app",
					ImageDigests: imageDigests(v1beta1.ApplicationImageDigest{Image: workloadImage, Digest: workloadDigest}),
				},
			},
			cacheEntries: []provider.CacheEntry{cacheEntry(appID, parentImage, "")},
			want:         []v1beta1.ApplicationImageDigest{{Image: workloadImage, Digest: workloadDigest}},
		},
		{
			name: "When result and display names differ, it should match cache by application ID",
			result: AppStatusResult{
				ID: appID,
				Status: v1beta1.DeviceApplicationStatus{
					Name: "parent-app",
				},
			},
			cacheEntries: []provider.CacheEntry{
				cacheEntry(appID, parentImage, parentDigest),
				cacheEntry("parent-app", "quay.io/example/wrong:v1", "sha256:wrong"),
			},
			want: []v1beta1.ApplicationImageDigest{{Image: parentImage, Digest: parentDigest}},
		},
		{
			name: "When the cached parent pair is already present, it should not duplicate it",
			result: AppStatusResult{
				ID: appID,
				Status: v1beta1.DeviceApplicationStatus{
					Name:         "parent-app",
					ImageDigests: imageDigests(v1beta1.ApplicationImageDigest{Image: parentImage, Digest: parentDigest}),
				},
			},
			cacheEntries: []provider.CacheEntry{cacheEntry(appID, parentImage, parentDigest)},
			want:         []v1beta1.ApplicationImageDigest{{Image: parentImage, Digest: parentDigest}},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			cache := provider.NewOCITargetCache()
			for _, entry := range tc.cacheEntries {
				cache.Set(entry)
			}

			results := []AppStatusResult{tc.result}
			addCachedParentImageDigests(results, cache)

			require.NotNil(t, results[0].Status.ImageDigests)
			require.Equal(t, tc.want, *results[0].Status.ImageDigests)
		})
	}
}
