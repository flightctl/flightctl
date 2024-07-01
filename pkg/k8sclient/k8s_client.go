package k8sclient

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

type K8SClient interface {
	GetSecret(namespace, name string) (*corev1.Secret, error)
}

type k8sClient struct {
	clientset *kubernetes.Clientset
}

func NewK8SClient() (K8SClient, error) {
	config, err := rest.InClusterConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to create in-cluster config: %w", err)
	}

	// Create a clientset
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create clientset: %w", err)
	}
	return &k8sClient{
		clientset: clientset,
	}, nil
}

func (k *k8sClient) GetSecret(namespace, name string) (*corev1.Secret, error) {
	secret, err := k.clientset.CoreV1().Secrets(namespace).Get(context.TODO(), name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to get secret %s/%s: %w", namespace, name, err)
	}
	return secret, nil
}
