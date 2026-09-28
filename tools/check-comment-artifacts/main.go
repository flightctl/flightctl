// Command check-comment-artifacts fails if Go comments contain plan/design
// artifact markers (e.g. "D4:", "AC5", "per design §4.2").
//
// Comments should explain behavior. Track acceptance criteria and design
// decisions in Jira, design docs, and PRs — not in source comments.
package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"go/scanner"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const (
	toolDir        = "tools/check-comment-artifacts"
	gitListTimeout = 30 * time.Second
)

// Forbidden markers inside Go comments.
var forbidden = regexp.MustCompile(
	`D\d+:|per D\d+|decision D\d+|Locked: D|\bAC\d+\b|design §|per design|Required by design`,
)

type hit struct {
	path    string
	line    int
	snippet string
}

func main() {
	rootFlag := flag.String("root", "", "repository root (default: two levels above this tool)")
	flag.Parse()

	root, err := resolveRoot(*rootFlag)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(2)
	}
	if err := ensureRoot(root); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(2)
	}

	hits, err := scanRepo(root)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(2)
	}
	if len(hits) > 0 {
		fmt.Fprintln(os.Stderr, "error: Go comments must not cite plan/decision tags or design-doc section IDs.")
		fmt.Fprintln(os.Stderr, "Explain behavior in comments; keep AC/D/design-§ references in Jira, design docs, or PRs.")
		fmt.Fprintln(os.Stderr)
		for _, h := range hits {
			fmt.Fprintf(os.Stderr, "%s:%d:%s\n", h.path, h.line, h.snippet)
		}
		fmt.Fprintln(os.Stderr)
		fmt.Fprintln(os.Stderr, "See tools/check-comment-artifacts/")
		os.Exit(1)
	}

	fmt.Println("OK: no plan/design artifact markers in Go comments.")
}

func resolveRoot(explicit string) (string, error) {
	if explicit != "" {
		return filepath.Abs(explicit)
	}
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	// When run via `cd tools/check-comment-artifacts && go run .`, cwd is the tool dir.
	if filepath.Base(wd) == "check-comment-artifacts" {
		return filepath.Abs(filepath.Join(wd, "../.."))
	}
	return filepath.Abs(wd)
}

func ensureRoot(root string) error {
	fi, err := os.Stat(root)
	if err != nil {
		return fmt.Errorf("root %q: %w", root, err)
	}
	if !fi.IsDir() {
		return fmt.Errorf("root %q is not a directory", root)
	}
	return nil
}

func scanRepo(root string) ([]hit, error) {
	rels, err := listTrackedGoFiles(root)
	if err != nil {
		return nil, err
	}
	return scanFiles(root, rels)
}

func listTrackedGoFiles(root string) ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), gitListTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "git", "-C", root, "ls-files", "-z", "--", "*.go")
	out, err := cmd.Output()
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, fmt.Errorf("git ls-files: timed out after %s", gitListTimeout)
		}
		if ee, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("git ls-files: %w: %s", err, bytes.TrimSpace(ee.Stderr))
		}
		return nil, fmt.Errorf("git ls-files: %w", err)
	}

	var rels []string
	for _, rel := range bytes.Split(out, []byte{0}) {
		if len(rel) == 0 {
			continue
		}
		path := filepath.ToSlash(string(rel))
		if !shouldScan(path) {
			continue
		}
		rels = append(rels, path)
	}
	return rels, nil
}

func shouldScan(rel string) bool {
	rel = filepath.ToSlash(rel)
	if !strings.HasSuffix(rel, ".go") || strings.HasSuffix(rel, ".gen.go") {
		return false
	}
	if rel == toolDir || strings.HasPrefix(rel, toolDir+"/") {
		return false
	}
	return true
}

func scanFiles(root string, relPaths []string) ([]hit, error) {
	var hits []hit
	for _, rel := range relPaths {
		if !shouldScan(rel) {
			continue
		}
		fileHits, err := scanFile(root, filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				// Index still lists the path after an unstaged deletion.
				continue
			}
			return nil, err
		}
		hits = append(hits, fileHits...)
	}
	return hits, nil
}

func scanFile(root, path string) ([]hit, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	fset := token.NewFileSet()
	file := fset.AddFile(path, fset.Base(), len(src))
	var s scanner.Scanner
	s.Init(file, src, nil, scanner.ScanComments)

	rel, err := filepath.Rel(root, path)
	if err != nil {
		rel = path
	}

	var hits []hit
	for {
		pos, tok, lit := s.Scan()
		if tok == token.EOF {
			break
		}
		if tok != token.COMMENT {
			continue
		}
		loc := matchLocation(lit)
		if loc == nil {
			continue
		}
		position := fset.Position(pos)
		hits = append(hits, hit{
			path:    filepath.ToSlash(rel),
			line:    position.Line + loc.lineOffset,
			snippet: loc.snippet,
		})
	}
	return hits, nil
}

type matchLoc struct {
	lineOffset int
	snippet    string
}

// matchLocation finds the first forbidden marker in a comment token and
// returns its line offset within the token plus that line as the snippet.
// Block comments are a single token starting at /*; the marker may be later.
func matchLocation(lit string) *matchLoc {
	idx := forbidden.FindStringIndex(lit)
	if idx == nil {
		return nil
	}
	prefix := lit[:idx[0]]
	lineOffset := strings.Count(prefix, "\n")
	lines := strings.Split(lit, "\n")
	snippet := strings.TrimSpace(lines[lineOffset])
	return &matchLoc{lineOffset: lineOffset, snippet: snippet}
}
