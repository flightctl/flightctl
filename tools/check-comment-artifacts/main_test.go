package main

import (
	"os"
	"path/filepath"
	"slices"
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
	want := []int{3, 6, 10, 13}
	slices.Sort(lines)
	slices.Sort(want)
	if !slices.Equal(lines, want) {
		t.Fatalf("lines = %v, want %v", lines, want)
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
