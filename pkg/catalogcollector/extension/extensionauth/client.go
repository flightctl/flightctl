package extensionauth

import "net/http"

// HTTPClient is implemented by extensions that authenticate outgoing HTTP
// requests.
type HTTPClient interface {
	RoundTripper(base http.RoundTripper) (http.RoundTripper, error)
}
