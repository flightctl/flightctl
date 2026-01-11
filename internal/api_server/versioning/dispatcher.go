package versioning

import (
	"net/http"
)

// VersionDispatcher routes requests to version-specific handlers based on
// the API version stored in the request context by WithAPIVersion middleware.
type VersionDispatcher struct {
	V1      http.Handler
	V1Beta1 http.Handler
}

// ServeHTTP implements http.Handler.
func (d VersionDispatcher) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch APIVersionFromContext(r.Context()) {
	case APIV1:
		d.V1.ServeHTTP(w, r)
	default:
		d.V1Beta1.ServeHTTP(w, r)
	}
}
