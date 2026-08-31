package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

type Issue struct {
	Check   string
	Message string
}

func (i Issue) String() string {
	return fmt.Sprintf("[%s] %s", i.Check, i.Message)
}

func runAllChecks(repoRoot string, services []ExpandedService) []Issue {
	var issues []Issue
	issues = append(issues, checkPublishMatrix(repoRoot, services)...)
	issues = append(issues, checkAirGapObservability(repoRoot, services)...)
	issues = append(issues, checkImagesYAML(repoRoot, services)...)
	issues = append(issues, checkLocalImagesYAML(repoRoot, services)...)
	issues = append(issues, checkContainerfiles(repoRoot, services)...)
	issues = append(issues, checkMakeBuildContainers(repoRoot, services)...)
	issues = append(issues, checkMakeBuildBinaries(repoRoot, services)...)
	issues = append(issues, checkPodmanSave(repoRoot, services)...)
	issues = append(issues, checkCollectLogs(repoRoot, services)...)
	issues = append(issues, checkTagOverride(repoRoot, services)...)
	issues = append(issues, checkHelm(repoRoot, services)...)
	issues = append(issues, checkNginx(repoRoot, services)...)
	issues = append(issues, checkQuadlet(repoRoot, services)...)
	issues = append(issues, checkTLS(repoRoot, services)...)
	return issues
}

func namesWhere(services []ExpandedService, pred func(ExpandedService) bool) []string {
	var out []string
	for _, s := range services {
		if pred(s) {
			out = append(out, s.Name)
		}
	}
	return out
}

func checkPublishMatrix(repoRoot string, services []ExpandedService) []Issue {
	const check = "publish-matrix"
	want := toSet(namesWhere(services, func(s ExpandedService) bool { return s.Publish }))
	data, err := os.ReadFile(filepath.Join(repoRoot, ".github/workflows/publish-containers.yaml"))
	if err != nil {
		return []Issue{{Check: check, Message: err.Error()}}
	}
	got, err := parsePublishImages(string(data))
	if err != nil {
		return []Issue{{Check: check, Message: err.Error()}}
	}
	d := DiffSets(want, got)
	if d.Empty() {
		return nil
	}
	return []Issue{{Check: check, Message: strings.TrimSpace(d.Format("publish set mismatch"))}}
}

func parsePublishImages(content string) (map[string]struct{}, error) {
	// Current layout: top-level env SUPPORTED_IMAGES: "api worker ..."
	if m := regexp.MustCompile(`(?m)^\s*SUPPORTED_IMAGES:\s*"([^"]+)"`).FindStringSubmatch(content); m != nil {
		got := map[string]struct{}{}
		for _, part := range strings.Fields(m[1]) {
			got[part] = struct{}{}
		}
		return got, nil
	}
	// Older layout: strategy.matrix.image: ['api', 'worker', ...]
	if m := regexp.MustCompile(`(?m)^\s*image:\s*\[([^\]]+)\]`).FindStringSubmatch(content); m != nil {
		got := map[string]struct{}{}
		for _, part := range strings.Split(m[1], ",") {
			part = strings.TrimSpace(part)
			part = strings.Trim(part, "'\"")
			if part != "" {
				got[part] = struct{}{}
			}
		}
		return got, nil
	}
	return nil, fmt.Errorf("could not find SUPPORTED_IMAGES or matrix.image list in publish-containers.yaml")
}

func checkAirGapObservability(repoRoot string, services []ExpandedService) []Issue {
	const check = "air-gap-observability"
	want := toSet(namesWhere(services, func(s ExpandedService) bool { return s.ObservabilityOnly }))
	data, err := os.ReadFile(filepath.Join(repoRoot, "scripts/air-gap/generate-embed/main.go"))
	if err != nil {
		return []Issue{{Check: check, Message: err.Error()}}
	}
	got, err := parseObservabilityOnlyImages(string(data))
	if err != nil {
		return []Issue{{Check: check, Message: err.Error()}}
	}
	d := DiffSets(want, got)
	if d.Empty() {
		return nil
	}
	return []Issue{{Check: check, Message: strings.TrimSpace(d.Format("observabilityOnlyImages mismatch vs registry observabilityOnly"))}}
}

func parseObservabilityOnlyImages(src string) (map[string]struct{}, error) {
	idx := strings.Index(src, "var observabilityOnlyImages")
	if idx < 0 {
		return nil, fmt.Errorf("could not find observabilityOnlyImages in generate-embed/main.go")
	}
	block := src[idx:]
	if end := strings.Index(block, "}"); end >= 0 {
		block = block[:end]
	}
	re := regexp.MustCompile(`"([a-z0-9-]+)"\s*:\s*true`)
	got := map[string]struct{}{}
	for _, line := range strings.Split(block, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "//") {
			continue
		}
		if m := re.FindStringSubmatch(trimmed); m != nil {
			got[m[1]] = struct{}{}
		}
	}
	return got, nil
}

func checkImagesYAML(repoRoot string, services []ExpandedService) []Issue {
	return checkImagesKeys(repoRoot, services, "packaging/images/el9/images.yaml", "images-el9",
		func(s ExpandedService) bool { return s.InImagesYaml })
}

func checkLocalImagesYAML(repoRoot string, services []ExpandedService) []Issue {
	var issues []Issue
	for _, osName := range []string{"el9", "el10"} {
		path := fmt.Sprintf("packaging/images/%s/local-images.yaml", osName)
		issues = append(issues, checkImagesKeys(repoRoot, services, path, "local-images-"+osName,
			func(s ExpandedService) bool { return s.InImagesYaml })...)
	}
	issues = append(issues, checkImagesKeys(repoRoot, services, "packaging/images/el10/images.yaml", "images-el10",
		func(s ExpandedService) bool { return s.InImagesYaml })...)
	return issues
}

func checkImagesKeys(repoRoot string, services []ExpandedService, rel, check string, pred func(ExpandedService) bool) []Issue {
	want := toSet(namesWhere(services, pred))
	data, err := os.ReadFile(filepath.Join(repoRoot, rel))
	if err != nil {
		return []Issue{{Check: check, Message: err.Error()}}
	}
	var doc map[string]any
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return []Issue{{Check: check, Message: fmt.Sprintf("parse %s: %v", rel, err)}}
	}
	got := map[string]struct{}{}
	for k := range doc {
		got[k] = struct{}{}
	}
	d := DiffSets(want, got)
	if d.Empty() {
		return nil
	}
	return []Issue{{Check: check, Message: strings.TrimSpace(d.Format(rel + " key mismatch"))}}
}

func ignoreContainerfile(svc string) bool {
	return svc == "proxy" // legacy/unused Containerfile.proxy (el9 only)
}

func checkContainerfiles(repoRoot string, services []ExpandedService) []Issue {
	const check = "containerfiles"
	want := toSet(namesWhere(services, func(s ExpandedService) bool { return s.BuildContainer }))
	var issues []Issue
	for _, osName := range []string{"el9", "el10"} {
		dir := filepath.Join(repoRoot, "packaging/images", osName)
		entries, err := os.ReadDir(dir)
		if err != nil {
			issues = append(issues, Issue{Check: check, Message: err.Error()})
			continue
		}
		got := map[string]struct{}{}
		for _, e := range entries {
			name := e.Name()
			if !strings.HasPrefix(name, "Containerfile.") {
				continue
			}
			svc := strings.TrimPrefix(name, "Containerfile.")
			if ignoreContainerfile(svc) {
				continue
			}
			got[svc] = struct{}{}
		}
		for name := range want {
			if _, ok := got[name]; !ok {
				issues = append(issues, Issue{Check: check, Message: fmt.Sprintf("missing packaging/images/%s/Containerfile.%s", osName, name)})
			}
		}
		for name := range got {
			if _, ok := want[name]; !ok {
				issues = append(issues, Issue{Check: check, Message: fmt.Sprintf("orphan packaging/images/%s/Containerfile.%s (not buildContainer in registry)", osName, name)})
			}
		}
	}
	return issues
}

func checkMakeBuildContainers(repoRoot string, services []ExpandedService) []Issue {
	const check = "make-build-containers"
	data, err := os.ReadFile(filepath.Join(repoRoot, "Makefile"))
	if err != nil {
		return []Issue{{Check: check, Message: err.Error()}}
	}
	re := regexp.MustCompile(`(?m)^build-containers:\s*(.*)$`)
	m := re.FindSubmatch(data)
	if m == nil {
		return []Issue{{Check: check, Message: "could not find build-containers target in Makefile"}}
	}
	deps := strings.Fields(string(m[1]))
	got := map[string]struct{}{}
	for _, d := range deps {
		got[d] = struct{}{}
	}
	want := map[string]struct{}{}
	for _, s := range services {
		if s.BuildContainer {
			want[s.MakeContainerTarget] = struct{}{}
		}
	}
	d := DiffSets(want, got)
	if d.Empty() {
		return nil
	}
	return []Issue{{Check: check, Message: strings.TrimSpace(d.Format("build-containers deps mismatch"))}}
}

func checkMakeBuildBinaries(repoRoot string, services []ExpandedService) []Issue {
	const check = "make-build-binaries"
	data, err := os.ReadFile(filepath.Join(repoRoot, "Makefile"))
	if err != nil {
		return []Issue{{Check: check, Message: err.Error()}}
	}
	// The multi-binary build target lists ./cmd/flightctl-* after build-cli build-pam-issuer
	re := regexp.MustCompile(`(?ms)^build:.*?\n((?:\t.*\n)+)`)
	m := re.FindSubmatch(data)
	if m == nil {
		return []Issue{{Check: check, Message: "could not find build target recipe in Makefile"}}
	}
	got := map[string]struct{}{}
	for _, line := range strings.Split(string(m[1]), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "./cmd/flightctl-") {
			name := strings.TrimPrefix(line, "./cmd/flightctl-")
			name = strings.TrimSuffix(name, " \\")
			name = strings.TrimSpace(name)
			got[name] = struct{}{}
		}
	}
	want := toSet(namesWhere(services, func(s ExpandedService) bool { return s.BuildBinary }))
	var missing []string
	for name := range want {
		if _, ok := got[name]; !ok {
			missing = append(missing, name)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return []Issue{{Check: check, Message: fmt.Sprintf("make build binary list missing: %s", strings.Join(sorted(missing), ", "))}}
}

func checkPodmanSave(repoRoot string, services []ExpandedService) []Issue {
	const check = "podman-save"
	data, err := os.ReadFile(filepath.Join(repoRoot, "deploy/deploy.mk"))
	if err != nil {
		return []Issue{{Check: check, Message: err.Error()}}
	}
	re := regexp.MustCompile(`podman save flightctl-([a-z0-9-]+)-\$\(OS\):latest`)
	got := map[string]struct{}{}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		if m := re.FindStringSubmatch(line); m != nil {
			got[m[1]] = struct{}{}
		}
	}
	want := toSet(namesWhere(services, func(s ExpandedService) bool {
		return s.BuildContainer && s.Publish
	}))
	d := DiffSets(want, got)
	if d.Empty() {
		return nil
	}
	return []Issue{{Check: check, Message: strings.TrimSpace(d.Format("deploy.mk podman save list mismatch"))}}
}

func checkCollectLogs(repoRoot string, services []ExpandedService) []Issue {
	const check = "collect-logs"
	data, err := os.ReadFile(filepath.Join(repoRoot, ".github/actions/collect-logs/action.yml"))
	if err != nil {
		return []Issue{{Check: check, Message: err.Error()}}
	}
	re := regexp.MustCompile(`for deployment in ([^;]+)`)
	m := re.FindSubmatch(data)
	if m == nil {
		return []Issue{{Check: check, Message: "could not find deployment loop in collect-logs action"}}
	}
	got := map[string]struct{}{}
	for _, part := range strings.Fields(string(m[1])) {
		part = strings.TrimPrefix(part, "flightctl-")
		got[part] = struct{}{}
	}
	want := toSet(namesWhere(services, func(s ExpandedService) bool { return s.CollectLogs }))
	d := DiffSets(want, got)
	if d.Empty() {
		return nil
	}
	return []Issue{{Check: check, Message: strings.TrimSpace(d.Format("collect-logs deployments mismatch"))}}
}

func checkTagOverride(repoRoot string, services []ExpandedService) []Issue {
	const check = "tag-override"
	data, err := os.ReadFile(filepath.Join(repoRoot, "internal/quadlet/renderer/renderer.go"))
	if err != nil {
		return []Issue{{Check: check, Message: err.Error()}}
	}
	src := string(data)

	fieldRe := regexp.MustCompile(`(\w+)\s+ImageConfig\s+\x60mapstructure:"([^"]+)"\x60`)
	keyByField := map[string]string{}
	for _, m := range fieldRe.FindAllStringSubmatch(src, -1) {
		keyByField[m[1]] = m[2]
	}

	idx := strings.Index(src, "func (config *RendererConfig) ApplyFlightctlServicesTagOverride")
	if idx < 0 {
		return []Issue{{Check: check, Message: "could not find ApplyFlightctlServicesTagOverride"}}
	}
	fn := src[idx:]
	if end := strings.Index(fn, "\nfunc "); end > 0 {
		fn = fn[:end]
	}

	assignRe := regexp.MustCompile(`config\.(\w+)\.Tag\s*=\s*tag`)
	got := map[string]struct{}{}
	for _, m := range assignRe.FindAllStringSubmatch(fn, -1) {
		if key, ok := keyByField[m[1]]; ok {
			got[key] = struct{}{}
		}
	}
	want := toSet(namesWhere(services, func(s ExpandedService) bool { return s.TagOverride }))
	d := DiffSets(want, got)
	if d.Empty() {
		return nil
	}
	return []Issue{{Check: check, Message: strings.TrimSpace(d.Format("ApplyFlightctlServicesTagOverride mismatch"))}}
}
