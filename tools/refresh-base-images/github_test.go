package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGitHubClientListOpenPRs(t *testing.T) {
	t.Run("When open PRs exist it should return them", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet {
				t.Errorf("expected GET, got %s", r.Method)
			}
			if !strings.Contains(r.URL.String(), "/repos/flightctl/flightctl/pulls") {
				t.Errorf("unexpected URL: %s", r.URL)
			}
			if r.URL.Query().Get("head") != "flightctl:auto/refresh-base-images" {
				t.Errorf("unexpected head param: %s", r.URL.Query().Get("head"))
			}
			if r.Header.Get("Authorization") != "Bearer test-token" {
				t.Errorf("missing or wrong auth header")
			}

			prs := []PullRequest{{Number: 42, HTMLURL: "https://github.com/flightctl/flightctl/pull/42", State: "open"}}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(prs)
		}))
		defer server.Close()

		client := &GitHubClient{HTTPClient: server.Client(), Token: "test-token", BaseURL: server.URL}
		prs, err := client.ListOpenPRs(context.Background(), "flightctl", "flightctl", "auto/refresh-base-images")
		if err != nil {
			t.Fatalf("ListOpenPRs() error = %v", err)
		}
		if len(prs) != 1 || prs[0].Number != 42 {
			t.Errorf("expected PR #42, got %+v", prs)
		}
	})

	t.Run("When no open PRs exist it should return empty slice", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte("[]"))
		}))
		defer server.Close()

		client := &GitHubClient{HTTPClient: server.Client(), Token: "test-token", BaseURL: server.URL}
		prs, err := client.ListOpenPRs(context.Background(), "flightctl", "flightctl", "auto/refresh-base-images")
		if err != nil {
			t.Fatalf("ListOpenPRs() error = %v", err)
		}
		if len(prs) != 0 {
			t.Errorf("expected 0 PRs, got %d", len(prs))
		}
	})
}

func TestGitHubClientCreatePR(t *testing.T) {
	t.Run("When creating a PR it should send correct payload and return result", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				t.Errorf("expected POST, got %s", r.Method)
			}

			var payload map[string]string
			json.NewDecoder(r.Body).Decode(&payload)

			if payload["title"] != "NO-ISSUE: Refresh base image references" {
				t.Errorf("unexpected title: %s", payload["title"])
			}
			if payload["head"] != "auto/refresh-base-images" {
				t.Errorf("unexpected head: %s", payload["head"])
			}
			if payload["base"] != "main" {
				t.Errorf("unexpected base: %s", payload["base"])
			}

			w.WriteHeader(http.StatusCreated)
			pr := PullRequest{Number: 100, HTMLURL: "https://github.com/flightctl/flightctl/pull/100"}
			json.NewEncoder(w).Encode(pr)
		}))
		defer server.Close()

		client := &GitHubClient{HTTPClient: server.Client(), Token: "test-token", BaseURL: server.URL}
		pr, err := client.CreatePR(context.Background(), "flightctl", "flightctl",
			"NO-ISSUE: Refresh base image references", "body text", "auto/refresh-base-images", "main")
		if err != nil {
			t.Fatalf("CreatePR() error = %v", err)
		}
		if pr.Number != 100 {
			t.Errorf("expected PR #100, got #%d", pr.Number)
		}
	})

	t.Run("When API returns error it should propagate", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusUnprocessableEntity)
			w.Write([]byte(`{"message":"Validation Failed"}`))
		}))
		defer server.Close()

		client := &GitHubClient{HTTPClient: server.Client(), Token: "test-token", BaseURL: server.URL}
		_, err := client.CreatePR(context.Background(), "flightctl", "flightctl",
			"title", "body", "head", "main")
		if err == nil {
			t.Error("expected error for 422 response")
		}
		if !strings.Contains(err.Error(), "422") {
			t.Errorf("error should contain status code: %v", err)
		}
	})
}

func TestGitHubClientUpdatePR(t *testing.T) {
	t.Run("When updating a PR body it should send PATCH with correct payload", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPatch {
				t.Errorf("expected PATCH, got %s", r.Method)
			}
			if !strings.HasSuffix(r.URL.Path, "/repos/flightctl/flightctl/pulls/42") {
				t.Errorf("unexpected URL path: %s", r.URL.Path)
			}

			var payload map[string]string
			json.NewDecoder(r.Body).Decode(&payload)
			if payload["body"] != "updated body" {
				t.Errorf("unexpected body: %s", payload["body"])
			}

			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(PullRequest{Number: 42})
		}))
		defer server.Close()

		client := &GitHubClient{HTTPClient: server.Client(), Token: "test-token", BaseURL: server.URL}
		err := client.UpdatePR(context.Background(), "flightctl", "flightctl", 42, "updated body")
		if err != nil {
			t.Fatalf("UpdatePR() error = %v", err)
		}
	})
}

func TestBuildPRBody(t *testing.T) {
	t.Run("When updates have mixed changes it should generate correct markdown", func(t *testing.T) {
		updates := map[string]VersionUpdate{
			"9": {
				Any:       true,
				Micro:     ImageUpdate{Updated: true, Tag: "9.8-9999999999"},
				Minimal:   ImageUpdate{Updated: false, Tag: "9.7-1763362218"},
				GoToolset: ImageUpdate{Updated: true, Tag: "1.26.8-9999999999"},
			},
			"10": {
				Any:     true,
				Micro:   ImageUpdate{Updated: true, Tag: "10.2-8888888888"},
				Minimal: ImageUpdate{Updated: false, Tag: "10.1-1769677092"},
			},
		}

		body := BuildPRBody(updates, "flightctl/flightctl")

		if !strings.Contains(body, "### EL9") {
			t.Error("missing EL9 section")
		}
		if !strings.Contains(body, "### EL10") {
			t.Error("missing EL10 section")
		}
		if !strings.Contains(body, "ubi-micro (flightctl-base): 9.8-9999999999") {
			t.Error("missing EL9 micro update")
		}
		if !strings.Contains(body, "go-toolset: 1.26.8-9999999999") {
			t.Error("missing EL9 go-toolset update")
		}
		if strings.Contains(body, "ubi-minimal: 9.7") {
			t.Error("should not include non-updated ubi-minimal for EL9")
		}
		if !strings.Contains(body, "ubi-micro (flightctl-base): 10.2-8888888888") {
			t.Error("missing EL10 micro update")
		}
		if !strings.Contains(body, "refresh-base-images workflow") {
			t.Error("missing workflow link")
		}
	})

	t.Run("When no versions have updates it should generate minimal body", func(t *testing.T) {
		updates := map[string]VersionUpdate{
			"9":  {Any: false},
			"10": {Any: false},
		}

		body := BuildPRBody(updates, "flightctl/flightctl")

		if strings.Contains(body, "### EL") {
			t.Error("should not have any EL sections when nothing updated")
		}
		if !strings.Contains(body, "## Updated base image references") {
			t.Error("missing header")
		}
	})

	t.Run("When versions are unordered it should produce sorted output", func(t *testing.T) {
		updates := map[string]VersionUpdate{
			"10": {Any: true, Micro: ImageUpdate{Updated: true, Tag: "10.2-1"}},
			"9":  {Any: true, Micro: ImageUpdate{Updated: true, Tag: "9.8-1"}},
		}

		body := BuildPRBody(updates, "flightctl/flightctl")

		idx9 := strings.Index(body, "### EL9")
		idx10 := strings.Index(body, "### EL10")
		if idx9 < 0 || idx10 < 0 {
			t.Fatal("missing EL sections")
		}
		if idx9 > idx10 {
			t.Error("EL9 should appear before EL10 (sorted)")
		}
	})
}
