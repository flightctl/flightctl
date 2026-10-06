package flightctldestination

import "net/http"

// organizationRoundTripper appends the org_id query parameter to every
// outgoing request. It must be applied after auth transports so the parameter
// is always present by the time the request reaches the server.
type organizationRoundTripper struct {
	orgID string
	base  http.RoundTripper
}

func (t *organizationRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	cloned := req.Clone(req.Context())
	query := cloned.URL.Query()
	query.Set("org_id", t.orgID)
	cloned.URL.RawQuery = query.Encode()
	return t.base.RoundTrip(cloned)
}
