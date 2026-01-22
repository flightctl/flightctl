package client

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
)

const (
	helmCmd            = "helm"
	defaultHelmTimeout = 5 * time.Minute
)

// HelmOption is a functional option for configuring helm operations.
type HelmOption func(*helmOptions)

type helmOptions struct {
	namespace        string
	valuesPaths      []string
	kubeconfigPath   string
	atomic           bool
	createNamespace  bool
	install          bool
	timeout          time.Duration
	postRendererPath string
	postRendererArgs []string
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

	chartPath, version := SplitChartRef(chartRef)
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

	if options.postRendererPath != "" {
		args = append(args, "--post-renderer", options.postRendererPath)
		for _, arg := range options.postRendererArgs {
			args = append(args, "--post-renderer-args", arg)
		}
	}

	_, stderr, exitCode := h.exec.ExecuteWithContext(ctx, helmCmd, args...)
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
