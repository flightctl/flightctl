package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// TagLister fetches available tags for a container image repository.
type TagLister interface {
	ListTags(ctx context.Context, registry, repo string) ([]string, error)
}

// RegistryClient queries the Docker v2 HTTP API for image tags.
// It handles the bearer-token authentication flow used by registries
// such as registry.access.redhat.com.
type RegistryClient struct {
	HTTPClient *http.Client
}

// NewRegistryClient creates a RegistryClient with sensible defaults.
func NewRegistryClient() *RegistryClient {
	return &RegistryClient{
		HTTPClient: &http.Client{Timeout: 30 * time.Second},
	}
}

// tagsResponse is the Docker v2 tag list response.
type tagsResponse struct {
	Tags []string `json:"tags"`
}

// tokenResponse is the Docker v2 auth token response.
type tokenResponse struct {
	Token string `json:"token"`
}

// bearerParamRe extracts key="value" pairs from WWW-Authenticate headers.
var bearerParamRe = regexp.MustCompile(`(\w+)="([^"]*)"`)

// Pagination safety limits to prevent unbounded traversal or memory exhaustion.
const (
	maxPaginationPages = 100
	maxTotalTags       = 10000
)

// ListTags returns all tags for the given registry and repository.
// For example, registry="registry.access.redhat.com", repo="ubi9/ubi-micro".
// It follows pagination Link headers to collect the complete tag list.
// Pagination is bounded to at most maxPaginationPages pages and
// maxTotalTags total tags to prevent runaway requests or memory exhaustion.
func (r *RegistryClient) ListTags(ctx context.Context, registry, repo string) ([]string, error) {
	firstURL := fmt.Sprintf("https://%s/v2/%s/tags/list", registry, repo)

	// Obtain a bearer token if needed (probe the first URL).
	token, err := r.authenticate(ctx, firstURL, registry)
	if err != nil {
		return nil, err
	}

	var allTags []string
	nextURL := firstURL
	pages := 0

	for nextURL != "" {
		pages++
		if pages > maxPaginationPages {
			return nil, fmt.Errorf("pagination limit exceeded: more than %d pages for %s/%s", maxPaginationPages, registry, repo)
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, nextURL, nil)
		if err != nil {
			return nil, fmt.Errorf("creating request: %w", err)
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}

		resp, err := r.HTTPClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("fetching tags from %s: %w", nextURL, err)
		}

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
			resp.Body.Close()
			return nil, fmt.Errorf("unexpected status %d from %s: %s", resp.StatusCode, nextURL, body)
		}

		var page tagsResponse
		if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
			resp.Body.Close()
			return nil, fmt.Errorf("decoding tags response: %w", err)
		}
		resp.Body.Close()

		allTags = append(allTags, page.Tags...)

		if len(allTags) > maxTotalTags {
			return nil, fmt.Errorf("tag count limit exceeded: more than %d tags for %s/%s", maxTotalTags, registry, repo)
		}

		nextURL, err = resolveNextLink(resp.Header.Get("Link"), resp.Request.URL, registry)
		if err != nil {
			return nil, err
		}
	}

	return allTags, nil
}

// authenticate probes the given URL and, if the registry returns 401,
// fetches a bearer token. Returns "" if no auth is required.
func (r *RegistryClient) authenticate(ctx context.Context, url, registry string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("creating request: %w", err)
	}

	resp, err := r.HTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("probing %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		token, err := r.fetchBearerToken(ctx, resp.Header.Get("WWW-Authenticate"))
		if err != nil {
			return "", fmt.Errorf("authenticating to %s: %w", registry, err)
		}
		return token, nil
	}
	return "", nil
}

// resolveNextLink extracts the URL from a Link header with rel="next",
// resolves it against the current request URL (handling relative paths),
// and validates that the result uses HTTPS and targets the original
// registry host to prevent credential leakage.
// Returns "" when no next link is present.
func resolveNextLink(header string, requestURL *url.URL, registry string) (string, error) {
	raw := parseLinkNext(header)
	if raw == "" {
		return "", nil
	}

	ref, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("parsing Link next URL %q: %w", raw, err)
	}

	resolved := requestURL.ResolveReference(ref)

	if resolved.Scheme != "https" {
		return "", fmt.Errorf("pagination link %q uses scheme %q, expected https", resolved.String(), resolved.Scheme)
	}
	if resolved.Host != registry {
		return "", fmt.Errorf("pagination link %q targets host %q, expected %q", resolved.String(), resolved.Host, registry)
	}

	return resolved.String(), nil
}

// parseLinkNext extracts the raw URL from a Link header with rel="next".
// Returns "" if no next link is present.
func parseLinkNext(header string) string {
	if header == "" {
		return ""
	}
	for _, part := range strings.Split(header, ",") {
		part = strings.TrimSpace(part)
		if !strings.Contains(part, `rel="next"`) {
			continue
		}
		start := strings.Index(part, "<")
		end := strings.Index(part, ">")
		if start >= 0 && end > start {
			return part[start+1 : end]
		}
	}
	return ""
}

// fetchBearerToken extracts the realm, service, and scope from a
// WWW-Authenticate header and requests a bearer token.
func (r *RegistryClient) fetchBearerToken(ctx context.Context, wwwAuth string) (string, error) {
	if !strings.HasPrefix(wwwAuth, "Bearer ") {
		return "", fmt.Errorf("unsupported auth scheme: %s", wwwAuth)
	}

	params := make(map[string]string)
	for _, m := range bearerParamRe.FindAllStringSubmatch(wwwAuth, -1) {
		params[m[1]] = m[2]
	}

	realm, ok := params["realm"]
	if !ok {
		return "", fmt.Errorf("missing realm in WWW-Authenticate header")
	}

	authURL, err := url.Parse(realm)
	if err != nil {
		return "", fmt.Errorf("parsing realm URL %q: %w", realm, err)
	}
	q := authURL.Query()
	if service, ok := params["service"]; ok {
		q.Set("service", service)
	}
	if scope, ok := params["scope"]; ok {
		q.Set("scope", scope)
	}
	authURL.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, authURL.String(), nil)
	if err != nil {
		return "", fmt.Errorf("creating auth request: %w", err)
	}

	resp, err := r.HTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("requesting token from %s: %w", realm, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("auth endpoint returned %d", resp.StatusCode)
	}

	var tok tokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tok); err != nil {
		return "", fmt.Errorf("decoding token response: %w", err)
	}

	return tok.Token, nil
}
