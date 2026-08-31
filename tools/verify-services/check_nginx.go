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

func hasNginxRoutingDirective(content, host string) bool {
	for _, line := range strings.Split(content, "\n") {
		if nginxDirectiveHost(line) == host {
			return true
		}
	}
	return false
}

// nginxDirectiveHost returns the hostname from an active proxy_pass or
// upstream server directive. Inline comments and prefix hosts are ignored.
func nginxDirectiveHost(line string) string {
	if i := strings.Index(line, "#"); i >= 0 {
		line = line[:i]
	}
	trimmed := strings.TrimSpace(line)
	var arg string
	switch {
	case strings.HasPrefix(trimmed, "proxy_pass"):
		arg = strings.TrimSpace(strings.TrimPrefix(trimmed, "proxy_pass"))
	case strings.HasPrefix(trimmed, "server "):
		arg = strings.TrimSpace(strings.TrimPrefix(trimmed, "server"))
	default:
		return ""
	}
	arg = strings.TrimSuffix(arg, ";")
	fields := strings.Fields(arg)
	if len(fields) == 0 {
		return ""
	}
	u := fields[0]
	u = strings.TrimPrefix(u, "http://")
	u = strings.TrimPrefix(u, "https://")
	if i := strings.Index(u, "/"); i >= 0 {
		u = u[:i]
	}
	if i := strings.LastIndex(u, ":"); i >= 0 {
		u = u[:i]
	}
	return u
}
