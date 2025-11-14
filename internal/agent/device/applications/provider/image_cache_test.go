package provider

import (
	"testing"

	"github.com/flightctl/flightctl/internal/agent/device/dependency"
	"github.com/stretchr/testify/require"
)

func TestNestedTargetCacheGetSet(t *testing.T) {
	require := require.New(t)
	cache := NewOCITargetCache()

	require.Equal(0, cache.Len())

	entry, found := cache.Get("myapp")
	require.False(found)
	require.Empty(entry.Name)

	imageBasedEntry := CacheEntry{
		Name: "image-app",
		Parent: dependency.OCIPullTarget{
			Type:      dependency.OCITypeImage,
			Reference: "quay.io/myapp:v1",
			Digest:    "sha256:abc123",
		},
		Children: []dependency.OCIPullTarget{
			{Type: dependency.OCITypeImage, Reference: "quay.io/redis:latest"},
			{Type: dependency.OCITypeImage, Reference: "quay.io/postgres:16"},
		},
	}
	cache.Set(imageBasedEntry)
	require.Equal(1, cache.Len())

	entry, found = cache.Get("image-app")
	require.True(found)
	require.Equal("image-app", entry.Name)
	require.Equal("sha256:abc123", entry.Parent.Digest)
	require.Len(entry.Children, 2)
}

func TestNestedTargetCacheGC(t *testing.T) {
	testCases := []struct {
		name           string
		setupCache     func(*OCITargetCache)
		activeNames    []string
		expectedLen    int
		expectedRemain []string
	}{
		{
			name: "GC removes unreferenced applications",
			setupCache: func(c *OCITargetCache) {
				c.Set(CacheEntry{
					Name:     "active1",
					Parent:   dependency.OCIPullTarget{Reference: "quay.io/app1:v1", Digest: "sha256:aaa"},
					Children: []dependency.OCIPullTarget{{Type: dependency.OCITypeImage, Reference: "quay.io/redis:latest"}},
				})
				c.Set(CacheEntry{
					Name:     "active2",
					Parent:   dependency.OCIPullTarget{Reference: "quay.io/app2:v1", Digest: "sha256:bbb"},
					Children: []dependency.OCIPullTarget{{Type: dependency.OCITypeImage, Reference: "quay.io/postgres:16"}},
				})
				c.Set(CacheEntry{
					Name:     "stale1",
					Parent:   dependency.OCIPullTarget{Reference: "quay.io/app3:v1", Digest: "sha256:ccc"},
					Children: []dependency.OCIPullTarget{{Type: dependency.OCITypeImage, Reference: "quay.io/nginx:alpine"}},
				})
			},
			activeNames:    []string{"active1", "active2"},
			expectedLen:    2,
			expectedRemain: []string{"active1", "active2"},
		},
		{
			name: "GC with empty active list clears all",
			setupCache: func(c *OCITargetCache) {
				c.Set(CacheEntry{
					Name:   "app1",
					Parent: dependency.OCIPullTarget{Reference: "quay.io/app1:v1", Digest: "sha256:aaa"},
				})
				c.Set(CacheEntry{
					Name:   "app2",
					Parent: dependency.OCIPullTarget{Reference: "quay.io/app2:v1", Digest: "sha256:bbb"},
				})
			},
			activeNames:    []string{},
			expectedLen:    0,
			expectedRemain: []string{},
		},
		{
			name: "GC preserves all active applications",
			setupCache: func(c *OCITargetCache) {
				c.Set(CacheEntry{
					Name:   "app1",
					Parent: dependency.OCIPullTarget{Reference: "quay.io/app1:v1", Digest: "sha256:aaa"},
				})
				c.Set(CacheEntry{
					Name:   "app2",
					Parent: dependency.OCIPullTarget{Reference: "quay.io/app2:v1", Digest: "sha256:bbb"},
				})
			},
			activeNames:    []string{"app1", "app2"},
			expectedLen:    2,
			expectedRemain: []string{"app1", "app2"},
		},
		{
			name: "GC on empty cache is safe",
			setupCache: func(c *OCITargetCache) {
				// empty cache
			},
			activeNames:    []string{"app1"},
			expectedLen:    0,
			expectedRemain: []string{},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			cache := NewOCITargetCache()
			tc.setupCache(cache)

			cache.GC(tc.activeNames)

			require.Equal(tc.expectedLen, cache.Len())
			for _, name := range tc.expectedRemain {
				_, found := cache.Get(name)
				require.True(found, "expected app %s to remain after GC", name)
			}
		})
	}
}

func TestNestedTargetCacheClear(t *testing.T) {
	require := require.New(t)
	cache := NewOCITargetCache()

	cache.Set(CacheEntry{
		Name:     "image-app1",
		Parent:   dependency.OCIPullTarget{Reference: "quay.io/app1:v1", Digest: "sha256:abc"},
		Children: []dependency.OCIPullTarget{{Type: dependency.OCITypeImage, Reference: "quay.io/redis:latest"}},
	})
	cache.Set(CacheEntry{
		Name:     "image-app2",
		Parent:   dependency.OCIPullTarget{Reference: "quay.io/app2:v1", Digest: "sha256:def"},
		Children: []dependency.OCIPullTarget{{Type: dependency.OCITypeImage, Reference: "quay.io/nginx:alpine"}},
	})
	require.Equal(2, cache.Len())

	cache.Clear()
	require.Equal(0, cache.Len())

	_, found := cache.Get("image-app1")
	require.False(found)
	_, found = cache.Get("image-app2")
	require.False(found)
}

func TestNestedTargetCacheOverwriteEntry(t *testing.T) {
	require := require.New(t)
	cache := NewOCITargetCache()

	initialEntry := CacheEntry{
		Name:     "myapp",
		Parent:   dependency.OCIPullTarget{Reference: "quay.io/app:v1", Digest: "sha256:old"},
		Children: []dependency.OCIPullTarget{{Type: dependency.OCITypeImage, Reference: "quay.io/redis:6"}},
	}
	cache.Set(initialEntry)

	entry, found := cache.Get("myapp")
	require.True(found)
	require.Equal("sha256:old", entry.Parent.Digest)
	require.Len(entry.Children, 1)

	updatedEntry := CacheEntry{
		Name:   "myapp",
		Parent: dependency.OCIPullTarget{Reference: "quay.io/app:v1", Digest: "sha256:new"},
		Children: []dependency.OCIPullTarget{
			{Type: dependency.OCITypeImage, Reference: "quay.io/redis:7"},
			{Type: dependency.OCITypeImage, Reference: "quay.io/postgres:16"},
		},
	}
	cache.Set(updatedEntry)

	entry, found = cache.Get("myapp")
	require.True(found)
	require.Equal("sha256:new", entry.Parent.Digest)
	require.Len(entry.Children, 2)
	require.Equal(1, cache.Len()) // still only one entry
}

func TestNestedTargetCacheInvalidation(t *testing.T) {
	require := require.New(t)
	cache := NewOCITargetCache()

	// Cache entry for image-based app with old digest
	cache.Set(CacheEntry{
		Name:     "myapp",
		Parent:   dependency.OCIPullTarget{Reference: "quay.io/app:v1", Digest: "sha256:old"},
		Children: []dependency.OCIPullTarget{{Type: dependency.OCITypeImage, Reference: "quay.io/redis:6"}},
	})

	entry, found := cache.Get("myapp")
	require.True(found)
	require.NotNil(entry.Parent)

	// Simulate parent image digest change (new version pulled)
	currentDigest := "sha256:new"
	require.NotEqual(entry.Parent.Digest, currentDigest, "should detect digest change for cache invalidation")

	// Update cache with new digest and children (simulating re-extraction after invalidation)
	cache.Set(CacheEntry{
		Name:   "myapp",
		Parent: dependency.OCIPullTarget{Reference: "quay.io/app:v1", Digest: "sha256:new"},
		Children: []dependency.OCIPullTarget{
			{Type: dependency.OCITypeImage, Reference: "quay.io/redis:7"},
			{Type: dependency.OCITypeImage, Reference: "quay.io/postgres:16"},
		},
	})

	entry, found = cache.Get("myapp")
	require.True(found)
	require.Equal("sha256:new", entry.Parent.Digest)
	require.Len(entry.Children, 2)
}
