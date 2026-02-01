package provider

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

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
	helmValuesPath     = "/var/lib/flightctl/helm/values"
	helmValuesFileName = "flightctl-values.yaml"
)

// GetHelmProviderValuesPath returns the absolute path to the provider-generated values file
// for a given application name.
func GetHelmProviderValuesPath(appName string) string {
	return filepath.Join(helmValuesPath, appName, helmValuesFileName)
}

type helmProvider struct {
	log       *log.PrefixLogger
	clients   client.CLIClients
	rw        fileio.ReadWriter
	spec      *ApplicationSpec
	namespace string
}

func newHelmProvider(
	ctx context.Context,
	log *log.PrefixLogger,
	clients client.CLIClients,
	apiSpec *v1beta1.ApplicationProviderSpec,
	rwFactory fileio.ReadWriterFactory,
	cfg *parseConfig,
) (*helmProvider, error) {
	helmApp, err := (*apiSpec).AsHelmApplication()
	if err != nil {
		return nil, fmt.Errorf("getting helm application: %w", err)
	}

	appName := lo.FromPtr(helmApp.Name)
	if appName == "" {
		appName, err = helm.SanitizeReleaseName(helmApp.Image)
		if err != nil {
			return nil, fmt.Errorf("creating release name: %w", err)
		}
	}

	namespace := helm.AppNamespace(helmApp.Namespace, appName)

	chartPath := clients.Helm().GetChartPath(helmApp.Image)

	volumeManager, err := NewVolumeManager(log, appName, v1beta1.AppTypeHelm, v1beta1.CurrentProcessUsername, nil)
	if err != nil {
		return nil, err
	}

	rw, err := rwFactory(v1beta1.CurrentProcessUsername)
	if err != nil {
		return nil, fmt.Errorf("creating read/writer: %w", err)
	}

	return &helmProvider{
		log:       log,
		clients:   clients,
		rw:        rw,
		namespace: namespace,
		spec: &ApplicationSpec{
			Name:    appName,
			ID:      fmt.Sprintf("%s_%s", namespace, appName),
			AppType: v1beta1.AppTypeHelm,
			Path:    chartPath,
			HelmApp: &helmApp,
			Volume:  volumeManager,
		},
	}, nil
}

func (p *helmProvider) chartRef() string {
	return p.spec.HelmApp.Image
}

func (p *helmProvider) values() map[string]interface{} {
	return lo.FromPtr(p.spec.HelmApp.Values)
}

func (p *helmProvider) valuesFiles() []string {
	return lo.FromPtr(p.spec.HelmApp.ValuesFiles)
}

func (p *helmProvider) valuesDir() string {
	return filepath.Join(helmValuesPath, p.spec.Name)
}

func (p *helmProvider) Verify(ctx context.Context) error {
	var apiSpec v1beta1.ApplicationProviderSpec
	err := apiSpec.FromHelmApplication(*p.spec.HelmApp)
	if err != nil {
		return fmt.Errorf("validating application spec: %w", err)
	}
	if errs := v1beta1.ValidateHelmApplication(apiSpec, p.spec.Name, false); len(errs) > 0 {
		return fmt.Errorf("validating helm application: %w", errors.Join(errs...))
	}

	if err := ensureDependenciesFromAppType([]string{"helm"}); err != nil {
		return fmt.Errorf("%w: ensuring dependencies: %w", errors.ErrNoRetry, err)
	}

	if p.clients.Kube().Binary() == "" {
		return fmt.Errorf("kubectl or oc not installed for app %s", p.spec.Name)
	}

	version, err := p.clients.Helm().Version(ctx)
	if err != nil {
		return fmt.Errorf("helm version: %w", err)
	}
	if !version.GreaterOrEqual(3, 8) {
		return fmt.Errorf("helm version >= 3.8 required for OCI support, found %d.%d.%d", version.Major, version.Minor, version.Patch)
	}

	resolved, err := p.clients.Helm().IsResolved(p.chartRef())
	if err != nil {
		return fmt.Errorf("check chart resolved: %w", err)
	}
	if !resolved {
		return fmt.Errorf("chart %s not resolved", p.chartRef())
	}

	kubeconfigPath, err := p.clients.Kube().ResolveKubeconfig()
	if err != nil {
		p.log.Warnf("Cluster not available for Helm app %s: %v (will retry)", p.spec.Name, err)
		return fmt.Errorf("resolve kubeconfig: %w: %w", err, errors.ErrRetryable)
	}

	chartPath := p.spec.Path
	valuesPaths, cleanup, err := resolveHelmValues(p.spec.Name, chartPath, p.valuesFiles(), p.spec.HelmApp.Values, "", p.rw)
	if err != nil {
		return fmt.Errorf("resolving values: %w", err)
	}
	defer cleanup()

	var lintOpts []client.HelmOption
	if len(valuesPaths) > 0 {
		lintOpts = append(lintOpts, client.WithValuesFiles(valuesPaths))
	}

	if err := p.clients.Helm().Lint(ctx, chartPath, lintOpts...); err != nil {
		return fmt.Errorf("helm lint failed: %w", err)
	}

	dryRunOpts := []client.HelmOption{
		client.WithKubeconfig(kubeconfigPath),
		client.WithNamespace(p.namespace),
		client.WithCreateNamespace(),
	}
	if len(valuesPaths) > 0 {
		dryRunOpts = append(dryRunOpts, client.WithValuesFiles(valuesPaths))
	}

	if _, err := p.clients.Helm().DryRun(ctx, p.spec.Name, chartPath, dryRunOpts...); err != nil {
		if errors.Is(err, errors.ErrNetwork) || errors.IsTimeoutError(err) {
			p.log.Warnf("Cluster not reachable for Helm dry-run of %s: %v (will retry)", p.spec.Name, err)
			return fmt.Errorf("helm dry-run validation failed: %w: %w", err, errors.ErrRetryable)
		}
		return fmt.Errorf("helm dry-run validation failed: %w", err)
	}

	return nil
}

func (p *helmProvider) Install(ctx context.Context) error {
	values := p.values()
	if len(values) == 0 {
		return nil
	}

	valuesDir := p.valuesDir()
	if err := p.rw.MkdirAll(valuesDir, fileio.DefaultDirectoryPermissions); err != nil {
		return fmt.Errorf("create values directory: %w", err)
	}

	if err := writeFlightctlHelmValues(ctx, &values, valuesDir, p.rw); err != nil {
		return fmt.Errorf("write helm values file: %w", err)
	}

	return nil
}

func (p *helmProvider) Remove(ctx context.Context) error {
	valuesDir := p.valuesDir()
	exists, err := p.rw.PathExists(valuesDir)
	if err != nil {
		return fmt.Errorf("check values directory: %w", err)
	}
	if exists {
		if err := p.rw.RemoveAll(valuesDir); err != nil {
			return fmt.Errorf("remove values directory: %w", err)
		}
	}
	return nil
}

func (p *helmProvider) Name() string {
	return p.spec.Name
}

func (p *helmProvider) ID() string {
	return p.spec.ID
}

func (p *helmProvider) Spec() *ApplicationSpec {
	return p.spec
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

	absChartPath, err := filepath.Abs(filepath.Clean(chartPath))
	if err != nil {
		return nil, nil, fmt.Errorf("resolve chart path: %w", err)
	}

	for _, vf := range valuesFiles {
		if filepath.IsAbs(vf) {
			return nil, nil, fmt.Errorf("values file path must be relative: %s", vf)
		}

		absJoined, err := filepath.Abs(filepath.Join(absChartPath, filepath.Clean(vf)))
		if err != nil {
			return nil, nil, fmt.Errorf("resolve values file path %s: %w", vf, err)
		}

		rel, err := filepath.Rel(absChartPath, absJoined)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return nil, nil, fmt.Errorf("values file path escapes chart directory: %s", vf)
		}

		exists, err := rw.PathExists(absJoined)
		if err != nil {
			return nil, nil, fmt.Errorf("check values file %s: %w", vf, err)
		}
		if !exists {
			return nil, nil, fmt.Errorf("values file not found in chart: %s", vf)
		}
		paths = append(paths, absJoined)
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
