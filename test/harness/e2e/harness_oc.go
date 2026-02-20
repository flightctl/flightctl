package e2e

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"

	"github.com/sirupsen/logrus"
)

// getNodeIP returns the internal IP of the first cluster node.
// This is used in OCP environments to access NodePort services.
func getNodeIP() (string, error) {
	// #nosec G204 -- This is test code with controlled command
	cmd := exec.Command("oc", "get", "nodes", "-o",
		"jsonpath={.items[0].status.addresses[?(@.type==\"InternalIP\")].address}")
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to get node IP: %w", err)
	}
	ip := strings.TrimSpace(string(output))
	if ip == "" {
		return "", fmt.Errorf("no node IP found")
	}
	logrus.Debugf("Discovered node IP: %s", ip)
	return ip, nil
}

// getServiceNodePort returns the NodePort assigned to the specified service.
// This is used in OCP environments where NodePorts are dynamically assigned.
func getServiceNodePort(serviceName, namespace string) (int, error) {
	// #nosec G204 -- This is test code with controlled command
	cmd := exec.Command("oc", "get", "svc", serviceName,
		"-n", namespace,
		"-o", "jsonpath={.spec.ports[0].nodePort}")
	output, err := cmd.Output()
	if err != nil {
		return 0, fmt.Errorf("failed to get NodePort for service %s: %w", serviceName, err)
	}
	portStr := strings.TrimSpace(string(output))
	if portStr == "" {
		return 0, fmt.Errorf("no NodePort found for service %s", serviceName)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return 0, fmt.Errorf("invalid NodePort value %q for service %s: %w", portStr, serviceName, err)
	}
	logrus.Debugf("Discovered NodePort for %s: %d", serviceName, port)
	return port, nil
}

// getSecretData retrieves data from a Kubernetes secret using oc.
// Returns the base64-encoded value for the specified key.
func getSecretData(secretName, namespace, key string) (string, error) {
	// #nosec G204 -- This is test code with controlled inputs
	cmd := exec.Command("oc", "get", "secret", secretName,
		"-n", namespace,
		"-o", fmt.Sprintf("jsonpath={.data['%s']}", key))

	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("failed to get secret %s: %w, output: %s", secretName, err, string(output))
	}

	return strings.TrimSpace(string(output)), nil
}

// GetOpenShiftToken returns the current OpenShift bearer token from oc.
func (h *Harness) GetOpenShiftToken() (string, error) {
	if h == nil {
		return "", fmt.Errorf("harness is nil")
	}
	token, err := h.SH("oc", "whoami", "-t")
	if err != nil {
		return "", fmt.Errorf("failed to get openshift token from oc: %w", err)
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return "", fmt.Errorf("openshift token is empty")
	}
	return token, nil
}
