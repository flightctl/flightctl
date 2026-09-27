package provider

import (
	"slices"
	"sync"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/device/dependency"
)

// CacheEntry stores an application's parent image and any nested OCI targets.
type CacheEntry struct {
	// Name is the application ID that owns this entry.
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

// OCITargetCache caches parent image state and child OCI targets.
type OCITargetCache struct {
	mu      sync.RWMutex
	entries map[string]CacheEntry
}

// NewOCITargetCache creates a new cache instance
func NewOCITargetCache() *OCITargetCache {
	return &OCITargetCache{
		entries: make(map[string]CacheEntry),
	}
}

// Get retrieves cached parent image state and nested targets for the application ID.
// Returns the entry and true if found, empty entry and false otherwise.
func (c *OCITargetCache) Get(name string) (CacheEntry, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	entry, found := c.entries[name]
	entry.Children = slices.Clone(entry.Children)
	return entry, found
}

// Set stores a cache entry.
func (c *OCITargetCache) Set(entry CacheEntry) {
	entry.Children = slices.Clone(entry.Children)
	c.mu.Lock()
	defer c.mu.Unlock()

	c.entries[entry.Name] = entry
}

// GC removes cache entries for application IDs not in the activeIDs list.
// This prevents unbounded cache growth as entities are added/removed.
func (c *OCITargetCache) GC(activeIDs []string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// build set for O(1) lookup
	active := make(map[string]struct{}, len(activeIDs))
	for _, id := range activeIDs {
		active[id] = struct{}{}
	}

	// remove entries not in active set
	for id := range c.entries {
		if _, isActive := active[id]; !isActive {
			delete(c.entries, id)
		}
	}
}

// Len returns the number of entries in the cache
func (c *OCITargetCache) Len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return len(c.entries)
}

// Clear removes all entries from the cache
func (c *OCITargetCache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.entries = make(map[string]CacheEntry)
}
