package validation

import (
	"fmt"
	"strings"
)

// ValidateCatalogItemVersion reports whether v is a version string accepted
// for CatalogItem versions.
//
// The accepted grammar is intentionally the one Flight Control has always
// enforced for CatalogItem versions: a numeric core of two or three
// components ("1.0" and "1.0.0" are both accepted), an optional non-empty
// pre-release suffix introduced by "-", and optional build metadata
// introduced by "+". A leading "v" is rejected so that version strings are
// never silently rewritten.
//
// Callers that need to order version strings must not reimplement this
// grammar; collectors and other producers should validate with this function
// and derive their own comparison key from the validated value.
func ValidateCatalogItemVersion(v string) error {
	if strings.HasPrefix(v, "v") {
		return fmt.Errorf("version must not have 'v' prefix; use semver format (e.g., 1.0.0)")
	}

	// Handle build metadata (+build.123)
	v = strings.SplitN(v, "+", 2)[0]

	// Basic semver pattern: MAJOR.MINOR.PATCH with optional pre-release
	// Examples: 1.0.0, 1.2.3-alpha, 1.2.3-rc.1
	parts := strings.SplitN(v, "-", 2)
	if len(parts) == 2 && parts[1] == "" {
		return fmt.Errorf("pre-release identifier must not be empty (trailing hyphen)")
	}
	coreParts := strings.Split(parts[0], ".")

	if len(coreParts) < 2 || len(coreParts) > 3 {
		return fmt.Errorf("must be valid semver (e.g., 1.0.0, 2.1.0-rc1)")
	}

	for i, part := range coreParts {
		if part == "" {
			return fmt.Errorf("must be valid semver (e.g., 1.0.0, 2.1.0-rc1)")
		}
		// Check that each part is numeric
		for _, c := range part {
			if c < '0' || c > '9' {
				return fmt.Errorf("version component %d must be numeric", i+1)
			}
		}
	}

	return nil
}
