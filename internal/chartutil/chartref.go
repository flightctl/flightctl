package chartutil

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/containers/image/v5/docker/reference"
)

// ParseChartRef extracts the chart name and version/digest from a chart reference.
// Supports both tag-based (oci://registry/chart:version) and digest-based (oci://registry/chart@sha256:...) references.
func ParseChartRef(chartRef string) (name, version string, err error) {
	ref := strings.TrimPrefix(chartRef, "oci://")

	parsed, err := reference.ParseNormalizedNamed(ref)
	if err != nil {
		return "", "", fmt.Errorf("parse chart reference: %w", err)
	}

	pathParts := strings.Split(reference.Path(parsed), "/")
	name = pathParts[len(pathParts)-1]
	if name == "" {
		return "", "", fmt.Errorf("chart reference missing chart name: %s", chartRef)
	}

	if digested, ok := parsed.(reference.Digested); ok {
		version = digested.Digest().String()
	} else if tagged, ok := parsed.(reference.Tagged); ok {
		version = tagged.Tag()
	} else {
		return "", "", fmt.Errorf("chart reference missing version tag or digest: %s", chartRef)
	}

	return name, version, nil
}

// SplitChartRef splits a chart reference into the chart path and version components.
// For tag-based references (oci://registry/chart:version), returns (oci://registry/chart, version).
// For digest-based references (oci://registry/chart@sha256:...), returns (chartRef, "") since
// the digest must remain part of the URL for helm pull.
func SplitChartRef(chartRef string) (chartPath, version string) {
	ref := chartRef
	hasOCIPrefix := strings.HasPrefix(ref, "oci://")
	if hasOCIPrefix {
		ref = strings.TrimPrefix(ref, "oci://")
	}

	parsed, err := reference.ParseNormalizedNamed(ref)
	if err != nil {
		return chartRef, ""
	}

	if tagged, ok := parsed.(reference.Tagged); ok {
		version = tagged.Tag()
		trimmed := reference.TrimNamed(parsed)
		if hasOCIPrefix {
			chartPath = "oci://" + trimmed.String()
		} else {
			chartPath = trimmed.String()
		}
		return chartPath, version
	}

	if _, ok := parsed.(reference.Digested); ok {
		return chartRef, ""
	}

	return chartRef, ""
}

// NormalizeChartRef ensures a chart reference has the oci:// scheme.
// If no scheme is present, it assumes OCI and adds the prefix.
func NormalizeChartRef(chartRef string) string {
	parsed, err := url.Parse(chartRef)
	if err != nil || parsed.Scheme == "" {
		return "oci://" + chartRef
	}
	return chartRef
}
