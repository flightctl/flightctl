package flightctldestination

import (
	"net/http"
	"net/url"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// roundTripFunc adapts a function to the http.RoundTripper interface so tests
// can capture the request without making real network calls.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func newOrgTransport(orgID string) (*organizationRoundTripper, chan *http.Request) {
	received := make(chan *http.Request, 1)
	base := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		received <- req
		return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}, nil
	})
	return &organizationRoundTripper{orgID: orgID, base: base}, received
}

func mustRequest(t *testing.T, rawURL string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, rawURL, http.NoBody)
	require.NoError(t, err)
	return req
}

func TestOrgTransport_AddsOrgID(t *testing.T) {
	tr, received := newOrgTransport("aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee")
	req := mustRequest(t, "https://api.example.com/catalogs")

	_, err := tr.RoundTrip(req)
	require.NoError(t, err)

	sent := <-received
	require.Equal(t, "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee", sent.URL.Query().Get("org_id"))
}

func TestOrgTransport_PreservesExistingParams(t *testing.T) {
	tr, received := newOrgTransport("aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee")
	req := mustRequest(t, "https://api.example.com/catalogs?limit=10&labelSelector=env%3Dprod")

	_, err := tr.RoundTrip(req)
	require.NoError(t, err)

	sent := <-received
	q := sent.URL.Query()
	require.Equal(t, "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee", q.Get("org_id"))
	require.Equal(t, "10", q.Get("limit"))
	require.Equal(t, "env=prod", q.Get("labelSelector"))
}

func TestOrgTransport_ReplacesExistingOrgID(t *testing.T) {
	tr, received := newOrgTransport("aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee")
	req := mustRequest(t, "https://api.example.com/catalogs?org_id=11111111-1111-1111-1111-111111111111")

	_, err := tr.RoundTrip(req)
	require.NoError(t, err)

	sent := <-received
	require.Equal(t, "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee", sent.URL.Query().Get("org_id"),
		"configured org_id must overwrite pre-existing value")
}

func TestOrgTransport_DoesNotMutateOriginalRequest(t *testing.T) {
	tr, _ := newOrgTransport("aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee")
	original := mustRequest(t, "https://api.example.com/catalogs?limit=5")
	originalRawQuery := original.URL.RawQuery

	_, err := tr.RoundTrip(original)
	require.NoError(t, err)

	require.Equal(t, originalRawQuery, original.URL.RawQuery,
		"RoundTrip must not mutate the original request URL")
}

func TestOrgTransport_Concurrent(t *testing.T) {
	const workers = 50
	orgID := "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"

	var (
		mu     sync.Mutex
		errors []string
	)

	base := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		got := req.URL.Query().Get("org_id")
		if got != orgID {
			mu.Lock()
			errors = append(errors, "got org_id="+got)
			mu.Unlock()
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       http.NoBody,
		}, nil
	})
	tr := &organizationRoundTripper{orgID: orgID, base: base}

	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			rawURL := "https://api.example.com/catalogs"
			if i%2 == 0 {
				rawURL += "?limit=10"
			}
			req, _ := http.NewRequest(http.MethodGet, rawURL, http.NoBody)
			req.URL = &url.URL{
				Scheme:   "https",
				Host:     "api.example.com",
				Path:     "/catalogs",
				RawQuery: req.URL.RawQuery,
			}
			_, _ = tr.RoundTrip(req)
		}(i)
	}
	wg.Wait()

	require.Empty(t, errors, "concurrent RoundTrip calls must always set the correct org_id")
}
