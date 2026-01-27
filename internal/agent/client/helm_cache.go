package client

import (
	"context"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/containers/image/v5/docker/reference"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/pkg/log"
)

const (
	chartReadyMarkerFile = ".flightctl-chart-ready"
	chartYAMLFile        = "Chart.yaml"
	// HelmChartsDir is the subdirectory within the data directory where helm charts are cached.
	HelmChartsDir = "helm/charts"
)

type helmChartCache struct {
	helm       *Helm
	chartsDir  string
	readWriter fileio.ReadWriter
	log        *log.PrefixLogger
}

func newHelmChartCache(helm *Helm, chartsDir string, readWriter fileio.ReadWriter, log *log.PrefixLogger) *helmChartCache {
	return &helmChartCache{
		helm:       helm,
		chartsDir:  chartsDir,
		readWriter: readWriter,
		log:        log,
	}
}

func (c *helmChartCache) ChartDir(chartRef string) string {
	return filepath.Join(c.chartsDir, SanitizeChartRef(chartRef))
}

func (c *helmChartCache) IsChartResolved(chartDir string) (bool, error) {
	markerPath := filepath.Join(chartDir, chartReadyMarkerFile)
	return c.readWriter.PathExists(markerPath, fileio.WithSkipContentCheck())
}

func (c *helmChartCache) MarkChartResolved(chartDir string) error {
	markerPath := filepath.Join(chartDir, chartReadyMarkerFile)
	return c.readWriter.WriteFile(markerPath, []byte{}, fileio.DefaultFilePermissions)
}

func (c *helmChartCache) ChartExists(chartDir string) (bool, error) {
	exists, err := c.readWriter.PathExists(chartDir)
	if err != nil {
		return false, fmt.Errorf("check chart directory: %w", err)
	}
	if !exists {
		return false, nil
	}

	chartYAMLPath := filepath.Join(chartDir, chartYAMLFile)
	exists, err = c.readWriter.PathExists(chartYAMLPath)
	if err != nil {
		return false, fmt.Errorf("check Chart.yaml: %w", err)
	}
	return exists, nil
}

func (c *helmChartCache) RemoveChart(chartDir string) error {
	exists, err := c.readWriter.PathExists(chartDir)
	if err != nil {
		return fmt.Errorf("check chart directory: %w", err)
	}
	if !exists {
		return nil
	}

	if err := c.readWriter.RemoveAll(chartDir); err != nil {
		return fmt.Errorf("remove chart directory: %w", err)
	}
	return nil
}

// RemoveChartByRef removes a cached chart by its reference.
// It resolves the chart path internally using the chart reference.
func (c *helmChartCache) RemoveChartByRef(chartRef string) error {
	chartDir := c.ChartDir(chartRef)
	return c.RemoveChart(chartDir)
}

func (c *helmChartCache) EnsureChartsDir() error {
	exists, err := c.readWriter.PathExists(c.chartsDir)
	if err != nil {
		return fmt.Errorf("check charts directory: %w", err)
	}
	if exists {
		return nil
	}

	if err := c.readWriter.MkdirAll(c.chartsDir, fileio.DefaultDirectoryPermissions); err != nil {
		return fmt.Errorf("create charts directory: %w", err)
	}
	return nil
}

func (c *helmChartCache) ResolveChart(ctx context.Context, chartRef, chartDir string, opts ...ClientOption) error {
	resolved, err := c.IsChartResolved(chartDir)
	if err != nil {
		return fmt.Errorf("check if chart resolved: %w", err)
	}
	if resolved {
		return nil
	}

	chartExists, err := c.ChartExists(chartDir)
	if err != nil {
		return fmt.Errorf("check if chart exists: %w", err)
	}

	if !chartExists {
		if err := c.RemoveChart(chartDir); err != nil {
			return fmt.Errorf("remove stale chart directory: %w", err)
		}

		if err := c.EnsureChartsDir(); err != nil {
			return err
		}

		if err := c.helm.Pull(ctx, chartRef, c.chartsDir, opts...); err != nil {
			return fmt.Errorf("pull chart: %w", err)
		}

		chartName, _, err := ParseChartRef(chartRef)
		if err != nil {
			return fmt.Errorf("parse chart ref for rename: %w", err)
		}
		extractedDir := filepath.Join(c.chartsDir, chartName)
		if extractedDir != chartDir {
			if err := c.readWriter.Rename(extractedDir, chartDir); err != nil {
				return fmt.Errorf("rename chart directory: %w", err)
			}
		}
	}

	if err := c.helm.DependencyUpdate(ctx, chartDir, opts...); err != nil {
		return fmt.Errorf("update dependencies: %w", err)
	}

	if err := c.MarkChartResolved(chartDir); err != nil {
		return fmt.Errorf("mark chart resolved: %w", err)
	}

	return nil
}

func (c *helmChartCache) Pull(ctx context.Context, chartRef string, opts ...ClientOption) error {
	chartDir := c.ChartDir(chartRef)
	return c.ResolveChart(ctx, chartRef, chartDir, opts...)
}

func (c *helmChartCache) IsResolved(chartRef string) (bool, error) {
	chartDir := c.ChartDir(chartRef)
	return c.IsChartResolved(chartDir)
}

func (c *helmChartCache) GetChartPath(chartRef string) string {
	return c.ChartDir(chartRef)
}

// ChartRefType indicates whether a chart reference uses a tag or digest.
type ChartRefType int

const (
	ChartRefTypeTag ChartRefType = iota
	ChartRefTypeDigest
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

// SanitizeChartRef converts a chart reference into a filesystem-safe directory name.
func SanitizeChartRef(chartRef string) string {
	ref := strings.TrimPrefix(chartRef, "oci://")
	ref = strings.ReplaceAll(ref, "/", "_")
	ref = strings.ReplaceAll(ref, ":", "_")
	ref = strings.ReplaceAll(ref, "@", "_")
	return ref
}
