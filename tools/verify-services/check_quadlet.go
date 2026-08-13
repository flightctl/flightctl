package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func checkQuadlet(repoRoot string, services []ExpandedService) []Issue {
	const check = "quadlet"
	var issues []Issue

	manifestData, err := os.ReadFile(filepath.Join(repoRoot, "internal/quadlet/renderer/manifest.go"))
	if err != nil {
		return []Issue{{Check: check, Message: err.Error()}}
	}
	manifest := string(manifestData)

	targetData, err := os.ReadFile(filepath.Join(repoRoot, "deploy/podman/flightctl.target"))
	if err != nil {
		return []Issue{{Check: check, Message: err.Error()}}
	}
	target := string(targetData)

	for _, s := range services {
		if !s.Quadlet {
			continue
		}
		dir := filepath.Join(repoRoot, "deploy/podman", "flightctl-"+s.Name)
		entries, err := os.ReadDir(dir)
		if err != nil {
			issues = append(issues, Issue{Check: check, Message: fmt.Sprintf("%s: missing quadlet dir %s", s.Name, dir)})
			continue
		}
		hasContainer := false
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".container") {
				hasContainer = true
				break
			}
		}
		if !hasContainer {
			issues = append(issues, Issue{Check: check, Message: fmt.Sprintf("%s: no .container file under %s", s.Name, dir)})
		}

		needle := "deploy/podman/flightctl-" + s.Name + "/"
		if !strings.Contains(manifest, needle) {
			issues = append(issues, Issue{Check: check, Message: fmt.Sprintf("%s: manifest.go does not reference %s", s.Name, needle)})
		}

		if s.InFlightctlTarget {
			want := "flightctl-" + s.Name + ".service"
			if !strings.Contains(target, "Wants="+want) {
				issues = append(issues, Issue{Check: check, Message: fmt.Sprintf("%s: flightctl.target missing Wants=%s", s.Name, want)})
			}
		}
	}
	return issues
}
