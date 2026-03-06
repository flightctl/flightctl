// Package k8s provides Kubernetes-specific implementations of the infra providers.
package k8s

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/flightctl/flightctl/test/e2e/infra"
	"github.com/sirupsen/logrus"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// Env vars to override detected namespaces (no fallbacks; detection or override required).
const (
	envInternalNS = "FLIGHTCTL_INTERNAL_NS" // override for internal namespace (worker, redis)
	envExternalNS = "FLIGHTCTL_EXTERNAL_NS" // override for external namespace (API, DB, UI, alertmanager, telemetry-gateway, etc.)
)

// Only two namespace types: internal (worker, redis) and external (everything else).
type namespaceType int

const (
	nsInternal namespaceType = iota // worker, redis
	nsExternal                      // API, DB, UI, alertmanager, telemetry-gateway, alertmanager-proxy, imagebuilder, periodic
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
// Kind: internal = flightctl-internal (kv, worker, db, alertmanager, periodic, imagebuilder-worker); external = flightctl-external (api, ui, alertmanager-proxy, telemetry-gateway, imagebuilder-api).
var k8sServiceRegistry = map[infra.ServiceName]k8sServiceInfo{
	infra.ServiceRedis:              {ServiceName: "flightctl-kv", ConfigMapName: "flightctl-kv-config", Label: "flightctl.service=flightctl-kv", Port: 6379, NSType: nsInternal},
	infra.ServiceAPI:                {ServiceName: "flightctl-api", ConfigMapName: "flightctl-api-config", Label: "flightctl.service=flightctl-api", Port: 3443, NSType: nsExternal},
	infra.ServiceWorker:             {ServiceName: "flightctl-worker", ConfigMapName: "flightctl-worker-config", Label: "flightctl.service=flightctl-worker", Port: 7443, NSType: nsInternal},
	infra.ServicePeriodic:           {ServiceName: "flightctl-periodic", ConfigMapName: "flightctl-periodic-config", Label: "flightctl.service=flightctl-periodic", Port: 0, NSType: nsInternal},
	infra.ServiceTelemetryGateway:   {ServiceName: "flightctl-telemetry-gateway", ConfigMapName: "flightctl-telemetry-gateway-config", Label: "flightctl.service=flightctl-telemetry-gateway", Port: 9464, NSType: nsExternal},
	infra.ServiceDB:                 {ServiceName: "flightctl-db", ConfigMapName: "flightctl-db-config", Label: "flightctl.service=flightctl-db", Port: 5432, NSType: nsInternal},
	infra.ServiceUI:                 {ServiceName: "flightctl-ui", ConfigMapName: "flightctl-ui-config", Label: "flightctl.service=flightctl-ui", Port: 9001, NSType: nsExternal},
	infra.ServiceAlertmanager:       {ServiceName: "flightctl-alertmanager", ConfigMapName: "flightctl-alertmanager-config", Label: "flightctl.service=flightctl-alertmanager", Port: 9093, NSType: nsInternal},
	infra.ServiceAlertmanagerProxy:  {ServiceName: "flightctl-alertmanager-proxy", ConfigMapName: "flightctl-alertmanager-proxy-config", Label: "flightctl.service=flightctl-alertmanager-proxy", Port: 8443, NSType: nsExternal},
	infra.ServiceImageBuilderAPI:    {ServiceName: "flightctl-imagebuilder-api", ConfigMapName: "flightctl-imagebuilder-api-config", Label: "flightctl.service=flightctl-imagebuilder-api", Port: 8445, NSType: nsExternal},
	infra.ServiceImageBuilderWorker: {ServiceName: "flightctl-imagebuilder-worker", ConfigMapName: "flightctl-imagebuilder-worker-config", Label: "flightctl.service=flightctl-imagebuilder-worker", Port: 8080, NSType: nsInternal},
}

// exposeCacheEntry holds a cached port-forward URL and its cleanup.
type exposeCacheEntry struct {
	url     string
	cleanup func()
}

// InfraProvider implements infra.InfraProvider for Kubernetes environments.
type InfraProvider struct {
	client            kubernetes.Interface // used for namespace detection when overrides not set
	internalNamespace string               // flightctl-internal: worker, kv, db, alertmanager, periodic, imagebuilder-worker
	externalNamespace string               // flightctl-external: api, ui, alertmanager-proxy, telemetry-gateway, imagebuilder-api
	envType           string               // "kind" or "ocp"
	kubeConfig        string
	kubeContext       string
	exposeCache       map[string]exposeCacheEntry
	exposeCacheMu     sync.Mutex
}

// NewInfraProvider creates a new K8s InfraProvider.
// If namespace is empty, it will be auto-detected via the k8s client.
func NewInfraProvider(namespace string) (*InfraProvider, error) {
	return NewInfraProviderWithConfig(namespace, nil, nil)
}

// NewInfraProviderWithConfig creates a new K8s InfraProvider with explicit configuration.
// client is optional; when nil a client is created from default kubeconfig for namespace detection.
// External namespace: config.Namespace (e.g. FLIGHTCTL_NS) or detect from pods with label flightctl.service=flightctl-api.
// Internal namespace: FLIGHTCTL_INTERNAL_NS or detect from pods with label flightctl.service=flightctl-worker.
// No fallbacks; returns error if detection fails when override not set.
func NewInfraProviderWithConfig(namespace string, config *infra.EnvironmentConfig, client kubernetes.Interface) (*InfraProvider, error) {
	p := &InfraProvider{}
	if config != nil {
		p.kubeConfig = config.KubeConfig
		p.kubeContext = config.KubeContext
	}
	p.envType = p.detectEnvironmentType()
	if client == nil {
		c, err := NewClient()
		if err != nil {
			return nil, fmt.Errorf("k8s client for detection: %w", err)
		}
		p.client = c
	} else {
		p.client = client
	}
	if namespace != "" {
		p.externalNamespace = namespace
	} else {
		ns, err := p.detectExternalNamespace()
		if err != nil {
			return nil, fmt.Errorf("external namespace: %w", err)
		}
		p.externalNamespace = ns
	}
	internalNS, err := p.detectInternalNamespace()
	if err != nil {
		return nil, fmt.Errorf("internal namespace: %w", err)
	}
	p.internalNamespace = internalNS
	return p, nil
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
	info, ns, err := p.getServiceInfo(service)
	if err != nil {
		return "", err
	}

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
func (p *InfraProvider) getServiceInfo(service infra.ServiceName) (k8sServiceInfo, string, error) {
	info, ok := k8sServiceRegistry[service]
	if !ok {
		info = k8sServiceInfo{
			ServiceName:   string(service),
			ConfigMapName: string(service) + "-config",
			Label:         fmt.Sprintf("flightctl.service=%s", service),
			Port:          0,
			NSType:        nsExternal,
		}
	}
	ns, err := p.resolveNamespace(info.NSType)
	if err != nil {
		return info, "", err
	}
	return info, ns, nil
}

// GetServiceNamespaceAndMetadata returns deployment name, pod label, and namespace for a service.
// Used by ServiceLifecycleProvider so all service/namespace resolution stays in InfraProvider.
func (p *InfraProvider) GetServiceNamespaceAndMetadata(service infra.ServiceName) (deploymentName, label, namespace string, err error) {
	info, ns, err := p.getServiceInfo(service)
	if err != nil {
		return "", "", "", err
	}
	return info.ServiceName, info.Label, ns, nil
}

// resolveNamespace returns the actual namespace based on type (internal or external only).
func (p *InfraProvider) resolveNamespace(nsType namespaceType) (string, error) {
	switch nsType {
	case nsInternal:
		return p.internalNamespace, nil
	case nsExternal:
		return p.externalNamespace, nil
	default:
		return p.externalNamespace, nil
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
	info, ns, err := p.getServiceInfo(service)
	if err != nil {
		return "", 0, err
	}

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
// Repeated calls for the same (service, protocol) return the same URL so polling (e.g. WaitForQueueAccessible) does not spawn a new port-forward every time.
// Port-forwards to svc/<ServiceName> in the service's namespace (internal for Redis/flightctl-kv).
// Returns the localhost URL and a cleanup function (no-op when cached; callers must not close the shared forward).
func (p *InfraProvider) ExposeService(service infra.ServiceName, protocol string) (string, func(), error) {
	cacheKey := string(service) + ":" + protocol
	p.exposeCacheMu.Lock()
	if p.exposeCache != nil {
		if e, ok := p.exposeCache[cacheKey]; ok {
			p.exposeCacheMu.Unlock()
			return e.url, func() {}, nil
		}
	} else {
		p.exposeCache = make(map[string]exposeCacheEntry)
	}
	p.exposeCacheMu.Unlock()

	info, ns, err := p.getServiceInfo(service)
	if err != nil {
		return "", nil, err
	}

	// Use the service's actual port from the cluster (same as GetServiceEndpoint)
	targetPort := info.Port
	if _, port, getErr := p.GetServiceEndpoint(service); getErr == nil {
		targetPort = port
	}

	// Find a free local port
	localPort, err := getFreePort()
	if err != nil {
		return "", nil, fmt.Errorf("failed to get free port: %w", err)
	}

	// Start port-forward to the Service (e.g. svc/flightctl-kv for Redis)
	portMapping := fmt.Sprintf("%d:%d", localPort, targetPort)
	args := p.kubectlArgs("port-forward", "-n", ns, "svc/"+info.ServiceName, portMapping)
	cmd := exec.Command("kubectl", args...)
	if err := cmd.Start(); err != nil {
		return "", nil, fmt.Errorf("failed to start port-forward for %s: %w", service, err)
	}

	// Give port-forward time to establish (tunnel and service backend must be ready)
	time.Sleep(2 * time.Second)

	url := fmt.Sprintf("%s://127.0.0.1:%d", protocol, localPort)
	cleanup := func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}

	p.exposeCacheMu.Lock()
	if e, ok := p.exposeCache[cacheKey]; ok {
		p.exposeCacheMu.Unlock()
		cleanup()
		return e.url, func() {}, nil
	}
	p.exposeCache[cacheKey] = exposeCacheEntry{url: url, cleanup: cleanup}
	p.exposeCacheMu.Unlock()

	logrus.Infof("K8s: port-forwarding %s to %s", service, url)
	return url, func() {}, nil
}

// InvalidateExposeCache stops any cached port-forward for the service (kills the process) and removes it from the cache.
// Call after restarting a service so the next ExposeService creates a new port-forward.
func (p *InfraProvider) InvalidateExposeCache(service infra.ServiceName) {
	prefix := string(service) + ":"
	p.exposeCacheMu.Lock()
	defer p.exposeCacheMu.Unlock()
	if p.exposeCache == nil {
		return
	}
	for key, e := range p.exposeCache {
		if strings.HasPrefix(key, prefix) {
			e.cleanup() // stop the port-forward process (Kill + Wait)
			delete(p.exposeCache, key)
		}
	}
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
	info, ns, err := p.getServiceInfo(service)
	if err != nil {
		return "", err
	}

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

// GetAPILoginToken returns a token for flightctl login --token.
// KIND: kubectl create token flightctl-admin -n <external namespace> (where API pods run, e.g. flightctl-external).
// OCP: oc whoami -t.
func (p *InfraProvider) GetAPILoginToken() (string, error) {
	switch p.envType {
	case infra.EnvironmentKind:
		args := p.kubectlArgs("create", "token", "flightctl-admin", "-n", p.externalNamespace)
		if p.kubeContext == "" {
			args = append(args, "--context", "kind-kind")
		}
		cmd := exec.Command("kubectl", args...)
		output, err := cmd.CombinedOutput()
		if err != nil {
			return "", fmt.Errorf("kubectl create token: %w: %s", err, strings.TrimSpace(string(output)))
		}
		return strings.TrimSpace(string(output)), nil
	case infra.EnvironmentOCP:
		cmd := exec.Command("oc", "whoami", "-t")
		output, err := cmd.CombinedOutput()
		if err != nil {
			return "", fmt.Errorf("oc whoami -t: %w: %s", err, strings.TrimSpace(string(output)))
		}
		return strings.TrimSpace(string(output)), nil
	default:
		return "", fmt.Errorf("GetAPILoginToken not supported for env type %q", p.envType)
	}
}

// SetServiceConfig updates a service's ConfigMap data key with the given content.
func (p *InfraProvider) SetServiceConfig(service infra.ServiceName, configKey, content string) error {
	info, ns, err := p.getServiceInfo(service)
	if err != nil {
		return err
	}
	patch := map[string]interface{}{
		"data": map[string]string{configKey: content},
	}
	patchBytes, err := json.Marshal(patch)
	if err != nil {
		return fmt.Errorf("marshal patch: %w", err)
	}
	tmp, err := os.CreateTemp("", "infra-configmap-patch-*.json")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(patchBytes); err != nil {
		return fmt.Errorf("write patch: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}
	args := p.kubectlArgs("patch", "configmap", info.ConfigMapName, "-n", ns, "--patch-file", tmp.Name(), "--type", "merge")
	cmd := exec.Command("kubectl", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("kubectl patch configmap %s: %w: %s", info.ConfigMapName, err, strings.TrimSpace(string(output)))
	}
	logrus.Infof("K8s: updated config %s/%s key %q", ns, info.ConfigMapName, configKey)
	return nil
}

// namespaceForService returns the namespace for a service (used by k8s SecretsProvider only).
func (p *InfraProvider) namespaceForService(service infra.ServiceName) (string, error) {
	_, ns, err := p.getServiceInfo(service)
	return ns, err
}

// GetInternalNamespace returns the namespace where internal services (worker, db, kv, etc.) run.
func (p *InfraProvider) GetInternalNamespace() string {
	return p.internalNamespace
}

// GetExternalNamespace returns the namespace where external services (API, UI, etc.) run (release namespace).
func (p *InfraProvider) GetExternalNamespace() string {
	return p.externalNamespace
}

// namespaceForResource determines the namespace for a resource based on naming conventions (internal vs external).
// Internal: kv, redis, worker, db, alertmanager, periodic, imagebuilder-worker.
func (p *InfraProvider) namespaceForResource(resourceName string) string {
	internal := strings.Contains(resourceName, "kv") || strings.Contains(resourceName, "redis") ||
		strings.Contains(resourceName, "worker") || strings.Contains(resourceName, "flightctl-db") ||
		strings.Contains(resourceName, "alertmanager") && !strings.Contains(resourceName, "proxy") ||
		strings.Contains(resourceName, "periodic") || strings.Contains(resourceName, "imagebuilder-worker")
	if internal {
		return p.internalNamespace
	}
	return p.externalNamespace
}

// detectEnvironmentType detects whether we're running in KIND or OCP.
func (p *InfraProvider) detectEnvironmentType() string {
	// Check environment variable first
	if envType := os.Getenv("E2E_ENVIRONMENT"); envType != "" {
		if envType == "kind" || envType == "ocp" {
			return envType
		}
	}

	// Try to detect from kubectl context
	cmd := exec.Command("kubectl", p.kubectlArgs("config", "current-context")...) //nolint:gosec // G204: args from internal test config (kubeConfig/kubeContext)
	output, err := cmd.Output()
	if err == nil {
		context := strings.TrimSpace(string(output))
		if strings.Contains(context, "kind") {
			return infra.EnvironmentKind
		}
	}

	// Check if this is OpenShift by looking for the openshift API
	cmd = exec.Command("kubectl", p.kubectlArgs("api-resources", "--api-group=route.openshift.io")...) //nolint:gosec // G204: args from internal test config (kubeConfig/kubeContext)
	if err := cmd.Run(); err == nil {
		return infra.EnvironmentOCP
	}

	// Default to kind
	return infra.EnvironmentKind
}

// detectExternalNamespace finds the namespace where external services (API, UI, etc.) are deployed.
// Override with FLIGHTCTL_EXTERNAL_NS. Detect from pods with label flightctl.service=flightctl-api. No fallback.
func (p *InfraProvider) detectExternalNamespace() (string, error) {
	if ns := os.Getenv(envExternalNS); ns != "" {
		return strings.TrimSpace(ns), nil
	}
	list, err := p.client.CoreV1().Pods(metav1.NamespaceAll).List(context.Background(), metav1.ListOptions{
		LabelSelector: "flightctl.service=flightctl-api",
		Limit:         1,
	})
	if err != nil {
		return "", fmt.Errorf("list pods flightctl.service=flightctl-api: %w", err)
	}
	if len(list.Items) == 0 {
		return "", fmt.Errorf("no pod with label flightctl.service=flightctl-api found (set %s to override)", envExternalNS)
	}
	return list.Items[0].Namespace, nil
}

// detectInternalNamespace finds the namespace where internal services (worker, kv, db, etc.) are deployed.
// Override with FLIGHTCTL_INTERNAL_NS. Detect from pods with label flightctl.service=flightctl-worker. No fallback.
func (p *InfraProvider) detectInternalNamespace() (string, error) {
	if ns := os.Getenv(envInternalNS); ns != "" {
		return strings.TrimSpace(ns), nil
	}
	list, err := p.client.CoreV1().Pods(metav1.NamespaceAll).List(context.Background(), metav1.ListOptions{
		LabelSelector: "flightctl.service=flightctl-worker",
		Limit:         1,
	})
	if err != nil {
		return "", fmt.Errorf("list pods flightctl.service=flightctl-worker: %w", err)
	}
	if len(list.Items) == 0 {
		return "", fmt.Errorf("no pod with label flightctl.service=flightctl-worker found (set %s to override)", envInternalNS)
	}
	return list.Items[0].Namespace, nil
}
