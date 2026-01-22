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
	binary string
}

// WithBinary sets a specific kubectl/oc binary path instead of auto-discovering.
func WithBinary(binary string) KubernetesOption {
	return func(opts *kubernetesOptions) {
		opts.binary = binary
	}
}

// Kube provides a client for executing kubectl/oc CLI commands.
type Kube struct {
	exec       executer.Executer
	log        *log.PrefixLogger
	binary     string
	readWriter fileio.ReadWriter
}

// NewKube creates a new Kube client. It auto-discovers kubectl or oc if no binary is specified.
// Use IsAvailable to check if a kubernetes CLI binary was found.
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

	binary := options.binary
	if binary == "" {
		binary = discoverKubernetesBinary()
	}

	return &Kube{
		exec:       exec,
		log:        log,
		binary:     binary,
		readWriter: readWriter,
	}
}

func discoverKubernetesBinary() string {
	if IsCommandAvailable(kubectlCmd) {
		return kubectlCmd
	}
	if IsCommandAvailable(ocCmd) {
		return ocCmd
	}
	return ""
}

// Binary returns the kubectl/oc binary path being used.
func (k *Kube) Binary() string {
	return k.binary
}

// IsAvailable returns true if a kubernetes CLI binary (kubectl or oc) is available.
func (k *Kube) IsAvailable() bool {
	return k.binary != ""
}

// RefreshBinary re-runs binary discovery to find kubectl or oc.
// This is a no-op if a binary is already set.
// Returns true if a binary is available.
func (k *Kube) RefreshBinary() bool {
	if k.binary != "" {
		return true
	}
	k.binary = discoverKubernetesBinary()
	return k.binary != ""
}

// WatchPodsCmd returns an exec.Cmd configured to watch pod events across all namespaces.
func (k *Kube) WatchPodsCmd(ctx context.Context, kubeconfigPath string) (*exec.Cmd, error) {
	if k.binary == "" {
		return nil, fmt.Errorf("kubernetes CLI binary not available")
	}

	args := []string{"get", "pods", "--watch", "--output-watch-events", "--all-namespaces", "-o", "json"}

	if kubeconfigPath != "" {
		args = append(args, "--kubeconfig", kubeconfigPath)
	}

	// #nosec G204 - binary is either hardcoded ("kubectl"/"oc") or explicitly configured, args are internally constructed
	return exec.CommandContext(ctx, k.binary, args...), nil
}

// ResolveKubeconfig finds a valid kubeconfig path by checking KUBECONFIG env,
// microshift path, and the default ~/.kube/config location.
// KUBECONFIG may contain multiple paths. The first existing path is returned.
func (k *Kube) ResolveKubeconfig() (string, error) {
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
				return path, nil
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

// Kustomize runs kubectl kustomize on the specified directory and returns the output.
func (k *Kube) Kustomize(ctx context.Context, dir string) (stdout, stderr string, exitCode int) {
	return k.exec.ExecuteWithContext(ctx, k.binary, "kustomize", dir)
}
