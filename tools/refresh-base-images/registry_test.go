package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
)

func TestRegistryClientListTags(t *testing.T) {
	t.Run("When registry returns tags directly it should parse them", func(t *testing.T) {
		tags := []string{"9.5-1700000000", "9.7-1762965531", "latest"}
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v2/ubi9/ubi-micro/tags/list" {
				resp := tagsResponse{Tags: tags}
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(resp)
				return
			}
			http.NotFound(w, r)
		}))
		defer server.Close()

		client := &RegistryClient{HTTPClient: server.Client()}
		// Extract host from server URL (strip https://)
		host := strings.TrimPrefix(server.URL, "https://")

		got, err := client.ListTags(context.Background(), host, "ubi9/ubi-micro")
		if err != nil {
			t.Fatalf("ListTags() error = %v", err)
		}
		if len(got) != len(tags) {
			t.Fatalf("got %d tags, want %d", len(got), len(tags))
		}
		for i, tag := range tags {
			if got[i] != tag {
				t.Errorf("tag[%d] = %q, want %q", i, got[i], tag)
			}
		}
	})

	t.Run("When registry requires bearer auth it should fetch token and retry", func(t *testing.T) {
		tags := []string{"10.1-1769518576"}
		const testToken = "test-bearer-token-12345"
		var authenticated atomic.Bool

		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.URL.Path == "/v2/ubi10/ubi-micro/tags/list":
				auth := r.Header.Get("Authorization")
				if auth == "Bearer "+testToken {
					authenticated.Store(true)
					resp := tagsResponse{Tags: tags}
					w.Header().Set("Content-Type", "application/json")
					json.NewEncoder(w).Encode(resp)
					return
				}
				// Return 401 with WWW-Authenticate pointing to the server's own URL.
				w.Header().Set("WWW-Authenticate",
					fmt.Sprintf(`Bearer realm="https://%s/v2/auth",service="test-registry",scope="repository:ubi10/ubi-micro:pull"`,
						r.Host))
				w.WriteHeader(http.StatusUnauthorized)

			case r.URL.Path == "/v2/auth":
				resp := tokenResponse{Token: testToken}
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(resp)

			default:
				http.NotFound(w, r)
			}
		}))
		defer server.Close()

		client := &RegistryClient{HTTPClient: server.Client()}
		host := strings.TrimPrefix(server.URL, "https://")

		got, err := client.ListTags(context.Background(), host, "ubi10/ubi-micro")
		if err != nil {
			t.Fatalf("ListTags() error = %v", err)
		}
		if !authenticated.Load() {
			t.Error("expected bearer auth flow to complete")
		}
		if len(got) != 1 || got[0] != "10.1-1769518576" {
			t.Errorf("got tags %v, want [10.1-1769518576]", got)
		}
	})

	t.Run("When registry returns 500 it should return an error", func(t *testing.T) {
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprint(w, "internal error")
		}))
		defer server.Close()

		client := &RegistryClient{HTTPClient: server.Client()}
		host := strings.TrimPrefix(server.URL, "https://")

		_, err := client.ListTags(context.Background(), host, "ubi9/ubi-micro")
		if err == nil {
			t.Error("expected error for 500 response")
		}
	})

	t.Run("When registry returns paginated results with relative Link it should resolve and follow", func(t *testing.T) {
		page1Tags := []string{"9.5-1700000000", "9.6-1730000000"}
		page2Tags := []string{"9.7-1762965531", "latest"}

		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.URL.Path == "/v2/ubi9/ubi-micro/tags/list" && r.URL.Query().Get("last") == "":
				resp := tagsResponse{Tags: page1Tags}
				w.Header().Set("Content-Type", "application/json")
				// Return a relative Link header (no scheme/host).
				w.Header().Set("Link", `</v2/ubi9/ubi-micro/tags/list?last=9.6-1730000000>; rel="next"`)
				json.NewEncoder(w).Encode(resp)
			case r.URL.Path == "/v2/ubi9/ubi-micro/tags/list" && r.URL.Query().Get("last") == "9.6-1730000000":
				resp := tagsResponse{Tags: page2Tags}
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(resp)
			default:
				http.NotFound(w, r)
			}
		}))
		defer server.Close()

		client := &RegistryClient{HTTPClient: server.Client()}
		host := strings.TrimPrefix(server.URL, "https://")

		got, err := client.ListTags(context.Background(), host, "ubi9/ubi-micro")
		if err != nil {
			t.Fatalf("ListTags() error = %v", err)
		}
		wantAll := append(page1Tags, page2Tags...)
		if len(got) != len(wantAll) {
			t.Fatalf("got %d tags, want %d", len(got), len(wantAll))
		}
		for i, tag := range wantAll {
			if got[i] != tag {
				t.Errorf("tag[%d] = %q, want %q", i, got[i], tag)
			}
		}
	})

	t.Run("When registry returns too many pages it should return a pagination limit error", func(t *testing.T) {
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Every page returns one tag and a Link to the next page — infinite pagination.
			resp := tagsResponse{Tags: []string{"tag-" + r.URL.Query().Get("page")}}
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Link", `</v2/repo/tags/list?page=next>; rel="next"`)
			json.NewEncoder(w).Encode(resp)
		}))
		defer server.Close()

		client := &RegistryClient{HTTPClient: server.Client()}
		host := strings.TrimPrefix(server.URL, "https://")

		_, err := client.ListTags(context.Background(), host, "repo")
		if err == nil {
			t.Fatal("expected pagination limit error")
		}
		if !strings.Contains(err.Error(), "pagination limit exceeded") {
			t.Errorf("error should mention pagination limit, got: %v", err)
		}
	})
}

func TestResolveNextLink(t *testing.T) {
	baseURL, _ := url.Parse("https://registry.example.com/v2/repo/tags/list")

	t.Run("When header is empty it should return empty string", func(t *testing.T) {
		got, err := resolveNextLink("", baseURL, "registry.example.com")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "" {
			t.Errorf("got %q, want empty", got)
		}
	})

	t.Run("When Link is a relative path it should resolve against request URL", func(t *testing.T) {
		header := `</v2/repo/tags/list?last=abc>; rel="next"`
		got, err := resolveNextLink(header, baseURL, "registry.example.com")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := "https://registry.example.com/v2/repo/tags/list?last=abc"
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("When Link is an absolute HTTPS URL with matching host it should pass", func(t *testing.T) {
		header := `<https://registry.example.com/v2/repo/tags/list?last=abc>; rel="next"`
		got, err := resolveNextLink(header, baseURL, "registry.example.com")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := "https://registry.example.com/v2/repo/tags/list?last=abc"
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("When Link uses HTTP scheme it should return an error", func(t *testing.T) {
		header := `<http://registry.example.com/v2/repo/tags/list?last=abc>; rel="next"`
		_, err := resolveNextLink(header, baseURL, "registry.example.com")
		if err == nil {
			t.Error("expected error for non-HTTPS link")
		}
		if !strings.Contains(err.Error(), "expected https") {
			t.Errorf("error should mention expected https, got: %v", err)
		}
	})

	t.Run("When Link targets a different host it should return an error", func(t *testing.T) {
		header := `<https://evil.example.com/v2/repo/tags/list?last=abc>; rel="next"`
		_, err := resolveNextLink(header, baseURL, "registry.example.com")
		if err == nil {
			t.Error("expected error for cross-host link")
		}
		if !strings.Contains(err.Error(), "expected") {
			t.Errorf("error should mention expected host, got: %v", err)
		}
	})
}
