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
	wants := unitWants(string(targetData))

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

		sourcePrefix := `Source: "deploy/podman/flightctl-` + s.Name + `/`
		if !strings.Contains(manifest, sourcePrefix) {
			issues = append(issues, Issue{Check: check, Message: fmt.Sprintf("%s: manifest.go has no Source asset under deploy/podman/flightctl-%s/", s.Name, s.Name)})
		}

		if s.InFlightctlTarget {
			want := "flightctl-" + s.Name + ".service"
			if _, ok := wants[want]; !ok {
				issues = append(issues, Issue{Check: check, Message: fmt.Sprintf("%s: flightctl.target [Unit] missing Wants=%s", s.Name, want)})
			}
		}
	}
	return issues
}

// unitWants returns active Wants= values from the [Unit] section.
func unitWants(content string) map[string]struct{} {
	out := map[string]struct{}{}
	inUnit := false
	for _, line := range systemdLogicalLines(content) {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
			inUnit = trimmed == "[Unit]"
			continue
		}
		if !inUnit || trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.HasPrefix(trimmed, "Wants=") {
			for _, unit := range strings.Fields(strings.TrimPrefix(trimmed, "Wants=")) {
				out[unit] = struct{}{}
			}
		}
	}
	return out
}

func systemdLogicalLines(content string) []string {
	var out []string
	var buf string
	for _, line := range strings.Split(content, "\n") {
		trimmedRight := strings.TrimRight(line, " \t")
		if strings.HasSuffix(trimmedRight, `\`) {
			buf += strings.TrimSuffix(trimmedRight, `\`) + " "
			continue
		}
		out = append(out, buf+line)
		buf = ""
	}
	if buf != "" {
		out = append(out, buf)
	}
	return out
}
