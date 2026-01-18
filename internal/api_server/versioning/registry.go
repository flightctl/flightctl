package versioning

import (
	"time"

	"github.com/flightctl/flightctl/internal/api/server"
)

// Registry provides version negotiation logic
type Registry struct {
	fallbackVersion Version
}

// NewRegistry creates a registry with the specified fallback version
// The fallback is only used when endpoint metadata is not available
func NewRegistry(fallbackVersion Version) *Registry {
	return &Registry{fallbackVersion: fallbackVersion}
}

// FallbackVersion returns the fallback version used when metadata is unavailable
func (r *Registry) FallbackVersion() Version {
	return r.fallbackVersion
}

// Negotiate determines the version to use based on request and endpoint metadata.
// When no version is requested, uses the first version from metadata (most preferred).
// Returns the negotiated version, list of supported versions, and error if not acceptable.
func (r *Registry) Negotiate(requested Version, metadata *server.EndpointMetadata) (Version, []Version, error) {
	supported := r.supportedVersions(metadata)

	// No version requested: use first from metadata (most preferred) or fallback
	if requested == "" {
		if len(supported) > 0 {
			return supported[0], supported, nil
		}
		return r.fallbackVersion, supported, nil
	}

	// Requested version: check if supported
	if r.isSupported(requested, supported) {
		return requested, supported, nil
	}

	return "", supported, ErrNotAcceptable
}

// DeprecationDate returns the deprecation date for a version on an endpoint, or nil
func (r *Registry) DeprecationDate(version Version, metadata *server.EndpointMetadata) *time.Time {
	if metadata == nil {
		return nil
	}
	for _, v := range metadata.Versions {
		if Version(v.Version) == version {
			return v.DeprecatedAt
		}
	}
	return nil
}

func (r *Registry) supportedVersions(metadata *server.EndpointMetadata) []Version {
	if metadata == nil {
		return nil
	}
	versions := make([]Version, 0, len(metadata.Versions))
	for _, v := range metadata.Versions {
		versions = append(versions, Version(v.Version))
	}
	return versions
}

func (r *Registry) isSupported(v Version, supported []Version) bool {
	for _, s := range supported {
		if s == v {
			return true
		}
	}
	return false
}
