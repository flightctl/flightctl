// Command check-comment-artifacts fails if Go comments contain plan/design
// artifact markers (e.g. "D4:", "AC5", "per design §4.2").
//
// Comments should explain behavior. Track acceptance criteria and design
// decisions in Jira, design docs, and PRs — not in source comments.
package main

import (
	"flag"
	"fmt"
	"go/scanner"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// tools/ is intentionally omitted: this checker and its tests contain marker
// fixtures on purpose.
var scanRoots = []string{"api", "cmd", "internal", "pkg", "test", "scripts"}

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
	var hits []hit
	for _, name := range scanRoots {
		base := filepath.Join(root, name)
		if _, err := os.Stat(base); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		err := filepath.WalkDir(base, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, ".gen.go") {
				return nil
			}
			fileHits, err := scanFile(root, path)
			if err != nil {
				return err
			}
			hits = append(hits, fileHits...)
			return nil
		})
		if err != nil {
			return nil, err
		}
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
