package helm

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/chartutil"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/instrumentation/encryption"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
	"gopkg.in/yaml.v3"
)

const (
	defaultRenderTimeout = 5 * time.Minute
	helmRegistryConfig   = "/root/.config/helm/registry/config.json"
	containerAuthConfig  = "/root/.config/containers/auth.json"
	helmRepositoryConfig = "/root/.config/helm/repositories.yaml"
	registryCertsDir     = "/etc/containers/certs.d"
)

// Renderer renders a target Helm chart locally and extracts the image
// references from its Kubernetes workloads.
type Renderer struct {
	exec    executer.Executer
	logger  logrus.FieldLogger
	timeout time.Duration
}

func NewRenderer(exec executer.Executer, logger logrus.FieldLogger) *Renderer {
	if exec == nil {
		exec = executer.NewCommonExecuter()
	}
	if logger == nil {
		logger = logrus.New()
	}
	return &Renderer{exec: exec, logger: logger, timeout: defaultRenderTimeout}
}

// ImageRefs pulls the target chart, applies the same chart-relative values
// files and inline values as the agent, renders the release, and extracts its
// workload images. renderedConfig is the config content from the same rendered
// spec, so registry and repository configuration matches what the agent will
// receive. The device's current status determines whether this is an upgrade
// render. registrySpec supplies fallback credentials and transport settings
// when the chart registry is configured as a Repository resource.
func (r *Renderer) ImageRefs(
	ctx context.Context,
	app v1beta1.HelmApplication,
	device *domain.Device,
	renderedConfig []byte,
	registrySpec *domain.OciRepoSpec,
) ([]string, error) {
	imageSpec, err := app.AsImageApplicationProviderSpec()
	if err != nil {
		return nil, fmt.Errorf("get chart image reference: %w", err)
	}
	if imageSpec.Image == "" {
		return nil, fmt.Errorf("chart image reference is empty")
	}

	chartName, _, err := chartutil.ParseChartRef(imageSpec.Image)
	if err != nil {
		return nil, fmt.Errorf("parse chart reference: %w", err)
	}
	if chartName == "." || chartName == ".." || filepath.Base(chartName) != chartName {
		return nil, fmt.Errorf("invalid chart name %q in reference %q", chartName, imageSpec.Image)
	}
	releaseName, err := releaseNameForApp(app, imageSpec.Image)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()

	workDir, err := os.MkdirTemp("", "flightctl-helm-render-")
	if err != nil {
		return nil, fmt.Errorf("create temporary Helm render directory: %w", err)
	}
	defer func() {
		if err := os.RemoveAll(workDir); err != nil {
			r.logger.WithError(err).Warn("failed to remove temporary Helm render directory")
		}
	}()

	chartRoot := filepath.Join(workDir, "chart")
	configHome := filepath.Join(workDir, "config")
	cacheHome := filepath.Join(workDir, "cache")
	dataHome := filepath.Join(workDir, "data")
	for _, dir := range []string{chartRoot, configHome, cacheHome, dataHome} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("create temporary Helm directory: %w", err)
		}
	}

	registryConfig, repositoryConfig, transportArgs, err := prepareHelmConfig(ctx, workDir, configHome, renderedConfig, registrySpec, imageSpec.Image)
	if err != nil {
		return nil, err
	}
	helmEnv := helmEnvironment(configHome, cacheHome, dataHome)

	normalizedRef := chartutil.NormalizeChartRef(imageSpec.Image)
	chartRef, version := chartutil.SplitChartRef(normalizedRef)
	pullArgs := []string{"pull", chartRef, "--untar", "--destination", chartRoot}
	if version != "" {
		pullArgs = append(pullArgs, "--version", version)
	}
	if registryConfig != "" {
		pullArgs = append(pullArgs, "--registry-config", registryConfig)
	}
	pullArgs = append(pullArgs, transportArgs...)
	if err := r.run(ctx, workDir, helmEnv, pullArgs...); err != nil {
		return nil, fmt.Errorf("pull target chart %s: %w", imageSpec.Image, err)
	}

	chartPath := filepath.Join(chartRoot, chartName)
	dependencyArgs := []string{"dependency", "update", chartPath}
	if registryConfig != "" {
		dependencyArgs = append(dependencyArgs, "--registry-config", registryConfig)
	}
	if repositoryConfig != "" {
		dependencyArgs = append(dependencyArgs, "--repository-config", repositoryConfig)
	}
	dependencyArgs = append(dependencyArgs, transportArgs...)
	if err := r.run(ctx, workDir, helmEnv, dependencyArgs...); err != nil {
		return nil, fmt.Errorf("update dependencies for target chart %s: %w", imageSpec.Image, err)
	}

	valuesPaths, err := targetValuesFiles(chartPath, app.ValuesFiles)
	if err != nil {
		return nil, err
	}
	if app.Values != nil && len(*app.Values) > 0 {
		values, err := yaml.Marshal(*app.Values)
		if err != nil {
			return nil, fmt.Errorf("marshal inline Helm values: %w", err)
		}
		valuesPath := filepath.Join(workDir, "flightctl-values.yaml")
		if err := os.WriteFile(valuesPath, values, 0o600); err != nil {
			return nil, fmt.Errorf("write inline Helm values: %w", err)
		}
		valuesPaths = append(valuesPaths, valuesPath)
	}

	templateArgs := []string{"template", releaseName, chartPath, "--skip-tests"}
	namespace := chartutil.AppNamespace(app.Namespace, releaseName)
	if namespace != "" {
		templateArgs = append(templateArgs, "--namespace", namespace)
	}
	if releaseIsInstalled(device, releaseName) {
		templateArgs = append(templateArgs, "--is-upgrade")
	}
	for _, valuesPath := range valuesPaths {
		templateArgs = append(templateArgs, "--values", valuesPath)
	}
	manifests, err := r.output(ctx, workDir, helmEnv, templateArgs...)
	if err != nil {
		return nil, fmt.Errorf("render target chart %s: %w", imageSpec.Image, err)
	}
	images, err := chartutil.ExtractImagesFromManifests(manifests)
	if err != nil {
		return nil, fmt.Errorf("extract workload images from target chart %s: %w", imageSpec.Image, err)
	}
	slices.Sort(images)
	return images, nil
}

// ImageRefsCacheKey returns a content-addressed key for the extracted image
// references of a Helm application. The key includes all inputs that affect
// templating, including whether the release is installed, and the cache TTL so
// changing the configured TTL cannot reuse an entry written under another TTL.
func ImageRefsCacheKey(
	orgID uuid.UUID,
	app v1beta1.HelmApplication,
	device *domain.Device,
	renderedConfig []byte,
	ttl time.Duration,
) (string, error) {
	imageSpec, err := app.AsImageApplicationProviderSpec()
	if err != nil {
		return "", fmt.Errorf("get chart image reference: %w", err)
	}
	releaseName, err := releaseNameForApp(app, imageSpec.Image)
	if err != nil {
		return "", err
	}
	appJSON, err := json.Marshal(app)
	if err != nil {
		return "", fmt.Errorf("encode Helm application for image refs cache key: %w", err)
	}
	appHash := sha256.Sum256(appJSON)
	configHash := sha256.Sum256(renderedConfig)
	keyMaterial, err := json.Marshal(struct {
		OrgID              string
		AppHash            string
		ConfigHash         string
		ReleaseIsInstalled bool
		TTL                int64
	}{
		OrgID:              orgID.String(),
		AppHash:            hex.EncodeToString(appHash[:]),
		ConfigHash:         hex.EncodeToString(configHash[:]),
		ReleaseIsInstalled: releaseIsInstalled(device, releaseName),
		TTL:                int64(ttl),
	})
	if err != nil {
		return "", fmt.Errorf("encode Helm image refs cache key: %w", err)
	}
	keyHash := sha256.Sum256(keyMaterial)
	return "helmImageRefs/v2/" + orgID.String() + "/" + hex.EncodeToString(keyHash[:]), nil
}

func releaseNameForApp(app v1beta1.HelmApplication, chartRef string) (string, error) {
	if app.Name != nil && *app.Name != "" {
		return *app.Name, nil
	}
	releaseName, err := chartutil.SanitizeReleaseName(chartRef)
	if err != nil {
		return "", fmt.Errorf("create release name: %w", err)
	}
	return releaseName, nil
}

func (r *Renderer) run(ctx context.Context, workDir string, env []string, args ...string) error {
	_, stderr, exitCode := r.exec.ExecuteWithContextFromDir(ctx, workDir, "helm", args, env...)
	if exitCode != 0 {
		return fmt.Errorf("helm %s failed with code %d: %s", args[0], exitCode, strings.TrimSpace(stderr))
	}
	return nil
}

func (r *Renderer) output(ctx context.Context, workDir string, env []string, args ...string) (string, error) {
	stdout, stderr, exitCode := r.exec.ExecuteWithContextFromDir(ctx, workDir, "helm", args, env...)
	if exitCode != 0 {
		return "", fmt.Errorf("helm %s failed with code %d: %s", args[0], exitCode, strings.TrimSpace(stderr))
	}
	return stdout, nil
}

func helmEnvironment(configHome, cacheHome, dataHome string) []string {
	env := make([]string, 0, len(os.Environ())+3)
	for _, item := range os.Environ() {
		key, _, _ := strings.Cut(item, "=")
		if key == "HELM_CONFIG_HOME" || key == "HELM_CACHE_HOME" || key == "HELM_DATA_HOME" ||
			key == "HELM_REGISTRY_CONFIG" || key == "HELM_REPOSITORY_CONFIG" ||
			key == "HELM_REPOSITORY_CACHE" || key == "HELM_PLUGINS" {
			continue
		}
		env = append(env, item)
	}
	return append(env,
		"HELM_CONFIG_HOME="+configHome,
		"HELM_CACHE_HOME="+cacheHome,
		"HELM_DATA_HOME="+dataHome,
	)
}

func targetValuesFiles(chartPath string, valuesFiles *[]string) ([]string, error) {
	if valuesFiles == nil {
		return nil, nil
	}
	resolvedChartPath, err := filepath.EvalSymlinks(chartPath)
	if err != nil {
		return nil, fmt.Errorf("resolve Helm chart directory: %w", err)
	}
	paths := make([]string, 0, len(*valuesFiles))
	for _, valueFile := range *valuesFiles {
		if filepath.IsAbs(valueFile) {
			return nil, fmt.Errorf("Helm values file path must be relative to the chart: %s", valueFile)
		}
		cleanPath := filepath.Clean(valueFile)
		absPath := filepath.Join(chartPath, cleanPath)
		rel, err := filepath.Rel(chartPath, absPath)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return nil, fmt.Errorf("Helm values file path escapes the chart directory: %s", valueFile)
		}
		info, err := os.Stat(absPath)
		if err != nil {
			return nil, fmt.Errorf("check Helm values file %s: %w", valueFile, err)
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("Helm values path is not a regular file: %s", valueFile)
		}
		resolvedValuePath, err := filepath.EvalSymlinks(absPath)
		if err != nil {
			return nil, fmt.Errorf("resolve Helm values file %s: %w", valueFile, err)
		}
		resolvedRel, err := filepath.Rel(resolvedChartPath, resolvedValuePath)
		if err != nil || resolvedRel == ".." || strings.HasPrefix(resolvedRel, ".."+string(filepath.Separator)) {
			return nil, fmt.Errorf("Helm values file symlink escapes the chart directory: %s", valueFile)
		}
		paths = append(paths, absPath)
	}
	return paths, nil
}

func releaseIsInstalled(device *domain.Device, releaseName string) bool {
	if device == nil || device.Status == nil {
		return false
	}
	for _, appStatus := range device.Status.Applications {
		if appStatus.Name == releaseName {
			return true
		}
	}
	return false
}

func chartRegistryHost(chartRef string) (string, error) {
	normalized := chartutil.NormalizeChartRef(chartRef)
	parsed, err := url.Parse(normalized)
	if err != nil {
		return "", fmt.Errorf("parse Helm chart reference %q: %w", chartRef, err)
	}
	if parsed.Scheme != "oci" || parsed.Host == "" {
		return "", fmt.Errorf("Helm chart reference %q is not an OCI reference", chartRef)
	}
	return parsed.Host, nil
}

func authConfigFromRepo(ctx context.Context, spec *domain.OciRepoSpec) ([]byte, error) {
	if spec == nil || spec.OciAuth == nil {
		return nil, nil
	}
	dockerAuth, err := spec.OciAuth.AsDockerAuth()
	if err != nil {
		return nil, fmt.Errorf("decode chart registry credentials: %w", err)
	}
	password, _, err := encryption.Decrypt(ctx, encryption.Ciphertext(dockerAuth.Password))
	if err != nil {
		return nil, fmt.Errorf("decrypt chart registry credentials: %w", err)
	}
	if dockerAuth.Username == "" || len(password) == 0 {
		return nil, fmt.Errorf("chart registry credentials are incomplete")
	}
	type dockerAuthEntry struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Auth     string `json:"auth"`
	}
	config := struct {
		Auths map[string]dockerAuthEntry `json:"auths"`
	}{
		Auths: map[string]dockerAuthEntry{
			spec.Registry: {
				Username: dockerAuth.Username,
				Password: string(password),
				Auth:     base64.StdEncoding.EncodeToString([]byte(dockerAuth.Username + ":" + string(password))),
			},
		},
	}
	data, err := json.Marshal(config)
	if err != nil {
		return nil, fmt.Errorf("marshal chart registry credentials: %w", err)
	}
	return data, nil
}
