package lifecycle

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/applications/helm"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/pkg/log"
)

const (
	flightctlManagedByLabel = "flightctl.io/managed-by=flightctl"
	helmPostRendererPlugin  = "flightctl-postrenderer"
)

var _ ActionHandler = (*HelmHandler)(nil)

// ExecutableResolver resolves the path to an executable binary.
type ExecutableResolver interface {
	Resolve() (string, error)
}

// OSExecutableResolver resolves the current process executable using os.Executable().
type OSExecutableResolver struct{}

func (r OSExecutableResolver) Resolve() (string, error) {
	bin, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("resolving executable: %w", err)
	}
	if bin == "" {
		return "", fmt.Errorf("executable not found")
	}
	return bin, nil
}

type HelmHandler struct {
	clients            client.CLIClients
	log                *log.PrefixLogger
	kubeconfigPath     string
	executableResolver ExecutableResolver
	rwFactory          fileio.ReadWriterFactory
}

// helmVersionConfig holds version-specific configuration for helm operations.
// This is necessary because Helm 3.x and 4.x have different approaches:
//
// Post-renderer differences:
//   - Helm 3.x: Accepts a binary path via --post-renderer flag
//   - Helm 4.x: Requires a plugin with type "postrenderer/v1" discovered via HELM_PLUGINS
//
// Atomic/rollback differences:
//   - Helm 3.x: Uses --atomic flag for automatic rollback on failure
//   - Helm 4.x: Renamed to --rollback-on-failure flag
type helmVersionConfig struct {
	optionFunc   func(appID string) client.HelmOption
	extraOptions []client.HelmOption
	cleanup      func()
}

func NewHelmHandler(log *log.PrefixLogger, clients client.CLIClients, kubeconfigPath string, resolver ExecutableResolver, rwFactory fileio.ReadWriterFactory) *HelmHandler {
	return &HelmHandler{
		clients:            clients,
		log:                log,
		kubeconfigPath:     kubeconfigPath,
		executableResolver: resolver,
		rwFactory:          rwFactory,
	}
}

func (h *HelmHandler) Execute(ctx context.Context, actions Actions) error {
	versionConfig, err := h.setupHelmVersionConfig(ctx)
	if err != nil {
		return fmt.Errorf("setting up post-renderer: %w", err)
	}
	defer versionConfig.cleanup()

	for _, action := range actions {
		switch action.Type {
		case ActionAdd:
			if err := h.add(ctx, &action, versionConfig); err != nil {
				return err
			}
		case ActionRemove:
			if err := h.remove(ctx, &action); err != nil {
				return err
			}
		case ActionUpdate:
			if err := h.update(ctx, &action, versionConfig); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unsupported action type: %s", action.Type)
		}
	}
	return nil
}

func (h *HelmHandler) add(ctx context.Context, action *Action, versionConfig *helmVersionConfig) error {
	releaseName := action.Name
	chartPath := action.Path
	helmSpec := h.getHelmSpec(action)
	namespace := helmSpec.Namespace

	h.log.Debugf("Installing helm release: %s namespace: %s chart: %s", releaseName, namespace, chartPath)

	kubeOpts := []client.KubeOption{
		client.WithKubeLabels([]string{flightctlManagedByLabel}),
	}
	if h.kubeconfigPath != "" {
		kubeOpts = append(kubeOpts, client.WithKubeKubeconfig(h.kubeconfigPath))
	}

	if err := h.clients.Kube().EnsureNamespace(ctx, namespace, kubeOpts...); err != nil {
		return fmt.Errorf("ensure namespace %s: %w", namespace, err)
	}

	opts := []client.HelmOption{
		client.WithNamespace(namespace),
		client.WithInstall(),
		versionConfig.optionFunc(action.ID),
	}
	opts = append(opts, versionConfig.extraOptions...)
	if h.kubeconfigPath != "" {
		opts = append(opts, client.WithKubeconfig(h.kubeconfigPath))
	}

	valuesFiles := h.resolveValuesFiles(chartPath, helmSpec)
	if len(valuesFiles) > 0 {
		opts = append(opts, client.WithValuesFiles(valuesFiles))
	}

	if err := h.clients.Helm().Upgrade(ctx, releaseName, chartPath, opts...); err != nil {
		return fmt.Errorf("helm upgrade --install %s: %w", releaseName, err)
	}

	h.log.Infof("Installed helm release: %s", releaseName)
	return nil
}

func (h *HelmHandler) remove(ctx context.Context, action *Action) error {
	releaseName := action.Name
	helmSpec := h.getHelmSpec(action)
	namespace := helmSpec.Namespace

	h.log.Debugf("Uninstalling helm release: %s namespace: %s", releaseName, namespace)

	opts := []client.HelmOption{
		client.WithNamespace(namespace),
		client.WithIgnoreNotFound(),
	}
	if h.kubeconfigPath != "" {
		opts = append(opts, client.WithKubeconfig(h.kubeconfigPath))
	}

	if err := h.clients.Helm().Uninstall(ctx, releaseName, opts...); err != nil {
		return fmt.Errorf("helm uninstall %s: %w", releaseName, err)
	}

	h.log.Infof("Uninstalled helm release: %s", releaseName)
	return nil
}

func (h *HelmHandler) update(ctx context.Context, action *Action, versionConfig *helmVersionConfig) error {
	releaseName := action.Name
	chartPath := action.Path
	helmSpec := h.getHelmSpec(action)
	namespace := helmSpec.Namespace

	h.log.Debugf("Upgrading helm release: %s namespace: %s chart: %s", releaseName, namespace, chartPath)

	opts := []client.HelmOption{
		client.WithNamespace(namespace),
		client.WithInstall(),
		versionConfig.optionFunc(action.ID),
	}
	opts = append(opts, versionConfig.extraOptions...)
	if h.kubeconfigPath != "" {
		opts = append(opts, client.WithKubeconfig(h.kubeconfigPath))
	}

	valuesFiles := h.resolveValuesFiles(chartPath, helmSpec)
	if len(valuesFiles) > 0 {
		opts = append(opts, client.WithValuesFiles(valuesFiles))
	}

	if err := h.clients.Helm().Upgrade(ctx, releaseName, chartPath, opts...); err != nil {
		return fmt.Errorf("helm upgrade %s: %w", releaseName, err)
	}

	h.log.Infof("Upgraded helm release: %s", releaseName)
	return nil
}

func (h *HelmHandler) getHelmSpec(action *Action) HelmSpec {
	var spec HelmSpec
	if s, ok := action.Spec.(HelmSpec); ok {
		spec = s
	}
	spec.Namespace = helm.AppNamespace(&spec.Namespace, action.Name)
	return spec
}

func (h *HelmHandler) resolveValuesFiles(chartPath string, helmSpec HelmSpec) []string {
	var valuesFiles []string

	for _, vf := range helmSpec.ValuesFiles {
		absPath := filepath.Join(chartPath, vf)
		valuesFiles = append(valuesFiles, absPath)
	}

	if helmSpec.ProviderValuesPath != "" {
		valuesFiles = append(valuesFiles, helmSpec.ProviderValuesPath)
	}

	return valuesFiles
}

// setupHelmVersionConfig detects the installed Helm version and returns the
// appropriate configuration for post-renderer and atomic/rollback behavior.
func (h *HelmHandler) setupHelmVersionConfig(ctx context.Context) (*helmVersionConfig, error) {
	version, err := h.clients.Helm().Version(ctx)
	if err != nil {
		return nil, fmt.Errorf("detecting helm version: %w", err)
	}

	if !version.GreaterOrEqual(4, 0) {
		// Helm 3.x: Pass binary path directly to --post-renderer flag.
		// The flightctl-agent binary is invoked with "helm-render" subcommand.
		bin, err := h.executableResolver.Resolve()
		if err != nil {
			return nil, fmt.Errorf("resolving executable: %w", err)
		}
		return &helmVersionConfig{
			optionFunc: func(appID string) client.HelmOption {
				return client.WithPostRenderer(bin, "helm-render", "--app", appID)
			},
			extraOptions: []client.HelmOption{
				client.WithAtomic(),
			},
			cleanup: func() {},
		}, nil
	}

	// Helm 4.x: Post-renderers must be plugins with type "postrenderer/v1".
	// We create a temporary plugin directory with a plugin.yaml that wraps
	// the flightctl-agent binary. The plugin is discovered via HELM_PLUGINS env var.
	pluginsDir, cleanup, err := h.createPluginDir()
	if err != nil {
		return nil, fmt.Errorf("creating plugin directory: %w", err)
	}

	return &helmVersionConfig{
		optionFunc: func(appID string) client.HelmOption {
			// Pass plugin name instead of binary path - Helm discovers it via HELM_PLUGINS
			return client.WithPostRenderer(helmPostRendererPlugin, "--app", appID)
		},
		extraOptions: []client.HelmOption{
			client.WithHelmEnv(map[string]string{
				"HELM_PLUGINS":    pluginsDir,
				"HELM_CACHE_HOME": pluginsDir,
			}),
			client.WithRollbackOnFailure(),
		},
		cleanup: cleanup,
	}, nil
}

// createPluginDir creates a temporary directory containing a Helm 4.x post-renderer plugin.
//
// Helm 4.x changed how post-renderers work - they must now be plugins rather than arbitrary
// binaries. A plugin requires a plugin.yaml manifest with specific fields:
//   - type: "postrenderer/v1" - identifies this as a post-renderer plugin
//   - runtime: "subprocess" - tells Helm to execute a binary
//   - runtimeConfig.platformCommand - specifies the binary and arguments to execute
//
// The plugin wraps the flightctl-agent binary, invoking it with the "helm-render" subcommand.
// Helm discovers plugins via the HELM_PLUGINS environment variable, which we set to point
// to this temporary directory.
//
// Returns the plugins directory path, a cleanup function to remove the directory, and any error.
func (h *HelmHandler) createPluginDir() (string, func(), error) {
	rw, err := h.rwFactory("")
	if err != nil {
		return "", nil, fmt.Errorf("creating read writer: %w", err)
	}

	pluginsDir, err := rw.MkdirTemp("helm-plugins")
	if err != nil {
		return "", nil, fmt.Errorf("creating temp directory: %w", err)
	}

	cleanup := func() {
		if err := rw.RemoveAll(pluginsDir); err != nil {
			h.log.Warnf("Failed to cleanup helm plugins directory %s: %v", pluginsDir, err)
		}
	}

	// Plugin directory structure: HELM_PLUGINS/<plugin-name>/plugin.yaml
	pluginDir := filepath.Join(pluginsDir, helmPostRendererPlugin)
	if err := rw.MkdirAll(pluginDir, fileio.DefaultDirectoryPermissions); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("creating plugin directory: %w", err)
	}

	bin, err := h.executableResolver.Resolve()
	if err != nil {
		cleanup()
		return "", nil, fmt.Errorf("resolving executable: %w", err)
	}

	// Create plugin.yaml manifest that wraps flightctl-agent with helm-render subcommand.
	// The --app flag is passed via --post-renderer-args at runtime.
	pluginYAML := fmt.Sprintf(`apiVersion: v1
name: %s
version: 1.0.0
type: postrenderer/v1
usage: "Post-renderer that injects flightctl app labels"
description: "Wraps flightctl-agent helm-render to inject app labels into manifests"
runtime: subprocess
runtimeConfig:
  platformCommand:
    - command: %s
      args:
        - helm-render
`, helmPostRendererPlugin, bin)

	pluginPath := filepath.Join(pluginDir, "plugin.yaml")
	if err := rw.WriteFile(pluginPath, []byte(pluginYAML), fileio.DefaultFilePermissions); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("writing plugin.yaml: %w", err)
	}

	h.log.Debugf("Created helm post-renderer plugin at %s", pluginPath)
	return pluginsDir, cleanup, nil
}
