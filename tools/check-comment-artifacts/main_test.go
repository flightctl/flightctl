package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestScanFiles_CatchesAllCommentForms(t *testing.T) {
	dir := t.TempDir()
	pkg := filepath.Join(dir, "internal", "pkg")
	if err := os.MkdirAll(pkg, 0o755); err != nil {
		t.Fatal(err)
	}

	src := `package pkg

// D4: line comment should fail
func A() {}

func B() { x := 1 // AC5 trailing comment should fail
	_ = x
}

/* per design §4.2 block comment should fail */
func C() {}

/*
unstarred block body with Locked: D9 should fail
*/
func D() {}

// This is fine: no artifact markers
func E() {}
`
	rel := "internal/pkg/sample.go"
	if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(rel)), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	hits, err := scanFiles(dir, []string{rel})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 4 {
		t.Fatalf("got %d hits, want 4: %+v", len(hits), hits)
	}

	var lines []int
	for _, h := range hits {
		lines = append(lines, h.line)
	}
	// Locked: D9 is on the body line of the block comment, not the opening /*.
	want := []int{3, 6, 10, 14}
	slices.Sort(lines)
	slices.Sort(want)
	if !slices.Equal(lines, want) {
		t.Fatalf("lines = %v, want %v; hits=%+v", lines, want, hits)
	}

	var lockedSnippet string
	for _, h := range hits {
		if h.line == 14 {
			lockedSnippet = h.snippet
		}
	}
	if !strings.Contains(lockedSnippet, "Locked: D9") {
		t.Fatalf("block-body hit snippet %q should contain the marker", lockedSnippet)
	}
}

func TestShouldScan_FiltersGeneratedAndToolFixtures(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"internal/foo.go", true},
		{"client/client.go", true},
		{"tools/verify-backports/main.go", true},
		{"api/types.gen.go", false},
		{"tools/check-comment-artifacts/main.go", false},
		{"tools/check-comment-artifacts/main_test.go", false},
		{"readme.md", false},
	}
	for _, tc := range cases {
		if got := shouldScan(tc.path); got != tc.want {
			t.Errorf("shouldScan(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

func TestScanRepo_UsesGitTrackedFiles(t *testing.T) {
	dir := t.TempDir()
	write := map[string]string{
		"client/client.go": "package client\n// D4: tracked client\n",
		"api/types.gen.go": "package api\n// D4: generated\n",
		"tools/check-comment-artifacts/fixture_test.go": "package main\n// D4: fixture\n",
		"internal/ok.go": "package internal\n// fine\n",
	}
	for rel, body := range write {
		path := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	runGit(t, dir, "init")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "test")
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "init")

	// Untracked marked file must not affect results (proves git ls-files, not walk).
	untracked := filepath.Join(dir, "untracked.go")
	if err := os.WriteFile(untracked, []byte("package main\n// D4: untracked\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	hits, err := scanRepo(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 {
		t.Fatalf("got %d hits, want 1 (client only): %+v", len(hits), hits)
	}
	if hits[0].path != "client/client.go" {
		t.Fatalf("path = %q, want client/client.go", hits[0].path)
	}
}

func TestScanFiles_SkipsMissingTrackedPaths(t *testing.T) {
	dir := t.TempDir()
	rel := "client/client.go"
	path := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("package client\n// D4: present\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	hits, err := scanFiles(dir, []string{rel, "gone/deleted.go"})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].path != rel {
		t.Fatalf("got %+v, want single hit on %s", hits, rel)
	}
}

func TestEnsureRoot_RejectsMissing(t *testing.T) {
	err := ensureRoot(filepath.Join(t.TempDir(), "does-not-exist"))
	if err == nil {
		t.Fatal("expected error for missing root")
	}
}

func TestEnsureRoot_AcceptsDir(t *testing.T) {
	if err := ensureRoot(t.TempDir()); err != nil {
		t.Fatal(err)
	}
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}
