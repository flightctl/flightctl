package client

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
	"gopkg.in/yaml.v3"
)

const (
	helmCmd            = "helm"
	defaultHelmTimeout = 5 * time.Minute
)

// HelmOption is a functional option for configuring helm operations.
type HelmOption func(*helmOptions)

type helmOptions struct {
	namespace         string
	valuesPaths       []string
	kubeconfigPath    string
	atomic            bool
	rollbackOnFailure bool
	createNamespace   bool
	install           bool
	timeout           time.Duration
	postRendererPath  string
	postRendererArgs  []string
	ignoreNotFound    bool
	helmEnv           map[string]string
}

// WithHelmEnv sets custom environment variables for this helm operation.
func WithHelmEnv(env map[string]string) HelmOption {
	return func(opts *helmOptions) {
		opts.helmEnv = env
	}
}

// WithNamespace sets the Kubernetes namespace for helm operations.
func WithNamespace(namespace string) HelmOption {
	return func(opts *helmOptions) {
		opts.namespace = namespace
	}
}

// WithValuesFile sets a single values file path for helm operations.
func WithValuesFile(path string) HelmOption {
	return func(opts *helmOptions) {
		opts.valuesPaths = []string{path}
	}
}

// WithValuesFiles sets multiple values file paths for helm operations.
func WithValuesFiles(paths []string) HelmOption {
	return func(opts *helmOptions) {
		opts.valuesPaths = paths
	}
}

// WithKubeconfig sets the kubeconfig file path for helm operations.
func WithKubeconfig(path string) HelmOption {
	return func(opts *helmOptions) {
		opts.kubeconfigPath = path
	}
}

// WithAtomic enables atomic mode for helm install/upgrade operations.
func WithAtomic() HelmOption {
	return func(opts *helmOptions) {
		opts.atomic = true
	}
}

// WithRollbackOnFailure enables the --rollback-on-failure flag for helm upgrade operations.
// This is the Helm 4.x replacement for --atomic.
func WithRollbackOnFailure() HelmOption {
	return func(opts *helmOptions) {
		opts.rollbackOnFailure = true
	}
}

// WithCreateNamespace enables automatic namespace creation for helm operations.
func WithCreateNamespace() HelmOption {
	return func(opts *helmOptions) {
		opts.createNamespace = true
	}
}

// WithInstall enables the --install flag for helm upgrade operations.
func WithInstall() HelmOption {
	return func(opts *helmOptions) {
		opts.install = true
	}
}

// WithHelmTimeout sets a custom timeout for helm operations.
func WithHelmTimeout(timeout time.Duration) HelmOption {
	return func(opts *helmOptions) {
		opts.timeout = timeout
	}
}

// WithPostRenderer sets the post-renderer binary path and arguments.
// The post-renderer is invoked after Helm renders templates, allowing
// modification of the manifests before they are applied.
func WithPostRenderer(path string, args ...string) HelmOption {
	return func(opts *helmOptions) {
		opts.postRendererPath = path
		opts.postRendererArgs = args
	}
}

// WithIgnoreNotFound treats "release not found" as a successful uninstall.
func WithIgnoreNotFound() HelmOption {
	return func(opts *helmOptions) {
		opts.ignoreNotFound = true
	}
}

// Helm provides a client for executing helm CLI commands.
type Helm struct {
	exec       executer.Executer
	log        *log.PrefixLogger
	timeout    time.Duration
	readWriter fileio.ReadWriter
	cache      *helmChartCache
}

// NewHelm creates a new Helm client with the specified logger, executor, and data directory.
func NewHelm(log *log.PrefixLogger, exec executer.Executer, readWriter fileio.ReadWriter, dataDir string) *Helm {
	chartsDir := filepath.Join(dataDir, HelmChartsDir)
	h := &Helm{
		log:        log,
		exec:       exec,
		timeout:    defaultHelmTimeout,
		readWriter: readWriter,
	}
	h.cache = newHelmChartCache(h, chartsDir, readWriter, log)
	return h
}

// helmEnv returns custom environment variables for helm commands.
// It inherits the current environment and adds any custom helm-specific variables from options.
func helmEnv(opts *helmOptions) []string {
	if opts == nil || opts.helmEnv == nil {
		return nil
	}
	env := os.Environ()
	for k, v := range opts.helmEnv {
		env = append(env, k+"="+v)
	}
	return env
}

// HelmVersion represents a parsed helm CLI version.
type HelmVersion struct {
	Major int
	Minor int
	Patch int
}

// GreaterOrEqual returns true if this version is greater than or equal to the specified major.minor version.
func (v HelmVersion) GreaterOrEqual(major, minor int) bool {
	if v.Major > major {
		return true
	}
	if v.Major == major && v.Minor >= minor {
		return true
	}
	return false
}

// Version returns the installed helm CLI version.
func (h *Helm) Version(ctx context.Context) (*HelmVersion, error) {
	ctx, cancel := context.WithTimeout(ctx, h.timeout)
	defer cancel()

	args := []string{"version", "--short"}
	stdout, stderr, exitCode := h.exec.ExecuteWithContext(ctx, helmCmd, args...)
	if exitCode != 0 {
		return nil, fmt.Errorf("helm version: %w", errors.FromStderr(stderr, exitCode))
	}

	return parseHelmVersion(stdout)
}

func parseHelmVersion(output string) (*HelmVersion, error) {
	output = strings.TrimSpace(output)
	output = strings.TrimPrefix(output, "v")

	plusIdx := strings.Index(output, "+")
	if plusIdx > 0 {
		output = output[:plusIdx]
	}

	dashIdx := strings.Index(output, "-")
	if dashIdx > 0 {
		output = output[:dashIdx]
	}

	parts := strings.SplitN(output, ".", 3)
	if len(parts) < 2 {
		return nil, fmt.Errorf("unexpected helm version format: %q", output)
	}

	major, err := strconv.Atoi(parts[0])
	if err != nil {
		return nil, fmt.Errorf("parse helm major version: %w", err)
	}

	minor, err := strconv.Atoi(parts[1])
	if err != nil {
		return nil, fmt.Errorf("parse helm minor version: %w", err)
	}

	var patch int
	if len(parts) >= 3 {
		patch, err = strconv.Atoi(parts[2])
		if err != nil {
			return nil, fmt.Errorf("parse helm patch version: %w", err)
		}
	}

	return &HelmVersion{Major: major, Minor: minor, Patch: patch}, nil
}

// Pull downloads a helm chart from a registry to the specified destination directory.
func (h *Helm) Pull(ctx context.Context, chartRef, destDir string, opts ...ClientOption) error {
	options := &clientOptions{}
	for _, opt := range opts {
		opt(options)
	}

	timeout := h.timeout
	if options.timeout > 0 {
		timeout = options.timeout
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	normalizedRef := NormalizeChartRef(chartRef)
	chartPath, version := SplitChartRef(normalizedRef)
	args := []string{"pull", chartPath, "--untar", "--destination", destDir}
	if version != "" {
		args = append(args, "--version", version)
	}

	if options.pullSecretPath != "" {
		exists, err := h.readWriter.PathExists(options.pullSecretPath)
		if err != nil {
			return fmt.Errorf("check registry config path: %w", err)
		}
		if !exists {
			h.log.Errorf("Registry config path does not exist: %s", options.pullSecretPath)
		} else {
			args = append(args, "--registry-config", options.pullSecretPath)
		}
	}

	_, stderr, exitCode := h.exec.ExecuteWithContext(ctx, helmCmd, args...)
	if exitCode != 0 {
		return fmt.Errorf("helm pull: %w", errors.FromStderr(stderr, exitCode))
	}

	return nil
}

// DependencyUpdate updates the dependencies for a chart at the specified path.
func (h *Helm) DependencyUpdate(ctx context.Context, chartPath string, opts ...ClientOption) error {
	options := &clientOptions{}
	for _, opt := range opts {
		opt(options)
	}

	timeout := h.timeout
	if options.timeout > 0 {
		timeout = options.timeout
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	args := []string{"dependency", "update", chartPath}

	if options.repositoryConfigPath != "" {
		exists, err := h.readWriter.PathExists(options.repositoryConfigPath)
		if err != nil {
			return fmt.Errorf("check repository config path: %w", err)
		}
		if !exists {
			h.log.Errorf("Repository config path does not exist: %s", options.repositoryConfigPath)
		} else {
			args = append(args, "--repository-config", options.repositoryConfigPath)
		}
	}

	if options.pullSecretPath != "" {
		exists, err := h.readWriter.PathExists(options.pullSecretPath)
		if err != nil {
			return fmt.Errorf("check registry config path: %w", err)
		}
		if !exists {
			h.log.Errorf("Registry config path does not exist: %s", options.pullSecretPath)
		} else {
			args = append(args, "--registry-config", options.pullSecretPath)
		}
	}

	_, stderr, exitCode := h.exec.ExecuteWithContext(ctx, helmCmd, args...)
	if exitCode != 0 {
		return fmt.Errorf("helm dependency update: %w", errors.FromStderr(stderr, exitCode))
	}

	return nil
}

// Install installs a helm chart as a release with the specified name.
func (h *Helm) Install(ctx context.Context, releaseName, chartPath string, opts ...HelmOption) error {
	options := &helmOptions{}
	for _, opt := range opts {
		opt(options)
	}

	timeout := h.timeout
	if options.timeout > 0 {
		timeout = options.timeout
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	args := []string{"install", releaseName, chartPath}

	if options.namespace != "" {
		args = append(args, "--namespace", options.namespace)
	}

	if options.createNamespace {
		args = append(args, "--create-namespace")
	}

	for _, valuesPath := range options.valuesPaths {
		exists, err := h.readWriter.PathExists(valuesPath)
		if err != nil {
			return fmt.Errorf("check values file path: %w", err)
		}
		if !exists {
			return fmt.Errorf("values file does not exist: %s", valuesPath)
		}
		args = append(args, "--values", valuesPath)
	}

	if options.kubeconfigPath != "" {
		exists, err := h.readWriter.PathExists(options.kubeconfigPath)
		if err != nil {
			return fmt.Errorf("check kubeconfig path: %w", err)
		}
		if !exists {
			return fmt.Errorf("kubeconfig does not exist: %s", options.kubeconfigPath)
		}
		args = append(args, "--kubeconfig", options.kubeconfigPath)
	}

	if options.atomic {
		args = append(args, "--atomic")
	}

	if options.rollbackOnFailure {
		args = append(args, "--rollback-on-failure")
	}

	if options.postRendererPath != "" {
		args = append(args, "--post-renderer", options.postRendererPath)
		for _, arg := range options.postRendererArgs {
			args = append(args, "--post-renderer-args", arg)
		}
	}

	_, stderr, exitCode := h.exec.ExecuteWithContext(ctx, helmCmd, args...)
	if exitCode != 0 {
		return fmt.Errorf("helm install: %w", errors.FromStderr(stderr, exitCode))
	}

	return nil
}

// Upgrade upgrades an existing helm release to a new chart version.
func (h *Helm) Upgrade(ctx context.Context, releaseName, chartPath string, opts ...HelmOption) error {
	options := &helmOptions{}
	for _, opt := range opts {
		opt(options)
	}

	timeout := h.timeout
	if options.timeout > 0 {
		timeout = options.timeout
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	args := []string{"upgrade", releaseName, chartPath}

	if options.install {
		args = append(args, "--install")
	}

	if options.namespace != "" {
		args = append(args, "--namespace", options.namespace)
	}

	if options.createNamespace {
		args = append(args, "--create-namespace")
	}

	for _, valuesPath := range options.valuesPaths {
		exists, err := h.readWriter.PathExists(valuesPath)
		if err != nil {
			return fmt.Errorf("check values file path: %w", err)
		}
		if !exists {
			return fmt.Errorf("values file does not exist: %s", valuesPath)
		}
		args = append(args, "--values", valuesPath)
	}

	if options.kubeconfigPath != "" {
		exists, err := h.readWriter.PathExists(options.kubeconfigPath)
		if err != nil {
			return fmt.Errorf("check kubeconfig path: %w", err)
		}
		if !exists {
			return fmt.Errorf("kubeconfig does not exist: %s", options.kubeconfigPath)
		}
		args = append(args, "--kubeconfig", options.kubeconfigPath)
	}

	if options.atomic {
		args = append(args, "--atomic")
	}

	if options.rollbackOnFailure {
		args = append(args, "--rollback-on-failure")
	}

	if options.postRendererPath != "" {
		args = append(args, "--post-renderer", options.postRendererPath)
		for _, arg := range options.postRendererArgs {
			args = append(args, "--post-renderer-args", arg)
		}
	}

	var stdout, stderr string
	var exitCode int
	if env := helmEnv(options); env != nil {
		stdout, stderr, exitCode = h.exec.ExecuteWithContextFromDir(ctx, "", helmCmd, args, env...)
	} else {
		stdout, stderr, exitCode = h.exec.ExecuteWithContext(ctx, helmCmd, args...)
	}
	_ = stdout
	if exitCode != 0 {
		return fmt.Errorf("helm upgrade: %w", errors.FromStderr(stderr, exitCode))
	}

	return nil
}

// Uninstall removes a helm release from the cluster.
func (h *Helm) Uninstall(ctx context.Context, releaseName string, opts ...HelmOption) error {
	options := &helmOptions{}
	for _, opt := range opts {
		opt(options)
	}

	timeout := h.timeout
	if options.timeout > 0 {
		timeout = options.timeout
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	args := []string{"uninstall", releaseName}

	if options.namespace != "" {
		args = append(args, "--namespace", options.namespace)
	}

	if options.kubeconfigPath != "" {
		exists, err := h.readWriter.PathExists(options.kubeconfigPath)
		if err != nil {
			return fmt.Errorf("check kubeconfig path: %w", err)
		}
		if !exists {
			return fmt.Errorf("kubeconfig does not exist: %s", options.kubeconfigPath)
		}
		args = append(args, "--kubeconfig", options.kubeconfigPath)
	}

	if options.ignoreNotFound {
		args = append(args, "--ignore-not-found")
	}

	_, stderr, exitCode := h.exec.ExecuteWithContext(ctx, helmCmd, args...)
	if exitCode != 0 {
		return fmt.Errorf("helm uninstall: %w", errors.FromStderr(stderr, exitCode))
	}

	return nil
}

// Template renders a helm chart and returns the resulting Kubernetes manifests.
func (h *Helm) Template(ctx context.Context, releaseName, chartPath string, opts ...HelmOption) (string, error) {
	options := &helmOptions{}
	for _, opt := range opts {
		opt(options)
	}

	timeout := h.timeout
	if options.timeout > 0 {
		timeout = options.timeout
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	args := []string{"template", releaseName, chartPath, "--skip-tests"}

	if options.namespace != "" {
		args = append(args, "--namespace", options.namespace)
	}

	for _, valuesPath := range options.valuesPaths {
		exists, err := h.readWriter.PathExists(valuesPath)
		if err != nil {
			return "", fmt.Errorf("check values file path: %w", err)
		}
		if !exists {
			return "", fmt.Errorf("values file does not exist: %s", valuesPath)
		}
		args = append(args, "--values", valuesPath)
	}

	stdout, stderr, exitCode := h.exec.ExecuteWithContext(ctx, helmCmd, args...)
	if exitCode != 0 {
		return "", fmt.Errorf("helm template: %w", errors.FromStderr(stderr, exitCode))
	}

	return stdout, nil
}

// Lint runs helm lint on a chart to validate its structure and templates.
func (h *Helm) Lint(ctx context.Context, chartPath string, opts ...HelmOption) error {
	options := &helmOptions{}
	for _, opt := range opts {
		opt(options)
	}

	timeout := h.timeout
	if options.timeout > 0 {
		timeout = options.timeout
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	args := []string{"lint", chartPath}

	for _, valuesPath := range options.valuesPaths {
		exists, err := h.readWriter.PathExists(valuesPath)
		if err != nil {
			return fmt.Errorf("check values file path: %w", err)
		}
		if !exists {
			return fmt.Errorf("values file does not exist: %s", valuesPath)
		}
		args = append(args, "--values", valuesPath)
	}

	_, stderr, exitCode := h.exec.ExecuteWithContext(ctx, helmCmd, args...)
	if exitCode != 0 {
		return fmt.Errorf("helm lint: %w", errors.FromStderr(stderr, exitCode))
	}

	return nil
}

// helmDryRunOutput represents the YAML output structure from helm dry-run.
type helmDryRunOutput struct {
	Manifest string `yaml:"manifest"`
}

// DryRun performs a helm upgrade --install --dry-run=server to validate
// the chart against the Kubernetes API server without actually installing it.
// Returns only the manifest (raw YAML) for compatibility with Template output.
func (h *Helm) DryRun(ctx context.Context, releaseName, chartPath string, opts ...HelmOption) (string, error) {
	options := &helmOptions{}
	for _, opt := range opts {
		opt(options)
	}

	timeout := h.timeout
	if options.timeout > 0 {
		timeout = options.timeout
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	args := []string{"upgrade", "--install", "--dry-run=server", "-o", "yaml", releaseName, chartPath}

	if options.namespace != "" {
		args = append(args, "--namespace", options.namespace)
	}

	if options.createNamespace {
		args = append(args, "--create-namespace")
	}

	for _, valuesPath := range options.valuesPaths {
		exists, err := h.readWriter.PathExists(valuesPath)
		if err != nil {
			return "", fmt.Errorf("check values file path: %w", err)
		}
		if !exists {
			return "", fmt.Errorf("values file does not exist: %s", valuesPath)
		}
		args = append(args, "--values", valuesPath)
	}

	if options.kubeconfigPath != "" {
		exists, err := h.readWriter.PathExists(options.kubeconfigPath)
		if err != nil {
			return "", fmt.Errorf("check kubeconfig path: %w", err)
		}
		if !exists {
			return "", fmt.Errorf("kubeconfig does not exist: %s", options.kubeconfigPath)
		}
		args = append(args, "--kubeconfig", options.kubeconfigPath)
	}

	stdout, stderr, exitCode := h.exec.ExecuteWithContext(ctx, helmCmd, args...)
	if exitCode != 0 {
		return "", fmt.Errorf("helm dry-run: %w", errors.FromStderr(stderr, exitCode))
	}

	var output helmDryRunOutput
	if err := yaml.Unmarshal([]byte(stdout), &output); err != nil {
		return "", fmt.Errorf("parse dry-run output: %w", err)
	}

	return output.Manifest, nil
}

// Resolve ensures a chart is pulled and its dependencies are updated.
func (h *Helm) Resolve(ctx context.Context, chartRef string, opts ...ClientOption) error {
	return h.cache.Pull(ctx, chartRef, opts...)
}

// IsResolved returns true if the chart has been fully resolved (pulled and dependencies updated).
func (h *Helm) IsResolved(chartRef string) (bool, error) {
	return h.cache.IsResolved(chartRef)
}

// GetChartPath returns the local filesystem path for a cached chart.
func (h *Helm) GetChartPath(chartRef string) string {
	return h.cache.GetChartPath(chartRef)
}

// RemoveChart removes a cached helm chart by its reference.
// The chart reference format is: oci://registry/chart:version or registry/chart:version
func (h *Helm) RemoveChart(chartRef string) error {
	return h.cache.RemoveChartByRef(chartRef)
}
