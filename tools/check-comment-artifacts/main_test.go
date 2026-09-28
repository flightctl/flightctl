package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestScanFile_CatchesAllCommentForms(t *testing.T) {
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
	path := filepath.Join(pkg, "sample.go")
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	hits, err := scanRepo(dir)
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

func TestScanFile_SkipsGenerated(t *testing.T) {
	dir := t.TempDir()
	pkg := filepath.Join(dir, "api")
	if err := os.MkdirAll(pkg, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkg, "types.gen.go"), []byte("package api\n// D4: generated\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	hits, err := scanRepo(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 0 {
		t.Fatalf("expected no hits for .gen.go, got %+v", hits)
	}
}

func TestScanFile_IncludesScripts(t *testing.T) {
	dir := t.TempDir()
	pkg := filepath.Join(dir, "scripts", "air-gap")
	if err := os.MkdirAll(pkg, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkg, "main.go"), []byte("package main\n// D4: in scripts\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	hits, err := scanRepo(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 {
		t.Fatalf("expected hit under scripts/, got %+v", hits)
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
