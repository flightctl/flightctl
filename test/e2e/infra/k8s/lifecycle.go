// Package k8s provides Kubernetes-specific implementations of the infra providers.
package k8s

import (
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/flightctl/flightctl/test/e2e/infra"
	"github.com/sirupsen/logrus"
)

// ServiceLifecycleProvider implements infra.ServiceLifecycleProvider for Kubernetes environments.
type ServiceLifecycleProvider struct {
	namespace       string
	workerNamespace string
	kubeConfig      string
	kubeContext     string
}

// NewServiceLifecycleProvider creates a new K8s ServiceLifecycleProvider.
// If namespace is empty, it will be auto-detected.
func NewServiceLifecycleProvider(namespace string) *ServiceLifecycleProvider {
	return NewServiceLifecycleProviderWithConfig(namespace, nil)
}

// NewServiceLifecycleProviderWithConfig creates a new K8s ServiceLifecycleProvider with explicit configuration.
func NewServiceLifecycleProviderWithConfig(namespace string, config *infra.EnvironmentConfig) *ServiceLifecycleProvider {
	p := &ServiceLifecycleProvider{
		namespace: namespace,
	}

	// Apply config if provided
	if config != nil {
		if config.Namespace != "" {
			p.namespace = config.Namespace
		}
		p.kubeConfig = config.KubeConfig
		p.kubeContext = config.KubeContext
	}

	if p.namespace == "" {
		p.namespace = p.detectMainNamespace()
	}
	p.workerNamespace = p.detectWorkerNamespace(p.namespace)
	return p
}

// serviceToK8sResource maps logical service names to K8s deployment names and labels.
func (p *ServiceLifecycleProvider) serviceToK8sResource(service infra.ServiceName) (deploymentName, label, namespace string) {
	switch service {
	case infra.ServiceRedis:
		return "flightctl-kv", "flightctl.service=flightctl-kv", p.detectRedisNamespace()
	case infra.ServiceAPI:
		return "flightctl-api", "flightctl.service=flightctl-api", p.namespace
	case infra.ServiceWorker:
		return "flightctl-worker", "flightctl.service=flightctl-worker", p.workerNamespace
	case infra.ServicePeriodic:
		return "flightctl-periodic", "flightctl.service=flightctl-periodic", p.namespace
	case infra.ServiceTelemetryGateway:
		return "flightctl-telemetry-gateway", "flightctl.service=flightctl-telemetry-gateway", "flightctl-external"
	case infra.ServiceDB:
		return "flightctl-db", "flightctl.service=flightctl-db", p.namespace
	case infra.ServiceUI:
		return "flightctl-ui", "flightctl.service=flightctl-ui", p.namespace
	case infra.ServiceAlertmanager:
		return "flightctl-alertmanager", "flightctl.service=flightctl-alertmanager", p.namespace
	case infra.ServiceImageBuilderAPI:
		return "flightctl-imagebuilder-api", "flightctl.service=flightctl-imagebuilder-api", p.namespace
	default:
		return string(service), fmt.Sprintf("flightctl.service=%s", service), p.namespace
	}
}

// IsRunning checks if a service is currently running.
func (p *ServiceLifecycleProvider) IsRunning(service infra.ServiceName) (bool, error) {
	_, label, ns := p.serviceToK8sResource(service)

	cmd := exec.Command("kubectl", "get", "pod", "-n", ns, "-l", label, "-o", "jsonpath={.items[0].status.phase}")
	output, err := cmd.Output()
	if err != nil {
		return false, fmt.Errorf("failed to get pod status: %w", err)
	}

	phase := strings.TrimSpace(string(output))
	return phase == "Running", nil
}

// Start starts a stopped service by scaling the deployment to 1 replica.
func (p *ServiceLifecycleProvider) Start(service infra.ServiceName) error {
	deploymentName, _, ns := p.serviceToK8sResource(service)

	cmd := exec.Command("kubectl", "scale", "deployment", deploymentName, "-n", ns, "--replicas=1")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to scale up %s deployment: %w", deploymentName, err)
	}

	logrus.Infof("K8s: scaled up deployment %s in namespace %s", deploymentName, ns)
	return nil
}

// Stop stops a running service by scaling the deployment to 0 replicas.
func (p *ServiceLifecycleProvider) Stop(service infra.ServiceName) error {
	deploymentName, _, ns := p.serviceToK8sResource(service)

	cmd := exec.Command("kubectl", "scale", "deployment", deploymentName, "-n", ns, "--replicas=0")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to scale down %s deployment: %w", deploymentName, err)
	}

	logrus.Infof("K8s: scaled down deployment %s in namespace %s", deploymentName, ns)
	return nil
}

// Restart restarts a service by deleting its pod.
func (p *ServiceLifecycleProvider) Restart(service infra.ServiceName) error {
	_, label, ns := p.serviceToK8sResource(service)

	cmd := exec.Command("kubectl", "delete", "pod", "-n", ns, "-l", label, "--wait=false")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to delete pod for %s: %w", service, err)
	}

	logrus.Infof("K8s: deleted pod with label %s in namespace %s", label, ns)
	return nil
}

// WaitForReady waits for a service to be ready.
func (p *ServiceLifecycleProvider) WaitForReady(service infra.ServiceName, timeout time.Duration) error {
	_, label, ns := p.serviceToK8sResource(service)

	deadline := time.Now().Add(timeout)
	polling := 250 * time.Millisecond

	for time.Now().Before(deadline) {
		// Check pod phase
		cmd := exec.Command("kubectl", "get", "pod", "-n", ns, "-l", label, "-o", "jsonpath={.items[0].status.phase}")
		output, err := cmd.Output()
		if err == nil {
			phase := strings.TrimSpace(string(output))
			if phase == "Running" {
				// Also check ready condition
				cmd = exec.Command("kubectl", "get", "pod", "-n", ns, "-l", label, "-o", "jsonpath={.items[0].status.conditions[?(@.type=='Ready')].status}")
				output, err = cmd.Output()
				if err == nil && strings.TrimSpace(string(output)) == "True" {
					logrus.Infof("K8s: service %s is ready in namespace %s", service, ns)
					return nil
				}
			}
		}
		time.Sleep(polling)
	}

	return fmt.Errorf("timeout waiting for %s to be ready", service)
}

// AreServicesHealthy checks if all flightctl services are healthy.
func (p *ServiceLifecycleProvider) AreServicesHealthy() (bool, error) {
	// Check Worker first (most critical for queue processing)
	workerHealthy, err := p.isDeploymentHealthy("flightctl-worker", p.workerNamespace)
	if err != nil || !workerHealthy {
		return false, err
	}

	// Check API (less critical but good to verify)
	apiHealthy, err := p.isDeploymentHealthy("flightctl-api", p.namespace)
	if err != nil {
		// API might not exist - worker being healthy is sufficient
		logrus.Warnf("K8s: could not check flightctl-api: %v", err)
		return true, nil
	}

	return apiHealthy, nil
}

func (p *ServiceLifecycleProvider) isDeploymentHealthy(deploymentName, namespace string) (bool, error) {
	cmd := exec.Command("kubectl", "get", "deployment", deploymentName, "-n", namespace, "-o", "jsonpath={.status.readyReplicas}")
	output, err := cmd.Output()
	if err != nil {
		return false, fmt.Errorf("failed to get deployment %s status: %w", deploymentName, err)
	}

	readyReplicas := strings.TrimSpace(string(output))
	if readyReplicas == "" || readyReplicas == "0" {
		return false, nil
	}

	return true, nil
}

// detectMainNamespace tries to find the namespace where flightctl-api is deployed.
func (p *ServiceLifecycleProvider) detectMainNamespace() string {
	cmd := exec.Command("kubectl", "get", "deployment", "flightctl-api", "--all-namespaces", "-o", "jsonpath={.items[0].metadata.namespace}")
	output, err := cmd.Output()
	if err == nil && len(output) > 0 {
		ns := strings.TrimSpace(string(output))
		if ns != "" {
			logrus.Infof("K8s: detected main namespace: %s", ns)
			return ns
		}
	}

	// Try common namespaces
	for _, ns := range []string{"flightctl", "default", "flightctl-system"} {
		cmd := exec.Command("kubectl", "get", "deployment", "flightctl-api", "-n", ns, "--ignore-not-found", "-o", "name")
		output, err := cmd.Output()
		if err == nil && strings.Contains(string(output), "flightctl-api") {
			logrus.Infof("K8s: detected main namespace: %s", ns)
			return ns
		}
	}

	logrus.Warn("K8s: could not detect main namespace, using default: flightctl")
	return "flightctl"
}

// detectWorkerNamespace tries to find the namespace where flightctl-worker is deployed.
func (p *ServiceLifecycleProvider) detectWorkerNamespace(mainNamespace string) string {
	cmd := exec.Command("kubectl", "get", "deployment", "flightctl-worker", "--all-namespaces", "-o", "jsonpath={.items[0].metadata.namespace}")
	output, err := cmd.Output()
	if err == nil && len(output) > 0 {
		ns := strings.TrimSpace(string(output))
		if ns != "" {
			logrus.Infof("K8s: detected worker namespace: %s", ns)
			return ns
		}
	}

	// Try main namespace first, then common namespaces
	namespaces := []string{}
	if mainNamespace != "" {
		namespaces = append(namespaces, mainNamespace)
	}
	namespaces = append(namespaces, "flightctl-internal", "flightctl", "default", "flightctl-system")

	for _, ns := range namespaces {
		cmd := exec.Command("kubectl", "get", "deployment", "flightctl-worker", "-n", ns, "--ignore-not-found", "-o", "name")
		output, err = cmd.Output()
		if err == nil && strings.Contains(string(output), "flightctl-worker") {
			logrus.Infof("K8s: detected worker namespace: %s", ns)
			return ns
		}
	}

	logrus.Warn("K8s: could not detect worker namespace, using default: flightctl-internal")
	return "flightctl-internal"
}

// detectRedisNamespace tries to find the namespace where Redis is deployed.
func (p *ServiceLifecycleProvider) detectRedisNamespace() string {
	// Try pod first
	cmd := exec.Command("kubectl", "get", "pod", "--all-namespaces", "-l", "flightctl.service=flightctl-kv", "-o", "jsonpath={.items[0].metadata.namespace}")
	output, err := cmd.Output()
	if err == nil && len(output) > 0 {
		ns := strings.TrimSpace(string(output))
		if ns != "" {
			return ns
		}
	}

	// Try deployment/statefulset
	cmd = exec.Command("kubectl", "get", "deployment,statefulset", "--all-namespaces", "-l", "flightctl.service=flightctl-kv", "-o", "jsonpath={.items[0].metadata.namespace}")
	output, err = cmd.Output()
	if err == nil && len(output) > 0 {
		ns := strings.TrimSpace(string(output))
		if ns != "" {
			return ns
		}
	}

	// Try common namespaces
	for _, ns := range []string{"flightctl-internal", "flightctl", "default", "flightctl-system"} {
		cmd := exec.Command("kubectl", "get", "pod", "-n", ns, "-l", "flightctl.service=flightctl-kv", "--ignore-not-found", "-o", "name")
		output, err = cmd.Output()
		if err == nil && strings.Contains(string(output), "flightctl-kv") {
			return ns
		}
	}

	return "flightctl-internal"
}
