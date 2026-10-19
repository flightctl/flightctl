// Package k8s provides Kubernetes-specific implementations of the infra providers.
package k8s

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sirupsen/logrus"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// firstExistingPath picks one file out of a KUBECONFIG-style list. kubectl's
// --kubeconfig and clientcmd's ExplicitPath both name a single file, so a
// multi-entry list has to be narrowed to one before it can be handed to
// either; otherwise the Go client and the command lines disagree about which
// file they read. The first entry is the fallback so the error a missing file
// produces names the path the caller asked for.
func firstExistingPath(list string) string {
	entries := filepath.SplitList(list)
	if len(entries) == 0 {
		return list
	}
	for _, entry := range entries {
		if entry == "" {
			continue
		}
		if _, err := os.Stat(entry); err == nil {
			return entry
		}
	}
	return entries[0]
}

// resolveKubeConfigPath returns the path to kubeconfig using well-known locations.
func resolveKubeConfigPath() (string, error) {
	if kc, ok := os.LookupEnv("KUBECONFIG"); ok && kc != "" {
		return firstExistingPath(kc), nil
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

// ResolveKubeConfig decides which kubeconfig file and context a run uses.
//
// It exists so that the Go client and the kubectl/helm command lines can be
// given the same answer. Resolving them separately is how a run ends up with
// a client talking to the current context of one file while kubectl talks to
// a different file, or to a different context of the same file - and the two
// then disagree about what is deployed.
//
// path wins over the KUBECONFIG environment variable, because a caller that
// set it programmatically means it. An empty result for the context means
// "whatever the file's current context is": that is what the client does by
// default, and callers that build command lines leave --context off, so their
// own defaults still apply.
func ResolveKubeConfig(path, kubeContext string) (string, string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		resolved, err := resolveKubeConfigPath()
		if err != nil {
			return "", "", fmt.Errorf("unable to resolve kubeconfig location: %w", err)
		}
		path = resolved
	} else {
		path = firstExistingPath(path)
	}
	return path, strings.TrimSpace(kubeContext), nil
}

// NewClientForKubeConfig builds a Kubernetes client and rest config for an
// explicit kubeconfig file and context. An empty context selects the file's
// current context. Pair it with ResolveKubeConfig so the caller can hand the
// same two values to anything that shells out to kubectl or helm.
func NewClientForKubeConfig(path, kubeContext string) (kubernetes.Interface, *rest.Config, error) {
	if strings.TrimSpace(path) == "" {
		return nil, nil, fmt.Errorf("kubeconfig path is required")
	}
	logrus.Debugf("Using kubeconfig: %s (context %q)", path, kubeContext)

	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	loadingRules.ExplicitPath = path
	overrides := &clientcmd.ConfigOverrides{CurrentContext: kubeContext}

	config, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, overrides).ClientConfig()
	if err != nil {
		return nil, nil, fmt.Errorf("building kubeconfig: %w", err)
	}
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, nil, fmt.Errorf("creating kubernetes client: %w", err)
	}
	return clientset, config, nil
}

// NewClient creates a Kubernetes client from the default kubeconfig resolution.
// Used by infra when building K8s providers and exposed so the harness can set h.Cluster.
func NewClient() (kubernetes.Interface, error) {
	clientset, _, err := NewClientAndConfig()
	return clientset, err
}

// NewClientAndConfig returns a Kubernetes client and rest config from the default kubeconfig.
// The config is needed for port-forward and exec. Use NewClient() when only the interface is needed.
//
// It takes no configured path or context; callers that have one should resolve
// it with ResolveKubeConfig and use NewClientForKubeConfig, so the command
// lines they build can be given the same values.
func NewClientAndConfig() (kubernetes.Interface, *rest.Config, error) {
	path, kubeContext, err := ResolveKubeConfig("", "")
	if err != nil {
		return nil, nil, err
	}
	return NewClientForKubeConfig(path, kubeContext)
}
