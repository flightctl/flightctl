// Package catalognameprocessor provides a catalog name mapping processor for
// the flightctl catalog collector.
//
// It renames Catalog resources in a snapshot according to a configured mapping,
// and updates the corresponding CatalogItem.metadata.catalog field so items
// remain attached to the correctly named catalog.
package catalognameprocessor

import (
	"fmt"
	"sort"
)

// Config holds the provider-specific configuration for the catalog name
// mapping processor. It implements [config.Validator].
type Config struct {
	// Mappings is a map from existing Catalog names to replacement names.
	// All keys and values must be non-empty strings. Keys must be unique.
	// Values must be unique (two source names may not map to the same target).
	Mappings map[string]string `json:"mappings"`
}

// Validate checks that the mapping configuration is complete and consistent.
func (c *Config) Validate() error {
	if len(c.Mappings) == 0 {
		return fmt.Errorf("mappings must not be empty")
	}

	// Stable traversal makes errors deterministic when multiple fields
	// contain invalid mappings.
	keys := make([]string, 0, len(c.Mappings))
	for src := range c.Mappings {
		keys = append(keys, src)
	}
	sort.Strings(keys)

	targets := make(map[string]string, len(c.Mappings))
	for _, src := range keys {
		dst := c.Mappings[src]
		if src == "" {
			return fmt.Errorf("mapping source name must not be empty")
		}
		if dst == "" {
			return fmt.Errorf("mapping destination for %q must not be empty", src)
		}
		if prev, conflict := targets[dst]; conflict {
			return fmt.Errorf("mappings %q and %q both target the same name %q", prev, src, dst)
		}
		targets[dst] = src
	}
	return nil
}
