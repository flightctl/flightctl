package versioning

import (
	"context"
)

// Version represents an API version string
type Version string

const (
	V1Beta1 Version = "v1beta1"
	// V1 Version = "v1"  // Add when v1 is introduced
)

// Header names
const (
	HeaderAPIVersion           = "Flightctl-API-Version"
	HeaderAPIVersionsSupported = "Flightctl-API-Versions-Supported"
	HeaderDeprecation          = "Deprecation"
)

type versionCtxKey struct{}

// ContextWithVersion stores the negotiated version in context
func ContextWithVersion(ctx context.Context, v Version) context.Context {
	return context.WithValue(ctx, versionCtxKey{}, v)
}

// VersionFromContext retrieves the negotiated version from context
func VersionFromContext(ctx context.Context) (Version, bool) {
	v, ok := ctx.Value(versionCtxKey{}).(Version)
	return v, ok
}

// IsValid returns true if the version string is a known version
func (v Version) IsValid() bool {
	switch v {
	case V1Beta1:
		return true
	default:
		return false
	}
}
