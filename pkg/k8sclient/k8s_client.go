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
	ListProjects(ctx context.Context, token string, opts ...ListProjectsOption) ([]byte, error)
	ListRoleBindingsForUser(ctx context.Context, namespace, username string) ([]string, error)
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

func (k *k8sClient) ListProjects(ctx context.Context, token string, opts ...ListProjectsOption) ([]byte, error) {
	req := k.clientset.RESTClient().Get().AbsPath("/apis/project.openshift.io/v1/projects")
	if token != "" {
		req.SetHeader("Authorization", fmt.Sprintf("Bearer %s", token))
	}

	// Apply options
	for _, opt := range opts {
		opt(req)
	}

	return req.DoRaw(ctx)
}

type ListProjectsOption func(*rest.Request)

// WithLabelSelector adds a label selector to filter projects server-side
// This is useful when the annotation filter corresponds to a label
func WithLabelSelector(selector string) ListProjectsOption {
	return func(req *rest.Request) {
		if selector != "" {
			req.Param("labelSelector", selector)
		}
	}
}

func (k *k8sClient) ListRoleBindingsForUser(ctx context.Context, namespace, username string) ([]string, error) {
	roleBindings, err := k.ListRoleBindings(ctx, namespace)
	if err != nil {
		return nil, err
	}

	var roleNames []string
	for _, binding := range roleBindings.Items {
		for _, subject := range binding.Subjects {
			if subject.Kind == "User" && subject.Name == username {
				roleNames = append(roleNames, binding.RoleRef.Name)
				break
			}
			if subject.Kind == "ServiceAccount" {
				namespace := subject.Namespace
				if namespace == "" {
					namespace = binding.Namespace
				}
				if fmt.Sprintf("system:serviceaccount:%s:%s", namespace, subject.Name) == username {
					roleNames = append(roleNames, binding.RoleRef.Name)
					break
				}
			}
		}
	}

	return roleNames, nil
}

type Option func(*rest.Request)

func WithToken(token string) Option {
	return func(req *rest.Request) {
		req.SetHeader("Authorization", fmt.Sprintf("Bearer %s", token))
	}
}
