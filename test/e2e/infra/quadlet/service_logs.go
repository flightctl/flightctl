package quadlet

import (
	"context"
	"fmt"
	"strconv"

	"github.com/flightctl/flightctl/test/e2e/infra"
)

// GetServiceLogs returns recent journal logs for a Flight Control Quadlet service.
func (p *InfraProvider) GetServiceLogs(ctx context.Context, service infra.ServiceName, tailLines int) (string, error) {
	if p == nil {
		return "", fmt.Errorf("Quadlet infra provider is nil")
	}
	if ctx == nil {
		return "", fmt.Errorf("Quadlet service log context is nil")
	}
	if tailLines <= 0 {
		return "", fmt.Errorf("service log tail lines must be positive")
	}
	info := GetServiceInfo(service)
	if info.SystemdUnit == "" {
		return "", fmt.Errorf("unknown service %q", service)
	}
	out, err := p.RunCommandContext(ctx, "journalctl", "-u", info.SystemdUnit, "-n", strconv.Itoa(tailLines), "--no-pager")
	if err != nil {
		return out, fmt.Errorf("read journal logs for %s: %w", service, err)
	}
	if out == "" {
		return "", fmt.Errorf("journal logs for %s were empty", service)
	}
	return out, nil
}
