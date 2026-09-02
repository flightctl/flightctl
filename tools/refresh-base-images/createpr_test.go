package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// mockGitRunner records all git operations and can simulate staged changes.
type mockGitRunner struct {
	commands     [][]string
	hasChanges   bool // controls what "diff --staged --quiet" returns
	outputFunc   func(args ...string) (string, error)
	runErr       error // returned for any run() call if set
	failCommands map[string]error
}

func (m *mockGitRunner) run(args ...string) error {
	m.commands = append(m.commands, args)

	// Check if this specific command should fail.
	key := strings.Join(args, " ")
	if err, ok := m.failCommands[key]; ok {
		return err
	}

	if m.runErr != nil {
		return m.runErr
	}

	// Simulate git diff --staged --quiet behavior:
	// exit 0 (no error) = no changes; exit 1 (error) = has changes.
	if len(args) >= 3 && args[0] == "diff" && args[1] == "--staged" && args[2] == "--quiet" {
		if m.hasChanges {
			return fmt.Errorf("exit status 1")
		}
		return nil
	}
	return nil
}

func (m *mockGitRunner) runOutput(args ...string) (string, error) {
	m.commands = append(m.commands, args)
	if m.outputFunc != nil {
		return m.outputFunc(args...)
	}
	return "", nil
}

func (m *mockGitRunner) containsCommand(prefix ...string) bool {
	for _, cmd := range m.commands {
		if len(cmd) >= len(prefix) {
			match := true
			for i, p := range prefix {
				if cmd[i] != p {
					match = false
					break
				}
			}
			if match {
				return true
			}
		}
	}
	return false
}

func TestCreatePR(t *testing.T) {
	// Helper: create temp dir with images.yaml for updates.
	setupDir := func(t *testing.T) string {
		t.Helper()
		dir := t.TempDir()
		el9Dir := filepath.Join(dir, "el9")
		if err := os.MkdirAll(el9Dir, 0o755); err != nil {
			t.Fatal(err)
		}
		content := `api:
  image: quay.io/flightctl/flightctl-api-el9
  tag: latest
  build_base: registry.access.redhat.com/ubi9/go-toolset:1.26.7-1787774815
  run_base: quay.io/flightctl/flightctl-base:9.6-1234567890
`
		if err := os.WriteFile(filepath.Join(el9Dir, "images.yaml"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return dir
	}

	t.Run("When changes exist and no PR is open it should create a new PR", func(t *testing.T) {
		dir := setupDir(t)
		git := &mockGitRunner{hasChanges: true, failCommands: map[string]error{}}

		var listCalled, createCalled bool
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/pulls"):
				listCalled = true
				w.Header().Set("Content-Type", "application/json")
				w.Write([]byte("[]")) // No existing PRs
			case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/pulls"):
				createCalled = true

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
				if !strings.Contains(payload["body"], "ubi-micro") {
					t.Errorf("PR body should mention ubi-micro update")
				}

				w.WriteHeader(http.StatusCreated)
				json.NewEncoder(w).Encode(PullRequest{Number: 99, HTMLURL: "https://github.com/test/test/pull/99"})
			default:
				t.Errorf("unexpected request: %s %s", r.Method, r.URL)
				w.WriteHeader(http.StatusNotFound)
			}
		}))
		defer server.Close()

		gh := &GitHubClient{HTTPClient: server.Client(), Token: "test-token", BaseURL: server.URL}

		updates := map[string]VersionUpdate{
			"9": {Any: true, Micro: ImageUpdate{Updated: true, Tag: "9.8-9999999999"}},
		}

		err := createPR(context.Background(), gh, git, dir, updates,
			"registry.access.redhat.com", "quay.io/flightctl",
			"test/test", "auto/refresh-base-images", "main")
		if err != nil {
			t.Fatalf("createPR() error = %v", err)
		}

		if !listCalled {
			t.Error("should have listed existing PRs")
		}
		if !createCalled {
			t.Error("should have created a new PR")
		}
		if !git.containsCommand("config", "user.name") {
			t.Error("should have configured git user.name")
		}
		if !git.containsCommand("config", "user.email") {
			t.Error("should have configured git user.email")
		}
		if !git.containsCommand("checkout", "-B", "auto/refresh-base-images") {
			t.Error("should have created branch")
		}
		if !git.containsCommand("add", "packaging/images/") {
			t.Error("should have staged changes")
		}
		if !git.containsCommand("commit", "-m", "NO-ISSUE: Refresh base image references") {
			t.Error("should have committed")
		}
		if !git.containsCommand("push", "--force", "origin", "auto/refresh-base-images") {
			t.Error("should have force-pushed")
		}
	})

	t.Run("When changes exist and a PR is already open it should update the existing PR", func(t *testing.T) {
		dir := setupDir(t)
		git := &mockGitRunner{hasChanges: true, failCommands: map[string]error{}}

		var updateCalled bool
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/pulls"):
				prs := []PullRequest{{Number: 42, HTMLURL: "https://github.com/test/test/pull/42", State: "open"}}
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(prs)
			case r.Method == http.MethodPatch && strings.HasSuffix(r.URL.Path, "/pulls/42"):
				updateCalled = true
				w.WriteHeader(http.StatusOK)
				json.NewEncoder(w).Encode(PullRequest{Number: 42})
			default:
				t.Errorf("unexpected request: %s %s", r.Method, r.URL)
				w.WriteHeader(http.StatusNotFound)
			}
		}))
		defer server.Close()

		gh := &GitHubClient{HTTPClient: server.Client(), Token: "test-token", BaseURL: server.URL}

		updates := map[string]VersionUpdate{
			"9": {Any: true, Micro: ImageUpdate{Updated: true, Tag: "9.8-9999999999"}},
		}

		err := createPR(context.Background(), gh, git, dir, updates,
			"registry.access.redhat.com", "quay.io/flightctl",
			"test/test", "auto/refresh-base-images", "main")
		if err != nil {
			t.Fatalf("createPR() error = %v", err)
		}

		if !updateCalled {
			t.Error("should have updated existing PR #42")
		}
	})

	t.Run("When no files change after update it should skip PR creation", func(t *testing.T) {
		dir := setupDir(t)
		git := &mockGitRunner{hasChanges: false, failCommands: map[string]error{}}

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			t.Errorf("GitHub API should not be called when no changes; got %s %s", r.Method, r.URL)
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer server.Close()

		gh := &GitHubClient{HTTPClient: server.Client(), Token: "test-token", BaseURL: server.URL}

		// Provide updates that don't actually change anything (tag matches current).
		updates := map[string]VersionUpdate{
			"9": {Any: true, Micro: ImageUpdate{Updated: true, Tag: "9.6-1234567890"}},
		}

		err := createPR(context.Background(), gh, git, dir, updates,
			"registry.access.redhat.com", "quay.io/flightctl",
			"test/test", "auto/refresh-base-images", "main")
		if err != nil {
			t.Fatalf("createPR() error = %v", err)
		}

		if git.containsCommand("commit") {
			t.Error("should not have committed when no changes")
		}
		if git.containsCommand("push") {
			t.Error("should not have pushed when no changes")
		}
	})

	t.Run("When git push fails it should return an error", func(t *testing.T) {
		dir := setupDir(t)
		git := &mockGitRunner{
			hasChanges:   true,
			failCommands: map[string]error{"push --force origin auto/refresh-base-images": fmt.Errorf("push rejected")},
		}

		gh := &GitHubClient{Token: "test-token", BaseURL: "http://unused"}

		updates := map[string]VersionUpdate{
			"9": {Any: true, Micro: ImageUpdate{Updated: true, Tag: "9.8-9999999999"}},
		}

		err := createPR(context.Background(), gh, git, dir, updates,
			"registry.access.redhat.com", "quay.io/flightctl",
			"test/test", "auto/refresh-base-images", "main")
		if err == nil {
			t.Fatal("expected error when git push fails")
		}
		if !strings.Contains(err.Error(), "git push") {
			t.Errorf("error should mention git push, got: %v", err)
		}
	})

	t.Run("When repo format is invalid it should return an error", func(t *testing.T) {
		dir := setupDir(t)
		git := &mockGitRunner{hasChanges: true, failCommands: map[string]error{}}

		gh := &GitHubClient{Token: "test-token", BaseURL: "http://unused"}

		updates := map[string]VersionUpdate{
			"9": {Any: true, Micro: ImageUpdate{Updated: true, Tag: "9.8-9999999999"}},
		}

		err := createPR(context.Background(), gh, git, dir, updates,
			"registry.access.redhat.com", "quay.io/flightctl",
			"invalid-repo-no-slash", "auto/refresh-base-images", "main")
		if err == nil {
			t.Fatal("expected error for invalid repo format")
		}
		if !strings.Contains(err.Error(), "invalid repo format") {
			t.Errorf("error should mention invalid repo format, got: %v", err)
		}
	})

	t.Run("When GitHub API fails to create PR it should return an error", func(t *testing.T) {
		dir := setupDir(t)
		git := &mockGitRunner{hasChanges: true, failCommands: map[string]error{}}

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/pulls"):
				w.Header().Set("Content-Type", "application/json")
				w.Write([]byte("[]"))
			case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/pulls"):
				w.WriteHeader(http.StatusInternalServerError)
				w.Write([]byte(`{"message":"Internal Server Error"}`))
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		}))
		defer server.Close()

		gh := &GitHubClient{HTTPClient: server.Client(), Token: "test-token", BaseURL: server.URL}

		updates := map[string]VersionUpdate{
			"9": {Any: true, Micro: ImageUpdate{Updated: true, Tag: "9.8-9999999999"}},
		}

		err := createPR(context.Background(), gh, git, dir, updates,
			"registry.access.redhat.com", "quay.io/flightctl",
			"test/test", "auto/refresh-base-images", "main")
		if err == nil {
			t.Fatal("expected error when GitHub API fails")
		}
		if !strings.Contains(err.Error(), "creating PR") {
			t.Errorf("error should mention creating PR, got: %v", err)
		}
	})
}
