package versioning

import (
	"net/http"
	"strconv"
	"strings"
	"time"
)

// API Version constants
const (
	V1Beta1 = "v1beta1"
)

// API Header names
const (
	HeaderAPIVersion           = "Flightctl-API-Version"
	HeaderAPIVersionsSupported = "Flightctl-API-Versions-Supported"
	HeaderDeprecation          = "Deprecation"
)

// Printf is a function type for printing formatted deprecation messages.
type Printf func(format string, args ...any)

// parseDeprecationTimestamp parses a Unix timestamp from the Deprecation header
// format "@<timestamp>" (e.g., "@1688169599"). Returns the parsed time and true
// if successful, or zero time and false if the format is invalid.
func parseDeprecationTimestamp(value string) (time.Time, bool) {
	if !strings.HasPrefix(value, "@") {
		return time.Time{}, false
	}
	ts, err := strconv.ParseInt(value[1:], 10, 64)
	if err != nil {
		return time.Time{}, false
	}
	return time.Unix(ts, 0), true
}

// TransportOption is a functional option for configuring the versioning transport.
type TransportOption func(*transportOptions)

type transportOptions struct {
	apiVersion        string
	deprecationPrintf Printf // nil = no deprecation reporting
}

// WithAPIVersion sets the API version header value.
func WithAPIVersion(version string) TransportOption {
	return func(o *transportOptions) {
		o.apiVersion = version
	}
}

// WithAPIV1Beta1 is a convenience option that sets API version to v1beta1.
func WithAPIV1Beta1() TransportOption {
	return WithAPIVersion(V1Beta1)
}

// WithDeprecationPrintf enables deprecation reporting via the given function.
// If not set, deprecation headers are ignored (effectively disabled).
func WithDeprecationPrintf(fn Printf) TransportOption {
	return func(o *transportOptions) {
		o.deprecationPrintf = fn
	}
}

// versioningTransport wraps an http.RoundTripper to inject version headers
// and optionally report deprecation warnings.
type versioningTransport struct {
	base              http.RoundTripper
	apiVersion        string
	deprecationPrintf Printf
}

// UnwrapTransport returns the underlying base transport.
func (t *versioningTransport) UnwrapTransport() http.RoundTripper {
	return t.base
}

// reportDeprecationIfNeeded checks the response for a Deprecation header and reports
// a warning if the deprecation date has passed.
func (t *versioningTransport) reportDeprecationIfNeeded(req *http.Request, resp *http.Response) {
	if t.deprecationPrintf == nil {
		return
	}
	depValue := resp.Header.Get(HeaderDeprecation)
	if depValue == "" {
		return
	}
	depTime, ok := parseDeprecationTimestamp(depValue)
	if !ok || !time.Now().UTC().After(depTime) {
		return
	}
	version := resp.Header.Get(HeaderAPIVersion)
	if version == "" {
		version = "the version"
	}
	t.deprecationPrintf("Deprecated API: %s %s: %s will be removed", req.Method, req.URL.String(), version)
}

// NewTransport creates an http.RoundTripper that wraps the given base transport.
// It injects the API version header if missing (when apiVersion is set via options)
// and optionally reports deprecation warnings using the configured Printf function.
// The API version must be explicitly provided via WithAPIVersion or WithAPIV1Beta1.
func NewTransport(base http.RoundTripper, opts ...TransportOption) http.RoundTripper {
	options := &transportOptions{}
	for _, opt := range opts {
		opt(options)
	}

	if base == nil {
		base = http.DefaultTransport
	}

	return &versioningTransport{
		base:              base,
		apiVersion:        options.apiVersion,
		deprecationPrintf: options.deprecationPrintf,
	}
}

// RoundTrip implements http.RoundTripper.
func (t *versioningTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if t.apiVersion != "" && req.Header.Get(HeaderAPIVersion) == "" {
		clone := req.Clone(req.Context())
		clone.Header.Set(HeaderAPIVersion, t.apiVersion)
		req = clone
	}

	resp, err := t.base.RoundTrip(req)
	if err != nil {
		return resp, err
	}

	t.reportDeprecationIfNeeded(req, resp)

	return resp, nil
}
