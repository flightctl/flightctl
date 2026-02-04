package client

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
)

const (
	kubectlCmd = "kubectl"
	ocCmd      = "oc"

	microshiftKubeconfigPath = "/var/lib/microshift/resources/kubeadmin/kubeconfig"
)

// KubernetesOption is a functional option for configuring the Kube client.
type KubernetesOption func(*kubernetesOptions)

type kubernetesOptions struct {
	binary         string
	kubeconfigPath string
}

// WithBinary sets a specific kubectl/oc binary path instead of auto-discovering.
func WithBinary(binary string) KubernetesOption {
	return func(opts *kubernetesOptions) {
		opts.binary = binary
	}
}

// WithKubeconfigPath sets the kubeconfig path instead of auto-discovering.
func WithKubeconfigPath(path string) KubernetesOption {
	return func(opts *kubernetesOptions) {
		opts.kubeconfigPath = path
	}
}

// KubeOption is a functional option for configuring individual kube operations.
type KubeOption func(*kubeOptions)

type kubeOptions struct {
	kubeconfigPath string
	labels         []string
}

// WithKubeKubeconfig sets the kubeconfig file path for kube operations.
func WithKubeKubeconfig(path string) KubeOption {
	return func(opts *kubeOptions) {
		opts.kubeconfigPath = path
	}
}

// WithKubeLabels sets labels to apply during kube operations.
// Each label should be in the format "key=value".
func WithKubeLabels(labels []string) KubeOption {
	return func(opts *kubeOptions) {
		opts.labels = labels
	}
}

// Kube provides a client for executing kubectl/oc CLI commands.
type Kube struct {
	exec             executer.Executer
	log              *log.PrefixLogger
	readWriter       fileio.ReadWriter
	commandAvailable func(string) bool

	binary             string
	kubeconfigPath     string
	kubeconfigResolved bool
}

// NewKube creates a new Kube client with lazy initialization.
// The kubernetes binary and kubeconfig are discovered on first use.
// Use WithBinary option to set a specific binary path instead of auto-discovering.
// Use WithKubeconfigPath option to set a specific kubeconfig path.
func NewKube(
	log *log.PrefixLogger,
	exec executer.Executer,
	readWriter fileio.ReadWriter,
	opts ...KubernetesOption,
) *Kube {
	options := &kubernetesOptions{}
	for _, opt := range opts {
		opt(options)
	}

	k := &Kube{
		exec:               exec,
		log:                log,
		readWriter:         readWriter,
		commandAvailable:   IsCommandAvailable,
		binary:             options.binary,
		kubeconfigPath:     options.kubeconfigPath,
		kubeconfigResolved: options.kubeconfigPath != "",
	}
	return k
}

func (k *Kube) discoverBinary() string {
	if k.commandAvailable(kubectlCmd) {
		return kubectlCmd
	}
	if k.commandAvailable(ocCmd) {
		return ocCmd
	}
	return ""
}

// Binary returns the kubectl/oc binary path being used.
// This triggers resolution if not already done.
// Returns empty string if no kubernetes CLI binary is available.
func (k *Kube) Binary() string {
	_ = k.resolve()
	return k.binary
}

// IsAvailable returns true if kubernetes is fully configured.
// This attempts to resolve binary and kubeconfig if not already done.
func (k *Kube) IsAvailable() bool {
	return k.resolve() == nil
}

// resolve attempts to discover binary and kubeconfig, caching the results.
func (k *Kube) resolve() error {
	if k.binary != "" && k.kubeconfigResolved {
		return nil
	}

	if k.binary == "" {
		k.binary = k.discoverBinary()
	}
	if k.binary == "" {
		return fmt.Errorf("kubernetes CLI binary not available")
	}

	if k.kubeconfigResolved {
		return nil
	}

	path, err := k.resolveKubeconfig()
	if err != nil {
		return err
	}

	k.kubeconfigPath = path
	k.kubeconfigResolved = true
	k.log.Debugf("Kubernetes resolved: binary=%s kubeconfig=%s", k.binary, k.kubeconfigPath)
	return nil
}

// resolveKubeconfig finds a valid kubeconfig path.
func (k *Kube) resolveKubeconfig() (string, error) {
	var checkedPaths []string

	if kubeconfigEnv := os.Getenv("KUBECONFIG"); kubeconfigEnv != "" {
		paths := filepath.SplitList(kubeconfigEnv)
		for _, path := range paths {
			if path == "" {
				continue
			}
			checkedPaths = append(checkedPaths, fmt.Sprintf("%s (KUBECONFIG env)", path))
			exists, err := k.readWriter.PathExists(path)
			if err != nil {
				return "", fmt.Errorf("check KUBECONFIG path: %w (checked: %s)", err, strings.Join(checkedPaths, ", "))
			}
			if exists {
				return "", nil
			}
		}
	}

	checkedPaths = append(checkedPaths, microshiftKubeconfigPath)
	exists, err := k.readWriter.PathExists(microshiftKubeconfigPath)
	if err != nil {
		return "", fmt.Errorf("check microshift kubeconfig path: %w (checked: %s)", err, strings.Join(checkedPaths, ", "))
	}
	if exists {
		return microshiftKubeconfigPath, nil
	}

	homeDir := os.Getenv("HOME")
	if homeDir == "" {
		return "", fmt.Errorf("no kubeconfig found, checked: %s (HOME environment variable not set, cannot check default path)", strings.Join(checkedPaths, ", "))
	}
	defaultPath := filepath.Join(homeDir, ".kube", "config")
	checkedPaths = append(checkedPaths, defaultPath)
	exists, err = k.readWriter.PathExists(defaultPath)
	if err != nil {
		return "", fmt.Errorf("check default kubeconfig path: %w (checked: %s)", err, strings.Join(checkedPaths, ", "))
	}
	if exists {
		return defaultPath, nil
	}

	return "", fmt.Errorf("no kubeconfig found, checked: %s", strings.Join(checkedPaths, ", "))
}

// WatchPodsCmd returns an exec.Cmd configured to watch pod events across all namespaces.
// Use WithKubeLabels to filter pods by label selector.
func (k *Kube) WatchPodsCmd(ctx context.Context, opts ...KubeOption) (*exec.Cmd, error) {
	binary := k.Binary()

	if binary == "" {
		return nil, fmt.Errorf("kubernetes CLI binary not available")
	}

	options := &kubeOptions{}
	for _, opt := range opts {
		opt(options)
	}

	args := []string{"get", "pods", "--watch", "--output-watch-events", "--all-namespaces", "-o", "json"}

	if len(options.labels) > 0 {
		args = append(args, "-l", strings.Join(options.labels, ","))
	}

	if options.kubeconfigPath != "" {
		args = append(args, "--kubeconfig", options.kubeconfigPath)
	}

	// #nosec G204 - binary is either hardcoded ("kubectl"/"oc") or explicitly configured, args are internally constructed
	return exec.CommandContext(ctx, binary, args...), nil
}

// EnsureNamespace creates a namespace if it doesn't exist and applies any specified labels.
func (k *Kube) EnsureNamespace(ctx context.Context, namespace string, opts ...KubeOption) error {
	binary := k.Binary()

	if binary == "" {
		return fmt.Errorf("kubernetes CLI binary not available")
	}

	options := &kubeOptions{}
	for _, opt := range opts {
		opt(options)
	}

	exists, err := k.NamespaceExists(ctx, namespace, opts...)
	if err != nil {
		return fmt.Errorf("check namespace exists: %w", err)
	}

	if exists {
		return nil
	}

	createArgs := []string{"create", "namespace", namespace}
	if options.kubeconfigPath != "" {
		createArgs = append(createArgs, "--kubeconfig", options.kubeconfigPath)
	}
	_, stderr, exitCode := k.exec.ExecuteWithContext(ctx, binary, createArgs...)
	if exitCode != 0 {
		return fmt.Errorf("create namespace: %s", stderr)
	}

	if len(options.labels) > 0 {
		labelArgs := []string{"label", "namespace", namespace}
		labelArgs = append(labelArgs, options.labels...)
		labelArgs = append(labelArgs, "--overwrite")
		if options.kubeconfigPath != "" {
			labelArgs = append(labelArgs, "--kubeconfig", options.kubeconfigPath)
		}
		_, stderr, exitCode := k.exec.ExecuteWithContext(ctx, binary, labelArgs...)
		if exitCode != 0 {
			return fmt.Errorf("label namespace: %s", stderr)
		}
	}

	return nil
}

// NamespaceExists checks if a namespace exists in the cluster.
func (k *Kube) NamespaceExists(ctx context.Context, namespace string, opts ...KubeOption) (bool, error) {
	binary := k.Binary()
	if binary == "" {
		return false, fmt.Errorf("kubernetes CLI binary not available")
	}

	options := &kubeOptions{}
	for _, opt := range opts {
		opt(options)
	}

	args := []string{"get", "namespace", namespace}
	if options.kubeconfigPath != "" {
		args = append(args, "--kubeconfig", options.kubeconfigPath)
	}

	_, stderr, exitCode := k.exec.ExecuteWithContext(ctx, binary, args...)
	if exitCode == 0 {
		return true, nil
	}
	if strings.Contains(stderr, "not found") {
		return false, nil
	}
	return false, fmt.Errorf("get namespace: %s", stderr)
}

// ResolveKubeconfig resolves and returns the kubeconfig path.
// This also ensures the binary is resolved.
// The result is cached after the first successful resolution.
func (k *Kube) ResolveKubeconfig() (string, error) {
	if err := k.resolve(); err != nil {
		return "", err
	}
	return k.kubeconfigPath, nil
}

// Kustomize runs kubectl kustomize on the specified directory and returns the output.
func (k *Kube) Kustomize(ctx context.Context, dir string) (stdout, stderr string, exitCode int) {
	binary := k.Binary()
	if binary == "" {
		return "", "kubernetes CLI binary not available", 1
	}
	return k.exec.ExecuteWithContext(ctx, binary, "kustomize", dir)
}
