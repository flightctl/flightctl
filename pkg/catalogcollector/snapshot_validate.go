package catalogcollector

import (
	"errors"
	"fmt"
)

// ValidateSnapshot validates a CatalogSnapshot before destination reconciliation.
func ValidateSnapshot(snapshot *CatalogSnapshot) error {
	if snapshot == nil {
		return fmt.Errorf("snapshot is nil")
	}
	if snapshot.Revision == "" {
		return fmt.Errorf("snapshot revision is empty")
	}

	var errs []error
	catalogNames := make(map[string]bool)

	for i, catalog := range snapshot.Catalogs {
		if validationErrors := catalog.Validate(); len(validationErrors) > 0 {
			errs = append(errs, fmt.Errorf("catalog[%d]: %w", i, errors.Join(validationErrors...)))
		}
		name := ""
		if catalog.Metadata.Name != nil {
			name = *catalog.Metadata.Name
		}
		if name == "" {
			errs = append(errs, fmt.Errorf("catalog[%d]: name is required", i))
		} else if catalogNames[name] {
			errs = append(errs, fmt.Errorf("catalog[%d]: duplicate name %q", i, name))
		} else {
			catalogNames[name] = true
		}
	}

	itemIdentities := make(map[string]bool)
	for i, item := range snapshot.CatalogItems {
		if validationErrors := item.Validate(); len(validationErrors) > 0 {
			errs = append(errs, fmt.Errorf("catalogItem[%d]: %w", i, errors.Join(validationErrors...)))
		}
		catalog := item.Metadata.Catalog
		name := ""
		if item.Metadata.Name != nil {
			name = *item.Metadata.Name
		}
		if catalog == "" || name == "" {
			errs = append(errs, fmt.Errorf("catalogItem[%d]: catalog and name identity is required", i))
		} else {
			identity := catalog + "/" + name
			if itemIdentities[identity] {
				errs = append(errs, fmt.Errorf("catalogItem[%d]: duplicate identity %q", i, identity))
			} else {
				itemIdentities[identity] = true
			}
		}
	}

	return errors.Join(errs...)
}
