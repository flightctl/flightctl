package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func checkNginx(repoRoot string, services []ExpandedService) []Issue {
	const check = "nginx"
	data, err := os.ReadFile(filepath.Join(repoRoot, "deploy/podman/flightctl-gateway/flightctl-gateway-config/nginx.conf.template"))
	if err != nil {
		return []Issue{{Check: check, Message: err.Error()}}
	}
	content := string(data)
	var issues []Issue
	for _, s := range services {
		if !s.RequireGateway {
			continue
		}
		host := "flightctl-" + s.Name
		if !hasNginxRoutingDirective(content, host) {
			issues = append(issues, Issue{Check: check, Message: fmt.Sprintf("%s: nginx.conf.template has no active proxy_pass/server directive for %q", s.Name, host)})
		}
	}
	return issues
}

// hasNginxRoutingDirective reports an active (non-comment) proxy_pass or
// upstream server line that references host.
func hasNginxRoutingDirective(content, host string) bool {
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if !strings.Contains(trimmed, host) {
			continue
		}
		if strings.HasPrefix(trimmed, "proxy_pass") || strings.HasPrefix(trimmed, "server ") {
			return true
		}
	}
	return false
}
