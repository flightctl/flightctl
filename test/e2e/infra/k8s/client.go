// Package k8s provides Kubernetes-specific implementations of the infra providers.
package k8s

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/sirupsen/logrus"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// resolveKubeConfigPath returns the path to kubeconfig using well-known locations.
func resolveKubeConfigPath() (string, error) {
	if kc, ok := os.LookupEnv("KUBECONFIG"); ok && kc != "" {
		return kc, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to get home directory: %w", err)
	}
	paths := []string{
		filepath.Join(home, ".kube", "config"),
		filepath.Join(string(filepath.Separator), "home", "kni", "clusterconfigs", "kubeconfig"),
		filepath.Join(string(filepath.Separator), "home", "kni", "auth", "clusterconfigs", "kubeconfig"),
	}
	for _, path := range paths {
		if _, err = os.Stat(path); err == nil {
			return path, nil
		}
	}
	return "", fmt.Errorf("failed to find kubeconfig file in the paths: %v", paths)
}

// NewClient creates a Kubernetes client from the default kubeconfig resolution.
// Used by infra when building K8s providers and exposed so the harness can set h.Cluster.
func NewClient() (kubernetes.Interface, error) {
	clientset, _, err := NewClientAndConfig()
	return clientset, err
}

// NewClientAndConfig returns a Kubernetes client and rest config from the default kubeconfig.
// The config is needed for port-forward and exec. Use NewClient() when only the interface is needed.
func NewClientAndConfig() (kubernetes.Interface, *rest.Config, error) {
	kubeconfig, err := resolveKubeConfigPath()
	if err != nil {
		return nil, nil, fmt.Errorf("unable to resolve kubeconfig location: %w", err)
	}
	logrus.Debugf("Using kubeconfig: %s", kubeconfig)
	config, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		return nil, nil, fmt.Errorf("building kubeconfig: %w", err)
	}
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, nil, fmt.Errorf("creating kubernetes client: %w", err)
	}
	return clientset, config, nil
}
