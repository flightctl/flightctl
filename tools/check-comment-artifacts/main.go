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

var scanRoots = []string{"api", "cmd", "internal", "pkg", "test"}

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
		if !forbidden.MatchString(lit) {
			continue
		}
		position := fset.Position(pos)
		snippet := firstLine(lit)
		hits = append(hits, hit{
			path:    filepath.ToSlash(rel),
			line:    position.Line,
			snippet: snippet,
		})
	}
	return hits, nil
}

func firstLine(lit string) string {
	lit = strings.TrimSpace(lit)
	if i := strings.IndexByte(lit, '\n'); i >= 0 {
		lit = lit[:i]
	}
	return strings.TrimSpace(lit)
}
