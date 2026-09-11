package infra

import (
	"context"
	"fmt"
	"strings"
	"time"
)

const serviceFailurePollInterval = 250 * time.Millisecond

// ServiceLogProvider retrieves recent logs for a Flight Control service in the
// active deployment environment.
type ServiceLogProvider interface {
	GetServiceLogs(ctx context.Context, service ServiceName, tailLines int) (string, error)
}

// ServiceFailureObserver waits for both the expected startup error and a failed
// readiness check. A container without a readiness probe can briefly be ready
// before configuration validation exits, so initial readiness is not conclusive.
func ServiceFailureObserver(ctx context.Context, lifecycle ServiceLifecycleProvider, logs ServiceLogProvider, service ServiceName, timeout time.Duration, tailLines int, expectedLog string) (string, error) {
	if lifecycle == nil || logs == nil || timeout <= 0 || strings.TrimSpace(expectedLog) == "" {
		return "", fmt.Errorf("service failure observer requires providers, a positive timeout and an expected log message")
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ticker := time.NewTicker(serviceFailurePollInterval)
	defer ticker.Stop()
	var serviceLogs string
	var readinessErr, logsErr error
	for {
		if err := ctx.Err(); err != nil {
			if readinessErr != nil {
				return serviceLogs, fmt.Errorf("service %s did not become ready: %w; expected startup error %q not confirmed: %v; log retrieval: %v", service, readinessErr, expectedLog, err, logsErr)
			}
			return serviceLogs, fmt.Errorf("service %s startup failure %q not observed: %w; log retrieval: %v", service, expectedLog, err, logsErr)
		}
		serviceLogs, logsErr = logs.GetServiceLogs(ctx, service, tailLines)
		if logsErr == nil && strings.Contains(serviceLogs, expectedLog) {
			deadline, _ := ctx.Deadline()
			readinessErr = lifecycle.WaitForReady(service, min(serviceFailurePollInterval, time.Until(deadline)))
			if readinessErr != nil && ctx.Err() == nil {
				return serviceLogs, nil
			}
		}
		select {
		case <-ctx.Done():
		case <-ticker.C:
		}
	}
}
