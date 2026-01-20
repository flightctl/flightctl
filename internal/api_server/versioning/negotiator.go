package versioning

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	apiversioning "github.com/flightctl/flightctl/api/versioning"
	"github.com/flightctl/flightctl/internal/api/server"
)

// ErrNotAcceptable indicates the requested version is not supported for the endpoint.
var ErrNotAcceptable = errors.New("requested API version not acceptable")

// Negotiator provides version negotiation logic.
type Negotiator struct {
	fallbackVersion Version
}

// NewNegotiator creates a negotiator with the specified fallback version.
func NewNegotiator(fallbackVersion Version) *Negotiator {
	return &Negotiator{fallbackVersion: fallbackVersion}
}

// FallbackVersion returns the fallback version.
func (n *Negotiator) FallbackVersion() Version {
	return n.fallbackVersion
}

// NegotiateMiddleware is HTTP middleware that performs version negotiation.
func (n *Negotiator) NegotiateMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		requested := Version(req.Header.Get(apiversioning.HeaderAPIVersion))
		metadata := server.GetEndpointMetadata(req)

		negotiated, deprecatedAt, err := n.negotiate(requested, metadata)
		if err != nil {
			n.writeError(w, metadata)
			return
		}

		n.setHeaders(w, negotiated, deprecatedAt)

		next.ServeHTTP(w, req.WithContext(ContextWithVersion(req.Context(), negotiated)))
	})
}

// negotiate determines the version to use. Returns negotiated version, deprecation date, and error.
func (n *Negotiator) negotiate(requested Version, metadata *server.EndpointMetadata) (Version, *time.Time, error) {
	// No metadata or empty versions: accept only fallback version.
	if metadata == nil || len(metadata.Versions) == 0 {
		if requested == "" || requested == n.fallbackVersion {
			return n.fallbackVersion, nil, nil
		}
		return "", nil, ErrNotAcceptable
	}

	// No version requested: use first (most preferred, versions are ordered).
	if requested == "" {
		v := metadata.Versions[0]
		return Version(v.Version), v.DeprecatedAt, nil
	}

	// Find requested version in supported list.
	for _, v := range metadata.Versions {
		if Version(v.Version) == requested {
			return requested, v.DeprecatedAt, nil
		}
	}

	return "", nil, ErrNotAcceptable
}

// setHeaders sets version-related response headers for successful negotiation.
// Note: supported versions header is intentionally NOT set here (only on 406).
func (n *Negotiator) setHeaders(w http.ResponseWriter, negotiated Version, deprecatedAt *time.Time) {
	addVary(w.Header(), apiversioning.HeaderAPIVersion)

	// Negotiated version header.
	w.Header().Set(apiversioning.HeaderAPIVersion, string(negotiated))

	// Deprecation header format must be "@<epoch-seconds>".
	if deprecatedAt != nil {
		w.Header().Set(apiversioning.HeaderDeprecation, "@"+strconv.FormatInt(deprecatedAt.UTC().Unix(), 10))
	}
}

// writeError writes a 406 Not Acceptable response.
// Supported versions header is returned ONLY here (on 406).
func (n *Negotiator) writeError(w http.ResponseWriter, metadata *server.EndpointMetadata) {
	addVary(w.Header(), apiversioning.HeaderAPIVersion)

	supportedStr := supportedVersionsString(metadata, n.fallbackVersion)
	if supportedStr != "" {
		w.Header().Set(apiversioning.HeaderAPIVersionsSupported, supportedStr)
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusNotAcceptable)

	msg := "Requested API version is not supported for this endpoint."
	if supportedStr != "" {
		msg += " Supported versions: " + supportedStr
	}

	_ = json.NewEncoder(w).Encode(api.Status{
		Code:    http.StatusNotAcceptable,
		Message: msg,
	})
}

func supportedVersionsString(metadata *server.EndpointMetadata, fallback Version) string {
	// If endpoint provides versions, list them.
	if metadata != nil && len(metadata.Versions) > 0 {
		var b strings.Builder
		for i, v := range metadata.Versions {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(v.Version)
		}
		return b.String()
	}

	// Fixed-contract endpoints: only fallback is supported.
	if fallback != "" {
		return string(fallback)
	}
	return ""
}

// addVary adds a Vary token if it's not already present.
func addVary(h http.Header, token string) {
	if token == "" {
		return
	}

	for _, v := range h.Values("Vary") {
		for _, part := range strings.Split(v, ",") {
			if strings.EqualFold(strings.TrimSpace(part), token) {
				return
			}
		}
	}
	h.Add("Vary", token)
}
