package k8sclient

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

const (
	defaultBurst         = 1000
	defaultQPS           = 500
	externalApiTokenPath = "/var/flightctl/k8s/token" //nolint:gosec
)

type K8SClient interface {
	GetSecret(ctx context.Context, namespace, name string) (*corev1.Secret, error)
	PostCRD(ctx context.Context, crdGVK string, body []byte, opts ...Option) ([]byte, error)
	ListRoleBindings(ctx context.Context, namespace string) (*rbacv1.RoleBindingList, error)
}

type k8sClient struct {
	clientset *kubernetes.Clientset
}

func NewK8SClient() (K8SClient, error) {
	config, err := rest.InClusterConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to create in-cluster config: %w", err)
	}
	config.Burst = defaultBurst
	config.QPS = defaultQPS
	return newClient(config)
}

func NewK8SExternalClient(apiUrl string, insecure bool, caCert string) (K8SClient, error) {
	config := &rest.Config{
		Host: apiUrl,
		TLSClientConfig: rest.TLSClientConfig{
			Insecure: insecure,
			CAData:   []byte(caCert),
		},
		Burst:           defaultBurst,
		QPS:             defaultQPS,
		BearerTokenFile: externalApiTokenPath,
	}

	return newClient(config)
}

func newClient(config *rest.Config) (K8SClient, error) {
	// Create a clientset
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create clientset: %w", err)
	}
	return &k8sClient{clientset: clientset}, nil
}

func (k *k8sClient) GetSecret(ctx context.Context, namespace, name string) (*corev1.Secret, error) {
	secret, err := k.clientset.CoreV1().Secrets(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to get secret %s/%s: %w", namespace, name, err)
	}
	return secret, nil
}

func (k *k8sClient) PostCRD(ctx context.Context, crdGVK string, body []byte, opts ...Option) ([]byte, error) {
	req := k.clientset.RESTClient().Post().AbsPath(fmt.Sprintf("/apis/%s", crdGVK)).Body(body)
	for _, opt := range opts {
		opt(req)
	}
	return req.DoRaw(ctx)
}

func (k *k8sClient) ListRoleBindings(ctx context.Context, namespace string) (*rbacv1.RoleBindingList, error) {
	roleBindings, err := k.clientset.RbacV1().RoleBindings(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list rolebindings in namespace %s: %w", namespace, err)
	}
	return roleBindings, nil
}

type Option func(*rest.Request)

func WithToken(token string) Option {
	return func(req *rest.Request) {
		req.SetHeader("Authorization", fmt.Sprintf("Bearer %s", token))
	}
}
