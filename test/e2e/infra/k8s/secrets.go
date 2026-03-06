// Package k8s provides Kubernetes-specific implementations of the infra providers.
package k8s

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/flightctl/flightctl/test/e2e/infra"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

// SecretsProvider implements infra.SecretsProvider for Kubernetes environments.
type SecretsProvider struct {
	client kubernetes.Interface
	infraP *InfraProvider
}

// NewSecretsProvider creates a new K8s SecretsProvider.
// infraP is used to resolve namespace for GetSecretDataForService; pass nil only if not using that method.
// If client is nil, it is created from the default kubeconfig.
func NewSecretsProvider(client kubernetes.Interface, infraP *InfraProvider) (infra.SecretsProvider, error) {
	if client == nil {
		kubeconfig := os.Getenv("KUBECONFIG")
		if kubeconfig == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return nil, fmt.Errorf("failed to get home directory: %w", err)
			}
			kubeconfig = filepath.Join(home, ".kube", "config")
		}
		config, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
		if err != nil {
			return nil, fmt.Errorf("failed to build kubeconfig: %w", err)
		}
		c, err := kubernetes.NewForConfig(config)
		if err != nil {
			return nil, fmt.Errorf("failed to create kubernetes client: %w", err)
		}
		client = c
	}
	return &SecretsProvider{client: client, infraP: infraP}, nil
}

// GetSecretData returns the value of a key in a Kubernetes Secret.
// K8s API returns Secret.Data as decoded bytes.
func (p *SecretsProvider) GetSecretData(ctx context.Context, namespace, secretName, key string) ([]byte, error) {
	secret, err := p.client.CoreV1().Secrets(namespace).Get(ctx, secretName, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	data, ok := secret.Data[key]
	if !ok {
		return nil, fmt.Errorf("secret %s/%s has no key %q", namespace, secretName, key)
	}
	return data, nil
}

// GetSecretDataForService returns the value of a key in a secret for the given service.
// Namespace is resolved from the service (e.g. Redis/flightctl-kv uses internal namespace, same as Helm).
// For Redis use secretName "flightctl-kv-secret" and key "password" (Helm default).
func (p *SecretsProvider) GetSecretDataForService(ctx context.Context, service infra.ServiceName, secretName, key string) ([]byte, error) {
	if p.infraP == nil {
		return nil, fmt.Errorf("k8s SecretsProvider has no InfraProvider; cannot resolve namespace for service")
	}
	namespace, err := p.infraP.namespaceForService(service)
	if err != nil {
		return nil, err
	}
	return p.GetSecretData(ctx, namespace, secretName, key)
}

// CreateSecret creates a Secret with the given namespace, name, and string data.
// Idempotent: if the secret already exists, it is updated to match stringData.
func (p *SecretsProvider) CreateSecret(ctx context.Context, namespace, name string, stringData map[string]string) error {
	if namespace == "" || name == "" {
		return fmt.Errorf("namespace and name are required")
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		StringData: stringData,
	}
	_, err := p.client.CoreV1().Secrets(namespace).Create(ctx, secret, metav1.CreateOptions{})
	if err == nil {
		return nil
	}
	if !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("create secret %s/%s: %w", namespace, name, err)
	}
	// Update existing secret so content matches (e.g. after cluster restore).
	existing, getErr := p.client.CoreV1().Secrets(namespace).Get(ctx, name, metav1.GetOptions{})
	if getErr != nil {
		return fmt.Errorf("get existing secret %s/%s: %w", namespace, name, getErr)
	}
	existing.StringData = stringData
	_, err = p.client.CoreV1().Secrets(namespace).Update(ctx, existing, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("update secret %s/%s: %w", namespace, name, err)
	}
	return nil
}
