package e2e

import (
	"fmt"
	"strings"
	"time"

	"github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
)

const (
	MicroshiftKubeconfigPath = "/var/lib/microshift/resources/kubeadmin/kubeconfig"
	MicroshiftPullSecretPath = "/etc/crio/openshift-pull-secret" //nolint:gosec // G101: file path, not credentials
)

// WaitForMicroshiftReady waits for all pods in the microshift cluster to reach Ready status.
func (h *Harness) WaitForMicroshiftReady(kubeconfigPath string) error {
	timeout := util.DURATION_TIMEOUT
	interval := 10 * time.Second
	start := time.Now()
	var lastErr error

	for time.Since(start) < timeout {
		cmd := []string{
			"sudo", "oc", "wait",
			"--for=condition=Ready", "pods",
			"--all", "-A", "--timeout=60s",
			fmt.Sprintf("--kubeconfig=%s", kubeconfigPath),
		}

		_, err := h.VM.RunSSH(cmd, nil)
		if err == nil {
			GinkgoWriter.Printf("All microshift pods are ready\n")
			return nil
		}

		lastErr = err
		time.Sleep(interval)
	}

	return fmt.Errorf("timeout waiting for microshift pods to be ready: %w", lastErr)
}

func (h *Harness) EnsureMicroshiftConfigs() error {
	stdout, err := h.WaitForFileInDevice(MicroshiftPullSecretPath, util.TIMEOUT_5M, util.SHORT_POLLING)
	if err != nil {
		return fmt.Errorf("checking pull secret: %w", err)
	}
	if !strings.Contains(stdout.String(), "file was found") {
		return fmt.Errorf("pull secret not found")
	}
	stdout, err = h.WaitForFileInDevice(MicroshiftKubeconfigPath, util.TIMEOUT_5M, util.SHORT_POLLING)
	if err != nil {
		return fmt.Errorf("checking kubeconfig path: %w", err)
	}
	if !strings.Contains(stdout.String(), "file was found") {
		return fmt.Errorf("kubeconfig not found")
	}
	return nil
}

// GetPodsInNamespace returns a list of pod names in the specified namespace.
func (h *Harness) GetPodsInNamespace(namespace string) ([]string, error) {
	cmd := []string{
		"sudo", "oc", "get", "pods",
		"-n", namespace,
		"--no-headers",
		"-o", "custom-columns=NAME:.metadata.name",
		fmt.Sprintf("--kubeconfig=%s", MicroshiftKubeconfigPath),
	}

	stdout, err := h.VM.RunSSH(cmd, nil)
	output := strings.TrimSpace(stdout.String())
	if err != nil {
		if strings.Contains(output, "NotFound") || strings.Contains(output, "not found") {
			return []string{}, nil
		}
		return nil, fmt.Errorf("failed to get pods in namespace %s: %w", namespace, err)
	}

	if output == "" || strings.Contains(output, "No resources found") {
		return []string{}, nil
	}

	var pods []string
	for _, line := range strings.Split(output, "\n") {
		pod := strings.TrimSpace(line)
		if pod != "" {
			pods = append(pods, pod)
		}
	}
	return pods, nil
}

// WaitForNoPodsInNamespace waits until there are no pods in the specified namespace.
func (h *Harness) WaitForNoPodsInNamespace(namespace string, timeout time.Duration) error {
	interval := 5 * time.Second
	start := time.Now()
	var lastPodCount int

	for time.Since(start) < timeout {
		pods, err := h.GetPodsInNamespace(namespace)
		if err != nil {
			GinkgoWriter.Printf("Error checking pods in namespace %s: %v\n", namespace, err)
			time.Sleep(interval)
			continue
		}

		if len(pods) == 0 {
			GinkgoWriter.Printf("No pods found in namespace %s\n", namespace)
			return nil
		}

		if len(pods) != lastPodCount {
			GinkgoWriter.Printf("Waiting for %d pod(s) to be removed from namespace %s: %v\n", len(pods), namespace, pods)
			lastPodCount = len(pods)
		}
		time.Sleep(interval)
	}

	pods, _ := h.GetPodsInNamespace(namespace)
	return fmt.Errorf("timeout waiting for pods to be removed from namespace %s, remaining pods: %v", namespace, pods)
}

// GetCrictlImages returns the output of crictl images command.
func (h *Harness) GetCrictlImages() (string, error) {
	cmd := []string{"sudo", "crictl", "images"}
	stdout, err := h.VM.RunSSH(cmd, nil)
	if err != nil {
		return "", fmt.Errorf("failed to get crictl images: %w", err)
	}
	return stdout.String(), nil
}

// CrictlImageExists checks if an image matching the given substring exists in crictl images.
func (h *Harness) CrictlImageExists(imageSubstring string) (bool, error) {
	images, err := h.GetCrictlImages()
	if err != nil {
		return false, err
	}
	return strings.Contains(images, imageSubstring), nil
}
