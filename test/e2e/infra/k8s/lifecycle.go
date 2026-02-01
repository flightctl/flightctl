// Package k8s provides Kubernetes-specific implementations of the infra providers.
package k8s

import (
	"context"
	"fmt"
	"time"

	"github.com/flightctl/flightctl/test/e2e/infra"
	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// ServiceLifecycleProvider implements infra.ServiceLifecycleProvider for Kubernetes environments.
// All service and namespace resolution is delegated to InfraProvider.
type ServiceLifecycleProvider struct {
	client kubernetes.Interface
	infraP *InfraProvider
}

// NewServiceLifecycleProviderWithConfig creates a new K8s ServiceLifecycleProvider.
// infraP is required; all service/namespace resolution comes from it.
func NewServiceLifecycleProviderWithConfig(client kubernetes.Interface, infraP *InfraProvider) *ServiceLifecycleProvider {
	if infraP == nil {
		panic("k8s ServiceLifecycleProvider requires a non-nil InfraProvider")
	}
	return &ServiceLifecycleProvider{
		client: client,
		infraP: infraP,
	}
}

// IsRunning checks if a service is currently running.
func (p *ServiceLifecycleProvider) IsRunning(service infra.ServiceName) (bool, error) {
	if p.client == nil {
		return false, fmt.Errorf("no Kubernetes client")
	}
	_, label, ns, err := p.infraP.GetServiceNamespaceAndMetadata(service)
	if err != nil {
		return false, err
	}
	ctx := context.Background()

	var list *corev1.PodList
	list, err = p.client.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{LabelSelector: label})
	if err != nil {
		return false, fmt.Errorf("failed to list pods: %w", err)
	}
	if len(list.Items) == 0 {
		return false, nil
	}
	phase := list.Items[0].Status.Phase
	return phase == corev1.PodRunning, nil
}

// Start starts a stopped service by scaling the deployment to 1 replica.
func (p *ServiceLifecycleProvider) Start(service infra.ServiceName) error {
	if p.client == nil {
		return fmt.Errorf("no Kubernetes client")
	}
	deploymentName, _, ns, err := p.infraP.GetServiceNamespaceAndMetadata(service)
	if err != nil {
		return err
	}
	ctx := context.Background()

	dep, err := p.client.AppsV1().Deployments(ns).Get(ctx, deploymentName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get deployment %s: %w", deploymentName, err)
	}
	one := int32(1)
	dep.Spec.Replicas = &one
	if _, err := p.client.AppsV1().Deployments(ns).Update(ctx, dep, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("failed to scale up %s: %w", deploymentName, err)
	}
	logrus.Infof("K8s: scaled up deployment %s in namespace %s", deploymentName, ns)
	return nil
}

// Stop stops a running service by scaling the deployment to 0 replicas.
func (p *ServiceLifecycleProvider) Stop(service infra.ServiceName) error {
	if p.client == nil {
		return fmt.Errorf("no Kubernetes client")
	}
	deploymentName, _, ns, err := p.infraP.GetServiceNamespaceAndMetadata(service)
	if err != nil {
		return err
	}
	ctx := context.Background()

	dep, err := p.client.AppsV1().Deployments(ns).Get(ctx, deploymentName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get deployment %s: %w", deploymentName, err)
	}
	zero := int32(0)
	dep.Spec.Replicas = &zero
	if _, err := p.client.AppsV1().Deployments(ns).Update(ctx, dep, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("failed to scale down %s: %w", deploymentName, err)
	}
	logrus.Infof("K8s: scaled down deployment %s in namespace %s", deploymentName, ns)
	return nil
}

// Restart restarts a service by deleting its pod.
func (p *ServiceLifecycleProvider) Restart(service infra.ServiceName) error {
	if p.client == nil {
		return fmt.Errorf("no Kubernetes client")
	}
	_, label, ns, err := p.infraP.GetServiceNamespaceAndMetadata(service)
	if err != nil {
		return err
	}
	ctx := context.Background()

	err = p.client.CoreV1().Pods(ns).DeleteCollection(ctx, metav1.DeleteOptions{}, metav1.ListOptions{LabelSelector: label})
	if err != nil {
		return fmt.Errorf("failed to delete pod for %s: %w", service, err)
	}
	p.infraP.InvalidateExposeCache(service)
	logrus.Infof("K8s: deleted pod with label %s in namespace %s", label, ns)
	return nil
}

// WaitForReady waits for a service to be ready (pod Ready, then Service has endpoints).
func (p *ServiceLifecycleProvider) WaitForReady(service infra.ServiceName, timeout time.Duration) error {
	if p.client == nil {
		return fmt.Errorf("no Kubernetes client")
	}
	svcName, label, ns, err := p.infraP.GetServiceNamespaceAndMetadata(service)
	if err != nil {
		return err
	}
	ctx := context.Background()
	deadline := time.Now().Add(timeout)
	polling := 250 * time.Millisecond

	var list *corev1.PodList
	for time.Now().Before(deadline) {
		list, err = p.client.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{LabelSelector: label})
		if err == nil && len(list.Items) > 0 {
			pod := &list.Items[0]
			if pod.Status.Phase == corev1.PodRunning {
				for _, c := range pod.Status.Conditions {
					if c.Type == corev1.PodReady && c.Status == corev1.ConditionTrue {
						// Pod is ready; wait for Service endpoints so port-forward has a backend
						if err := p.waitForServiceEndpoints(ctx, ns, svcName, deadline); err != nil {
							return err
						}
						logrus.Infof("K8s: service %s is ready in namespace %s", service, ns)
						return nil
					}
				}
			}
		}
		time.Sleep(polling)
	}
	return fmt.Errorf("timeout waiting for %s to be ready", service)
}

// waitForServiceEndpoints waits until the Service has at least one ready endpoint.
func (p *ServiceLifecycleProvider) waitForServiceEndpoints(ctx context.Context, ns, svcName string, deadline time.Time) error {
	for time.Now().Before(deadline) {
		eps, err := p.client.CoreV1().Endpoints(ns).Get(ctx, svcName, metav1.GetOptions{})
		if err != nil {
			time.Sleep(250 * time.Millisecond)
			continue
		}
		for _, sub := range eps.Subsets {
			if len(sub.Addresses) > 0 {
				return nil
			}
		}
		time.Sleep(250 * time.Millisecond)
	}
	return fmt.Errorf("timeout waiting for service %s endpoints", svcName)
}

// AreServicesHealthy checks if all flightctl services are healthy.
func (p *ServiceLifecycleProvider) AreServicesHealthy() (bool, error) {
	_, _, workerNS, err := p.infraP.GetServiceNamespaceAndMetadata(infra.ServiceWorker)
	if err != nil {
		return false, err
	}
	_, _, apiNS, err := p.infraP.GetServiceNamespaceAndMetadata(infra.ServiceAPI)
	if err != nil {
		return false, err
	}
	// Check Worker first (most critical for queue processing)
	workerHealthy, err := p.isDeploymentHealthy("flightctl-worker", workerNS)
	if err != nil || !workerHealthy {
		return false, err
	}
	// Check API (less critical but good to verify)
	apiHealthy, err := p.isDeploymentHealthy("flightctl-api", apiNS)
	if err != nil {
		// API might not exist - worker being healthy is sufficient
		logrus.Warnf("K8s: could not check flightctl-api: %v", err)
		return true, nil
	}
	return apiHealthy, nil
}

func (p *ServiceLifecycleProvider) isDeploymentHealthy(deploymentName, namespace string) (bool, error) {
	if p.client == nil {
		return false, fmt.Errorf("no Kubernetes client")
	}
	dep, err := p.client.AppsV1().Deployments(namespace).Get(context.Background(), deploymentName, metav1.GetOptions{})
	if err != nil {
		return false, fmt.Errorf("failed to get deployment %s: %w", deploymentName, err)
	}
	return dep.Status.ReadyReplicas >= 1, nil
}
