package provider

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/applications/helm"
	"github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/samber/lo"
	"gopkg.in/yaml.v3"
)

const (
	// helmValuesPath is the base directory where provider-generated values files are stored.
	helmValuesPath = "/var/lib/flightctl/helm/values"
	// HelmValuesFileName is the name of the provider-generated values file.
	helmValuesFileName = "flightctl-values.yaml"
)

// GetHelmProviderValuesPath returns the absolute path to the provider-generated values file
// for a given application name.
func GetHelmProviderValuesPath(appName string) string {
	return filepath.Join(helmValuesPath, appName, helmValuesFileName)
}

var _ appTypeHandler = (*helmHandler)(nil)

type helmHandler struct {
	name    string
	spec    *v1beta1.ImageApplicationProviderSpec
	clients client.CLIClients
	rw      fileio.ReadWriter
	log     *log.PrefixLogger
}

func (h *helmHandler) chartRef() string {
	return h.spec.Image
}

func (h *helmHandler) values() map[string]interface{} {
	return lo.FromPtr(h.spec.Values)
}

func (h *helmHandler) valuesFiles() []string {
	return lo.FromPtr(h.spec.ValuesFiles)
}

func (h *helmHandler) Verify(ctx context.Context, path string) error {
	errs := v1beta1.ValidateHelmImageApplicationSpec(h.name, h.spec)
	if err := ensureDependenciesFromAppType([]string{"helm"}); err != nil {
		errs = append(errs, fmt.Errorf("ensuring dependencies: %w", err))
	}
	if len(errs) > 0 {
		return fmt.Errorf("validating helm app: %w", errors.Join(errs...))
	}

	if h.clients.Kube().Binary() == "" {
		return fmt.Errorf("kubectl or oc not installed for app %s", h.name)
	}

	version, err := h.clients.Helm().Version(ctx)
	if err != nil {
		return fmt.Errorf("helm version: %w", err)
	}
	if !version.GreaterOrEqual(3, 8) {
		return fmt.Errorf("helm version >= 3.8 required for OCI support, found %d.%d.%d", version.Major, version.Minor, version.Patch)
	}

	resolved, err := h.clients.Helm().IsResolved(h.chartRef())
	if err != nil {
		return fmt.Errorf("check chart resolved: %w", err)
	}
	if !resolved {
		return fmt.Errorf("chart %s not resolved", h.chartRef())
	}

	kubeconfigPath, err := h.clients.Kube().ResolveKubeconfig()
	if err != nil {
		h.log.Warnf("Cluster not available for Helm app %s: %v (will retry)", h.name, err)
		return fmt.Errorf("resolve kubeconfig: %w: %w", err, errors.ErrRetryable)
	}

	chartPath := h.AppPath()

	valuesPaths, cleanup, err := resolveHelmValues(h.name, chartPath, h.valuesFiles(), h.spec.Values, path, h.rw)
	if err != nil {
		return fmt.Errorf("resolving values: %w", err)
	}
	defer cleanup()

	var lintOpts []client.HelmOption
	if len(valuesPaths) > 0 {
		lintOpts = append(lintOpts, client.WithValuesFiles(valuesPaths))
	}

	if err := h.clients.Helm().Lint(ctx, chartPath, lintOpts...); err != nil {
		return fmt.Errorf("helm lint failed: %w", err)
	}

	dryRunOpts := []client.HelmOption{
		client.WithKubeconfig(kubeconfigPath),
		client.WithCreateNamespace(),
	}
	if len(valuesPaths) > 0 {
		dryRunOpts = append(dryRunOpts, client.WithValuesFiles(valuesPaths))
	}

	if _, err := h.clients.Helm().DryRun(ctx, h.name, chartPath, dryRunOpts...); err != nil {
		if errors.Is(err, errors.ErrNetwork) || errors.IsTimeoutError(err) {
			h.log.Warnf("Cluster not reachable for Helm dry-run of %s: %v (will retry)", h.name, err)
			return fmt.Errorf("helm dry-run validation failed: %w: %w", err, errors.ErrRetryable)
		}
		return fmt.Errorf("helm dry-run validation failed: %w", err)
	}

	return nil
}

func (h *helmHandler) Install(ctx context.Context) error {
	values := h.values()
	if len(values) == 0 {
		return nil
	}

	valuesDir := h.valuesDir()
	if err := h.rw.MkdirAll(valuesDir, fileio.DefaultDirectoryPermissions); err != nil {
		return fmt.Errorf("create values directory: %w", err)
	}

	if err := writeFlightctlHelmValues(ctx, &values, valuesDir, h.rw); err != nil {
		return fmt.Errorf("write helm values file: %w", err)
	}

	return nil
}

func (h *helmHandler) Remove(ctx context.Context) error {
	valuesDir := h.valuesDir()
	exists, err := h.rw.PathExists(valuesDir)
	if err != nil {
		return fmt.Errorf("check values directory: %w", err)
	}
	if exists {
		if err := h.rw.RemoveAll(valuesDir); err != nil {
			return fmt.Errorf("remove values directory: %w", err)
		}
	}
	return nil
}

func (h *helmHandler) AppPath() string {
	return h.clients.Helm().GetChartPath(h.chartRef())
}

func (h *helmHandler) ID() string {
	return fmt.Sprintf("%s_%s", helm.AppNamespace(h.spec.Namespace, h.name), h.name)
}

func (h *helmHandler) Volumes() ([]*Volume, error) {
	return nil, nil
}

func (h *helmHandler) valuesDir() string {
	return filepath.Join(helmValuesPath, h.name)
}

// ProviderValuesPath returns the absolute path to the provider-generated values file.
// Returns empty string if there are no inline values.
func (h *helmHandler) ProviderValuesPath() string {
	if len(h.values()) == 0 {
		return ""
	}
	return filepath.Join(h.valuesDir(), helmValuesFileName)
}

func writeFlightctlHelmValues(_ context.Context, values *map[string]any, dir string, rw fileio.ReadWriter) error {
	if values != nil && len(*values) > 0 {
		data, err := yaml.Marshal(values)
		if err != nil {
			return fmt.Errorf("marshal values: %w", err)
		}
		if err := rw.WriteFile(filepath.Join(dir, helmValuesFileName), data, fileio.DefaultFilePermissions); err != nil {
			return fmt.Errorf("write flightctl values: %w", err)
		}
	}
	return nil
}

// resolveHelmValues resolves values files to absolute paths and writes inline values.
// It validates that chart-relative values files exist in the chart directory.
// If tempPath is provided, inline values are written there; otherwise a temp directory is created.
// Returns the list of absolute values file paths, a cleanup function (may be nil), and any error.
func resolveHelmValues(
	appName string,
	chartPath string,
	valuesFiles []string,
	values *map[string]interface{},
	tempPath string,
	rw fileio.ReadWriter,
) ([]string, func(), error) {
	var paths []string

	for _, vf := range valuesFiles {
		absPath := filepath.Join(chartPath, vf)
		exists, err := rw.PathExists(absPath)
		if err != nil {
			return nil, nil, fmt.Errorf("check values file %s: %w", vf, err)
		}
		if !exists {
			return nil, nil, fmt.Errorf("values file not found in chart: %s", vf)
		}
		paths = append(paths, absPath)
	}

	cleanupFn := func() {}
	if values != nil && len(*values) > 0 {
		if tempPath != "" {
			valuesPath := filepath.Join(tempPath, helmValuesFileName)
			valuesData, err := yaml.Marshal(values)
			if err != nil {
				return nil, nil, fmt.Errorf("marshal inline values: %w", err)
			}
			if err := rw.WriteFile(valuesPath, valuesData, fileio.DefaultFilePermissions); err != nil {
				return nil, nil, fmt.Errorf("write inline values: %w", err)
			}
			paths = append(paths, valuesPath)
		} else {
			valuesPath, cleanup, err := writeHelmValuesTemp(rw, appName, *values)
			if err != nil {
				return nil, nil, fmt.Errorf("write inline values: %w", err)
			}
			cleanupFn = cleanup
			paths = append(paths, valuesPath)
		}
	}

	return paths, cleanupFn, nil
}

// writeHelmValuesTemp writes Helm values to a temporary file for use with helm dry-run.
func writeHelmValuesTemp(rw fileio.ReadWriter, appName string, values map[string]interface{}) (string, func(), error) {
	valuesData, err := yaml.Marshal(values)
	if err != nil {
		return "", nil, fmt.Errorf("marshal values: %w", err)
	}

	tmpDir, err := rw.MkdirTemp("helm_values")
	if err != nil {
		return "", nil, fmt.Errorf("create temp dir: %w", err)
	}

	valuesPath := filepath.Join(tmpDir, appName+"-values.yaml")
	if err := rw.WriteFile(valuesPath, valuesData, fileio.DefaultFilePermissions); err != nil {
		_ = rw.RemoveAll(tmpDir)
		return "", nil, fmt.Errorf("write values file: %w", err)
	}

	cleanup := func() {
		_ = rw.RemoveAll(tmpDir)
	}

	return valuesPath, cleanup, nil
}
