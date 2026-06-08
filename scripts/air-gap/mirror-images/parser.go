package main

import (
	"bufio"
	"fmt"
	"os"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// -----------------------------------------------------------------------------
// Input YAML types
//
// These structs map directly onto the two YAML sources the tool reads:
//   1. deploy/helm/helm-chart-opts.yaml        — per-variant Helm image map
//   2. packaging/images/{el9,el10,rhel9,rhel10}/images.yaml — full image config
//      used by both the quadlet renderer (RPM build) and this tool.  The tool
//      reads the whole file; Dedup() drops anything already covered by source 1.
// -----------------------------------------------------------------------------

// HelmChartOpts is the top-level structure of helm-chart-opts.yaml.
// The outer key is the variant name (e.g. "community-el9"); the value
// describes everything the Helm chart needs to deploy for that variant.
type HelmChartOpts map[string]ChartVariant

// ChartVariant holds the images sub-map for a single variant.
// Other fields in the file (name, description, annotations, etc.) are ignored.
type ChartVariant struct {
	Images map[string]ImageSpec `yaml:"images"`
}

// ImageSpec is a single image entry.  Tag is optional in helm-chart-opts.yaml
// (absent for core flightctl service images whose tag is set at packaging time);
// it is always present in the observability images.yaml files.
type ImageSpec struct {
	Image string `yaml:"image"`
	Tag   string `yaml:"tag"`
}

// ObsImages is the top-level structure of packaging/images/*/images.yaml.
// It reuses ImageSpec — every entry must have both image and tag.
type ObsImages map[string]ImageSpec

// observabilityOnlyImages are images that belong exclusively to the optional
// flightctl-observability stack (Prometheus + Grafana). They are excluded from
// the default bundle so that core-service deployments are not forced to mirror
// large third-party images. Mirror them separately when deploying the
// flightctl-observability RPM — see deploying-observability-linux.md.
var observabilityOnlyImages = map[string]bool{
	"grafana":    true,
	"prometheus": true,
}

// ChartMeta holds just the appVersion field from Chart.yaml.
// Used as the fallback tag for ImageSpec entries with no Tag.
type ChartMeta struct {
	AppVersion string `yaml:"appVersion"`
}

// -----------------------------------------------------------------------------
// ReadAppVersion
// -----------------------------------------------------------------------------

// ReadAppVersion reads appVersion from the Helm Chart.yaml at the given path.
func ReadAppVersion(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read Chart.yaml: %w", err)
	}

	var meta ChartMeta
	if err := yaml.Unmarshal(data, &meta); err != nil {
		return "", fmt.Errorf("parse Chart.yaml: %w", err)
	}

	return meta.AppVersion, nil
}

// -----------------------------------------------------------------------------
// ParseHelmChartOpts  (EDM-3958)
// -----------------------------------------------------------------------------

// ParseHelmChartOpts reads deploy/helm/helm-chart-opts.yaml and returns one
// ImagePair per image under the requested variant.
//
// Tag fallback: images whose Tag field is empty (e.g. api, worker, periodic)
// receive appVersion as their tag.  At packaging time the tag is overwritten
// with the release version; on a dev checkout appVersion is typically "latest".
func ParseHelmChartOpts(path, variant, appVersion string) ([]ImagePair, error) {
	logInfo("Parsing helm-chart-opts.yaml for variant '%s'...", variant)

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read helm-chart-opts.yaml: %w", err)
	}

	var opts HelmChartOpts
	if err := yaml.Unmarshal(data, &opts); err != nil {
		return nil, fmt.Errorf("parse helm-chart-opts.yaml: %w", err)
	}

	cv, ok := opts[variant]
	if !ok {
		return nil, fmt.Errorf("variant %q not found in helm-chart-opts.yaml", variant)
	}

	if len(cv.Images) == 0 {
		logWarn("No images found for variant '%s' in helm-chart-opts.yaml", variant)
		return nil, nil
	}

	logInfo("Found %d image entries in helm-chart-opts.yaml", len(cv.Images))

	pairs := make([]ImagePair, 0, len(cv.Images))
	for _, spec := range cv.Images {
		tag := spec.Tag
		if tag == "" || tag == "latest" {
			// No explicit tag, or tag is the "latest" placeholder: use the
			// chart appVersion (or --tag-override) so the bundled images match
			// the installed RPM version.
			tag = appVersion
		}
		image := normalizeDockerImage(spec.Image)
		pairs = append(pairs, ImagePair{
			Source: image + ":" + tag,
			Dest:   ImageToDest(image, tag),
		})
	}

	return pairs, nil
}

// -----------------------------------------------------------------------------
// ParseObsImages  (EDM-3959)
// -----------------------------------------------------------------------------

// ParseObsImages reads the RPM-only images file for the given variant.
//
// The el9/el10 files are also used by the RPM build to drive the quadlet
// renderer (packaging/rpm/flightctl.spec passes them to
// `flightctl-standalone render quadlets --config`), so they must remain
// complete.  The rhel9/rhel10 files cover only the images that differ for
// the downstream distribution (grafana, prometheus from registry.redhat.io).
// Dedup() in main.go drops any entries already covered by helm-chart-opts.
//
// File selection by variant:
//
//   - community-el9  → packaging/images/el9/images.yaml
//   - community-el10 → packaging/images/el10/images.yaml
//   - rhem-el9     → packaging/images/rhel9/images.yaml
//   - rhem-el10    → packaging/images/rhel10/images.yaml
//
// A missing file is treated as a non-fatal warning so the tool can still emit
// helm-chart-opts images even when the images file is absent.
func ParseObsImages(el9Path, el10Path, rhel9Path, rhel10Path, variant, tagFallback string) ([]ImagePair, error) {
	isRedhat := strings.Contains(variant, "rhem")
	isEl10 := strings.Contains(variant, "el10")

	var path, label string
	switch {
	case isRedhat && isEl10:
		path, label = rhel10Path, "rhel10"
	case isRedhat:
		path, label = rhel9Path, "rhel9"
	case isEl10:
		path, label = el10Path, "el10"
	default:
		path, label = el9Path, "el9"
	}

	logInfo("Parsing images from %s/images.yaml...", label)

	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		// Optional file — warn and continue
		logWarn("RPM images file not found: %s", path)
		logWarn("Skipping RPM-only image enumeration.")
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read RPM images file: %w", err)
	}

	var obs ObsImages
	if err := yaml.Unmarshal(data, &obs); err != nil {
		return nil, fmt.Errorf("parse RPM images file: %w", err)
	}

	logInfo("Found %d RPM-only image entries", len(obs))

	// For rhem variants every image is expected to come from registry.redhat.io;
	// warn only when community variants unexpectedly reference the downstream registry.
	warnOnRedhatRegistry := !strings.Contains(variant, "rhem")

	pairs := make([]ImagePair, 0, len(obs))
	for key, spec := range obs {
		if spec.Image == "" {
			logWarn("Skipping RPM image entry '%s': missing image field", key)
			continue
		}

		if observabilityOnlyImages[key] {
			logInfo("Skipping observability-only image '%s' (%s) — mirror separately for flightctl-observability", key, spec.Image)
			continue
		}

		tag := spec.Tag
		if tag == "" || tag == "latest" {
			// No explicit tag, or tag is the "latest" placeholder: apply the
			// effective tag (appVersion or --tag-override) so RPM-only images
			// such as pam-issuer and userinfo-proxy are bundled at the correct
			// release version rather than always pulling :latest.
			tag = tagFallback
		}

		if warnOnRedhatRegistry && strings.HasPrefix(spec.Image, "registry.redhat.io/") {
			logWarn("RPM image '%s' requires downstream registry access:", key)
			logWarn("  %s:%s", spec.Image, tag)
			logWarn("  Ensure the connected system has credentials for registry.redhat.io")
		}

		image := normalizeDockerImage(spec.Image)
		pairs = append(pairs, ImagePair{
			Source: image + ":" + tag,
			Dest:   ImageToDest(image, tag),
		})
	}

	return pairs, nil
}

// -----------------------------------------------------------------------------
// ParseRPMRequires  (EDM-3960)
// -----------------------------------------------------------------------------

// ParseRPMRequires scans the flightctl RPM spec file and returns a sorted,
// deduplicated list of runtime dependency package names.
//
// It collects all "Requires:" lines (not BuildRequires), strips version
// constraints (e.g. "openssl >= 1.1"), and excludes file-path dependencies
// that start with "/".
func ParseRPMRequires(specPath string) ([]string, error) {
	f, err := os.Open(specPath)
	if err != nil {
		return nil, fmt.Errorf("open spec file: %w", err)
	}
	defer f.Close()

	seen := make(map[string]struct{})

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()

		// Only process bare "Requires:" lines, not "BuildRequires:"
		if !strings.HasPrefix(line, "Requires:") {
			continue
		}

		// Second space-separated token is the package name (or constraint start)
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		pkg := fields[1]

		// Skip file-path dependencies (e.g. Requires: /bin/bash)
		if strings.HasPrefix(pkg, "/") {
			continue
		}

		// Strip inline version constraint: "openssl >= 1.1" → "openssl"
		pkg = strings.Split(pkg, "=")[0]
		pkg = strings.TrimRight(pkg, " \t<>!")
		pkg = strings.TrimSpace(pkg)

		if pkg != "" {
			seen[pkg] = struct{}{}
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan spec file: %w", err)
	}

	result := make([]string, 0, len(seen))
	for pkg := range seen {
		result = append(result, pkg)
	}
	sort.Strings(result)

	return result, nil
}
