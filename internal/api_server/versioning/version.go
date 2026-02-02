package versioning

import (
	"context"

	apiversioning "github.com/flightctl/flightctl/api/versioning"
)

// Version represents an API version string
type Version string

const (
	V1Alpha1 Version = Version(apiversioning.V1Alpha1)
	V1Beta1  Version = Version(apiversioning.V1Beta1)
	// V1 Version = "v1"  // Add when v1 is introduced
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
	case V1Alpha1, V1Beta1:
		return true
	default:
		return false
	}
}
