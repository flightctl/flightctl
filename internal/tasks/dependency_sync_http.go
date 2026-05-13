package tasks

import (
	"context"
	"fmt"
	"net/http"

	"github.com/flightctl/flightctl/internal/domain"
)

// httpConditionalGetFunc is the injectable function type for testing.
// Returns: fingerprint (ETag or Last-Modified value), HTTP status code, error.
// Empty fingerprint with status 200 means the endpoint doesn't support
// conditional requests (no ETag or Last-Modified in response).
type httpConditionalGetFunc func(ctx context.Context, repoURL string,
	httpSpec domain.HttpRepoSpec, storedFingerprint string) (fingerprint string, statusCode int, err error)

// httpConditionalGet sends a conditional GET to the given URL using stored
// fingerprint for If-None-Match/If-Modified-Since headers.
func httpConditionalGet(_ context.Context, repoURL string,
	httpSpec domain.HttpRepoSpec, storedFingerprint string) (string, int, error) {
	req, err := http.NewRequest("GET", repoURL, nil)
	if err != nil {
		return "", 0, fmt.Errorf("creating request: %w", err)
	}

	req, tlsConfig, err := buildHttpRepoRequestAuth(httpSpec, req)
	if err != nil {
		return "", 0, fmt.Errorf("building request auth: %w", err)
	}

	if storedFingerprint != "" {
		req.Header.Set("If-None-Match", storedFingerprint)
		req.Header.Set("If-Modified-Since", storedFingerprint)
	}

	client := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: tlsConfig,
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", 0, fmt.Errorf("sending request: %w", err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusNotModified:
		return "", http.StatusNotModified, nil
	case http.StatusOK:
		fingerprint := resp.Header.Get("ETag")
		if fingerprint == "" {
			fingerprint = resp.Header.Get("Last-Modified")
		}
		return fingerprint, http.StatusOK, nil
	default:
		return "", resp.StatusCode, fmt.Errorf("unexpected status code %d from %s", resp.StatusCode, repoURL)
	}
}
