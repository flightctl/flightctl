package versioning

import (
	"net/http"
	"strings"
	"time"

	"github.com/flightctl/flightctl/internal/api/server"
)

// Middleware creates a middleware that negotiates API version per-resource
func Middleware(registry *Registry) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requested := Version(r.Header.Get(HeaderAPIVersion))

			metadata, _ := server.GetEndpointMetadata(r)
			negotiated, supported, err := registry.Negotiate(requested, metadata)

			// Always set response headers
			setResponseHeaders(w, negotiated, supported, registry)

			// Set deprecation header if applicable
			if deprecatedAt := registry.DeprecationDate(negotiated, metadata); deprecatedAt != nil {
				w.Header().Set(HeaderDeprecation, formatRFC8594Date(deprecatedAt))
			}

			// Return 406 if version not acceptable
			if err != nil {
				writeNotAcceptable(w, supported, registry.FallbackVersion())
				return
			}

			// Store version in context and continue
			ctx := ContextWithVersion(r.Context(), negotiated)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func setResponseHeaders(w http.ResponseWriter, negotiated Version, supported []Version, registry *Registry) {
	// Vary header for cache differentiation
	w.Header().Add("Vary", HeaderAPIVersion)

	// Version used
	if negotiated != "" {
		w.Header().Set(HeaderAPIVersion, string(negotiated))
	} else {
		w.Header().Set(HeaderAPIVersion, string(registry.FallbackVersion()))
	}

	// Supported versions
	if len(supported) > 0 {
		strs := make([]string, len(supported))
		for i, v := range supported {
			strs[i] = string(v)
		}
		w.Header().Set(HeaderAPIVersionsSupported, strings.Join(strs, ", "))
	}
}

func formatRFC8594Date(t *time.Time) string {
	return t.UTC().Format(time.RFC1123)
}
