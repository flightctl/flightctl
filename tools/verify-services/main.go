package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
)

func main() {
	repoRoot := flag.String("repo-root", "", "path to flightctl repository root")
	flag.Parse()

	root := *repoRoot
	if root == "" {
		wd, err := os.Getwd()
		if err != nil {
			fmt.Fprintf(os.Stderr, "getwd: %v\n", err)
			os.Exit(2)
		}
		root = wd
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		fmt.Fprintf(os.Stderr, "abs: %v\n", err)
		os.Exit(2)
	}

	services, err := loadRegistry(abs)
	if err != nil {
		fmt.Fprintf(os.Stderr, "registry: %v\n", err)
		os.Exit(2)
	}

	issues := runAllChecks(abs, services)
	if len(issues) == 0 {
		fmt.Println("verify-services: OK")
		return
	}
	fmt.Fprintf(os.Stderr, "verify-services: %d issue(s)\n", len(issues))
	for _, i := range issues {
		fmt.Fprintf(os.Stderr, "  - %s\n", i)
	}
	os.Exit(1)
}
