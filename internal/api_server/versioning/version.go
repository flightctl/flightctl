package versioning

import (
	"context"
	"net/http"
	"strings"
)

// APIVersion represents a supported API version.
type APIVersion string

const (
	APIV1      APIVersion = "v1"
	APIV1Beta1 APIVersion = "v1beta1"
)

// APIVersionHeader is the HTTP header used to specify the API version.
const APIVersionHeader = "Flightctl-API-Version"

type ctxKey int

const apiVersionKey ctxKey = iota

// VersionRegistry validates and negotiates API versions.
type VersionRegistry interface {
	Negotiate(h http.Header) (APIVersion, error)
}

// DefaultRegistry is the default implementation of VersionRegistry.
type DefaultRegistry struct{}

// NewDefaultRegistry creates a new DefaultRegistry.
func NewDefaultRegistry() *DefaultRegistry {
	return &DefaultRegistry{}
}

// Negotiate implements VersionRegistry.
func (r *DefaultRegistry) Negotiate(h http.Header) (APIVersion, error) {
	requested := strings.ToLower(strings.TrimSpace(h.Get(APIVersionHeader)))
	if requested == "" {
		return APIV1Beta1, nil
	}

	switch APIVersion(requested) {
	case APIV1:
		return APIV1, nil
	case APIV1Beta1:
		return APIV1Beta1, nil
	default:
		return "", &UnsupportedVersionError{Version: requested}
	}
}

// UnsupportedVersionError is returned when an unsupported API version is requested.
type UnsupportedVersionError struct {
	Version string
}

func (e *UnsupportedVersionError) Error() string {
	return "unsupported API version: " + e.Version
}

// WithAPIVersion creates middleware that parses the API version header,
// validates it, and stores the negotiated version in the request context.
func WithAPIVersion(reg VersionRegistry) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			v, err := reg.Negotiate(r.Header)
			if err != nil {
				http.Error(w, "unsupported api version", http.StatusBadRequest)
				return
			}
			w.Header().Set(APIVersionHeader, string(v))
			ctx := context.WithValue(r.Context(), apiVersionKey, v)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// APIVersionFromContext extracts the API version from the context.
// Returns APIV1Beta1 (default) if not present.
func APIVersionFromContext(ctx context.Context) APIVersion {
	if v, ok := ctx.Value(apiVersionKey).(APIVersion); ok {
		return v
	}
	return APIV1Beta1
}
