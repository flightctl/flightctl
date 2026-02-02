package versioning

import (
	"net/http"
)

// API Version constants
const (
	V1Alpha1 = "v1alpha1"
	V1Beta1  = "v1beta1"
)

// API Header names
const (
	HeaderAPIVersion           = "Flightctl-API-Version"
	HeaderAPIVersionsSupported = "Flightctl-API-Versions-Supported"
	HeaderDeprecation          = "Deprecation"
)

// TransportOption is a functional option for configuring the versioning transport.
type TransportOption func(*transportOptions)

type transportOptions struct {
	apiVersion string
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

// versioningTransport wraps an http.RoundTripper to inject version headers.
type versioningTransport struct {
	base       http.RoundTripper
	apiVersion string
}

// UnwrapTransport returns the underlying base transport.
func (t *versioningTransport) UnwrapTransport() http.RoundTripper {
	return t.base
}

// NewTransport creates an http.RoundTripper that wraps the given base transport.
// It injects the API version header if missing (when apiVersion is set via options).
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
		base:       base,
		apiVersion: options.apiVersion,
	}
}

// RoundTrip implements http.RoundTripper.
func (t *versioningTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if t.apiVersion != "" && req.Header.Get(HeaderAPIVersion) == "" {
		clone := req.Clone(req.Context())
		clone.Header.Set(HeaderAPIVersion, t.apiVersion)
		req = clone
	}

	return t.base.RoundTrip(req)
}
