// Package k8s provides Kubernetes-specific implementations of the infra providers.
package k8s

import (
	"encoding/base64"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/flightctl/flightctl/test/e2e/infra"
	"github.com/sirupsen/logrus"
)

// namespaceType indicates how to resolve a service's namespace.
type namespaceType int

const (
	nsMain     namespaceType = iota // use p.namespace
	nsWorker                        // use p.workerNamespace
	nsRedis                         // use p.detectRedisNamespace()
	nsExternal                      // use "flightctl-external"
)

// k8sServiceInfo holds metadata about a K8s service.
type k8sServiceInfo struct {
	ServiceName   string        // K8s service name (e.g., "flightctl-api")
	ConfigMapName string        // ConfigMap name for config (e.g., "flightctl-api-config")
	Label         string        // Pod selector label
	Port          int           // Default port
	NSType        namespaceType // How to resolve namespace
}

// k8sServiceRegistry maps service names to their K8s-specific metadata.
var k8sServiceRegistry = map[infra.ServiceName]k8sServiceInfo{
	infra.ServiceRedis:            {ServiceName: "flightctl-kv", ConfigMapName: "flightctl-kv-config", Label: "flightctl.service=flightctl-kv", Port: 6379, NSType: nsRedis},
	infra.ServiceAPI:              {ServiceName: "flightctl-api", ConfigMapName: "flightctl-api-config", Label: "flightctl.service=flightctl-api", Port: 3443, NSType: nsMain},
	infra.ServiceWorker:           {ServiceName: "flightctl-worker", ConfigMapName: "flightctl-worker-config", Label: "flightctl.service=flightctl-worker", Port: 7443, NSType: nsWorker},
	infra.ServicePeriodic:         {ServiceName: "flightctl-periodic", ConfigMapName: "flightctl-periodic-config", Label: "flightctl.service=flightctl-periodic", Port: 0, NSType: nsMain},
	infra.ServiceTelemetryGateway: {ServiceName: "flightctl-telemetry-gateway", ConfigMapName: "flightctl-telemetry-gateway-config", Label: "flightctl.service=flightctl-telemetry-gateway", Port: 9464, NSType: nsExternal},
	infra.ServiceDB:               {ServiceName: "flightctl-db", ConfigMapName: "flightctl-db-config", Label: "flightctl.service=flightctl-db", Port: 5432, NSType: nsMain},
	infra.ServiceUI:               {ServiceName: "flightctl-ui", ConfigMapName: "flightctl-ui-config", Label: "flightctl.service=flightctl-ui", Port: 9001, NSType: nsMain},
	infra.ServiceAlertmanager:     {ServiceName: "flightctl-alertmanager", ConfigMapName: "flightctl-alertmanager-config", Label: "flightctl.service=flightctl-alertmanager", Port: 9093, NSType: nsMain},
	infra.ServiceImageBuilderAPI:  {ServiceName: "flightctl-imagebuilder-api", ConfigMapName: "flightctl-imagebuilder-api-config", Label: "flightctl.service=flightctl-imagebuilder-api", Port: 8445, NSType: nsMain},
}

// InfraProvider implements infra.InfraProvider for Kubernetes environments.
type InfraProvider struct {
	namespace       string
	workerNamespace string
	envType         string // "kind" or "ocp"
	kubeConfig      string // path to kubeconfig file
	kubeContext     string // kubeconfig context to use
}

// NewInfraProvider creates a new K8s InfraProvider.
// If namespace is empty, it will be auto-detected.
func NewInfraProvider(namespace string) *InfraProvider {
	return NewInfraProviderWithConfig(namespace, nil)
}

// NewInfraProviderWithConfig creates a new K8s InfraProvider with explicit configuration.
func NewInfraProviderWithConfig(namespace string, config *infra.EnvironmentConfig) *InfraProvider {
	p := &InfraProvider{
		namespace: namespace,
		envType:   detectEnvironmentType(),
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

// kubectlArgs returns the base kubectl arguments including kubeconfig and context if set.
func (p *InfraProvider) kubectlArgs(args ...string) []string {
	var baseArgs []string
	if p.kubeConfig != "" {
		baseArgs = append(baseArgs, "--kubeconfig", p.kubeConfig)
	}
	if p.kubeContext != "" {
		baseArgs = append(baseArgs, "--context", p.kubeContext)
	}
	return append(baseArgs, args...)
}

// GetConfigValue retrieves a configuration value from a ConfigMap.
func (p *InfraProvider) GetConfigValue(name, key string) (string, error) {
	// Determine namespace based on the config name
	ns := p.namespaceForResource(name)

	jsonPath := fmt.Sprintf("jsonpath={.data.%s}", key)
	args := p.kubectlArgs("get", "configmap", name, "-n", ns, "-o", jsonPath)
	cmd := exec.Command("kubectl", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("failed to get configmap %s/%s: %w: %s", ns, name, err, strings.TrimSpace(string(output)))
	}

	return string(output), nil
}

// GetServiceConfig retrieves the full configuration content for a service.
func (p *InfraProvider) GetServiceConfig(service infra.ServiceName) (string, error) {
	info, ns := p.getServiceInfo(service)

	jsonPath := `jsonpath={.data.config\.yaml}`
	args := p.kubectlArgs("get", "configmap", info.ConfigMapName, "-n", ns, "-o", jsonPath)
	cmd := exec.Command("kubectl", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("failed to get config for service %s: %w: %s", service, err, strings.TrimSpace(string(output)))
	}

	return string(output), nil
}

// getServiceInfo returns service metadata and resolved namespace.
func (p *InfraProvider) getServiceInfo(service infra.ServiceName) (k8sServiceInfo, string) {
	info, ok := k8sServiceRegistry[service]
	if !ok {
		// Default for unknown services
		info = k8sServiceInfo{
			ServiceName:   string(service),
			ConfigMapName: string(service) + "-config",
			Label:         fmt.Sprintf("flightctl.service=%s", service),
			Port:          0,
			NSType:        nsMain,
		}
	}
	return info, p.resolveNamespace(info.NSType)
}

// resolveNamespace returns the actual namespace based on type.
func (p *InfraProvider) resolveNamespace(nsType namespaceType) string {
	switch nsType {
	case nsWorker:
		return p.workerNamespace
	case nsRedis:
		return p.detectRedisNamespace()
	case nsExternal:
		return "flightctl-external"
	default:
		return p.namespace
	}
}

// GetSecretValue retrieves a secret value from a Secret (base64 decoded).
func (p *InfraProvider) GetSecretValue(name, key string) (string, error) {
	// Determine namespace based on the secret name
	ns := p.namespaceForResource(name)

	jsonPath := fmt.Sprintf("jsonpath={.data.%s}", key)
	args := p.kubectlArgs("get", "secret", name, "-n", ns, "-o", jsonPath)
	cmd := exec.Command("kubectl", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("failed to get secret %s/%s: %w: %s", ns, name, err, strings.TrimSpace(string(output)))
	}

	// Decode base64
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(output)))
	if err != nil {
		return "", fmt.Errorf("failed to decode secret value: %w", err)
	}

	return string(decoded), nil
}

// GetServiceEndpoint returns the host and port for a named service.
// For K8s, this returns the service DNS name and port within the cluster.
func (p *InfraProvider) GetServiceEndpoint(service infra.ServiceName) (string, int, error) {
	info, ns := p.getServiceInfo(service)

	// Get the service port from K8s
	args := p.kubectlArgs("get", "svc", info.ServiceName, "-n", ns, "-o", "jsonpath={.spec.ports[0].port}")
	cmd := exec.Command("kubectl", args...)
	output, err := cmd.Output()
	if err != nil {
		logrus.Warnf("K8s: could not get service %s port, using default %d: %v", info.ServiceName, info.Port, err)
		return fmt.Sprintf("%s.%s.svc.cluster.local", info.ServiceName, ns), info.Port, nil
	}

	port := info.Port
	if portStr := strings.TrimSpace(string(output)); portStr != "" {
		_, _ = fmt.Sscanf(portStr, "%d", &port)
	}

	return fmt.Sprintf("%s.%s.svc.cluster.local", info.ServiceName, ns), port, nil
}

// ExposeService makes an internal K8s service accessible from the test host via port-forwarding.
// Use this for services not normally exposed externally (e.g., metrics endpoints).
// Returns the localhost URL and a cleanup function that stops the port-forward.
func (p *InfraProvider) ExposeService(service infra.ServiceName, protocol string) (string, func(), error) {
	info, ns := p.getServiceInfo(service)

	// Find a free local port
	localPort, err := getFreePort()
	if err != nil {
		return "", nil, fmt.Errorf("failed to get free port: %w", err)
	}

	// Start port-forward
	portMapping := fmt.Sprintf("%d:%d", localPort, info.Port)
	args := p.kubectlArgs("port-forward", "-n", ns, "svc/"+info.ServiceName, portMapping)
	cmd := exec.Command("kubectl", args...)
	if err := cmd.Start(); err != nil {
		return "", nil, fmt.Errorf("failed to start port-forward for %s: %w", service, err)
	}

	// Give port-forward time to establish
	time.Sleep(500 * time.Millisecond)

	url := fmt.Sprintf("%s://127.0.0.1:%d", protocol, localPort)
	cleanup := func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}

	logrus.Infof("K8s: port-forwarding %s to %s", service, url)
	return url, cleanup, nil
}

// getFreePort finds an available port on localhost.
func getFreePort() (int, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port, nil
}

// ExecInService executes a command in the context of a service's pod.
func (p *InfraProvider) ExecInService(service infra.ServiceName, command []string) (string, error) {
	info, ns := p.getServiceInfo(service)

	// Get pod name
	args := p.kubectlArgs("get", "pod", "-n", ns, "-l", info.Label, "-o", "jsonpath={.items[0].metadata.name}")
	cmd := exec.Command("kubectl", args...)
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to get pod for service %s: %w", service, err)
	}

	podName := strings.TrimSpace(string(output))
	if podName == "" {
		return "", fmt.Errorf("no pod found for service %s", service)
	}

	// Execute command in pod
	execArgs := p.kubectlArgs("exec", "-n", ns, podName, "--")
	execArgs = append(execArgs, command...)
	cmd = exec.Command("kubectl", execArgs...)
	output, err = cmd.CombinedOutput()
	if err != nil {
		return string(output), fmt.Errorf("failed to exec in pod %s: %w: %s", podName, err, strings.TrimSpace(string(output)))
	}

	return string(output), nil
}

// GetEnvironmentType returns "kind" or "ocp".
func (p *InfraProvider) GetEnvironmentType() string {
	return p.envType
}

// GetNamespace returns the main namespace for flightctl services.
func (p *InfraProvider) GetNamespace() string {
	return p.namespace
}

// namespaceForResource determines the namespace for a resource based on naming conventions.
func (p *InfraProvider) namespaceForResource(resourceName string) string {
	// Resources with "kv" or "redis" go to the redis namespace
	if strings.Contains(resourceName, "kv") || strings.Contains(resourceName, "redis") {
		return p.detectRedisNamespace()
	}
	// Resources with "worker" go to the worker namespace
	if strings.Contains(resourceName, "worker") {
		return p.workerNamespace
	}
	// Default to main namespace
	return p.namespace
}

// detectEnvironmentType detects whether we're running in KIND or OCP.
func detectEnvironmentType() string {
	// Check environment variable first
	if envType := os.Getenv("E2E_ENVIRONMENT"); envType != "" {
		if envType == "kind" || envType == "ocp" {
			return envType
		}
	}

	// Try to detect from kubectl context
	cmd := exec.Command("kubectl", "config", "current-context")
	output, err := cmd.Output()
	if err == nil {
		context := strings.TrimSpace(string(output))
		if strings.Contains(context, "kind") {
			return infra.EnvironmentKind
		}
	}

	// Check if this is OpenShift by looking for the openshift API
	cmd = exec.Command("kubectl", "api-resources", "--api-group=route.openshift.io")
	if err := cmd.Run(); err == nil {
		return infra.EnvironmentOCP
	}

	// Default to kind
	return infra.EnvironmentKind
}

// detectMainNamespace tries to find the namespace where flightctl-api is deployed.
func (p *InfraProvider) detectMainNamespace() string {
	cmd := exec.Command("kubectl", "get", "deployment", "flightctl-api", "--all-namespaces", "-o", "jsonpath={.items[0].metadata.namespace}")
	output, err := cmd.Output()
	if err == nil && len(output) > 0 {
		ns := strings.TrimSpace(string(output))
		if ns != "" {
			return ns
		}
	}

	for _, ns := range []string{"flightctl", "default", "flightctl-system"} {
		cmd := exec.Command("kubectl", "get", "deployment", "flightctl-api", "-n", ns, "--ignore-not-found", "-o", "name")
		output, err := cmd.Output()
		if err == nil && strings.Contains(string(output), "flightctl-api") {
			return ns
		}
	}

	return "flightctl"
}

// detectWorkerNamespace tries to find the namespace where flightctl-worker is deployed.
func (p *InfraProvider) detectWorkerNamespace(mainNamespace string) string {
	cmd := exec.Command("kubectl", "get", "deployment", "flightctl-worker", "--all-namespaces", "-o", "jsonpath={.items[0].metadata.namespace}")
	output, err := cmd.Output()
	if err == nil && len(output) > 0 {
		ns := strings.TrimSpace(string(output))
		if ns != "" {
			return ns
		}
	}

	namespaces := []string{}
	if mainNamespace != "" {
		namespaces = append(namespaces, mainNamespace)
	}
	namespaces = append(namespaces, "flightctl-internal", "flightctl", "default", "flightctl-system")

	for _, ns := range namespaces {
		cmd := exec.Command("kubectl", "get", "deployment", "flightctl-worker", "-n", ns, "--ignore-not-found", "-o", "name")
		output, err = cmd.Output()
		if err == nil && strings.Contains(string(output), "flightctl-worker") {
			return ns
		}
	}

	return "flightctl-internal"
}

// detectRedisNamespace tries to find the namespace where Redis is deployed.
func (p *InfraProvider) detectRedisNamespace() string {
	cmd := exec.Command("kubectl", "get", "pod", "--all-namespaces", "-l", "flightctl.service=flightctl-kv", "-o", "jsonpath={.items[0].metadata.namespace}")
	output, err := cmd.Output()
	if err == nil && len(output) > 0 {
		ns := strings.TrimSpace(string(output))
		if ns != "" {
			return ns
		}
	}

	cmd = exec.Command("kubectl", "get", "deployment,statefulset", "--all-namespaces", "-l", "flightctl.service=flightctl-kv", "-o", "jsonpath={.items[0].metadata.namespace}")
	output, err = cmd.Output()
	if err == nil && len(output) > 0 {
		ns := strings.TrimSpace(string(output))
		if ns != "" {
			return ns
		}
	}

	for _, ns := range []string{"flightctl-internal", "flightctl", "default", "flightctl-system"} {
		cmd := exec.Command("kubectl", "get", "pod", "-n", ns, "-l", "flightctl.service=flightctl-kv", "--ignore-not-found", "-o", "name")
		output, err = cmd.Output()
		if err == nil && strings.Contains(string(output), "flightctl-kv") {
			return ns
		}
	}

	return "flightctl-internal"
}
