package k8s

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/flightctl/flightctl/test/e2e/infra"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// GetServiceLogs returns recent logs from all pods for a Flight Control service.
func (p *InfraProvider) GetServiceLogs(ctx context.Context, service infra.ServiceName, tailLines int) (string, error) {
	if p.client == nil {
		return "", fmt.Errorf("no Kubernetes client")
	}
	_, label, ns, err := p.GetServiceNamespaceAndMetadata(service)
	if err != nil {
		return "", err
	}
	pods, err := p.client.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{LabelSelector: label})
	if err != nil {
		return "", fmt.Errorf("list pods for %s: %w", service, err)
	}
	if len(pods.Items) == 0 {
		return "", fmt.Errorf("no pods found for %s", service)
	}

	var b strings.Builder
	logsFound := false
	var retrievalErr error
	for i := range pods.Items {
		pod := pods.Items[i]
		current, currentErr := p.podLogs(ctx, ns, pod.Name, tailLines, false)
		if currentErr == nil && current != "" {
			// strings.Builder writes never return a non-nil error.
			_, _ = b.WriteString(fmt.Sprintf("=== pod/%s current ===\n%s\n", pod.Name, current))
			logsFound = true
		} else if currentErr != nil {
			retrievalErr = errors.Join(retrievalErr, fmt.Errorf("pod/%s current logs: %w", pod.Name, currentErr))
		}
		previous, previousErr := p.podLogs(ctx, ns, pod.Name, tailLines, true)
		if previousErr == nil && previous != "" {
			// strings.Builder writes never return a non-nil error.
			_, _ = b.WriteString(fmt.Sprintf("=== pod/%s previous ===\n%s\n", pod.Name, previous))
			logsFound = true
		} else if previousErr != nil {
			retrievalErr = errors.Join(retrievalErr, fmt.Errorf("pod/%s previous logs: %w", pod.Name, previousErr))
		}
		if currentErr != nil && previousErr != nil {
			// strings.Builder writes never return a non-nil error.
			_, _ = b.WriteString(fmt.Sprintf("=== pod/%s logs unavailable: current=%v previous=%v ===\n", pod.Name, currentErr, previousErr))
		}
	}
	if !logsFound {
		if retrievalErr != nil {
			return b.String(), fmt.Errorf("no pod logs retrieved for %s: %w", service, retrievalErr)
		}
		return b.String(), fmt.Errorf("no pod logs retrieved for %s", service)
	}
	return b.String(), nil
}

func (p *InfraProvider) podLogs(ctx context.Context, namespace, podName string, tailLines int, previous bool) (logs string, err error) {
	lines := int64(tailLines)
	req := p.client.CoreV1().Pods(namespace).GetLogs(podName, &corev1.PodLogOptions{
		TailLines: &lines,
		Previous:  previous,
	})
	reader, err := req.Stream(ctx)
	if err != nil {
		return "", err
	}
	defer func() {
		if closeErr := reader.Close(); closeErr != nil && err == nil {
			err = fmt.Errorf("close pod log stream: %w", closeErr)
		}
	}()
	data, err := io.ReadAll(reader)
	if err != nil {
		return "", err
	}
	return string(data), nil
}
