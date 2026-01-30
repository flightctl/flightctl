package provider

import (
	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/device/dependency"
)

// CacheEntry represents cached nested targets extracted from image-based applications.
type CacheEntry struct {
	Name  string
	Owner v1beta1.Username
	// Parent is the parent image from which child OCI targets were extracted.
	Parent dependency.OCIPullTarget
	// Children are OCI targets extracted from parent image.
	Children []dependency.OCIPullTarget
}

func (e *CacheEntry) IsValid(ref string, digest string) bool {
	if e.Parent.Reference != ref {
		return false
	}
	if digest != "" && e.Parent.Digest != digest {
		return false
	}
	return true
}

// OCITargetCache caches child OCI targets extracted from parents.
type OCITargetCache struct {
	entries map[string]CacheEntry
}

// NewOCITargetCache creates a new cache instance
func NewOCITargetCache() *OCITargetCache {
	return &OCITargetCache{
		entries: make(map[string]CacheEntry),
	}
}

// Get retrieves cached nested targets for the given entity name.
// Returns the entry and true if found, empty entry and false otherwise.
func (c *OCITargetCache) Get(name string) (CacheEntry, bool) {
	entry, found := c.entries[name]
	return entry, found
}

// Set stores a cache entry.
func (c *OCITargetCache) Set(entry CacheEntry) {
	c.entries[entry.Name] = entry
}

// GC removes cache entries for entities not in the activeNames list.
// This prevents unbounded cache growth as entities are added/removed.
func (c *OCITargetCache) GC(activeNames []string) {
	// build set of active names for O(1) lookup
	active := make(map[string]struct{}, len(activeNames))
	for _, name := range activeNames {
		active[name] = struct{}{}
	}

	// remove entries not in active set
	for name := range c.entries {
		if _, isActive := active[name]; !isActive {
			delete(c.entries, name)
		}
	}
}

// Len returns the number of entries in the cache
func (c *OCITargetCache) Len() int {
	return len(c.entries)
}

// Clear removes all entries from the cache
func (c *OCITargetCache) Clear() {
	c.entries = make(map[string]CacheEntry)
}
